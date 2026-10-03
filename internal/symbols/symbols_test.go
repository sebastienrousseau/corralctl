// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package symbols

import (
	"context"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// find returns the first symbol with the given qualified name, for readable
// assertions.
func find(t *testing.T, syms []Symbol, qualified string) Symbol {
	t.Helper()
	for _, s := range syms {
		if s.Qualified() == qualified {
			return s
		}
	}
	t.Fatalf("no symbol %q in %v", qualified, names(syms))
	return Symbol{}
}

func names(syms []Symbol) []string {
	out := make([]string, 0, len(syms))
	for _, s := range syms {
		out = append(out, s.Qualified())
	}
	return out
}

const sample = `package demo

import "fmt"

// Doc comment.
type Client struct{ n int }

type Reader interface{ Read() error }

type alias = Client

const (
	Alpha = 1
	beta  = 2
)

const Solo = "x"

var (
	Exported   = 1
	unexported = 2
	_          = 3
)

func Top() {}

func lower() {}

func (c Client) Value() {}

func (c *Client) Pointer() {}

func (c *Generic[T]) OneParam() {}

func (p *Pair[K, V]) TwoParams() {}

func (Client) Anonymous() {}

func _() {}

var _ = fmt.Sprintf
`

func TestGoExtractorFindsEveryKind(t *testing.T) {
	syms, err := goExtractor{}.Extract("demo.go", []byte(sample))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}

	cases := []struct {
		qualified string
		kind      Kind
		exported  bool
		receiver  string
	}{
		{"Client", KindType, true, ""},
		{"Reader", KindInterface, true, ""},
		{"alias", KindType, false, ""},
		{"Alpha", KindConst, true, ""},
		{"beta", KindConst, false, ""},
		{"Solo", KindConst, true, ""},
		{"Exported", KindVar, true, ""},
		{"unexported", KindVar, false, ""},
		{"Top", KindFunc, true, ""},
		{"lower", KindFunc, false, ""},
		{"Client.Value", KindMethod, true, "Client"},
		{"Client.Pointer", KindMethod, true, "Client"},
		{"Generic.OneParam", KindMethod, true, "Generic"},
		{"Pair.TwoParams", KindMethod, true, "Pair"},
		{"Client.Anonymous", KindMethod, true, "Client"},
	}
	for _, tc := range cases {
		t.Run(tc.qualified, func(t *testing.T) {
			s := find(t, syms, tc.qualified)
			if s.Kind != tc.kind {
				t.Errorf("kind = %q, want %q", s.Kind, tc.kind)
			}
			if s.Exported != tc.exported {
				t.Errorf("exported = %v, want %v", s.Exported, tc.exported)
			}
			if s.Receiver != tc.receiver {
				t.Errorf("receiver = %q, want %q", s.Receiver, tc.receiver)
			}
			if s.Language != "go" {
				t.Errorf("language = %q, want go", s.Language)
			}
			if s.Line <= 0 {
				t.Errorf("line = %d, want a positive line", s.Line)
			}
		})
	}

	// The blank identifier declares nothing anyone can look up, in any form.
	for _, s := range syms {
		if s.Name == "_" {
			t.Errorf("blank identifier was indexed as %+v", s)
		}
	}
}

// TestGoExtractorLinesArePerName pins that each name in a grouped
// declaration reports its own line, not the group's.
func TestGoExtractorLinesArePerName(t *testing.T) {
	src := "package p\n\nconst (\n\tA = 1\n\tB = 2\n\tC = 3\n)\n"
	syms, err := goExtractor{}.Extract("p.go", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"A": 4, "B": 5, "C": 6}
	for name, line := range want {
		if got := find(t, syms, name).Line; got != line {
			t.Errorf("%s is on line %d, want %d", name, got, line)
		}
	}
}

