// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package search

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestASCIIFoldAgreesWithRegex is the guard on the fast path.
//
// A case-insensitive search now compares bytes instead of running the regex
// engine, which is sound only because just two runes outside ASCII fold onto an
// ASCII letter, and files containing them still take the engine. If that
// reasoning is wrong the search silently stops matching things — so this runs
// both matchers over the same lines and requires the same answer.
//
// The other tests in this package cannot catch it: they compare indexed against
// exhaustive search, and both sides take the fast path.
func TestASCIIFoldAgreesWithRegex(t *testing.T) {
	lines := []string{
		"", "k", "K", "a k here", "KKK", "kelvin",
		"the Kelvin sign K in text",
		"long s ſ here",
		"mixed K and k",
		"no match at all",
		"prefix-K-suffix",
		"é accented but ordinary",
	}
	for _, pattern := range []string{"k", "K", "kel", "Kelvin", "ka", "K", "s", "long s"} {
		m, err := Compile(Query{Pattern: pattern, MaxHits: 10})
		if err != nil {
			t.Fatalf("compile %q: %v", pattern, err)
		}
		if !m.CanFoldASCII() {
			// Non-ASCII pattern: no fast path, nothing to compare.
			continue
		}
		for _, line := range lines {
			want := m.MatchLine(line)
			got := m.indexFold([]byte(line))
			if NeedsUnicodeFold([]byte(line)) {
				// This file takes the regex path in searchFile, so the byte
				// path's answer is not used and need not agree.
				continue
			}
			if got != want {
				t.Errorf("pattern %q line %q: fold=%d regex=%d", pattern, line, got, want)
			}
		}
	}
}

// TestKelvinSignStillMatches is the end-to-end half.
//
// (?i)k matches U+212A KELVIN SIGN. The byte path cannot know that, so the file
// holding it must be routed to the regex engine. If that routing breaks, a
// search for "k" quietly stops finding it — the exact silent miss the whole
// design is built to avoid.
func TestKelvinSignStillMatches(t *testing.T) {
	root := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("plain.go", "package p\nvar k = 1\n")
	write("kelvin.go", "package p\n// Kelvin sign here\n")
	write("longs.go", "package p\n// ſomething\n")

	m, err := Compile(Query{Pattern: "k", MaxHits: 50})
	if err != nil {
		t.Fatal(err)
	}
	res, err := SearchRepo(context.Background(), root, m, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]bool{}
	for _, h := range res.Hits {
		files[h.File] = true
	}
	if !files["kelvin.go"] {
		t.Errorf("a case-insensitive search for \"k\" missed U+212A; hits were %v", res.Hits)
	}
	if !files["plain.go"] {
		t.Errorf("it also missed the ordinary k; hits were %v", res.Hits)
	}

	// And the same through the index, which has its own reason to drop it.
	ix, err := BuildIndex(context.Background(), root, func(string) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	ms, err := Compile(Query{Pattern: "sign", MaxHits: 50})
	if err != nil {
		t.Fatal(err)
	}
	cands, ok := ix.Candidates(ms)
	if ok && len(cands) == 0 {
		t.Error("the index excluded every file for a term that is present")
	}
}
