// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"math/rand"
	"sync"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/search"
)

// The trigram index cache.
//
// A content search without an index reads every candidate file: 58,170 of them
// on the workspace this was measured against, on every query, which is what
// held corral_search_code at a 7.9s p95 however cheap the per-file work became.
// The index says which files could possibly contain the pattern, so the rest
// are never opened.
//
// This follows the symbol cache exactly, and for the same reasons — the same
// budget in bytes rather than entries, the same refusal to evict a fresh entry
// to admit another, the same background warming. Those were not arbitrary
// choices there and they are not here: a cache too small for a workspace-wide
// query is not merely less useful, it converges on holding nothing, because a
// sweep evicts in precisely the order it will next ask for.

// indexCacheTTL is how long an index is trusted.
//
// Longer than the symbol cache's, because an index is more expensive to build
// and staleness costs less: a stale index can only send the search to the wrong
// set of files, and a file that has changed since indexing is still read and
// matched exactly. The failure mode is a missed match in a file edited within
// the window, which the warmer closes on its next pass.
const indexCacheTTL = 5 * time.Minute

// defaultMaxIndexBytes is the budget. Measured on the reporting workspace, an
// index costs roughly a tenth of the source it covers, so this holds a
// workspace several times larger than the one that produced the report.
const defaultMaxIndexBytes = 192 << 20

var maxIndexBytes = envInt("CORRAL_INDEX_CACHE_BYTES", defaultMaxIndexBytes)

// jitteredTTL spreads expiry over the last third of the window.
//
// Every index is built in the same warming pass, so a fixed TTL expires them
// all within a second of each other and the next pass rebuilds the entire
// workspace at once — minutes of CPU, competing with the queries it exists to
// make fast. Staggering means a refresh touches a slice of the workspace at a
// time and the cache is never empty.
func jitteredTTL(base time.Duration) time.Duration {
	spread := base / 3
	//nolint:gosec // cache expiry, not a security decision
	return base - spread + time.Duration(rand.Int63n(int64(spread)*2))
}

type indexEntry struct {
	ix      *search.Index
	expires time.Time
}

type indexCache struct {
	mu      sync.Mutex
	entries map[string]indexEntry
	bytes   int
}

func newIndexCache() *indexCache {
	return &indexCache{entries: map[string]indexEntry{}}
}

func (c *indexCache) get(path string) (*search.Index, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[path]
	if !ok {
		return nil, false
	}
	if time.Now().After(e.expires) {
		c.bytes -= e.ix.Bytes()
		delete(c.entries, path)
		return nil, false
	}
	return e.ix, true
}

// put stores an index if it fits.
//
// Expired entries are dropped first. If the cache is still at budget after
// that, the new index is not admitted: everything left is fresh, and evicting
// a fresh entry to make room is what turns a workspace sweep into a cache that
// never holds anything.
func (c *indexCache) put(path string, ix *search.Index) {
	if ix == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	incoming := ix.Bytes()

	// One repository must not be able to evict the workspace.
	if incoming > maxIndexBytes/4 {
		return
	}
	if old, ok := c.entries[path]; ok {
		c.bytes -= old.ix.Bytes()
		delete(c.entries, path)
	}
	if c.bytes+incoming > maxIndexBytes {
		for k, e := range c.entries {
			if now.After(e.expires) {
				c.bytes -= e.ix.Bytes()
				delete(c.entries, k)
			}
		}
	}
	if c.bytes+incoming > maxIndexBytes {
		return
	}
	c.entries[path] = indexEntry{ix: ix, expires: now.Add(jitteredTTL(indexCacheTTL))}
	c.bytes += incoming
}

// stats reports what the cache holds, for diagnostics and tests.
func (c *indexCache) stats() (entries, bytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries), c.bytes
}

// searchOneRepo searches one repository, through the index when there is one.
//
// The index only ever decides which files to open. Matching, bounding and
// ordering are the same code either way, so an indexed search and an
// exhaustive one cannot disagree about a file they both read — which is the
// property internal/search's TestIndexedSearchMatchesExhaustive pins.
//
// Every path that cannot be reasoned about falls back to the full walk: no
// index yet, an index that covers only part of the repository, a regex, a
// pattern too short for a trigram. Falling back is slower and always correct,
// which is the right direction for a filter to be wrong in.
func (s *Server) searchOneRepo(ctx context.Context, repo *RepoEntry, m *search.Matcher) (*search.Result, bool, error) {
	allowed := func(rel string) bool {
		_, ok := fileAllowed(rel, s.extraFileExts)
		return ok
	}

	ix, ok := s.indexCache.get(repo.Path)
	if !ok {
		res, err := searchRepo(ctx, repo.Path, m, allowed)
		return res, false, err
	}
	cands, narrowed := ix.Candidates(m)
	if !narrowed {
		res, err := searchRepo(ctx, repo.Path, m, allowed)
		return res, false, err
	}
	res, err := search.SearchRepoPaths(ctx, repo.Path, m, search.FilterCandidates(cands, m), ix.Truncated())
	return res, true, err
}

// indexFor builds and caches one repository's index.
var buildIndex = search.BuildIndex

func (s *Server) indexFor(ctx context.Context, repo *RepoEntry) {
	if _, ok := s.indexCache.get(repo.Path); ok {
		return
	}
	allowed := func(rel string) bool {
		_, ok := fileAllowed(rel, s.extraFileExts)
		return ok
	}
	ix, err := buildIndex(ctx, repo.Path, allowed)
	if err != nil {
		// Speculative work: a repository that cannot be indexed is searched
		// exhaustively instead, and reports its own error if it cannot be
		// read at all.
		return
	}
	s.indexCache.put(repo.Path, ix)
}
