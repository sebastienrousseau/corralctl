// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package search

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

// indexTree writes a tree that exercises what the index has to reason about:
// ordinary source, a file where the term appears only in a comment, UTF-8, a
// binary file, a file too short to have a trigram, and a file with a term that
// shares trigrams with the query without containing it.
func indexTree(tb testing.TB) string {
	tb.Helper()
	root := tb.TempDir()
	write := func(name, content string) {
		tb.Helper()
		full := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			tb.Fatal(err)
		}
	}
	write("a.go", "package a\n\nfunc Needle() int { return 1 }\n")
	write("b.go", "package b\n\n// needle appears only in this comment\nfunc Other() {}\n")
	write("sub/c.go", "package c\n\nfunc NeedleHaystack() {}\nfunc haystack() {}\n")
	write("d.md", "# Notes\n\nnothing of interest here\n")
	write("utf8.go", "package u\n\n// café naïve — ünïcödé\nconst K = \"K\"\n")
	write("short.txt", "hi")
	write("near.go", "package n\n\n// need1e and neeedle and nedle\n")
	write("bin.dat", "\x00\x01\x02binary needle inside\x00")
	write("e_test.go", "package a\n\nfunc TestNeedle(t *T) {}\n")
	return root
}

// TestIndexedSearchMatchesExhaustive is the property the whole index rests on.
//
// The index decides which files to open. If it ever drops a file that holds a
// match, the search reports "no matches" and the caller has no way to tell that
// from the truth — a wrong answer delivered with full confidence. So the only
// assertion that matters is that an indexed search and an exhaustive one return
// the same hits, for every query shape, including the ones the index declines
// to narrow.
func TestIndexedSearchMatchesExhaustive(t *testing.T) {
	root := indexTree(t)
	allowed := func(string) bool { return true }

	ix, err := BuildIndex(context.Background(), root, allowed)
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	t.Logf("indexed %d files, %d bytes", ix.Files(), ix.Bytes())

	queries := []Query{
		{Pattern: "Needle", CaseSensitive: true},
		{Pattern: "needle"},
		{Pattern: "NEEDLE"},
		{Pattern: "haystack"},
		{Pattern: "Haystack", CaseSensitive: true},
		{Pattern: "nothing of interest"},
		{Pattern: "café"},
		{Pattern: "K"},
		{Pattern: "zzz-absent-token"},
		{Pattern: "ne"},                       // too short for a trigram
		{Pattern: "nee.le", Regex: true},      // regex: index declines
		{Pattern: "^func", Regex: true},       // anchored regex
		{Pattern: "func", IncludeTests: true}, // test files in scope
		{Pattern: "func"},                     // test files excluded
		{Pattern: "func", PathGlob: "sub/*"},  // path filtered
	}

	for _, q := range queries {
		q.MaxHits = 100
		name := fmt.Sprintf("%s/ci=%v/re=%v/tests=%v/glob=%s",
			q.Pattern, !q.CaseSensitive, q.Regex, q.IncludeTests, q.PathGlob)
		t.Run(name, func(t *testing.T) {
			m, err := Compile(q)
			if err != nil {
				t.Fatalf("compile: %v", err)
			}

			want, err := SearchRepo(context.Background(), root, m, allowed)
			if err != nil {
				t.Fatal(err)
			}

			cands, ok := ix.Candidates(m)
			var got *Result
			if !ok {
				// The index has no opinion, so the caller must search
				// everything — which is what the exhaustive path already did.
				got = want
			} else {
				cands = FilterCandidates(cands, m)
				got, err = SearchRepoPaths(context.Background(), root, m, cands, ix.Truncated())
				if err != nil {
					t.Fatal(err)
				}
			}

			if len(got.Hits) != len(want.Hits) {
				t.Fatalf("indexed found %d hits, exhaustive found %d\nindexed:   %v\nexhaustive: %v",
					len(got.Hits), len(want.Hits), hitList(got), hitList(want))
			}
			for i := range want.Hits {
				if got.Hits[i] != want.Hits[i] {
					t.Errorf("hit %d differs:\n  indexed:    %+v\n  exhaustive: %+v",
						i, got.Hits[i], want.Hits[i])
				}
			}
			if ok {
				t.Logf("read %d of %d files", len(cands), ix.Files())
			}
		})
	}
}

func hitList(r *Result) []string {
	out := make([]string, 0, len(r.Hits))
	for _, h := range r.Hits {
		out = append(out, fmt.Sprintf("%s:%d", h.File, h.Line))
	}
	return out
}

