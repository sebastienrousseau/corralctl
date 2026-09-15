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
)

// buildSearchTree writes repos with files of realistic length.
//
// File length is the variable that matters here: the prefilter replaces work
// proportional to the number of lines with one pass over the bytes, so a
// benchmark over 20-line files measures almost none of the difference it makes
// to a real source tree.
func buildSearchTree(tb testing.TB, files, linesPerFile int) string {
	tb.Helper()
	root := tb.TempDir()
	var sb strings.Builder
	for l := 0; l < linesPerFile; l++ {
		fmt.Fprintf(&sb, "func Helper%d() { return doSomethingUseful(%d) }\n", l, l)
	}
	body := sb.String()
	for i := 0; i < files; i++ {
		dir := filepath.Join(root, fmt.Sprintf("pkg%02d", i%10))
		if err := os.MkdirAll(dir, 0o750); err != nil {
			tb.Fatal(err)
		}
		name := filepath.Join(dir, fmt.Sprintf("file%04d.go", i))
		if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
			tb.Fatal(err)
		}
	}
	return root
}

// BenchmarkPrefilter compares the whole-file test against line-by-line
// scanning, over identical files, in one process.
//
// Separate runs cannot answer this on a loaded machine: measured back to back
// while unrelated builds ran, the same code varied by a factor of three.
// Interleaving both arms in one binary makes the contention common to both.
//
// The pattern matches nothing, which is the case that dominates a workspace
// search: almost every file has nothing to say, and the question is how
// cheaply that can be established.
func BenchmarkPrefilter(b *testing.B) {
	root := buildSearchTree(b, 400, 400)
	m, err := Compile(Query{Pattern: "zzz-no-such-token-zzz", MaxHits: 50})
	if err != nil {
		b.Fatal(err)
	}
	allowed := func(string) bool { return true }

	for _, tc := range []struct {
		name string
		on   bool
	}{{"prefilter", true}, {"linescan", false}} {
		b.Run(tc.name, func(b *testing.B) {
			prefilterEnabled = tc.on
			b.Cleanup(func() { prefilterEnabled = true })
			b.ReportAllocs()
			for b.Loop() {
				if _, err := SearchRepo(context.Background(), root, m, allowed); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// TestPrefilterDoesNotChangeResults is the correctness half.
//
// The prefilter is an optimisation, so the only thing that matters is that it
// changes nothing. This runs the same queries both ways and compares the hits,
// including the regex cases the prefilter deliberately declines to handle
// because they can anchor.
func TestPrefilterDoesNotChangeResults(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package a\nfunc Needle() {}\nvar x = 1\n")
	write(t, root, "b.go", "package b\n// needle in a comment\nfunc Other() {}\n")
	write(t, root, "c.md", "nothing of interest\nhere at all\n")
	write(t, root, "d.go", "needle\nneedle\nneedle\n")

	allowed := func(string) bool { return true }
	for _, q := range []Query{
		{Pattern: "Needle", CaseSensitive: true, MaxHits: 50},
		{Pattern: "needle", MaxHits: 50},
		{Pattern: "^needle", Regex: true, MaxHits: 50},
		{Pattern: "needle$", Regex: true, MaxHits: 50},
		{Pattern: "nee.le", Regex: true, MaxHits: 50},
		{Pattern: "zzz-absent", MaxHits: 50},
	} {
		m, err := Compile(q)
		if err != nil {
			t.Fatalf("compile %+v: %v", q, err)
		}

		prefilterEnabled = true
		with, err := SearchRepo(context.Background(), root, m, allowed)
		if err != nil {
			t.Fatal(err)
		}
		prefilterEnabled = false
		without, err := SearchRepo(context.Background(), root, m, allowed)
		prefilterEnabled = true
		if err != nil {
			t.Fatal(err)
		}

		if len(with.Hits) != len(without.Hits) {
			t.Errorf("pattern %q: prefilter found %d hits, line scan found %d",
				q.Pattern, len(with.Hits), len(without.Hits))
			continue
		}
		for i := range with.Hits {
			if with.Hits[i] != without.Hits[i] {
				t.Errorf("pattern %q hit %d: %+v vs %+v",
					q.Pattern, i, with.Hits[i], without.Hits[i])
			}
		}
	}
}
