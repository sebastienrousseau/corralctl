// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package search

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Bounds. Every one of these exists because a workspace contains at least
// one repository that will otherwise make a search unusable — a vendored
// monorepo, a checked-in dataset, a minified bundle on one 4 MB line.
var (
	// maxFilesPerRepo bounds the walk.
	maxFilesPerRepo = 20_000
	// maxFileBytes skips a file too large to be hand-written source. A
	// generated bundle or a committed binary is never the answer to
	// "where is this used", and reading it costs more than every real
	// file put together.
	maxFileBytes int64 = 2 << 20 // 2 MiB
	// maxLineBytes bounds one line. A minified file is a single enormous
	// line; matching in it is useless and buffering it is expensive.
	//
	// 64 KiB, not the 4096 this constant held before, because 4096 was never
	// the limit that applied. It was passed as bufio.Scanner's maxTokenSize,
	// and a Scanner only consults that when it has to GROW its buffer — the
	// buffer here started at 64 KiB, so any line up to that size was returned
	// whole and a hit on a 5000-byte line was reported. Reading the file
	// directly makes whatever number is here the real one, so it is set to
	// what the code actually did rather than to what the constant claimed.
	// TestSearchCodeBoundsTheReportedLine pins that behaviour.
	maxLineBytes = 64 << 10
	// maxHitTextBytes bounds the reported text of a hit, before the MCP
	// layer sanitises it further.
	maxHitTextBytes = 400
)

// searchWorkers bounds the read pool.
//
// Higher than GOMAXPROCS because this is I/O bound: a worker waiting on a
// read is not using a core, and the page cache makes the second pass over
// a workspace far cheaper than the first.
var searchWorkers = func() int {
	return clampWorkers(runtime.GOMAXPROCS(0) * 4)
}

const maxSearchWorkers = 32

// clampWorkers holds a worker count inside [1, maxSearchWorkers].
func clampWorkers(n int) int {
	if n < 1 {
		return 1
	}
	if n > maxSearchWorkers {
		return maxSearchWorkers
	}
	return n
}

// FileFilter reports whether a repository-relative path may be searched.
//
// Supplied by the caller rather than decided here, because the policy that
// matters lives in the MCP layer: the same allowlist that decides what the
// file resource will serve. A search that could match inside a file the
// server refuses to hand over would be a way to read it one line at a
// time.
type FileFilter func(rel string) bool

// IsTestFile reports whether a path looks like test code, by the
// conventions the major ecosystems share.
//
// Deliberately generous. A false positive hides a hit behind
// include_tests, which the caller can set; a false negative buries real
// answers under a repository's test suite, which they cannot undo.
func IsTestFile(rel string) bool {
	lower := strings.ToLower(rel)
	base := lower
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	switch {
	case strings.HasSuffix(base, "_test.go"),
		strings.HasSuffix(base, "_test.py"),
		strings.HasPrefix(base, "test_"),
		strings.HasSuffix(base, "_test.rs"),
		strings.Contains(base, ".test."),
		strings.Contains(base, ".spec."):
		return true
	}
	for _, seg := range strings.Split(lower, "/") {
		switch seg {
		case "test", "tests", "__tests__", "spec", "benches", "testdata":
			return true
		}
	}
	return false
}

// skipDir reports directories never worth descending into: build output
// and dependency trees, which dwarf hand-written source and are never what
// somebody is looking for in their own workspace.
func skipDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", "node_modules", "vendor",
		"dist", "build", "target", "bin", "obj", ".venv", "venv",
		"__pycache__", ".next", ".cache", ".terraform", "DerivedData", "Pods":
		return true
	}
	return false
}

