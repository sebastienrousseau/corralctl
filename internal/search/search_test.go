// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package search

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// write lays down one file under root, creating parents.
func write(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// workspace lays out a small repository with the shapes that matter.
func workspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, root, "main.go", "package main\n\nconst Timeout = 30\n\nfunc main() {\n\t_ = Timeout\n}\n")
	write(t, root, "internal/util.go", "package internal\n\n// Timeout is documented here.\nvar Timeout = 30\n")
	write(t, root, "main_test.go", "package main\n\nfunc TestTimeout(t *testing.T) {}\n")
	write(t, root, "README.md", "# Docs\n\nThe TIMEOUT is configurable.\n")
	write(t, root, "node_modules/dep/index.js", "const Timeout = 1;\n")
	write(t, root, ".git/config", "[core]\n\ttimeout = 5\n")
	return root
}

// run searches root and fails on error.
func run(t *testing.T, root string, q Query, allowed FileFilter) *Result {
	t.Helper()
	m, err := Compile(q)
	if err != nil {
		t.Fatalf("compile %+v: %v", q, err)
	}
	res, err := SearchRepo(context.Background(), root, m, allowed)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	return res
}

// files lists the distinct files a result touched, for readable failures.
func files(res *Result) string {
	var b strings.Builder
	for _, h := range res.Hits {
		fmt.Fprintf(&b, "\n  %s:%d:%d %s", h.File, h.Line, h.Column, h.Text)
	}
	if b.Len() == 0 {
		return " (nothing)"
	}
	return b.String()
}

func hasFile(res *Result, rel string) bool {
	for _, h := range res.Hits {
		if h.File == rel {
			return true
		}
	}
	return false
}

func TestSearchFindsMatchesAcrossFiles(t *testing.T) {
	root := workspace(t)
	res := run(t, root, Query{Pattern: "Timeout"}, nil)

	if !hasFile(res, "main.go") {
		t.Errorf("main.go should match: %s", files(res))
	}
	if !hasFile(res, "internal/util.go") {
		t.Errorf("internal/util.go should match: %s", files(res))
	}
	// Case-insensitive by default: somebody searching for "timeout" wants
	// TIMEOUT too.
	if !hasFile(res, "README.md") {
		t.Errorf("README.md's TIMEOUT should match a case-insensitive search: %s", files(res))
	}
	// Test files are excluded by default, for the same reason the symbol
	// index excludes them.
	if hasFile(res, "main_test.go") {
		t.Errorf("test files should be excluded by default: %s", files(res))
	}
	// Dependency trees are never the answer to a question about your own
	// workspace.
	if hasFile(res, "node_modules/dep/index.js") {
		t.Errorf("node_modules should not be searched: %s", files(res))
	}
	if hasFile(res, ".git/config") {
		t.Errorf(".git should not be searched: %s", files(res))
	}
}

func TestSearchIsOrderStable(t *testing.T) {
	root := workspace(t)
	first := run(t, root, Query{Pattern: "Timeout"}, nil)
	for i := 0; i < 8; i++ {
		again := run(t, root, Query{Pattern: "Timeout"}, nil)
		if len(again.Hits) != len(first.Hits) {
			t.Fatalf("run %d found %d hits, first found %d", i, len(again.Hits), len(first.Hits))
		}
		for j := range again.Hits {
			if again.Hits[j] != first.Hits[j] {
				t.Fatalf("run %d differs at %d:\n got %+v\nwant %+v", i, j, again.Hits[j], first.Hits[j])
			}
		}
	}
}

func TestSearchReportsAccuratePositions(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package a\n\nfunc Find() {}\n")
	res := run(t, root, Query{Pattern: "Find", CaseSensitive: true}, nil)

	if len(res.Hits) != 1 {
		t.Fatalf("expected one hit, got %s", files(res))
	}
	h := res.Hits[0]
	if h.Line != 3 {
		t.Errorf("line = %d, want 3", h.Line)
	}
	// Column is 1-indexed into the untrimmed line: "func Find() {}".
	if h.Column != 6 {
		t.Errorf("column = %d, want 6", h.Column)
	}
	if h.Text != "func Find() {}" {
		t.Errorf("text = %q", h.Text)
	}
}