// TestGoExtractorToleratesBrokenSource is the property that keeps one
// mid-edit file from blanking a repository's index.
func TestGoExtractorToleratesBrokenSource(t *testing.T) {
	broken := "package p\n\nfunc Good() {}\n\nfunc Bad( {\n"
	syms, err := goExtractor{}.Extract("p.go", []byte(broken))
	if err != nil {
		t.Fatalf("a parse error must not be returned as an error: %v", err)
	}
	// Whatever the parser salvaged is worth having; Good precedes the break.
	found := false
	for _, s := range syms {
		if s.Name == "Good" {
			found = true
		}
	}
	if !found {
		t.Errorf("declarations before the syntax error were lost: %v", names(syms))
	}

	// Source with nothing salvageable returns nothing, still without error.
	syms, err = goExtractor{}.Extract("p.go", []byte("\x00\x01 not go at all"))
	if err != nil {
		t.Errorf("unparseable input must not error: %v", err)
	}
	if len(syms) != 0 {
		t.Errorf("expected no symbols, got %v", names(syms))
	}
}

func TestGoExtractorMarksTestFiles(t *testing.T) {
	src := "package p\n\nfunc TestThing() {}\n"
	syms, err := goExtractor{}.Extract("thing_test.go", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if !find(t, syms, "TestThing").Test {
		t.Error("a declaration in _test.go must be marked as a test")
	}

	syms, _ = goExtractor{}.Extract("thing.go", []byte(src))
	if find(t, syms, "TestThing").Test {
		t.Error("a declaration outside _test.go must not be marked as a test")
	}
}

func TestQualified(t *testing.T) {
	if got := (Symbol{Name: "Do"}).Qualified(); got != "Do" {
		t.Errorf("Qualified = %q, want Do", got)
	}
	if got := (Symbol{Name: "Do", Receiver: "Client"}).Qualified(); got != "Client.Do" {
		t.Errorf("Qualified = %q, want Client.Do", got)
	}
}

func TestParseKind(t *testing.T) {
	for _, k := range AllKinds {
		got, err := ParseKind(string(k))
		if err != nil || got != k {
			t.Errorf("ParseKind(%q) = (%q, %v)", k, got, err)
		}
	}
	if got, err := ParseKind("  FUNC "); err != nil || got != KindFunc {
		t.Errorf("ParseKind should trim and lowercase: (%q, %v)", got, err)
	}
	// An unsupported kind is an error, never a silent no-match.
	err := func() error { _, e := ParseKind("macro"); return e }()
	if err == nil {
		t.Fatal("expected an error for an unknown kind")
	}
	for _, want := range []string{"macro", "func", "interface"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name %q", err, want)
		}
	}
}

func TestExtractorForAndLanguages(t *testing.T) {
	if _, ok := ExtractorFor("main.go"); !ok {
		t.Error(".go should have an extractor")
	}
	if _, ok := ExtractorFor("deep/dir/MAIN.GO"); !ok {
		t.Error("extension matching should be case-insensitive")
	}
	if _, ok := ExtractorFor("Makefile"); ok {
		t.Error("a file with no extension should have no extractor")
	}
	if _, ok := ExtractorFor("script.rb"); ok {
		t.Error("an unindexed extension should have no extractor")
	}
	want := []string{"c", "cpp", "go", "javascript", "protobuf", "python", "rust", "typescript"}
	got := Languages()
	if len(got) != len(want) {
		t.Fatalf("Languages = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Languages = %v, want %v (sorted)", got, want)
		}
	}
}

// TestRegisterRejectsDuplicateExtensions covers the guard that stops two
// extractors silently fighting over a file type.
func TestRegisterRejectsDuplicateExtensions(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("registering a duplicate extension should panic")
		}
	}()
	register(goExtractor{})
}

