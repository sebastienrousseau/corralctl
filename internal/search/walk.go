// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package search

import (
	"context"
	"io/fs"
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
	maxLineBytes = 64 << 10
	// maxHitTextBytes bounds the reported text of a hit, before the MCP
	// layer sanitises it further.
	maxHitTextBytes = 400
)

// searchWorkers bounds the read pool.
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
type FileFilter func(rel string) bool

// IsTestFile reports whether a path looks like test code, by the
// conventions the major ecosystems share.
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
func SearchRepo(ctx context.Context, root string, m *Matcher, allowed FileFilter) (*Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	paths, hitFileCap, skippedLarge, err := discover(ctx, root, m.IncludeTests(), m.pathGlob, allowed)
	if err != nil {
		return nil, err
	}
	return searchPaths(ctx, root, m, paths, hitFileCap || skippedLarge), nil
}

// SearchRepoPaths searches only the listed files.
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
			var local []Hit
			buf := &fileBuffers{data: make([]byte, 0, 64*1024)}
			for {
				i := int(next.Add(1)) - 1
				if i >= len(paths) || ctx.Err() != nil || hitsCap.Load() {
					break
				}
				fileHits, more := searchFile(root, paths[i], m, buf)
				local = append(local, fileHits...)
				if more {
					hitsCap.Store(true)
					break
				}
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
		paths        []string
		hitFileCap   bool
		skippedLarge bool
	)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
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
		// A symlink is never searched: a clone can commit one pointing
		// anywhere on disk, or at a refused file under an allowed name.
		// searchFile refuses links too, for a path swapped after this walk.
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if len(paths) >= maxFilesPerRepo {
			hitFileCap = true
			return fs.SkipAll
		}
		rel := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))

		if !includeTests && IsTestFile(rel) {
			return nil
		}
		if pathGlob != "" && !matchGlob(pathGlob, rel) {
			return nil
		}
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
	return paths, hitFileCap, skippedLarge, nil
}

// matchGlob reports whether rel matches the pattern, against both the full
// relative path and the bare filename.
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