func TestSearchCaseSensitivity(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package a\n\nvar timeout = 1\nvar TIMEOUT = 2\n")

	insensitive := run(t, root, Query{Pattern: "TimeOut"}, nil)
	if len(insensitive.Hits) != 2 {
		t.Errorf("case-insensitive should match both: %s", files(insensitive))
	}
	sensitive := run(t, root, Query{Pattern: "TIMEOUT", CaseSensitive: true}, nil)
	if len(sensitive.Hits) != 1 || sensitive.Hits[0].Line != 4 {
		t.Errorf("case-sensitive should match only line 4: %s", files(sensitive))
	}
}

func TestSearchRegex(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package a\n\nfunc Alpha() {}\nfunc Beta() {}\nvar Gamma = 1\n")

	res := run(t, root, Query{Pattern: `^func \w+\(`, Regex: true}, nil)
	if len(res.Hits) != 2 {
		t.Errorf("expected two function declarations: %s", files(res))
	}
	// Case-insensitivity applies to a regex too, via the flag rather than
	// by mangling the pattern.
	res = run(t, root, Query{Pattern: "ALPHA", Regex: true}, nil)
	if len(res.Hits) != 1 {
		t.Errorf("a regex should be case-insensitive by default: %s", files(res))
	}
}

// TestCompileRejectsRatherThanReturningNothing is the property that keeps a
// broken query from reading as a confident "nothing here".
func TestCompileRejectsRatherThanReturningNothing(t *testing.T) {
	for name, q := range map[string]Query{
		"empty":         {Pattern: ""},
		"blank":         {Pattern: "   "},
		"bad regex":     {Pattern: "func(", Regex: true},
		"too long":      {Pattern: strings.Repeat("x", maxPatternLen+1)},
		"invalid utf-8": {Pattern: string([]byte{0xff, 0xfe})},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Compile(q); err == nil {
				t.Error("expected an error, not a matcher that finds nothing")
			}
		})
	}
}

func TestCompileRegexErrorNamesThePatternTheCallerWrote(t *testing.T) {
	_, err := Compile(Query{Pattern: "func(", Regex: true})
	if err == nil {
		t.Fatal("expected an error")
	}
	// Not "(?i)func(", which the caller did not write and would not
	// recognise as their own input.
	if strings.Contains(err.Error(), "(?i)") {
		t.Errorf("the error should quote the caller's pattern, got %v", err)
	}
	if !strings.Contains(err.Error(), `"func("`) {
		t.Errorf("the error should name the pattern, got %v", err)
	}
}

// TestSearchRespectsTheFileFilter is the security property: search must not
// become a way to read a refused file one line at a time.
func TestSearchRespectsTheFileFilter(t *testing.T) {
	root := t.TempDir()
	write(t, root, "app.go", "package a // secret lives elsewhere\n")
	write(t, root, ".env", "AWS_SECRET_ACCESS_KEY=hunter2\n")
	write(t, root, "credentials.json", `{"secret": "hunter2"}`+"\n")

	onlyGo := func(rel string) bool { return strings.HasSuffix(rel, ".go") }
	res := run(t, root, Query{Pattern: "secret"}, onlyGo)

	if !hasFile(res, "app.go") {
		t.Errorf("the allowed file should match: %s", files(res))
	}
	for _, denied := range []string{".env", "credentials.json"} {
		if hasFile(res, denied) {
			t.Fatalf("a filtered file must never produce a hit: %s", files(res))
		}
	}
	// And it was never opened: the filter runs in the walk, so a denied
	// file is not counted among the files read.
	if res.Files != 1 {
		t.Errorf("read %d files, want 1 — a denied file should not be opened", res.Files)
	}
}