// SearchRepo walks one repository and returns the lines matching m.
//
// The walk is serial and the reading is concurrent, for the same reason
// the workspace scan and the symbol walk split them: directory entries are
// cheap and already cached, per-file work is not.
//
// ctx cancellation is honoured between files, so an agent that gives up
// does not leave a pool reading a monorepo.
func SearchRepo(ctx context.Context, root string, m *Matcher, allowed FileFilter) (*Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	paths, hitFileCap, skippedLarge, err := discover(ctx, root, m.IncludeTests(), m.pathGlob, allowed)
	if err != nil {
		return nil, err
	}
	// Either bound means the answer is partial, and a caller is told so.
	return searchPaths(ctx, root, m, paths, hitFileCap || skippedLarge), nil
}

// SearchRepoPaths searches only the listed files.
//
// Used with a trigram index, which decides which files could possibly match so
// the rest are never opened. Everything after that point — reading, matching,
// bounding, ordering — is the same code the full walk runs, so an indexed
// search and an exhaustive one cannot disagree about a file they both read.
// TestIndexedSearchMatchesExhaustive asserts that over a tree of both.
//
// paths must be in walk order and already filtered by the query's path_glob
// and include_tests, since those are per-query and the index is not.
func SearchRepoPaths(ctx context.Context, root string, m *Matcher, paths []string, truncated bool) (*Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return searchPaths(ctx, root, m, paths, truncated), nil
}

// searchPaths is the shared body: read these files, report the matches.
func searchPaths(ctx context.Context, root string, m *Matcher, paths []string, truncated bool) *Result {
	res := &Result{Files: len(paths), Truncated: truncated}
	if len(paths) == 0 {
		return res
	}

	var (
		mu      sync.Mutex
		next    atomic.Int64
		wg      sync.WaitGroup
		hitsCap atomic.Bool
	)
	workers := clampWorkers(min(searchWorkers(), len(paths)))

	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			// Each worker accumulates locally and merges once, so the
			// mutex is contended per worker rather than per file.
			var local []Hit
			// One read buffer per worker, not per file.
			//
			// This was a sync.Pool, which looked right and was not: a
			// workspace search runs hundreds of goroutines, a Pool is
			// per-P with a shared overflow, and GC drains it every cycle,
			// so Get missed far more often than it hit. Every miss
			// allocated 64KB, and readBounded became 2.15GB — 55% of all
			// allocation in a query. A worker outlives every file it
			// reads, so the buffer belongs to it.
			buf := &fileBuffers{data: make([]byte, 0, 64*1024)}
			for {
				i := int(next.Add(1)) - 1
				if i >= len(paths) || ctx.Err() != nil || hitsCap.Load() {
					break
				}
				fileHits, more := searchFile(root, paths[i], m, buf)
				local = append(local, fileHits...)
				if more {
					// The file had matches this result will not carry, so
					// the answer is partial however few hits came back.
					hitsCap.Store(true)
					break
				}
				// A cheap global check so a query matching everything
				// stops the whole pool rather than every worker filling
				// its own cap.
				if len(local) >= m.MaxHits() {
					hitsCap.Store(true)
					break
				}
			}
			if len(local) == 0 {
				return
			}
			mu.Lock()
			res.Hits = append(res.Hits, local...)
			mu.Unlock()
		}()
	}
	wg.Wait()

	// Workers append in whatever order they finish, so determinism comes
	// from this sort rather than from arrival order. Without it two
	// identical searches would disagree and an agent paging through
	// results would see them shuffle.
	// Only the first match on a line is reported, so (file, line) is
	// unique and orders the result completely.
	sort.Slice(res.Hits, func(i, j int) bool {
		a, b := res.Hits[i], res.Hits[j]
		if a.File != b.File {
			return a.File < b.File
		}
		return a.Line < b.Line
	})

	if len(res.Hits) > m.MaxHits() {
		res.Hits = res.Hits[:m.MaxHits()]
		res.Truncated = true
	}
	if hitsCap.Load() {
		res.Truncated = true
	}
	return res
}

