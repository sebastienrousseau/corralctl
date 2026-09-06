//go:build ignore

// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// claims_check verifies that what the project claims about itself is still
// true.
//
// Some documents make measurable assertions: a coverage percentage, a list
// of packages, a count of benchmarks. Those go stale silently, because
// nothing fails when a number in prose stops matching the code — and the
// documents that carry them are the ones read by people deciding whether
// to trust the project. .bestpractices.json is an OpenSSF submission; it
// claimed 90.2% coverage "as of v0.0.11" through twenty releases, by which
// point the real figure was 100% across twelve packages. Understated, but
// wrong, and wrong in a public place.
//
// This runs the measurement and compares. It is deliberately not clever:
// where a claim can be checked against reality, it is; where it cannot, it
// is not pretended otherwise.
//
//	go run scripts/claims_check.go [-coverprofile path]
//
// With no profile it runs the suite itself, which is slow. CI passes the
// profile it has already produced.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

func main() {
	profile := flag.String("coverprofile", "",
		"an existing coverage profile to read instead of running the suite")
	flag.Parse()

	var problems []string
	pkgs, overall, err := coverage(*profile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "claims check: measuring coverage: %v\n", err)
		os.Exit(1)
	}

	covProblems := checkCoverageClaims(pkgs, overall)
	if len(covProblems) > 0 && *profile == "" {
		// Measured once and disagreed. Measure again before failing.
		//
		// A single low reading was observed in this package once and could
		// not be reproduced in thirty-seven attempts; the likeliest cause
		// is a worker-pool branch in internal/search that does not always
		// execute under contention. Whatever it is, a gate that fails at
		// random is worse than the drift it guards against — people learn
		// to re-run it, and then they re-run it on the day it was right.
		// So a disagreement has to survive a second independent
		// measurement.
		fmt.Fprintln(os.Stderr, "claims check: coverage disagreed; measuring again before failing")
		if p2, o2, err2 := coverage(""); err2 == nil {
			if second := checkCoverageClaims(p2, o2); len(second) == 0 {
				fmt.Fprintln(os.Stderr, "claims check: the second measurement agrees; treating the first as noise")
				covProblems = nil
			} else {
				covProblems = second
			}
		}
	}
	problems = append(problems, covProblems...)
	problems = append(problems, checkBenchmarkCoverage()...)
	problems = append(problems, checkExampleCoverage()...)
	problems = append(problems, checkLintClaim()...)

	if len(problems) > 0 {
		fmt.Fprintln(os.Stderr, "claims check failed:")
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "  - "+p)
		}
		os.Exit(1)
	}
	fmt.Printf("Claims check: %d packages at %.1f%% coverage, and the documents that say so agree\n",
		len(pkgs), overall)
}

// coverage returns the per-package percentages and the overall figure.
func coverage(profile string) (map[string]float64, float64, error) {
	pkgs := map[string]float64{}

	// Per-package percentages come from `go test -cover`, which reports
	// them directly. A profile alone cannot produce them without
	// re-deriving package boundaries.
	out, err := exec.Command("go", "test", "./...", "-count=1", "-cover").CombinedOutput()
	if err != nil {
		return nil, 0, fmt.Errorf("go test -cover: %v\n%s", err, out)
	}
	line := regexp.MustCompile(`(?m)^ok\s+(\S+)\s+\S+\s+coverage:\s+([0-9.]+)% of statements`)
	for _, m := range line.FindAllStringSubmatch(string(out), -1) {
		pkgs[strings.TrimPrefix(m[1], modulePath+"/")] = mustFloat(m[2])
	}
	if len(pkgs) == 0 {
		return nil, 0, fmt.Errorf("no packages reported coverage:\n%s", out)
	}

	// The overall figure is the statement-weighted total, which only a
	// profile gives. Recompute it rather than averaging the per-package
	// numbers, which would weight a tiny package like a large one.
	if profile == "" {
		profile = "coverage.claims.out"
		defer func() { _ = os.Remove(profile) }()
		if o, err := exec.Command("go", "test", "./...", "-count=1", "-coverprofile="+profile).CombinedOutput(); err != nil {
			return nil, 0, fmt.Errorf("go test -coverprofile: %v\n%s", err, o)
		}
	}
	// Counted from the profile rather than read off `go tool cover -func`.
	//
	// That command rounds its total to one decimal, so 4874 of 4875
	// statements prints as "100.0%" — and a claim of 100% then agrees with
	// a suite that is not at 100%. It also attributes blocks to named
	// functions, so a statement inside a `var f = func(){...}` literal is
	// absent from the report entirely, uncovered or not. Between the two, a
	// three-statement regression was invisible: the branch in
	// internal/search that only ran on some scheduler interleavings sat
	// uncovered under -race while this gate reported agreement.
	exact, uncovered, err := exactCoverage(profile)
	if err != nil {
		return nil, 0, err
	}
	uncoveredBlocks = uncovered
	return pkgs, exact, nil
}