func TestSearchPathGlob(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "target\n")
	write(t, root, "b.md", "target\n")
	write(t, root, "internal/c.go", "target\n")

	// A bare filename pattern is what somebody types, and it must work
	// against files at any depth.
	res := run(t, root, Query{Pattern: "target", PathGlob: "*.go"}, nil)
	if len(res.Hits) != 2 {
		t.Errorf("*.go should match both Go files: %s", files(res))
	}
	// A path pattern is matched against the full relative path.
	res = run(t, root, Query{Pattern: "target", PathGlob: "internal/*.go"}, nil)
	if len(res.Hits) != 1 || res.Hits[0].File != "internal/c.go" {
		t.Errorf("a path glob should match on the path: %s", files(res))
	}
}

func TestSearchIncludeTests(t *testing.T) {
	root := workspace(t)
	without := run(t, root, Query{Pattern: "Timeout"}, nil)
	with := run(t, root, Query{Pattern: "Timeout", IncludeTests: true}, nil)

	if hasFile(without, "main_test.go") {
		t.Error("tests are excluded by default")
	}
	if !hasFile(with, "main_test.go") {
		t.Errorf("include_tests should bring them back: %s", files(with))
	}
}

func TestIsTestFile(t *testing.T) {
	for _, rel := range []string{
		"a_test.go", "test_a.py", "a_test.py", "a_test.rs",
		"a.test.ts", "a.spec.js", "tests/a.go", "__tests__/a.ts",
		"spec/a.rb", "benches/a.rs", "pkg/testdata/a.go",
		"A_TEST.GO",
	} {
		if !IsTestFile(rel) {
			t.Errorf("IsTestFile(%q) should be true", rel)
		}
	}
	for _, rel := range []string{
		"main.go", "src/app.py", "latest.go", "contest.rs", "protest/a.go",
	} {
		if IsTestFile(rel) {
			t.Errorf("IsTestFile(%q) should be false", rel)
		}
	}
}

// TestSearchSkipsBinaryFiles: a match inside a compiled artefact is noise,
// and printing a line of it is worse than noise.
func TestSearchSkipsBinaryFiles(t *testing.T) {
	root := t.TempDir()
	write(t, root, "data.go", "package a\nvar x = \"needle\"\n")
	write(t, root, "blob.md", "needle\x00needle\n")

	res := run(t, root, Query{Pattern: "needle"}, nil)
	if !hasFile(res, "data.go") {
		t.Errorf("the text file should match: %s", files(res))
	}
	if hasFile(res, "blob.md") {
		t.Errorf("a file with a NUL byte is binary: %s", files(res))
	}
}

// TestSearchSkipsOversizeFiles covers the bound that keeps one committed
// dataset from costing more than every real file put together.
func TestSearchSkipsOversizeFiles(t *testing.T) {
	old := maxFileBytes
	maxFileBytes = 64
	t.Cleanup(func() { maxFileBytes = old })

	root := t.TempDir()
	write(t, root, "small.go", "needle\n")
	write(t, root, "big.go", strings.Repeat("needle ", 200)+"\n")

	res := run(t, root, Query{Pattern: "needle"}, nil)
	if !hasFile(res, "small.go") {
		t.Errorf("the small file should match: %s", files(res))
	}
	if hasFile(res, "big.go") {
		t.Error("a file over the size bound should be skipped")
	}
	if !res.Truncated {
		t.Error("skipping a file makes the answer partial, and that must be reported")
	}
}

// TestSearchSurvivesAMinifiedLine: one enormous line must not fail the
// file, or the repository.
func TestSearchSurvivesAMinifiedLine(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "needle\n"+strings.Repeat("x", maxLineBytes*2)+"\nneedle\n")

	res := run(t, root, Query{Pattern: "needle"}, nil)
	// The first line is before the oversize one and must survive; whether
	// the third is reached depends on the scanner, and either is fine.
	if len(res.Hits) == 0 {
		t.Errorf("the readable lines should still match: %s", files(res))
	}
	if res.Hits[0].Line != 1 {
		t.Errorf("first hit at line %d, want 1", res.Hits[0].Line)
	}
}