// discover collects the searchable files, in walk order.
func discover(ctx context.Context, root string, includeTests bool, pathGlob string, allowed FileFilter) ([]string, bool, bool, error) {
	var (
		paths []string
		// hitFileCap means the walk stopped early, so this list is NOT every
		// file a search would read. An index built from it cannot be used to
		// rule files out, because the ones it never saw would be ruled out too.
		hitFileCap bool
		// skippedLarge means a file was passed over for being too big. That is
		// not the same thing at all: the search skips it on exactly the same
		// rule, so the list is still complete with respect to what a search
		// reads. Conflating the two is what disabled the index for a whole
		// repository because it contained one lockfile.
		skippedLarge bool
	)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			// Unreadable subtree: skip it rather than abandon the walk.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if path != root && skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if len(paths) >= maxFilesPerRepo {
			hitFileCap = true
			return fs.SkipAll
		}
		// WalkDir yields paths under root, so the prefix is always there
		// and this is total — unlike filepath.Rel, whose error branch
		// could never be taken and so could never be tested.
		rel := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))

		if !includeTests && IsTestFile(rel) {
			return nil
		}
		if pathGlob != "" && !matchGlob(pathGlob, rel) {
			return nil
		}
		// The file policy is the caller's, and it is what stops a search
		// reading a credential file one line at a time.
		if allowed != nil && !allowed(rel) {
			return nil
		}
		if info, statErr := d.Info(); statErr == nil && info.Size() > maxFileBytes {
			skippedLarge = true
			return nil
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil && ctx.Err() != nil {
		return nil, false, false, err
	}
	// Any other walk error is best-effort: a partial file list still
	// produces a useful answer, and an unreadable root shows up as zero
	// files rather than as a failure the agent has to interpret.
	return paths, hitFileCap, skippedLarge, nil
}

// matchGlob reports whether rel matches the pattern, against both the full
// relative path and the bare filename.
//
// Both, because "*.go" is what somebody types and it is a filename
// pattern, while "internal/*/*.go" is a path pattern. Trying one and then
// the other is what makes the obvious input work.
func matchGlob(pattern, rel string) bool {
	if ok, err := filepath.Match(pattern, rel); err == nil && ok {
		return true
	}
	base := rel
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	ok, err := filepath.Match(pattern, base)
	return err == nil && ok
}

// searchFile reads one file and returns its matching lines, and whether it
// stopped before the end of the file.
//
// The second return is what makes a truncated answer say so. Without it, a
// file whose matches exactly fill the cap is indistinguishable from one
// that happened to contain exactly that many — and the caller reports a
// partial answer as complete.
//
// Errors are swallowed on purpose: a file that vanished between the walk
// and the read, or that turned out to be unreadable, is not a reason to
// fail a search across thousands of others.
// fileBuffers is the per-file scratch a search needs.
//
// Pooled because it was allocated per file, and a workspace-wide query reads
// tens of thousands of files: on a 234-repository workspace, 58,170 candidate
// files at 8KB + 64KB each meant gigabytes of garbage to answer one question.
// That is why a repeated search was no faster than a cold one — the cost was
// never the disk, it was the allocator.
type fileBuffers struct {
	data []byte
}

// newFileBuffer is the per-worker read scratch.
func newFileBuffer() *fileBuffers {
	return &fileBuffers{data: make([]byte, 0, 64*1024)}
}