// TestIndexNarrowsMeaningfully checks the index is worth having.
//
// Correctness alone is satisfied by an index that returns every file, so the
// test above would pass against something useless. This pins that a selective
// term actually reads a small fraction of the tree.
func TestIndexNarrowsMeaningfully(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 200; i++ {
		body := fmt.Sprintf("package p%d\n\nfunc Common%d() {}\n", i, i)
		if i == 7 {
			body += "func VeryDistinctiveName() {}\n"
		}
		name := filepath.Join(root, fmt.Sprintf("f%03d.go", i))
		if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	allowed := func(string) bool { return true }
	ix, err := BuildIndex(context.Background(), root, allowed)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Compile(Query{Pattern: "VeryDistinctiveName", MaxHits: 10})
	if err != nil {
		t.Fatal(err)
	}
	cands, ok := ix.Candidates(m)
	if !ok {
		t.Fatal("the index declined to narrow a plain literal")
	}
	if len(cands) > 5 {
		t.Errorf("read %d of %d files for a term in exactly one; the index is not narrowing",
			len(cands), ix.Files())
	}
	if !strings.Contains(strings.Join(cands, " "), "f007.go") {
		t.Errorf("the one file that contains the term is not a candidate: %v", cands)
	}
	t.Logf("narrowed %d files to %d", ix.Files(), len(cands))
}

// TestFoldRiskSetIsComplete recomputes the fold-risk set from Unicode itself.
//
// The index drops a file when its trigrams cannot match. That is sound only
// because matching is a byte comparison — except for case-insensitive queries,
// where Go's (?i) folds Unicode and a rune outside ASCII can match an ASCII
// letter. Every such rune must be in foldRiskSeqs, or a search silently misses
// the file holding it.
//
// Hard-coding two runes from a conversation is exactly the kind of claim that
// rots, so this derives the set the same way the constant was derived and fails
// if a future Unicode table disagrees.
func TestFoldRiskSetIsComplete(t *testing.T) {
	want := map[string]rune{}
	for r := rune(0x80); r <= unicode.MaxRune; r++ {
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < 0x80 && (('a' <= f && f <= 'z') || ('A' <= f && f <= 'Z')) {
				want[string(r)] = r
				break
			}
		}
	}
	got := map[string]bool{}
	for _, seq := range foldRiskSeqs {
		got[string(seq)] = true
	}
	for s, r := range want {
		if !got[s] {
			t.Errorf("U+%04X %q folds to an ASCII letter but is not in foldRiskSeqs; "+
				"a case-insensitive search can now miss files containing it", r, r)
		}
	}
	for s := range got {
		if _, ok := want[s]; !ok {
			t.Errorf("%q is in foldRiskSeqs but folds to nothing in ASCII", s)
		}
	}
	t.Logf("%d runes outside ASCII fold to an ASCII letter", len(want))
}

// TestIndexIgnoresOrdinaryNonASCII pins the fix that made the index worth
// having.
//
// Treating every file with a byte above 0x7F as unreadable-by-index was safe
// and useless: one em dash in a comment put a file beyond the index's reach,
// and nearly every source file has one. A file with an accent and no fold-risk
// rune must be filtered on its trigrams like any other.
func TestIndexIgnoresOrdinaryNonASCII(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 50; i++ {
		body := fmt.Sprintf("package p%d\n\n// a comment — with an em dash and café\nfunc F%d() {}\n", i, i)
		if i == 11 {
			body += "func TheOnlyOne() {}\n"
		}
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%02d.go", i)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ix, err := BuildIndex(context.Background(), root, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	m, err := Compile(Query{Pattern: "TheOnlyOne", MaxHits: 10})
	if err != nil {
		t.Fatal(err)
	}
	cands, ok := ix.Candidates(m)
	if !ok {
		t.Fatal("the index declined a plain literal")
	}
	if len(cands) > 3 {
		t.Errorf("read %d of %d files; ordinary non-ASCII text is defeating the index",
			len(cands), ix.Files())
	}
	t.Logf("narrowed %d files to %d despite every file containing non-ASCII", ix.Files(), len(cands))
}

// TestOneLargeFileDoesNotDisableTheIndex is the regression for the bug that
// cost this feature most of its value.
//
// discover reports two different bounds and they were collapsed into one flag:
// the per-repository file cap, which leaves the file list genuinely incomplete,
// and a single file skipped for being over the size limit, which does not — the
// search skips that file on exactly the same rule, so the index still describes
// everything a search would read.
//
// Because Candidates refuses to narrow a "truncated" index, one oversized file
// — a lockfile, a bundled asset, a fixture — turned the index off for the whole
// repository. Measured on a real workspace, four repositories in that state
// contributed 10,130 of the 10,373 files a query opened: 98% of the work, from
// 2% of the repositories.
func TestOneLargeFileDoesNotDisableTheIndex(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 40; i++ {
		body := fmt.Sprintf("package p%d\n\nfunc Ordinary%d() {}\n", i, i)
		if i == 3 {
			body += "func TheSoughtName() {}\n"
		}
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%02d.go", i)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// One file over the size limit, which the search will skip anyway.
	big := make([]byte, maxFileBytes+1024)
	for i := range big {
		big[i] = 'x'
	}
	if err := os.WriteFile(filepath.Join(root, "bundle.js"), big, 0o600); err != nil {
		t.Fatal(err)
	}

	ix, err := BuildIndex(context.Background(), root, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	if !ix.Truncated() {
		t.Error("a skipped oversized file should still make the answer partial")
	}
	m, err := Compile(Query{Pattern: "TheSoughtName", MaxHits: 10})
	if err != nil {
		t.Fatal(err)
	}
	cands, ok := ix.Candidates(m)
	if !ok {
		t.Fatalf("the index declined because of one oversized file: %s", ix.DeclineReason(m))
	}
	if len(cands) > 3 {
		t.Errorf("narrowed to %d of %d files, expected a handful", len(cands), ix.Files())
	}

	// And the results must still match an exhaustive search.
	want, err := SearchRepo(context.Background(), root, m, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	got, err := SearchRepoPaths(context.Background(), root, m, FilterCandidates(cands, m), ix.Truncated())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Hits) != len(want.Hits) {
		t.Errorf("indexed found %d hits, exhaustive found %d", len(got.Hits), len(want.Hits))
	}
	t.Logf("narrowed %d files to %d with an oversized file present", ix.Files(), len(cands))
}