// uncoveredBlocks names the statement blocks the last measurement found
// uncovered, so a failure can say which they are rather than only that the
// number moved.
var uncoveredBlocks []string

// coverLine matches one block of a coverage profile:
//
//	path/file.go:12.34,56.78 3 1
//
// The trailing pair is the number of statements in the block and the number
// of times it ran.
var coverLine = regexp.MustCompile(`^(\S+)\s+(\d+)\s+(\d+)$`)

// exactCoverage counts covered statements against total, unrounded.
//
// A `go test ./...` profile can carry the same block more than once, because
// a file compiled into several test binaries is reported by each of them.
// Those duplicates have to be merged by taking the highest count — a block
// covered by one package and not another is covered. Summing the lines
// instead undercounts, which is a mistake worth naming here because it is
// easy to make and produces a plausible-looking wrong answer.
func exactCoverage(profile string) (float64, []string, error) {
	b, err := os.ReadFile(profile) // #nosec G304 -- path supplied by the caller or created above
	if err != nil {
		return 0, nil, fmt.Errorf("reading the coverage profile: %w", err)
	}

	type block struct{ stmts, count int }
	blocks := map[string]block{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}
		m := coverLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		stmts, _ := strconv.Atoi(m[2])
		count, _ := strconv.Atoi(m[3])
		if prev, ok := blocks[m[1]]; ok && prev.count > count {
			count = prev.count
		}
		blocks[m[1]] = block{stmts: stmts, count: count}
	}
	if len(blocks) == 0 {
		return 0, nil, fmt.Errorf("no statement blocks in %s", profile)
	}

	total, covered := 0, 0
	var uncovered []string
	for pos, blk := range blocks {
		total += blk.stmts
		if blk.count > 0 {
			covered += blk.stmts
			continue
		}
		uncovered = append(uncovered, pos)
	}
	sort.Strings(uncovered)
	return 100 * float64(covered) / float64(total), uncovered, nil
}

const modulePath = "github.com/sebastienrousseau/corralctl"

// coverageClaim matches a stated overall percentage.
var coverageClaim = regexp.MustCompile(`Statement coverage is ([0-9.]+)% overall`)

// packageCount matches the stated number of packages.
var packageCount = regexp.MustCompile(`across all (\d+) packages`)

// checkCoverageClaims compares .bestpractices.json against the measurement.
//
// Both the percentage and the package count are checked. The count matters
// on its own: a claim of "100% across all 9 packages" stays true about the
// percentage while silently omitting three packages added since.
func checkCoverageClaims(pkgs map[string]float64, overall float64) []string {
	b, err := os.ReadFile(".bestpractices.json")
	if err != nil {
		return []string{fmt.Sprintf("reading .bestpractices.json: %v", err)}
	}
	body := string(b)

	var problems []string
	claims := coverageClaim.FindAllStringSubmatch(body, -1)
	if len(claims) == 0 {
		return []string{".bestpractices.json states no coverage figure; " +
			"either restore one or drop this check"}
	}
	for _, m := range claims {
		claimed := mustFloat(m[1])
		if !sameToOneDecimal(claimed, overall) {
			problems = append(problems, fmt.Sprintf(
				".bestpractices.json claims %.1f%% statement coverage; the suite measures %.4f%%",
				claimed, overall))
			break
		}
		// A claim of 100% is the one figure that cannot be approximate.
		// Rounding makes 4874 of 4875 statements agree with it to one
		// decimal, and that is exactly how an uncovered branch survived:
		// the claim stayed true-looking while the suite stopped covering
		// everything. Anything short of every statement fails here, and
		// says which ones.
		if claimed == 100 && overall < 100 {
			problems = append(problems, fmt.Sprintf(
				".bestpractices.json claims 100%% statement coverage; the suite measures "+
					"%.4f%% — %d statement block(s) uncovered:\n      %s",
				overall, len(uncoveredBlocks), strings.Join(uncoveredBlocks, "\n      ")))
			break
		}
	}
	for _, m := range packageCount.FindAllStringSubmatch(body, -1) {
		claimed, _ := strconv.Atoi(m[1])
		if claimed != len(pkgs) {
			problems = append(problems, fmt.Sprintf(
				".bestpractices.json claims %d packages; the suite covers %d", claimed, len(pkgs)))
			break
		}
	}
	// Every package it names must exist, so a renamed or removed package
	// cannot linger in a public claim.
	for name := range pkgs {
		if !strings.Contains(body, name) {
			problems = append(problems, fmt.Sprintf(
				".bestpractices.json does not name package %s, which the suite covers", name))
		}
	}
	sort.Strings(problems)
	return problems
}

// benchmarked are the packages whose performance the project makes claims
// about, and which therefore have to keep a benchmark.
//
// Not every package: a benchmark nobody reads is noise. These are the ones
// on the path a user waits for — the workspace scan, symbol extraction and
// content search — and the CHANGELOG quotes figures for all three.
var benchmarked = []string{
	"internal/engine",
	"internal/mcp",
	"internal/search",
	"internal/symbols",
}