func TestSortIsDeterministic(t *testing.T) {
	in := []Symbol{
		{Name: "b", File: "z.go", Line: 1},
		{Name: "a", File: "a.go", Line: 9},
		{Name: "c", File: "a.go", Line: 2},
		{Name: "b", File: "a.go", Line: 2, Receiver: "T"},
	}
	Sort(in)
	// a.go:2 holds both "T.b" and "c"; the tie breaks on the qualified
	// name, and "T" sorts before "c" because uppercase precedes lowercase.
	want := []string{"T.b", "c", "a", "b"}
	if got := names(in); len(got) != len(want) {
		t.Fatalf("got %v", got)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("sorted = %v, want %v", got, want)
			}
		}
	}
}

func TestQueryMatch(t *testing.T) {
	fn := Symbol{Name: "Parse", Kind: KindFunc, Exported: true}
	method := Symbol{Name: "Do", Kind: KindMethod, Receiver: "Client", Exported: true}
	priv := Symbol{Name: "parse", Kind: KindFunc}
	testSym := Symbol{Name: "TestParse", Kind: KindFunc, Exported: true, Test: true}

	cases := []struct {
		name string
		q    Query
		s    Symbol
		want bool
	}{
		{"exact, case-insensitive", Query{Name: "parse"}, fn, true},
		{"exact miss", Query{Name: "par"}, fn, false},
		{"substring", Query{Name: "ars", Substring: true}, fn, true},
		{"substring miss", Query{Name: "zzz", Substring: true}, fn, false},
		{"empty name matches all", Query{}, fn, true},
		{"kind filter hit", Query{Name: "parse", Kind: KindFunc}, fn, true},
		{"kind filter miss", Query{Name: "parse", Kind: KindType}, fn, false},
		{"method by bare name", Query{Name: "do"}, method, true},
		{"method by qualified name", Query{Name: "Client.Do"}, method, true},
		{"exported only excludes private", Query{Name: "parse", ExportedOnly: true}, priv, false},
		{"exported only keeps exported", Query{Name: "parse", ExportedOnly: true}, fn, true},
		{"tests excluded by default", Query{Name: "TestParse"}, testSym, false},
		{"tests included on request", Query{Name: "TestParse", IncludeTests: true}, testSym, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.q.Match(tc.s); got != tc.want {
				t.Errorf("Match = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- repository walk ---

func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestExtractRepoSkipsNoise(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"main.go":                 "package main\n\nfunc Real() {}\n",
		"pkg/lib.go":              "package pkg\n\nfunc AlsoReal() {}\n",
		"vendor/dep/dep.go":       "package dep\n\nfunc Vendored() {}\n",
		"node_modules/m/m.go":     "package m\n\nfunc Noded() {}\n",
		"testdata/fixture.go":     "package fixture\n\nfunc Fixtured() {}\n",
		".git/hooks/hook.go":      "package hook\n\nfunc Hooked() {}\n",
		"api/service.pb.go":       "package api\n\nfunc Generated() {}\n",
		"internal/kind_string.go": "package internal\n\nfunc Stringer() {}\n",
		"README.md":               "# not source",
		"build/artifact/gen.go":   "package artifact\n\nfunc Built() {}\n",
	})

	res, err := ExtractRepo(context.Background(), root)
	if err != nil {
		t.Fatalf("ExtractRepo: %v", err)
	}

	got := map[string]bool{}
	for _, s := range res.Symbols {
		got[s.Name] = true
	}
	for _, want := range []string{"Real", "AlsoReal"} {
		if !got[want] {
			t.Errorf("%s should have been indexed", want)
		}
	}
	for _, unwanted := range []string{"Vendored", "Noded", "Fixtured", "Hooked", "Generated", "Stringer", "Built"} {
		if got[unwanted] {
			t.Errorf("%s should have been skipped", unwanted)
		}
	}
	if res.Files != 2 {
		t.Errorf("parsed %d files, want 2", res.Files)
	}
	if res.Truncated {
		t.Error("a small repository should not report truncation")
	}
}

func TestExtractRepoIsOrderStable(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 40; i++ {
		files[filepath.Join("pkg", string(rune('a'+i%26))+"_"+itoa(i)+".go")] =
			"package pkg\n\nfunc F" + itoa(i) + "() {}\n"
	}
	root := writeRepo(t, files)

	first, err := ExtractRepo(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for run := 0; run < 5; run++ {
		again, err := ExtractRepo(context.Background(), root)
		if err != nil {
			t.Fatal(err)
		}
		if len(again.Symbols) != len(first.Symbols) {
			t.Fatalf("run %d returned %d symbols, first returned %d", run, len(again.Symbols), len(first.Symbols))
		}
		for i := range again.Symbols {
			if again.Symbols[i] != first.Symbols[i] {
				t.Fatalf("run %d differs at %d: %+v vs %+v", run, i, again.Symbols[i], first.Symbols[i])
			}
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestExtractRepoEmptyAndMissing(t *testing.T) {
	empty := t.TempDir()
	res, err := ExtractRepo(context.Background(), empty)
	if err != nil {
		t.Fatalf("an empty repository is not an error: %v", err)
	}
	if len(res.Symbols) != 0 || res.Files != 0 {
		t.Errorf("empty repository yielded %+v", res)
	}

	// A root that does not exist yields an empty result rather than a
	// failure the agent has to handle.
	res, err = ExtractRepo(context.Background(), filepath.Join(empty, "nope"))
	if err != nil {
		t.Errorf("a missing root should not error: %v", err)
	}
	if res != nil && len(res.Symbols) != 0 {
		t.Errorf("missing root yielded symbols: %+v", res)
	}
}

func TestExtractRepoNilContext(t *testing.T) {
	root := writeRepo(t, map[string]string{"a.go": "package a\n\nfunc A() {}\n"})
	//nolint:staticcheck // SA1012: a nil context is what this guards.
	//lint:ignore SA1012 passing nil is the behaviour under test
	res, err := ExtractRepo(nil, root)
	if err != nil || len(res.Symbols) != 1 {
		t.Fatalf("nil context should be accepted: %+v %v", res, err)
	}
}

func TestExtractRepoHonoursCancellation(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < 50; i++ {
		files["pkg/f"+itoa(i)+".go"] = "package pkg\n\nfunc F" + itoa(i) + "() {}\n"
	}
	root := writeRepo(t, files)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ExtractRepo(ctx, root); err == nil {
		t.Error("a cancelled context should abort the extraction")
	}
}

func TestExtractRepoSkipsOversizeFiles(t *testing.T) {
	big := "package p\n\nfunc Huge() {}\n" + strings.Repeat("// filler\n", (maxFileBytes/10)+10)
	root := writeRepo(t, map[string]string{
		"big.go":   big,
		"small.go": "package p\n\nfunc Small() {}\n",
	})
	res, err := ExtractRepo(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range res.Symbols {
		if s.Name == "Huge" {
			t.Error("a file over the size cap should be skipped")
		}
	}
	if len(res.Symbols) != 1 || res.Symbols[0].Name != "Small" {
		t.Errorf("expected only Small, got %v", names(res.Symbols))
	}
}

func TestExtractRepoTruncatesAtTheSymbolCap(t *testing.T) {
	old := maxSymbolsPerRepoVar
	maxSymbolsPerRepoVar = 3
	t.Cleanup(func() { maxSymbolsPerRepoVar = old })

	root := writeRepo(t, map[string]string{
		"a.go": "package p\n\nfunc A() {}\nfunc B() {}\nfunc C() {}\nfunc D() {}\nfunc E() {}\n",
	})
	res, err := ExtractRepo(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Error("hitting the symbol cap must be reported as truncation")
	}
	if len(res.Symbols) != 3 {
		t.Errorf("got %d symbols, want the cap of 3", len(res.Symbols))
	}
}

func TestExtractRepoTruncatesAtTheFileCap(t *testing.T) {
	old := maxFilesPerRepoVar
	maxFilesPerRepoVar = 2
	t.Cleanup(func() { maxFilesPerRepoVar = old })

	root := writeRepo(t, map[string]string{
		"a.go": "package p\n\nfunc A() {}\n",
		"b.go": "package p\n\nfunc B() {}\n",
		"c.go": "package p\n\nfunc C() {}\n",
		"d.go": "package p\n\nfunc D() {}\n",
	})
	res, err := ExtractRepo(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Error("hitting the file cap must be reported as truncation")
	}
	if res.Files > 2 {
		t.Errorf("parsed %d files, want at most the cap of 2", res.Files)
	}
}

// TestParseOneSurvivesAnUnreadableFile covers the read-error branch.
//
// A directory named like a source file is the portable way to make
// os.ReadFile fail while os.Stat succeeds: chmod(0o000) does not work on
// Windows, which honours only a read-only bit and left the file perfectly
// readable — this test failed there for exactly that reason.
//
// parseOne is called directly because the walk skips directories, so
// ExtractRepo would never hand this path to it.
func TestParseOneSurvivesAnUnreadableFile(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "directory.go"), 0o750); err != nil {
		t.Fatal(err)
	}
	if got := parseOne(root, "directory.go"); got != nil {
		t.Errorf("an unreadable path should contribute nothing, got %v", names(got))
	}
}

func TestWalkWorkersIsAtLeastOne(t *testing.T) {
	if n := walkWorkers(); n < 1 {
		t.Errorf("walkWorkers = %d, want at least 1", n)
	}
	if got := clampWalkWorkers(0); got != 1 {
		t.Errorf("clampWalkWorkers(0) = %d, want 1", got)
	}
	if got := clampWalkWorkers(-4); got != 1 {
		t.Errorf("clampWalkWorkers(-4) = %d, want 1", got)
	}
	if got := clampWalkWorkers(7); got != 7 {
		t.Errorf("clampWalkWorkers(7) = %d, want 7", got)
	}
}

func TestSkipDirAndSkipSourceFile(t *testing.T) {
	for _, d := range []string{".git", "node_modules", "vendor", "testdata", "dist", "target"} {
		if !skipDir(d) {
			t.Errorf("skipDir(%q) should be true", d)
		}
	}
	if skipDir("internal") {
		t.Error("skipDir(internal) should be false")
	}
	skipped := []string{
		// path segments, in every language
		"vendor/x.go", "a/testdata/b.go", "__pycache__/m.py", "node_modules/lib/i.js",
		// generated, per language
		"x.pb.go", "y_string.go", "z.gen.go", "api.pb.gw.go", "m_generated.go",
		"svc_pb2.py", "svc_pb2_grpc.py", "svc_pb2.pyi",
		"app.min.js", "app.bundle.js", "t.generated.ts", "t.gen.ts", "m.min.mjs",
		"proto.pb.rs",
		// and the check is case-insensitive, because filesystems are
		"VENDOR/X.GO", "App.Min.JS",
	}
	for _, f := range skipped {
		if !skipSourceFile(f) {
			t.Errorf("skipSourceFile(%q) should be true", f)
		}
	}
	kept := []string{
		"internal/mcp/index.go", "src/app/main.py", "src/index.ts",
		"src/lib.rs", "packages/ui/Button.tsx",
		// near-misses: these are hand-written despite looking generated
		"stringer.go", "generated_docs.md.go", "minify.js",
	}
	for _, f := range kept {
		if skipSourceFile(f) {
			t.Errorf("skipSourceFile(%q) should be false", f)
		}
	}
}

// --- branches the happy path cannot reach ---

// TestGoExtractorSurvivesEveryShapeOfGarbage pins the tolerance the walk
// depends on: no input may produce an error, because one unparseable file
// must never blank a repository's index.
func TestGoExtractorSurvivesEveryShapeOfGarbage(t *testing.T) {
	garbage := map[string]string{
		"binary":      "\xff\xfe\x00",
		"empty":       "",
		"no package":  "func F() {}\n",
		"many errors": "package p\n" + strings.Repeat("func (((\n", 200),
	}
	for name, src := range garbage {
		t.Run(name, func(t *testing.T) {
			if _, err := (goExtractor{}).Extract("x.go", []byte(src)); err != nil {
				t.Errorf("Extract must not error: %v", err)
			}
		})
	}
}

// TestTypeNameExoticReceivers covers the receiver shapes the common path
// never produces, including the one that is not legal Go but costs nothing
// to keep total.
func TestTypeNameExoticReceivers(t *testing.T) {
	// A selector receiver is not legal Go, so it is asserted directly.
	if got := typeName(&ast.SelectorExpr{
		X:   &ast.Ident{Name: "pkg"},
		Sel: &ast.Ident{Name: "Type"},
	}); got != "Type" {
		t.Errorf("selector receiver = %q, want Type", got)
	}
	// An expression that is none of the handled shapes yields no receiver,
	// which downgrades the declaration to a plain func rather than
	// producing a method with an empty receiver.
	if got := typeName(&ast.BasicLit{Kind: token.INT, Value: "1"}); got != "" {
		t.Errorf("unhandled receiver expression = %q, want empty", got)
	}
	// An empty receiver list is not a method.
	if got := receiverName(&ast.FieldList{}); got != "" {
		t.Errorf("empty receiver list = %q, want empty", got)
	}
	if got := receiverName(nil); got != "" {
		t.Errorf("nil receiver list = %q, want empty", got)
	}
}

// TestExtractRepoUnreadableSubtree covers the walk's error branch: an
// unreadable directory is skipped, not fatal.
//
// Meaningful on Unix only. Windows honours a read-only bit rather than
// POSIX permissions, so the chmod is a no-op there and the test degrades
// to asserting the readable half still indexes — which is true either way.
// Coverage is measured on Linux, where the branch is genuinely taken.
func TestExtractRepoUnreadableSubtree(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"ok.go":           "package p\n\nfunc OK() {}\n",
		"locked/inner.go": "package q\n\nfunc Inner() {}\n",
	})
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Skipf("cannot chmod on this platform: %v", err)
	}
	// #nosec G302 -- restoring a directory, which needs its execute bit to
	// be traversable again for the temp-dir cleanup to succeed.
	t.Cleanup(func() { _ = os.Chmod(locked, 0o750) })

	res, err := ExtractRepo(context.Background(), root)
	if err != nil {
		t.Fatalf("an unreadable subtree must not fail the walk: %v", err)
	}
	found := false
	for _, s := range res.Symbols {
		if s.Name == "OK" {
			found = true
		}
	}
	if !found {
		t.Error("the readable part of the tree should still be indexed")
	}
}

// TestParseOneRejectsUnclaimedExtension covers the defensive check in
// parseOne: discover only queues claimed files, so this is belt and braces.
func TestParseOneRejectsUnclaimedExtension(t *testing.T) {
	root := writeRepo(t, map[string]string{"notes.txt": "hello"})
	if got := parseOne(root, "notes.txt"); got != nil {
		t.Errorf("an unclaimed extension should yield nothing, got %v", names(got))
	}
	if got := parseOne(root, "missing.go"); got != nil {
		t.Errorf("a missing file should yield nothing, got %v", names(got))
	}
}

// TestExtractRepoSingleWorker exercises the clamp that stops the pool
// exceeding the file count.
func TestExtractRepoSingleWorker(t *testing.T) {
	old := walkWorkers
	walkWorkers = func() int { return 64 }
	t.Cleanup(func() { walkWorkers = old })

	root := writeRepo(t, map[string]string{"only.go": "package p\n\nfunc Only() {}\n"})
	res, err := ExtractRepo(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Symbols) != 1 || res.Symbols[0].Name != "Only" {
		t.Errorf("got %v", names(res.Symbols))
	}
}