func TestSearchBoundsHitsAndReportsIt(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 0; i < 50; i++ {
		b.WriteString("needle\n")
	}
	write(t, root, "a.md", b.String())

	res := run(t, root, Query{Pattern: "needle", MaxHits: 5}, nil)
	if len(res.Hits) != 5 {
		t.Errorf("got %d hits, want the cap of 5", len(res.Hits))
	}
	if !res.Truncated {
		t.Error("hitting the cap must be reported, or a partial answer reads as complete")
	}
}

func TestSearchBoundsFilesAndReportsIt(t *testing.T) {
	old := maxFilesPerRepo
	maxFilesPerRepo = 3
	t.Cleanup(func() { maxFilesPerRepo = old })

	root := t.TempDir()
	for i := 0; i < 20; i++ {
		write(t, root, fmt.Sprintf("f%02d.md", i), "needle\n")
	}
	res := run(t, root, Query{Pattern: "needle"}, nil)
	if res.Files > 3 {
		t.Errorf("read %d files, want at most the cap of 3", res.Files)
	}
	if !res.Truncated {
		t.Error("hitting the file cap must be reported")
	}
}

func TestSearchOnlyFirstMatchPerLine(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "needle needle needle\n")
	res := run(t, root, Query{Pattern: "needle"}, nil)
	if len(res.Hits) != 1 {
		t.Errorf("a line is one place to look, got %d hits", len(res.Hits))
	}
	if res.Hits[0].Column != 1 {
		t.Errorf("column = %d, want the first match at 1", res.Hits[0].Column)
	}
}

func TestSearchEmptyAndMissingRoots(t *testing.T) {
	res := run(t, t.TempDir(), Query{Pattern: "anything"}, nil)
	if len(res.Hits) != 0 || res.Files != 0 {
		t.Errorf("an empty workspace yields nothing: %s", files(res))
	}
	if res.Truncated {
		t.Error("an empty workspace is not truncated")
	}

	// A root that does not exist is best-effort: zero files, not an error
	// the caller has to interpret.
	res = run(t, filepath.Join(t.TempDir(), "absent"), Query{Pattern: "x"}, nil)
	if len(res.Hits) != 0 {
		t.Error("a missing root yields nothing")
	}
}

