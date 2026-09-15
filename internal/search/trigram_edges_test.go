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
	"time"
)

// The index's edges: the branches a happy-path query never reaches.
//
// These are the paths that decide whether a search silently returns less than
// it should — a file the index could not read, a context cancelled mid-build,
// a trigram too common to store. Each one is a place where being wrong looks
// exactly like being right, which is why they are tested rather than reasoned
// about.

// countdownCtx reports no error until Err has been called n times, then
// reports one.
//
// BuildIndex checks ctx.Err() once per file, after discover has already
// walked. A pre-cancelled context never reaches that loop and a timeout is a
// race against the filesystem, so the only way to exercise it deterministically
// is to control when cancellation becomes visible.
type countdownCtx struct {
	context.Context
	calls *int
	after int
}

func (c countdownCtx) Err() error {
	*c.calls++
	if *c.calls > c.after {
		return context.Canceled
	}
	return nil
}

func (c countdownCtx) Done() <-chan struct{} { return nil }

func (c countdownCtx) Deadline() (time.Time, bool) { return time.Time{}, false }

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func allowAll(string) bool { return true }

func TestBuildIndexAcceptsNilContext(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", "package a\nfunc Alpha() {}\n")
	// Both directives are needed: CI runs staticcheck standalone, which reads
	// //lint:ignore and not //nolint; `make lint` runs it under golangci-lint,
	// which reads //nolint and strips //lint:ignore.
	//lint:ignore SA1012 a nil context is exactly what is under test
	//nolint:staticcheck // a nil context is exactly what is under test
	ix, err := BuildIndex(nil, root, allowAll)
	if err != nil {
		t.Fatalf("nil context should be treated as Background: %v", err)
	}
	if ix.Files() != 1 {
		t.Errorf("indexed %d files, want 1", ix.Files())
	}
}

func TestBuildIndexStopsOnCancellation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", "package a\nfunc Alpha() {}\n")

	t.Run("cancelled before the walk", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := BuildIndex(ctx, root, allowAll); err == nil {
			t.Error("a cancelled build should report the cancellation, not a partial index")
		}
	})

	t.Run("cancelled during the walk", func(t *testing.T) {
		for i := 0; i < 30; i++ {
			writeFile(t, root, fmt.Sprintf("f%02d.go", i), "package a\nfunc F() {}\n")
		}
		calls := 0
		// High enough to clear discover, low enough to land inside the file
		// loop that follows it.
		ctx := countdownCtx{Context: context.Background(), calls: &calls, after: 40}
		if _, err := BuildIndex(ctx, root, allowAll); err == nil {
			t.Error("cancellation during indexing should be reported")
		}
	})
}

func TestBuildIndexHandlesAwkwardFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "ok.go", "package a\nfunc Findable() {}\n")

	// Larger than the read buffer, so the buffer has to grow and be kept.
	writeFile(t, root, "big.go", "package b\n"+strings.Repeat("// filler line\n", 6000))

	// Binary: never searched, so it belongs in no candidate list.
	writeFile(t, root, "blob.go", "package d\n\x00\x01\x02binary\n")

	ix, err := BuildIndex(context.Background(), root, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Compile(Query{Pattern: "Findable", MaxHits: 10})
	if err != nil {
		t.Fatal(err)
	}
	cands, ok := ix.Candidates(m)
	if !ok {
		t.Fatal("index declined a plain literal")
	}
	joined := strings.Join(cands, " ")
	if !strings.Contains(joined, "ok.go") {
		t.Errorf("the matching file is not a candidate: %v", cands)
	}
	if strings.Contains(joined, "blob.go") {
		t.Errorf("a binary file is never searched and should not be a candidate: %v", cands)
	}
}

func TestCandidatesOnNilAndIncompleteIndex(t *testing.T) {
	m, err := Compile(Query{Pattern: "anything", MaxHits: 10})
	if err != nil {
		t.Fatal(err)
	}
	var nilIndex *Index
	if _, ok := nilIndex.Candidates(m); ok {
		t.Error("a nil index must have no opinion")
	}
	if _, ok := (&Index{incomplete: true}).Candidates(m); ok {
		t.Error("an incomplete file list must not be used to rule files out")
	}
}

// buildCommonTrigramTree makes a repository where the query's trigrams are too
// common to be stored, which is the case that sends a search back to the full
// walk.
func buildCommonTrigramTree(t *testing.T, n int) string {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < n; i++ {
		// Every file carries "aaaa", so its trigrams exceed both the fraction
		// and the absolute floor.
		writeFile(t, root, fmt.Sprintf("f%03d.go", i), "package p\n// aaaa aaaa aaaa\n")
	}
	return root
}

