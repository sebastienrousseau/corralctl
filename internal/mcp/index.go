// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package mcp implements the corral Model Context Protocol server: a
// stdio-based JSON-RPC server that exposes the local Corral-organised
// workspace (cloned repositories under ~/Code) to AI coding agents via
// the read-only tools and resources defined in this package.
//
// The server is a wedge for the "local index for AI" positioning
// described in the v0.0.8 design doc: GitHub's own MCP server already
// covers the remote API surface with 50+ tools, so corral-mcp focuses
// on the dimension only it can serve — a developer's already-cloned
// local mirror, organised by visibility and language, queryable
// without a network round-trip.
package mcp

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/sebastienrousseau/corralctl/internal/sanitize"
)

// maxIndexDepth bounds the walk so a misconfigured root (e.g. $HOME)
// cannot blow up scan time or memory on a deeply nested filesystem.
// Three levels covers the documented Visibility/Language/Repo layout
// plus a generous one-level slack for custom Layouts.
const maxIndexDepth = 4

// maxIndexRepos bounds memory and response size for accidentally broad roots.
var maxIndexRepos = 10_000

var (
	absIndex  = filepath.Abs
	statIndex = os.Stat
	walkIndex = filepath.WalkDir
	relIndex  = filepath.Rel
)

// Scan walks root looking for directories that contain a .git child
// and returns an Index. The walk stops descending into a directory
// once it has been identified as a repository root (no recursing into
// nested submodules or vendor trees) and respects maxIndexDepth.
//
// Per-entry errors are tolerated: a single unreadable directory does
// not abort the whole scan. The function only returns an error when
// the root itself is unreadable, so a caller can distinguish a
// misconfigured root from an empty workspace.
func Scan(root string) (*Index, error) {
	absRoot, err := absIndex(root)
	if err != nil {
		return nil, fmt.Errorf("resolving root: %w", err)
	}
	info, err := statIndex(absRoot)
	if err != nil {
		return nil, fmt.Errorf("stat root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("root %q is not a directory", absRoot)
	}

	idx := &Index{Root: absRoot}

	work := make(chan string, scanQueueDepth)
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		maxPool = scanWorkers()
		pool    int
	)
	spawn := func() {
		pool++
		wg.Add(1)
		go func() {
			defer wg.Done()
			var local []RepoEntry
			for path := range work {
				local = append(local, buildEntry(absRoot, path))
			}
			if len(local) == 0 {
				return
			}
			mu.Lock()
			idx.Repos = append(idx.Repos, local...)
			mu.Unlock()
		}()
	}

	for i := 0; i < initialScanWorkers && i < maxPool; i++ {
		spawn()
	}

	found := 0

	_ = walkIndex(absRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if found >= maxIndexRepos {
			idx.Truncated = true
			return fs.SkipAll
		}
		if walkErr != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		rel, err := relIndex(absRoot, path)
		if err != nil {
			return fs.SkipDir
		}
		depth := 0
		if rel != "." {
			depth = strings.Count(rel, string(filepath.Separator)) + 1
		}
		if depth > maxIndexDepth {
			return fs.SkipDir
		}
		if path == absRoot {
			return nil
		}
		if git.IsRepository(path) {
			found++
			select {
			case work <- path:
			default:
				if pool < maxPool {
					spawn()
				}
				work <- path
			}
			return fs.SkipDir
		}
		return nil
	})

	close(work)
	wg.Wait()

	sort.Slice(idx.Repos, func(i, j int) bool {
		return idx.Repos[i].RelPath < idx.Repos[j].RelPath
	})
	return idx, nil
}

// initialScanWorkers is the pool the walk starts with, before any
// backpressure. Enough to overlap enrichment with the walk on a small
// workspace without allocating a slice per idle worker.
const initialScanWorkers = 4

// scanQueueDepth buffers the walk's handoff to the pool.
const scanQueueDepth = 16

// maxScanWorkers caps the pool regardless of core count, so a machine with
// many cores does not open an unreasonable number of files at once.
const maxScanWorkers = 32

// scanWorkers bounds how many repositories are enriched at once.
var scanWorkers = func() int {
	return clampWorkers(runtime.GOMAXPROCS(0) * 4)
}

// clampWorkers bounds a proposed worker count to [1, maxScanWorkers].
func clampWorkers(n int) int {
	if n > maxScanWorkers {
		return maxScanWorkers
	}
	if n < 1 {
		return 1
	}
	return n
}

// buildEntry constructs a RepoEntry from a discovered repository path,
// extracting Visibility/Language from the leading path segments under
// the index root and best-effort enriching with remote URL + sidecar
// state.
func buildEntry(root, repoPath string) RepoEntry {
	rel, _ := filepath.Rel(root, repoPath)
	rel = filepath.ToSlash(rel)

	name := filepath.Base(repoPath)
	entry := RepoEntry{
		Name:         name,
		Path:         repoPath,
		RelPath:      rel,
		nameLower:    strings.ToLower(name),
		relPathLower: strings.ToLower(rel),
	}

	parts := strings.Split(rel, "/")
	if len(parts) >= 3 {
		entry.Visibility = parts[0]
		entry.Language = parts[1]
	} else if len(parts) == 2 {
		entry.Language = parts[0]
	}

	if url, err := git.RemoteOriginFromConfig(repoPath); err == nil {
		entry.RemoteURL = url
	}
	if state, ok := readState(repoPath); ok {
		entry.State = state
	}
	return entry
}

// Find returns the entry whose Name, RelPath, or RemoteURL repo segment
// equals or has the supplied query as a suffix. It is the primitive
// behind the corral_find_repo tool. Returns ErrRepoNotFound when no
// candidate matches and ErrAmbiguous when multiple do — the caller
// should surface both for the agent to disambiguate.
func (i *Index) Find(query string) (*RepoEntry, error) {
	if query == "" {
		return nil, ErrRepoNotFound
	}
	q := strings.ToLower(strings.TrimSpace(query))
	suffix := "/" + q
	var matches []*RepoEntry
	for idx := range i.Repos {
		r := &i.Repos[idx]
		name, rel := r.nameLower, r.relPathLower
		if name == "" && rel == "" {
			if strings.EqualFold(r.Name, q) ||
				strings.EqualFold(r.RelPath, q) ||
				strings.HasSuffix(strings.ToLower(r.RelPath), suffix) {
				matches = append(matches, r)
			}
			continue
		}
		if name == q || rel == q || strings.HasSuffix(rel, suffix) {
			matches = append(matches, r)
		}
	}
	switch len(matches) {
	case 0:
		return nil, ErrRepoNotFound
	case 1:
		return matches[0], nil
	default:
		names := make([]string, 0, len(matches))
		for _, m := range matches {
			names = append(names, sanitize.Untrusted(m.RelPath, maxEntryPath))
		}
		return nil, fmt.Errorf("%w: %s", ErrAmbiguous, strings.Join(names, ", "))
	}
}

// ErrRepoNotFound is returned by Index.Find when no entry matches.
var ErrRepoNotFound = errors.New("no repository matches the query")

// ErrAmbiguous is returned by Index.Find when more than one entry
// matches and the caller must disambiguate.
var ErrAmbiguous = errors.New("multiple repositories match the query")
