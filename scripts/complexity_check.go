//go:build ignore

// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// complexity_check holds every function and file to the project's
// complexity ceilings, against a committed baseline that may only shrink.
//
// The ceilings, per function unless stated:
//
//	cyclomatic   10   McCabe, as gocyclo counts it
//	cognitive    15   SonarSource, as gocognit counts it
//	halstead     30   Halstead difficulty, computed here (see halstead)
//	func-lines   60   lines of code from `func` to the closing brace
//	file-lines  500   lines of code in a file
//
// A line of code is a line holding at least one token, so blank lines and
// comments do not count: a well-explained function is not a long one.
//
// Every tracked non-test .go file is measured, scripts and examples
// included. Code written before the ceilings existed is recorded in
// scripts/complexity_baseline.tsv with the value it had. The check fails
// when:
//
//   - a function or file goes over a ceiling and is not in the baseline;
//   - a baseline entry gets worse;
//   - a baseline entry improves, or drops under its ceiling, and the
//     baseline still records the old value.
//
// The last rule is what makes the baseline shrink: an improvement has to be
// recorded, so it cannot be spent later on a regression. -update records
// improvements and refuses to run while anything has got worse, so the
// baseline can never absorb a regression. The one exception is creating
// the baseline when none exists, which records the tree as it stands; a
// pull request that deletes the file to do that again is plain in review.
//
// gocyclo and gocognit run through `go run module@version`, pinned below,
// so the module's go.mod gains no dependency for a development tool.
//
//	go run scripts/complexity_check.go          # check
//	go run scripts/complexity_check.go -update  # record improvements
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"io"
	"math"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

const (
	gocycloTool  = "github.com/fzipp/gocyclo/cmd/gocyclo@v0.6.0"
	gocognitTool = "github.com/uudashr/gocognit/cmd/gocognit@v1.3.0"
	baselinePath = "scripts/complexity_baseline.tsv"
)

// ceilings maps each metric to the highest value allowed.
var ceilings = map[string]float64{
	"cyclomatic": 10,
	"cognitive":  15,
	"halstead":   30,
	"func-lines": 60,
	"file-lines": 500,
}

// key identifies one measurement. fn is "-" for a file-level metric.
type key struct {
	metric, file, fn string
}

// offenders maps each measurement over its ceiling to its value.
type offenders map[key]float64

// record keeps the highest value seen for k, for the rare file that
// declares two functions with one name (several init funcs).
func (o offenders) record(k key, v float64) {
	if v > ceilings[k.metric] && v > o[k] {
		o[k] = v
	}
}

func main() {
	update := flag.Bool("update", false, "record improvements in the baseline")
	flag.Parse()
	if err := run(*update, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "complexity-check:", err)
		os.Exit(1)
	}
}

func run(update bool, w io.Writer) error {
	files, err := sourceFiles()
	if err != nil {
		return err
	}
	current, err := measure(files)
	if err != nil {
		return err
	}
	baseline, found, err := readBaseline(baselinePath)
	if err != nil {
		return err
	}
	regressions, stale := compare(baseline, current)
	for _, line := range append(regressions, stale...) {
		fmt.Fprintln(w, line)
	}
	if update && !found {
		regressions = nil
	}
	return conclude(update, regressions, stale, current, w)
}

// conclude turns the comparison into the exit status, writing the
// baseline when -update is set and nothing has regressed.
func conclude(update bool, regressions, stale []string, current offenders, w io.Writer) error {
	if len(regressions) > 0 {
		return fmt.Errorf("%d regression(s) over the ceilings; fix them, the baseline does not absorb regressions", len(regressions))
	}
	if update {
		if err := writeBaseline(baselinePath, current); err != nil {
			return err
		}
		fmt.Fprintf(w, "complexity-check: baseline rewritten, %d entries\n", len(current))
		return nil
	}
	if len(stale) > 0 {
		return fmt.Errorf("%d baseline entr(y/ies) improved; record them with: go run scripts/complexity_check.go -update", len(stale))
	}
	fmt.Fprintf(w, "complexity-check: no regressions, %d baselined offenders remain\n", len(current))
	return nil
}

// sourceFiles lists every tracked Go file that is not a test.
func sourceFiles() ([]string, error) {
	out, err := exec.Command("git", "ls-files", "--", "*.go").Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	var files []string
	for _, f := range strings.Fields(string(out)) {
		if !strings.HasSuffix(f, "_test.go") {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return nil, errors.New("no Go files found; run from the repository root")
	}
	return files, nil
}

func measure(files []string) (offenders, error) {
	got := offenders{}
	if err := runTool(gocycloTool, "cyclomatic", files, got); err != nil {
		return nil, err
	}
	if err := runTool(gocognitTool, "cognitive", files, got); err != nil {
		return nil, err
	}
	for _, f := range files {
		if err := measureFile(f, got); err != nil {
			return nil, err
		}
	}
	return got, nil
}

// runTool runs a pinned complexity tool over files and records every
// function it reports over the metric's ceiling. Both tools print
// "<complexity> <package> <function> <file:line:column>".
func runTool(tool, metric string, files []string, got offenders) error {
	args := append([]string{"run", tool}, files...)
	cmd := exec.Command("go", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("%s: %w\n%s", tool, err, stderr.String())
	}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		k, v, err := parseToolLine(metric, sc.Text())
		if err != nil {
			return fmt.Errorf("%s: %w", tool, err)
		}
		got.record(k, v)
	}
	return sc.Err()
}

