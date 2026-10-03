// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package symbols

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// Bounds on what a single repository may contribute.
//
// These exist because the index is built on demand, in front of an agent
// that is waiting, over a directory corral does not control. A monorepo,
// a checked-in dependency tree, or a generated-code explosion must degrade
// into a truncated answer rather than an unbounded one.
// maxFileBytes skips any source file larger than this. A Go file of more
// than a megabyte is generated, and generated code is not what a definition
// lookup is for.
const maxFileBytes = 1 << 20

// The file and symbol caps are vars rather than consts so a test can shrink
// them. Truncation is real behaviour with a real consequence — a partial
// index the caller must be told about — and asserting it by materialising
// 200,000 declarations would make the suite unusable.
var (
	// maxFilesPerRepoVar bounds the walk.
	maxFilesPerRepoVar = 20_000
	// maxSymbolsPerRepoVar bounds the result.
	maxSymbolsPerRepoVar = 200_000
)

// Result is one repository's extracted symbols.
type Result struct {
	// Symbols are the declarations found, sorted by file then line.
	Symbols []Symbol
	// Files is how many source files were parsed.
	Files int
	// Truncated reports that a bound was hit and the result is partial.
	// Callers must surface this: a silently partial index is worse than a
	// slow one, because the agent cannot tell a missing symbol from an
	// absent one.
	Truncated bool
}

// walkWorkers bounds the parse pool.
//
// Unlike the workspace scan, this work is genuinely CPU-bound — parsing is
// arithmetic, not waiting — so the pool matches the cores rather than
// oversubscribing them.
var walkWorkers = func() int {
	return clampWalkWorkers(runtime.GOMAXPROCS(0))
}

// clampWalkWorkers keeps the pool at one or more.
//
// Split out for the same reason the scan's clamp is: whether the bound
// applies depends on the host, so on any given machine the branch can never
// be taken and would sit permanently uncovered.
func clampWalkWorkers(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// skipDir reports directories never worth descending into. Build output and
// dependency trees dwarf hand-written source in a real workspace, and none
// of it is what someone is looking for.
func skipDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", "node_modules", "vendor", "testdata",
		"dist", "build", "target", "bin", "obj", ".venv", "venv",
		"__pycache__", ".next", ".cache", "DerivedData", "Pods":
		return true
	}
	return false
}

// generatedSuffixes are the filename endings that conventionally mean
// "written by a tool", per language.
//
// A definition lookup that answers with generated code has technically
// succeeded and practically failed: nobody wants the line in the protobuf
// stub, they want the .proto or the hand-written wrapper. Vendored and
// dependency trees are already excluded by skipDir; this is the same idea
// for files that sit among ordinary source.
var generatedSuffixes = []string{
	// Go
	".pb.go", ".pb.gw.go", "_generated.go", ".gen.go", "_string.go",
	// Python
	"_pb2.py", "_pb2_grpc.py", "_pb2.pyi",
	// TypeScript / JavaScript
	".min.js", ".min.mjs", ".bundle.js", ".generated.ts", ".gen.ts",
	// Rust
	".pb.rs",
	// C / C++
	".pb.cc", ".pb.h",
}

// skipSourceFile reports whether a discovered file should be left out of
// the index, by path segment or by generated-file convention.
//
// The segment check duplicates skipDir on purpose. skipDir prunes the walk
// and cannot see a path assembled some other way; this is checked against
// the repository-relative path, so it holds for every caller.
func skipSourceFile(rel string) bool {
	lower := strings.ToLower(rel)
	for _, seg := range strings.Split(lower, "/") {
		switch seg {
		case "vendor", "testdata", "node_modules", ".git", "__pycache__":
			return true
		}
	}
	for _, suffix := range generatedSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// ExtractRepo walks one repository and returns every symbol it can find.
//
// The walk is serial and the parsing is concurrent, for the same reason the
// workspace scan splits them: reading directory entries is cheap and
// already cached, while the per-file work is not.
//
// ctx cancellation is honoured between files, so an agent that gives up
// does not leave a pool parsing a monorepo.
func ExtractRepo(ctx context.Context, root string) (*Result, error) {
	return ExtractRepoCached(ctx, root, nil)
}

// ExtractRepoCached is ExtractRepo with a cache consulted before parsing.
//
// Extraction splits cleanly into a cheap walk and expensive parsing, and
// the walk produces a fingerprint of what it found. So a cache hit still
// walks — which is why a repository edited since the last call is never
// served stale — and skips only the parsing, which is where the seconds
// are. A nil cache is the uncached path.
func ExtractRepoCached(ctx context.Context, root string, cache Cache) (*Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	paths, truncated, fp, err := discover(ctx, root)
	if err != nil {
		return nil, err
	}

	if cache != nil {
		if cached, ok := cache.Get(root, fp); ok {
			return cached, nil
		}
	}

	res := &Result{Files: len(paths), Truncated: truncated}
	if len(paths) == 0 {
		if cache != nil {
			cache.Put(root, fp, res)
		}
		return res, nil
	}

	var (
		mu   sync.Mutex
		next atomic.Int64
		wg   sync.WaitGroup
	)
	workers := walkWorkers()
	if workers > len(paths) {
		workers = len(paths)
	}

	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			// Each worker accumulates locally and merges once, so the
			// mutex is contended per worker rather than per file.
			var local []Symbol
			for ctx.Err() == nil {
				i := int(next.Add(1)) - 1
				if i >= len(paths) {
					break
				}
				local = append(local, parseOne(root, paths[i])...)
			}
			mu.Lock()
			res.Symbols = append(res.Symbols, local...)
			mu.Unlock()
		}()
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if len(res.Symbols) > maxSymbolsPerRepoVar {
		// Sort before truncating, or which symbols survive would depend on
		// which worker finished first.
		Sort(res.Symbols)
		res.Symbols = res.Symbols[:maxSymbolsPerRepoVar]
		res.Truncated = true
	}
	// Workers finish in an arbitrary order, so the merged slice is
	// arbitrary until sorted. Two identical queries must not disagree.
	Sort(res.Symbols)

	if cache != nil {
		// Stored after the sort, so a cache hit and a cache miss return
		// results in the same order. A cache that changed the answer
		// would be worse than no cache.
		cache.Put(root, fp, res)
	}
	return res, nil
}