func TestSearchHonoursCancellation(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 200; i++ {
		write(t, root, fmt.Sprintf("f%03d.md", i), "needle\n")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	m, err := Compile(Query{Pattern: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	// A cancelled walk returns the error rather than silently reporting an
	// empty workspace, which would read as "no matches".
	if _, err := SearchRepo(ctx, root, m, nil); err == nil {
		t.Error("a cancelled search should report cancellation")
	}
}

func TestTrimHitText(t *testing.T) {
	if got := trimHitText("   spaced   "); got != "spaced" {
		t.Errorf("trimHitText = %q, want %q", got, "spaced")
	}
	long := strings.Repeat("a", maxHitTextBytes+50)
	got := trimHitText(long)
	if len(got) > maxHitTextBytes+len("…") {
		t.Errorf("trimHitText returned %d bytes", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Error("a truncated line should say so")
	}

	// Cutting must land on a rune boundary, or the result is invalid
	// UTF-8 and a JSON encoder will mangle it.
	runes := strings.Repeat("é", maxHitTextBytes)
	cut := trimHitText(runes)
	for _, r := range cut {
		if r == '\uFFFD' {
			t.Fatal("truncation split a rune")
		}
	}
}

func TestClampWorkers(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{-4, 1}, {0, 1}, {1, 1}, {8, 8},
		{maxSearchWorkers, maxSearchWorkers},
		{maxSearchWorkers + 1, maxSearchWorkers},
		{4096, maxSearchWorkers},
	} {
		if got := clampWorkers(tc.in); got != tc.want {
			t.Errorf("clampWorkers(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestSearchWorkersIsBounded(t *testing.T) {
	if n := searchWorkers(); n < 1 || n > maxSearchWorkers {
		t.Errorf("searchWorkers = %d, want within [1, %d]", n, maxSearchWorkers)
	}
}

// TestSearchAgreesWithASingleWorker pins that the pool size is an
// optimisation and not a behaviour switch.
func TestSearchAgreesWithASingleWorker(t *testing.T) {
	root := workspace(t)
	pooled := run(t, root, Query{Pattern: "Timeout"}, nil)

	old := searchWorkers
	searchWorkers = func() int { return 1 }
	t.Cleanup(func() { searchWorkers = old })

	serial := run(t, root, Query{Pattern: "Timeout"}, nil)
	if len(pooled.Hits) != len(serial.Hits) {
		t.Fatalf("pooled found %d, serial found %d", len(pooled.Hits), len(serial.Hits))
	}
	for i := range serial.Hits {
		if pooled.Hits[i] != serial.Hits[i] {
			t.Errorf("hit %d differs:\n pooled %+v\n serial %+v", i, pooled.Hits[i], serial.Hits[i])
		}
	}
}

func TestMatcherAccessors(t *testing.T) {
	m, err := Compile(Query{Pattern: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if m.MaxHits() != DefaultMaxHits {
		t.Errorf("MaxHits = %d, want the default %d", m.MaxHits(), DefaultMaxHits)
	}
	if m.IncludeTests() {
		t.Error("tests are excluded by default")
	}
	if got := m.MatchLine("no match here"); got != -1 {
		t.Errorf("MatchLine on a non-match = %d, want -1", got)
	}
}

func TestMatchGlobRejectsABadPattern(t *testing.T) {
	// filepath.Match returns an error for an unterminated bracket; a bad
	// glob must exclude rather than panic or match everything.
	if matchGlob("[", "a.go") {
		t.Error("a malformed glob should not match")
	}
}

func TestSkipDir(t *testing.T) {
	for _, d := range []string{".git", "node_modules", "vendor", "target", "__pycache__", ".terraform"} {
		if !skipDir(d) {
			t.Errorf("skipDir(%q) should be true", d)
		}
	}
	if skipDir("internal") {
		t.Error("skipDir(internal) should be false")
	}
}

// TestSearchAcceptsANilContext covers the caller that has no context to
// give — an embedder, or a test.
func TestSearchAcceptsANilContext(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.md", "needle\n")
	m, err := Compile(Query{Pattern: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	// staticcheck runs standalone in CI, where //nolint has no effect —
	// only //lint:ignore does. Passing nil is the case under test.
	//lint:ignore SA1012 a nil context is exactly what this asserts is handled
	res, err := SearchRepo(nil, root, m, nil) //nolint:staticcheck // see above
	if err != nil {
		t.Fatalf("a nil context should be treated as Background: %v", err)
	}
	if len(res.Hits) != 1 {
		t.Errorf("expected one hit, got %s", files(res))
	}
}

// TestSearchCapsAcrossFiles covers the pool-wide budget, as distinct from
// one file filling it: without it, every worker fills its own cap and a
// query matching everything returns workers×cap hits.
func TestSearchCapsAcrossFiles(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 60; i++ {
		write(t, root, fmt.Sprintf("f%02d.md", i), "needle\n")
	}
	res := run(t, root, Query{Pattern: "needle", MaxHits: 4}, nil)
	if len(res.Hits) > 4 {
		t.Errorf("got %d hits, want at most the cap of 4: %s", len(res.Hits), files(res))
	}
	if !res.Truncated {
		t.Error("stopping at the cap must be reported")
	}
}

// TestSearchFileHandlesAVanishedFile: a file can be deleted between the
// walk that listed it and the read that opens it, and that must not fail
// the search.
func TestSearchFileHandlesAVanishedFile(t *testing.T) {
	m, err := Compile(Query{Pattern: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	hits, more := searchFile(t.TempDir(), "gone.md", m)
	if hits != nil || more {
		t.Errorf("a missing file yields nothing, got %v %v", hits, more)
	}
}

// TestTrimHitTextCutsOnARuneBoundary uses a three-byte rune so the cut
// offset does not land on a boundary by arithmetic accident.
func TestTrimHitTextCutsOnARuneBoundary(t *testing.T) {
	// "中" is three bytes; maxHitTextBytes is not a multiple of three, so
	// the naive cut lands mid-rune.
	s := strings.Repeat("中", maxHitTextBytes)
	got := trimHitText(s)
	if !strings.HasSuffix(got, "…") {
		t.Fatal("a truncated line should say so")
	}
	body := strings.TrimSuffix(got, "…")
	for _, r := range body {
		if r == '�' {
			t.Fatal("truncation split a rune")
		}
	}
	if len(body)%3 != 0 {
		t.Errorf("cut at %d bytes, which is not a rune boundary", len(body))
	}
}

// TestSearchSkipsAnUnreadableDirectory: one subtree that cannot be read is
// not a reason to abandon a walk over thousands of files.
func TestSearchSkipsAnUnreadableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions do not gate traversal on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root traverses a 0o000 directory regardless of its mode")
	}
	root := t.TempDir()
	write(t, root, "visible.md", "needle\n")
	locked := filepath.Join(root, "locked")
	if err := os.MkdirAll(locked, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "hidden.md"), []byte("needle\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	// Restored so t.TempDir's cleanup can remove it. A directory needs its
	// execute bit to be traversable at all, which is why this is 0o700 and
	// not the 0o600 gosec's G302 asks for — that rule is about files.
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) // #nosec G302 -- a directory must be traversable to be removed

	res := run(t, root, Query{Pattern: "needle"}, nil)
	if !hasFile(res, "visible.md") {
		t.Errorf("the readable file should still match: %s", files(res))
	}
}

// TestCaseInsensitiveColumnIsAnOffsetIntoTheOriginal.
//
// The case-insensitive path used to lowercase the line and search that,
// then report the offset it found — but lowercasing can change a string's
// byte length, so the offset was into a string the caller never sees. On
// the line below the reported column was 4 where the match is at 7.
//
// It now compiles the literal to a quoted case-insensitive regex, which
// searches the original. That also removed an allocation per line that
// scaled with the line: 4864 B/op on a 4 KB line, against 16 B/op now.
func TestCaseInsensitiveColumnIsAnOffsetIntoTheOriginal(t *testing.T) {
	m, err := Compile(Query{Pattern: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	// U+0130 lowercases to two code points, so the lowercased copy is
	// longer than the original and every offset past it is shifted.
	line := "İİİ needle"
	if got, want := m.MatchLine(line), len("İİİ "); got != want {
		t.Errorf("MatchLine = %d, want %d — the column must index the original line", got, want)
	}
}

// TestCaseInsensitiveLiteralIsNotARegex: the pattern is quoted before it
// is compiled, so a literal containing regex metacharacters still matches
// itself rather than being interpreted.
func TestCaseInsensitiveLiteralIsNotARegex(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "x := cfg[\"a.b\"]\nfunc aXb() {}\n")

	// "a.b" as a regex would match "aXb". As a literal it must not.
	res := run(t, root, Query{Pattern: "A.B"}, nil)
	if len(res.Hits) != 1 {
		t.Fatalf("expected exactly the literal match, got %s", files(res))
	}
	if res.Hits[0].Line != 1 {
		t.Errorf("matched line %d, want 1 — the dot was treated as a wildcard", res.Hits[0].Line)
	}

	// And with Regex set, the same pattern is a pattern again.
	res = run(t, root, Query{Pattern: "a.b", Regex: true}, nil)
	if len(res.Hits) != 2 {
		t.Errorf("as a regex it should match both lines, got %s", files(res))
	}
}

// TestSearchRepoTruncatesAggregateAcrossWorkers pins the one branch in
// SearchRepo whose execution depended on how the scheduler happened to
// interleave the workers.
//
// Each worker stops once its own slice reaches MaxHits, so the aggregate can
// only exceed MaxHits when several workers contribute before any of them
// trips that cap. Whether that happened was a matter of timing: under -race
// the scheduler serialises enough that one worker usually reached the cap
// first, and the aggregate truncation below the wait never ran. Coverage
// therefore read 100% without -race and 99.94% with it, and
// scripts/claims_check.go could not see the difference because
// `go tool cover` rounds to one decimal — 4874 of 4875 statements still
// prints as 100.0%.
//
// The shape here removes the timing from the question. Eight files carry
// three matches each against a cap of ten, so:
//
//   - if the work spreads, four workers take two files each, none reaches
//     ten on its own, all twenty-four hits are collected;
//   - if one worker takes everything, it stops after the fourth file with
//     twelve;
//
// and twelve and twenty-four are both greater than ten. There is no
// interleaving that leaves the aggregate at or below the cap, so the branch
// runs on every execution rather than on most of them.
func TestSearchRepoTruncatesAggregateAcrossWorkers(t *testing.T) {
	const (
		maxHits        = 10
		fileCount      = 8
		matchesPerFile = 3
	)

	old := searchWorkers
	searchWorkers = func() int { return 4 }
	t.Cleanup(func() { searchWorkers = old })

	root := t.TempDir()
	for i := range fileCount {
		var b strings.Builder
		for j := range matchesPerFile {
			// Padding lines so a hit is never the whole file, and so the
			// line numbers differ between matches within one file.
			fmt.Fprintf(&b, "package p%d\n", i)
			fmt.Fprintf(&b, "// needle %d-%d\n", i, j)
		}
		write(t, root, fmt.Sprintf("pkg%d/file.go", i), b.String())
	}

	res := run(t, root, Query{Pattern: "needle", MaxHits: maxHits}, nil)

	// The aggregate was cut back to the cap, which is the branch under test.
	if got := len(res.Hits); got != maxHits {
		t.Errorf("len(Hits) = %d, want exactly the cap %d — the aggregate "+
			"truncation did not run (files: %s)", got, maxHits, files(res))
	}
	// A result that dropped hits must say so, or a caller pages through a
	// list believing it has seen everything.
	if !res.Truncated {
		t.Error("Truncated = false after the aggregate was cut to the cap")
	}

	// The invariant that has to hold however the workers interleave: the
	// result never carries more than the cap, and stays ordered so two
	// identical searches agree.
	if len(res.Hits) > maxHits {
		t.Fatalf("len(Hits) = %d exceeds the cap %d", len(res.Hits), maxHits)
	}
	for i := 1; i < len(res.Hits); i++ {
		a, b := res.Hits[i-1], res.Hits[i]
		if a.File > b.File || (a.File == b.File && a.Line >= b.Line) {
			t.Fatalf("hits out of order at %d: %s:%d then %s:%d",
				i, a.File, a.Line, b.File, b.Line)
		}
	}
}

// TestMatchBytesAgreesWithMatchLine pins the two forms to the same answer.
//
// MatchBytes exists only to avoid allocating a string per line, so any
// difference between it and MatchLine is a bug by definition — and one that
// would show up as a content search quietly missing or inventing matches,
// which no other test in this package would catch.
func TestMatchBytesAgreesWithMatchLine(t *testing.T) {
	lines := []string{
		"", "needle", "a needle here", "NEEDLE", "nee\ndle",
		"the needle and the needle", "haystack", "  needle  ",
		"日本語 needle unicode", "needle at end",
	}
	for _, q := range []Query{
		{Pattern: "needle", CaseSensitive: true},
		{Pattern: "needle"},               // case-insensitive: compiled to a regex
		{Pattern: "need.e", Regex: true},  // regex
		{Pattern: "^needle", Regex: true}, // anchored
		{Pattern: "NEEDLE", CaseSensitive: true},
	} {
		q.MaxHits = 10
		m, err := Compile(q)
		if err != nil {
			t.Fatalf("compile %+v: %v", q, err)
		}
		for _, line := range lines {
			want := m.MatchLine(line)
			got := m.MatchBytes([]byte(line))
			if got != want {
				t.Errorf("pattern %q line %q: MatchBytes=%d MatchLine=%d",
					q.Pattern, line, got, want)
			}
		}
	}
}