func TestCandidatesWhenEveryTrigramIsTooCommon(t *testing.T) {
	root := buildCommonTrigramTree(t, 200)
	ix, err := BuildIndex(context.Background(), root, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Compile(Query{Pattern: "aaaa", MaxHits: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ix.Candidates(m); ok {
		t.Error("with every trigram pruned there is nothing to narrow on; the index should say so")
	}
	// And the diagnostic should be able to say why.
	if r := ix.DeclineReason(m); !strings.Contains(r, "too-common") {
		t.Errorf("DeclineReason = %q, want it to mention the common trigrams", r)
	}
}

func TestCandidatesMixesCommonAndRareTrigrams(t *testing.T) {
	root := buildCommonTrigramTree(t, 200)
	// One file also holds a rare term whose trigrams overlap the common ones.
	writeFile(t, root, "rare.go", "package p\n// aaaa zqxjv\n")

	ix, err := BuildIndex(context.Background(), root, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Compile(Query{Pattern: "aaaa zqxjv", MaxHits: 10})
	if err != nil {
		t.Fatal(err)
	}
	cands, ok := ix.Candidates(m)
	if !ok {
		t.Fatal("a pattern with rare trigrams should still narrow")
	}
	if len(cands) > 5 {
		t.Errorf("narrowed to %d of %d; the rare trigrams should have done the work", len(cands), ix.Files())
	}
}

func TestCandidatesWhenTrigramsNeverCoincide(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", "package a\n// qqqzzz\n")
	writeFile(t, root, "b.go", "package b\n// zzzqqq\n")
	ix, err := BuildIndex(context.Background(), root, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	// Each trigram exists somewhere, but no single file holds them all.
	m, err := Compile(Query{Pattern: "qqqzzzqqq", MaxHits: 10})
	if err != nil {
		t.Fatal(err)
	}
	cands, ok := ix.Candidates(m)
	if !ok {
		t.Fatal("the index should have an opinion here")
	}
	if len(cands) != 0 {
		t.Errorf("no file can match, so no file should be read: %v", cands)
	}
}

func TestFingerprintDescribesTheTree(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", "package a\n")
	writeFile(t, root, "b.go", "package b\nfunc B() {}\n")

	//lint:ignore SA1012 nil context is part of the contract
	//nolint:staticcheck // nil context is part of the contract
	fp, err := Fingerprint(nil, root, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	if fp.Files != 2 {
		t.Errorf("Files = %d, want 2", fp.Files)
	}
	if fp.Bytes <= 0 || fp.ModUnixNano <= 0 {
		t.Errorf("fingerprint looks empty: %+v", fp)
	}

	// Changing a file must change the fingerprint, or a stale index would be
	// served as current.
	time.Sleep(10 * time.Millisecond)
	writeFile(t, root, "b.go", "package b\nfunc B() { println(1) }\n")
	fp2, err := Fingerprint(context.Background(), root, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	if fp2 == fp {
		t.Error("an edited file left the fingerprint unchanged; a stale index would pass validation")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Fingerprint(ctx, root, allowAll); err == nil {
		t.Error("a cancelled fingerprint should report it")
	}
}

func TestEnvFloatFallsBackRatherThanFailing(t *testing.T) {
	const name = "CORRAL_TEST_FRACTION"
	for _, tc := range []struct {
		raw  string
		want float64
	}{
		{"", 0.5},
		{"  ", 0.5},
		{"not-a-number", 0.5},
		{"0", 0.5},
		{"-1", 0.5},
		{"1.5", 0.5},
		{"0.25", 0.25},
		{"1", 1},
	} {
		t.Run("value="+tc.raw, func(t *testing.T) {
			t.Setenv(name, tc.raw)
			if got := envFloat(name, 0.5); got != tc.want {
				t.Errorf("envFloat(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestSearchRepoPathsAcceptsNilContext(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", "package a\nfunc Findable() {}\n")
	m, err := Compile(Query{Pattern: "Findable", MaxHits: 10})
	if err != nil {
		t.Fatal(err)
	}
	//lint:ignore SA1012 a nil context is part of the contract
	//nolint:staticcheck // a nil context is part of the contract
	res, err := SearchRepoPaths(nil, root, m, []string{"a.go"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Errorf("got %d hits, want 1", len(res.Hits))
	}
}

func TestSearchFileSurvivesAnUnreadableTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "adir"), 0o750); err != nil {
		t.Fatal(err)
	}
	m, err := Compile(Query{Pattern: "anything", MaxHits: 10})
	if err != nil {
		t.Fatal(err)
	}
	// A candidate list can name something that opens but cannot be read — a
	// directory is the reliable way to produce that. One bad entry must not
	// fail the query.
	res, err := SearchRepoPaths(context.Background(), root, m, []string{"adir"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 0 {
		t.Errorf("a directory produced hits: %v", res.Hits)
	}
}

func TestSearchHandlesCRLFAndOversizedFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "crlf.go", "package a\r\nfunc Findable() {}\r\n")
	m, err := Compile(Query{Pattern: "Findable", MaxHits: 10})
	if err != nil {
		t.Fatal(err)
	}
	res, err := SearchRepoPaths(context.Background(), root, m, []string{"crlf.go"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("CRLF file gave %d hits, want 1", len(res.Hits))
	}
	if strings.HasSuffix(res.Hits[0].Text, "\r") {
		t.Errorf("the carriage return reached the reported line: %q", res.Hits[0].Text)
	}

	// Larger than the read cap. discover would skip it, but a candidate list
	// can still name one, and reading must stop at the bound rather than pull
	// the whole file in.
	big := make([]byte, maxFileBytes+4096)
	for i := range big {
		big[i] = 'x'
	}
	copy(big, []byte("Findable\n"))
	if err := os.WriteFile(filepath.Join(root, "big.go"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err = SearchRepoPaths(context.Background(), root, m, []string{"big.go"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Errorf("oversized file gave %d hits, want the one on its first line", len(res.Hits))
	}
}

func TestReadForIndexTreatsOversizedFilesAsAlwaysRead(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", "package a\nfunc Alpha() {}\nfunc Beta() {}\n")
	// Lowering the cap is how this is reached: discover filters oversized
	// files out, so in normal operation only a file that grew between the walk
	// and the read gets here.
	swap(t, &maxIndexedFileBytes, int64(8))
	ix, err := BuildIndex(context.Background(), root, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Compile(Query{Pattern: "Alpha", MaxHits: 10})
	if err != nil {
		t.Fatal(err)
	}
	cands, ok := ix.Candidates(m)
	if !ok {
		t.Fatal("index declined")
	}
	if len(cands) != 1 || cands[0] != "a.go" {
		t.Errorf("a file too large to index must stay a candidate, got %v", cands)
	}
}

func TestFingerprintSkipsFilesItCannotStat(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", "package a\n")
	writeFile(t, root, "b.go", "package b\n")
	// A file can vanish between the walk listing it and the stat reading it.
	// That must not fail the fingerprint — a fingerprint that errors makes
	// every index look stale.
	swap(t, &osLstat, func(name string) (os.FileInfo, error) {
		if strings.HasSuffix(name, "b.go") {
			return nil, os.ErrNotExist
		}
		return os.Lstat(name)
	})
	fp, err := Fingerprint(context.Background(), root, allowAll)
	if err != nil {
		t.Fatalf("a vanished file should be skipped, not fatal: %v", err)
	}
	if fp.Files != 2 {
		t.Errorf("Files = %d; the walk still saw both", fp.Files)
	}
}

func TestDeclineReasonCountsAbsentTrigrams(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", "package a\nfunc Alpha() {}\n")
	ix, err := BuildIndex(context.Background(), root, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Compile(Query{Pattern: "zqxjvwk", MaxHits: 10})
	if err != nil {
		t.Fatal(err)
	}
	if r := ix.DeclineReason(m); !strings.Contains(r, "absent") {
		t.Errorf("DeclineReason = %q, want it to count the absent trigrams", r)
	}
}

// TestBuildIndexKeepsUnreadableFilesAsCandidates covers the branch where a file
// cannot be read at index time: it has no posting list, so it must be treated
// as always-matching rather than silently ruled out of every search.
//
// Windows is skipped because the premise cannot be set up there. os.Chmod on
// Windows toggles only the read-only attribute; it cannot deny read access, so
// the file would be indexed normally and the test would assert nothing. The
// branch is still covered — the coverage gate runs on Linux.
func TestBuildIndexKeepsUnreadableFilesAsCandidates(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Chmod cannot make a file unreadable on Windows")
	}
	root := t.TempDir()
	writeFile(t, root, "ok.go", "package a\nfunc Findable() {}\n")
	writeFile(t, root, "denied.go", "package c\nfunc Hidden() {}\n")
	if err := os.Chmod(filepath.Join(root, "denied.go"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(root, "denied.go"), 0o600) })

	ix, err := BuildIndex(context.Background(), root, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Compile(Query{Pattern: "Findable", MaxHits: 10})
	if err != nil {
		t.Fatal(err)
	}
	cands, ok := ix.Candidates(m)
	if !ok {
		t.Fatal("index declined a plain literal")
	}
	joined := strings.Join(cands, " ")
	if !strings.Contains(joined, "denied.go") {
		t.Errorf("an unreadable file must stay a candidate, it cannot be ruled out: %v", cands)
	}
	if !strings.Contains(joined, "ok.go") {
		t.Errorf("the matching file is not a candidate: %v", cands)
	}
}