// searchFile reads one file and returns the lines matching m.
//
// The file is read once into memory and tested as a whole before it is split
// into lines. That ordering is the point: almost every file in a workspace
// contains no match, and the previous version still paid to walk every line of
// every one of them through a scanner, calling the matcher once per line, to
// discover that. Asking "is it in here at all" first answers for the whole file
// in one pass and skips the rest.
//
// The prefilter applies only to a literal pattern. A user regex may anchor with
// ^ or $, where "matches somewhere in the file" and "matches on some line" are
// different questions, so those files are scanned as before.
func searchFile(root, rel string, m *Matcher, buf *fileBuffers) (hits []Hit, more bool) {
	f, err := os.Open(filepath.Join(root, rel)) // #nosec G304 -- rel is walk-derived and policy-filtered
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()

	data, readErr := readBounded(f, buf.data[:0])
	// Keep whatever growth happened, so the next file this worker reads
	// starts from the larger buffer rather than growing again.
	if cap(data) > cap(buf.data) {
		buf.data = data[:0]
	}
	if readErr != nil {
		return nil, false
	}

	// A binary file has no lines worth reporting, and a match inside one is
	// noise at best. Sniffing the first block is what git itself does.
	head := data
	if len(head) > 8000 {
		head = head[:8000]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return nil, false
	}

	// Decide once per file which matcher to use.
	//
	// The regex engine is needed only for a file containing a rune that folds
	// onto an ASCII letter — two exist in all of Unicode — or for a pattern
	// the byte path cannot express. Everything else, which is effectively
	// every file, takes the byte path and skips the engine that a profile
	// showed was costing 45% of a query's CPU, against 37% for the file reads
	// the search exists to do.
	fold := m.CanFoldASCII() && !NeedsUnicodeFold(data)

	if prefilterEnabled && m.CanPrefilter() {
		hit := false
		if fold {
			hit = m.containsFold(data)
		} else {
			hit = m.MatchAny(data)
		}
		if !hit {
			return nil, false
		}
	}

	line := 0
	rest := data
	for len(rest) > 0 {
		var raw []byte
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			raw, rest = rest[:i], rest[i+1:]
		} else {
			raw, rest = rest, nil
		}
		if n := len(raw); n > 0 && raw[n-1] == '\r' {
			raw = raw[:n-1]
		}
		line++
		if len(raw) > maxLineBytes {
			// A line this long means a minified or generated file. The
			// hits found so far are the useful half, and the answer is
			// marked partial — the same contract the scanner's buffer
			// limit gave before.
			return hits, true
		}
		var col int
		if fold {
			col = m.indexFold(raw)
		} else {
			col = m.MatchBytes(raw)
		}
		if col < 0 {
			continue
		}
		hits = append(hits, Hit{
			File:   rel,
			Line:   line,
			Column: col + 1,
			Text:   trimHitText(string(raw)),
		})
		if len(hits) >= m.MaxHits() {
			// There may or may not be more below; saying so is the safe
			// direction, because claiming a complete answer that is not
			// one is the failure that misleads.
			return hits, true
		}
	}
	return hits, false
}

// prefilterEnabled is a seam. A benchmark flips it to measure the same files,
// the same reads and the same matcher with and without the whole-file test, in
// one process — which is the only way to compare the two on a machine busy
// with unrelated work, where wall-clock between separate runs says more about
// the other processes than about this code.
var prefilterEnabled = true

// readFileBounded opens root/rel and reads it, bounded.
func readFileBounded(root, rel string, dst []byte) ([]byte, error) {
	f, err := os.Open(filepath.Join(root, rel)) // #nosec G304 -- rel is walk-derived and policy-filtered
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return readBounded(f, dst)
}

// readBounded reads at most maxFileBytes from f, appending into dst.
//
// Bounded rather than io.ReadAll: the walk already skips files over the limit,
// but a file can grow between being listed and being opened, and a search must
// not be the thing that reads an unbounded amount into memory.
func readBounded(f *os.File, dst []byte) ([]byte, error) {
	for {
		if len(dst) == cap(dst) {
			dst = append(dst, 0)[:len(dst)]
		}
		n, err := f.Read(dst[len(dst):cap(dst)])
		dst = dst[:len(dst)+n]
		if err != nil {
			if err == io.EOF {
				return dst, nil
			}
			return dst, err
		}
		if int64(len(dst)) >= maxFileBytes {
			return dst, nil
		}
	}
}

// trimHitText prepares a matching line for a caller: leading and trailing
// whitespace removed, and bounded.
//
// The indentation is dropped because it carries no information once the
// line number is known, and it is often most of the line.
func trimHitText(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxHitTextBytes {
		return s
	}
	// Cut on a rune boundary so the result is still valid UTF-8.
	cut := maxHitTextBytes
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// utf8Start reports whether b begins a UTF-8 sequence rather than
// continuing one.
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
