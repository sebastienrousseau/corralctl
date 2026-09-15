// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package search

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func buildTestIndex(t *testing.T) (*Index, string) {
	t.Helper()
	root := indexTree(t)
	ix, err := BuildIndex(context.Background(), root, func(string) bool { return true })
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	return ix, root
}

var testFP = DiskFingerprint{Files: 9, Bytes: 1234, ModUnixNano: 5678}

// TestOnDiskIndexRoundTrips is the property the file format rests on.
//
// A mapped index must answer every query exactly as the index it was written
// from. If it does not, a search looks in the wrong files and reports a
// confident wrong answer — the same failure the in-memory index is guarded
// against, now with a serialisation step in between where a field can be
// dropped or an offset mis-stated.
func TestOnDiskIndexRoundTrips(t *testing.T) {
	ix, root := buildTestIndex(t)
	path := filepath.Join(t.TempDir(), "a.idx")
	if err := WriteIndex(path, ix, testFP); err != nil {
		t.Fatalf("WriteIndex: %v", err)
	}

	mi, err := OpenIndex(path, testFP)
	if err != nil {
		t.Fatalf("OpenIndex: %v", err)
	}
	defer func() { _ = mi.Close() }()

	if mi.Files() != ix.Files() {
		t.Errorf("file count: mapped %d, built %d", mi.Files(), ix.Files())
	}
	if mi.Truncated() != ix.Truncated() {
		t.Errorf("truncated: mapped %v, built %v", mi.Truncated(), ix.Truncated())
	}

	for _, q := range []Query{
		{Pattern: "Needle", CaseSensitive: true},
		{Pattern: "needle"},
		{Pattern: "haystack"},
		{Pattern: "nothing of interest"},
		{Pattern: "zzz-absent-token"},
		{Pattern: "café"},
		{Pattern: "func"},
	} {
		q.MaxHits = 50
		m, err := Compile(q)
		if err != nil {
			t.Fatal(err)
		}
		wantC, wantOK := ix.Candidates(m)
		gotC, gotOK := mi.Candidates(m)
		if wantOK != gotOK {
			t.Errorf("%q: narrowed built=%v mapped=%v", q.Pattern, wantOK, gotOK)
			continue
		}
		if len(wantC) != len(gotC) {
			t.Errorf("%q: built %d candidates, mapped %d\n built: %v\nmapped: %v",
				q.Pattern, len(wantC), len(gotC), wantC, gotC)
			continue
		}
		for i := range wantC {
			if wantC[i] != gotC[i] {
				t.Errorf("%q candidate %d: built %q mapped %q", q.Pattern, i, wantC[i], gotC[i])
			}
		}

		// And the search driven by the mapped index must find what an
		// exhaustive search finds.
		want, err := SearchRepo(context.Background(), root, m, func(string) bool { return true })
		if err != nil {
			t.Fatal(err)
		}
		if gotOK {
			got, err := SearchRepoPaths(context.Background(), root, m,
				FilterCandidates(gotC, m), mi.Truncated())
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Hits) != len(want.Hits) {
				t.Errorf("%q: mapped index found %d hits, exhaustive found %d",
					q.Pattern, len(got.Hits), len(want.Hits))
			}
		}
	}
}

// TestOnDiskIndexRejectsStale checks an index built from a different state of
// the repository is refused rather than served.
func TestOnDiskIndexRejectsStale(t *testing.T) {
	ix, _ := buildTestIndex(t)
	path := filepath.Join(t.TempDir(), "a.idx")
	if err := WriteIndex(path, ix, testFP); err != nil {
		t.Fatal(err)
	}
	moved := testFP
	moved.ModUnixNano++
	if mi, err := OpenIndex(path, moved); err == nil {
		_ = mi.Close()
		t.Error("a stale index was accepted; a search would look in the wrong files")
	}
}

// TestOnDiskIndexRejectsDamage checks that a truncated or corrupted file is
// refused rather than mapped and read past its end.
//
// The arrays are read in place over the mapping, so a bad length is not a
// wrong answer — it is a read outside the mapped region. Every one of these
// must be caught by the header checks.
func TestOnDiskIndexRejectsDamage(t *testing.T) {
	ix, _ := buildTestIndex(t)
	dir := t.TempDir()
	good := filepath.Join(dir, "good.idx")
	if err := WriteIndex(good, ix, testFP); err != nil {
		t.Fatal(err)
	}
	sound, err := os.ReadFile(good) //nolint:gosec // a path this test just wrote
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		damage func([]byte) []byte
	}{
		{"empty", func([]byte) []byte { return nil }},
		{"header only", func(b []byte) []byte { return b[:headerSize] }},
		{"truncated body", func(b []byte) []byte { return b[:len(b)-8] }},
		{"bad magic", func(b []byte) []byte {
			c := append([]byte(nil), b...)
			c[0] = 'X'
			return c
		}},
		{"wrong version", func(b []byte) []byte {
			c := append([]byte(nil), b...)
			c[8] = 99
			return c
		}},
		{"absurd counts", func(b []byte) []byte {
			c := append([]byte(nil), b...)
			c[16], c[17], c[18], c[19] = 0xFF, 0xFF, 0xFF, 0x7F
			return c
		}},
		{"absurd postings", func(b []byte) []byte {
			c := append([]byte(nil), b...)
			c[20], c[21], c[22], c[23] = 0xFF, 0xFF, 0xFF, 0x7F
			return c
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(dir, tc.name+".idx")
			if err := os.WriteFile(p, tc.damage(sound), 0o600); err != nil {
				t.Fatal(err)
			}
			mi, err := OpenIndex(p, testFP)
			if err == nil {
				_ = mi.Close()
				t.Fatalf("%s was accepted", tc.name)
			}
		})
	}
}

// TestOnDiskIndexIsAtomic checks a reader never sees a half-written file.
func TestOnDiskIndexIsAtomic(t *testing.T) {
	ix, _ := buildTestIndex(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "a.idx")
	if err := WriteIndex(path, ix, testFP); err != nil {
		t.Fatal(err)
	}
	// A second write must replace the first without a window where the file
	// is short.
	if err := WriteIndex(path, ix, testFP); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "a.idx" {
			t.Errorf("left a temporary file behind: %s", e.Name())
		}
	}
	mi, err := OpenIndex(path, testFP)
	if err != nil {
		t.Fatalf("rewritten index does not open: %v", err)
	}
	_ = mi.Close()
}