func parseToolLine(metric, line string) (key, float64, error) {
	f := strings.Fields(line)
	if len(f) < 4 {
		return key{}, 0, fmt.Errorf("unexpected output line %q", line)
	}
	v, err := strconv.Atoi(f[0])
	if err != nil {
		return key{}, 0, fmt.Errorf("unexpected output line %q", line)
	}
	pos := f[len(f)-1]
	file := pos
	for i := 0; i < 2; i++ {
		file = file[:max(strings.LastIndex(file, ":"), 0)]
	}
	fn := strings.Join(f[2:len(f)-1], " ")
	return key{metric, file, fn}, float64(v), nil
}

// measureFile records the file's length and, for every function, its
// length and Halstead difficulty.
func measureFile(path string, got offenders) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	got.record(key{"file-lines", path, "-"}, float64(codeLines(src)))
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
	if err != nil {
		return err
	}
	for _, d := range file.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		body := src[fset.Position(fd.Pos()).Offset:fset.Position(fd.End()).Offset]
		name := funcName(fd)
		got.record(key{"func-lines", path, name}, float64(codeLines(body)))
		got.record(key{"halstead", path, name}, halstead(body))
	}
	return nil
}

// codeLines counts the lines of src that hold at least one token.
func codeLines(src []byte) int {
	fset := token.NewFileSet()
	file := fset.AddFile("", -1, len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, 0)
	lines := map[int]bool{}
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			return len(lines)
		}
		lines[file.Line(pos)] = true
	}
}

// funcName names a function the way gocyclo does: "F", "(T).M", "(*T).M".
func funcName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 {
		return fd.Name.Name
	}
	return "(" + recvName(fd.Recv.List[0].Type) + ")." + fd.Name.Name
}

func recvName(e ast.Expr) string {
	switch t := e.(type) {
	case *ast.StarExpr:
		return "*" + recvName(t.X)
	case *ast.IndexExpr:
		return recvName(t.X)
	case *ast.IndexListExpr:
		return recvName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return "?"
}

// halstead returns the Halstead difficulty of a function's source,
// D = (n1 / 2) * (N2 / n2), rounded to one decimal.
//
// Operands are identifiers and literals. Operators are every other token:
// operators, keywords and opening brackets. A closing bracket is the second
// half of the operator its opener already counted, and semicolons are
// mostly inserted by the scanner, so neither is counted.
func halstead(src []byte) float64 {
	fset := token.NewFileSet()
	var s scanner.Scanner
	s.Init(fset.AddFile("", -1, len(src)), src, nil, 0)
	operators, operands := map[token.Token]bool{}, map[string]bool{}
	total := 0
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		switch {
		case tok == token.IDENT || tok.IsLiteral():
			operands[tok.String()+" "+lit] = true
			total++
		case tok != token.SEMICOLON && tok != token.RPAREN && tok != token.RBRACK && tok != token.RBRACE:
			operators[tok] = true
		}
	}
	if len(operands) == 0 {
		return 0
	}
	d := float64(len(operators)) / 2 * float64(total) / float64(len(operands))
	return math.Round(d*10) / 10
}

// compare splits the differences between the baseline and the tree into
// regressions (new offenders, worse values) and stale entries (fixed or
// improved, still recorded at the old value).
func compare(baseline, current offenders) (regressions, stale []string) {
	for _, k := range sortedKeys(current) {
		line, regressed := classify(k, current[k], baseline)
		if regressed {
			regressions = append(regressions, line)
		} else if line != "" {
			stale = append(stale, line)
		}
	}
	for _, k := range sortedKeys(baseline) {
		if _, ok := current[k]; !ok {
			stale = append(stale, describe("FIXED ", k, "within its ceiling; remove it from the baseline"))
		}
	}
	return regressions, stale
}

// classify describes one current offender against the baseline, and says
// whether it is a regression. An unchanged entry returns "".
func classify(k key, v float64, baseline offenders) (string, bool) {
	b, ok := baseline[k]
	switch {
	case !ok:
		return describe("NEW   ", k, num(v)+", ceiling "+num(ceilings[k.metric])), true
	case v > b:
		return describe("WORSE ", k, num(v)+", baseline "+num(b)), true
	case v < b:
		return describe("BETTER", k, num(v)+", baseline "+num(b)), false
	}
	return "", false
}

func describe(verdict string, k key, detail string) string {
	return verdict + " " + k.metric + " " + k.file + " " + k.fn + ": " + detail
}

func sortedKeys(o offenders) []key {
	keys := make([]key, 0, len(o))
	for k := range o {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.metric != b.metric {
			return a.metric < b.metric
		}
		if a.file != b.file {
			return a.file < b.file
		}
		return a.fn < b.fn
	})
	return keys
}

func num(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

const baselineHeader = `# SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
# SPDX-License-Identifier: GPL-3.0-only
#
# Functions and files over the complexity ceilings, at the value each had
# when it was recorded. Generated by scripts/complexity_check.go -update;
# do not edit by hand. Entries may only be removed or lowered.
#
# metric	value	file	function
`

func writeBaseline(path string, o offenders) error {
	var b strings.Builder
	b.WriteString(baselineHeader)
	for _, k := range sortedKeys(o) {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\n", k.metric, num(o[k]), k.file, k.fn)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// readBaseline parses the baseline, reporting whether it exists.
func readBaseline(path string) (offenders, bool, error) {
	src, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return offenders{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	o, err := parseBaseline(path, string(src))
	return o, true, err
}

func parseBaseline(path, src string) (offenders, error) {
	o := offenders{}
	for i, line := range strings.Split(src, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 4 || ceilings[f[0]] == 0 {
			return nil, fmt.Errorf("%s:%d: malformed entry %q", path, i+1, line)
		}
		v, err := strconv.ParseFloat(f[1], 64)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: malformed value %q", path, i+1, f[1])
		}
		o[key{f[0], f[2], f[3]}] = v
	}
	return o, nil
}