// discover collects the source files an extractor claims, in walk order,
// and fingerprints them along the way.
//
// The fingerprint is free here: the walk already stats every entry, so
// accumulating a count, a byte total and the newest modification time
// costs nothing beyond three additions. That matters because the walk is
// the cheap half of extraction and parsing is the expensive half — a
// fingerprint that can be computed without parsing is exactly what lets a
// cache skip the parsing.
func discover(ctx context.Context, root string) ([]string, bool, Fingerprint, error) {
	var (
		paths     []string
		truncated bool
		fp        Fingerprint
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
		if len(paths) >= maxFilesPerRepoVar {
			truncated = true
			return fs.SkipAll
		}
		// filepath.Rel cannot fail here: WalkDir builds every path by
		// joining root with an entry name, so the two are always relatable.
		// The error is discarded rather than guarded, because a branch that
		// can never be taken is a branch that can never be tested.
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if _, ok := ExtractorFor(rel); !ok {
			return nil
		}
		if skipSourceFile(rel) {
			return nil
		}
		if info, statErr := d.Info(); statErr == nil {
			fp.Bytes += info.Size()
			if mod := info.ModTime().UnixNano(); mod > fp.ModUnixNano {
				fp.ModUnixNano = mod
			}
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil && ctx.Err() != nil {
		return nil, false, Fingerprint{}, err
	}
	fp.Files = len(paths)
	// Any other walk error is best-effort: a partial file list still
	// produces a useful index, and the root being unreadable shows up as
	// an empty result rather than a failure the agent must handle.
	sort.Strings(paths)
	return paths, truncated, fp, nil
}

// parseOne reads and extracts a single file. Every failure is silent by
// design: an unreadable or oversized file contributes nothing, and one bad
// file must not blank an otherwise good index.
func parseOne(root, rel string) []Symbol {
	full := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Stat(full)
	if err != nil || info.Size() > maxFileBytes {
		return nil
	}
	src, err := os.ReadFile(full) // #nosec G304 -- rel is discovered under root by the walk above
	if err != nil {
		return nil
	}
	ex, ok := ExtractorFor(rel)
	if !ok {
		return nil
	}
	syms, err := ex.Extract(rel, src)
	if err != nil {
		return nil
	}
	return syms
}

// Query selects symbols from a result set.
type Query struct {
	// Name matches the symbol name. Exact and case-insensitive unless
	// Substring is set.
	Name string
	// Substring makes Name a case-insensitive contains match.
	Substring bool
	// Kind, when non-empty, restricts to one kind.
	Kind Kind
	// ExportedOnly restricts to symbols visible outside their package.
	ExportedOnly bool
	// IncludeTests keeps declarations from test files. Off by default:
	// on a well-tested repository they outnumber everything else, and a
	// lookup that answers with a test function has technically succeeded
	// and practically failed.
	IncludeTests bool
}

// Match reports whether s satisfies q.
func (q Query) Match(s Symbol) bool {
	if s.Test && !q.IncludeTests {
		return false
	}
	if q.Kind != "" && s.Kind != q.Kind {
		return false
	}
	if q.ExportedOnly && !s.Exported {
		return false
	}
	if q.Name == "" {
		return true
	}
	name, want := strings.ToLower(s.Name), strings.ToLower(q.Name)
	if q.Substring {
		return strings.Contains(name, want)
	}
	// A method is findable by its bare name or by Receiver.Name, because
	// people write both.
	return name == want || strings.EqualFold(s.Qualified(), q.Name)
}