// checkBenchmarkCoverage requires a benchmark in each of those packages.
//
// internal/search and internal/symbols carry the 6.9s-to-1.3s figure and
// had no benchmark at all until this check's absence was noticed: a change
// making extraction three times slower would have passed every gate.
func checkBenchmarkCoverage() []string {
	var problems []string
	for _, pkg := range benchmarked {
		entries, err := os.ReadDir(pkg)
		if err != nil {
			problems = append(problems, fmt.Sprintf("reading %s: %v", pkg, err))
			continue
		}
		found := false
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), "_test.go") {
				continue
			}
			b, err := os.ReadFile(pkg + "/" + e.Name()) // #nosec G304 -- path from the table above
			if err != nil {
				continue
			}
			if strings.Contains(string(b), "\nfunc Benchmark") {
				found = true
				break
			}
		}
		if !found {
			problems = append(problems, fmt.Sprintf(
				"%s has no benchmark, and the project publishes performance figures for it", pkg))
		}
	}
	return problems
}

// checkExampleCoverage requires every program under examples/ to be
// referenced from prose.
//
// An example nobody links to is one nobody reads, and it rots without
// anybody noticing — it compiles, so example-check stays green.
func checkExampleCoverage() []string {
	entries, err := os.ReadDir("examples")
	if err != nil {
		return []string{fmt.Sprintf("reading examples/: %v", err)}
	}
	docs := readAll("README.md", "docs-site/content/reference.md", "DEVELOPMENT.md", "examples/README.md")

	var problems []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		if !strings.Contains(docs, e.Name()) {
			problems = append(problems, fmt.Sprintf(
				"examples/%s is not referenced from any document, so nothing would notice it rotting",
				e.Name()))
		}
	}
	sort.Strings(problems)
	return problems
}

// checkLintClaim keeps the README's linting badge honest.
//
// The badge it replaced was Go Report Card's, which was sunset after ten
// years — the service stopped, shields.io started answering "404: badge not
// found", and the README went on advertising a code-quality signal that no
// longer existed. Nothing caught it, because nothing checked.
//
// The replacement badge names golangci-lint, and that claim was not true
// either when it was written: .golangci.yml enabled errcheck, gosec, govet
// and staticcheck, but CI ran only vet and staticcheck through the reusable
// workflow. errcheck and gosec were enforced on whoever's laptop last typed
// `make lint`. So this checks the whole chain the badge implies — the config
// exists, and something in CI actually runs it — rather than the badge alone.
func checkLintClaim() []string {
	readme, err := os.ReadFile("README.md")
	if err != nil {
		return []string{fmt.Sprintf("reading README.md: %v", err)}
	}
	body := string(readme)

	var problems []string

	// Go Report Card is gone. Any badge pointing at it is dead by
	// definition, so this cannot come back by copy-paste from an older
	// README.
	if strings.Contains(body, "goreportcard.com") {
		problems = append(problems, "README.md links to goreportcard.com, "+
			"which was sunset; its badge endpoint returns 404")
	}

	if !strings.Contains(body, "golangci-lint") {
		// No claim, nothing to keep true.
		return problems
	}
	if !strings.Contains(body, "https://golangci-lint.run") {
		problems = append(problems, "README.md names golangci-lint but does not "+
			"link to https://golangci-lint.run")
	}
	if _, err := os.Stat(".golangci.yml"); err != nil {
		problems = append(problems, "README.md advertises golangci-lint but "+
			".golangci.yml is missing, so there is no configuration to run")
	}
	if !runsInCI("golangci-lint-action") {
		problems = append(problems, "README.md advertises golangci-lint but no "+
			"workflow runs it; a linter nothing runs is a habit, not a gate")
	}
	sort.Strings(problems)
	return problems
}

// runsInCI reports whether any workflow mentions needle.
func runsInCI(needle string) bool {
	entries, err := os.ReadDir(".github/workflows")
	if err != nil {
		return false
	}
	for _, e := range entries {
		n := e.Name()
		if !strings.HasSuffix(n, ".yml") && !strings.HasSuffix(n, ".yaml") {
			continue
		}
		b, err := os.ReadFile(".github/workflows/" + n) // #nosec G304 -- workflow dir listing
		if err != nil {
			continue
		}
		if strings.Contains(string(b), needle) {
			return true
		}
	}
	return false
}

// readAll concatenates the files that exist, ignoring the ones that do not.
func readAll(paths ...string) string {
	var b strings.Builder
	for _, p := range paths {
		if c, err := os.ReadFile(p); err == nil { // #nosec G304 -- fixed list above
			b.Write(c)
		}
	}
	return b.String()
}

// sameToOneDecimal compares two percentages at the precision they are
// written to, so 100.0 and 100.04 agree and 99.9 and 100.0 do not.
func sameToOneDecimal(a, b float64) bool {
	return fmt.Sprintf("%.1f", a) == fmt.Sprintf("%.1f", b)
}

func mustFloat(s string) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return -1
	}
	return f
}
