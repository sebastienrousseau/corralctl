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
//
// The indexes now live in mapped files, so what this cache holds is mappings
// rather than megabytes. That changes what it has to be careful about. The
// earlier in-memory version policed a byte budget, because every index was heap
// the runtime could never give back; a mapping costs address space and lets the
// kernel keep only the pages queries touch. What it must police instead is
// LIFETIME: an index's slices point into its mapping, so unmapping one while a
// query reads it is a use-after-free, not a slow search.
//
// Hence the reference counting below. It is not defensive — eviction and
// revalidation both unmap, and both can happen while a search is in flight.

// indexCacheTTL is how long an index is used before it is revalidated.
//
// Revalidation is a walk, not a rebuild: the fingerprint comes from stat-ing
// the files, and only a repository that actually changed is indexed again. So
// this can be short without being expensive.
const indexCacheTTL = 5 * time.Minute

// maxIndexEntries bounds how many mappings are held at once.
//
// A count, not a size: the pages are the kernel's to reclaim, so one more index
// costs address space and a map entry rather than resident memory. The cap
// exists so an enormous workspace cannot grow the table without bound, not to
// ration memory.
const maxIndexEntries = 4096

// jitteredTTL spreads expiry over the last third of the window.
//
// Every index is built in the same warming pass, so a fixed TTL expires them
// all within a second of each other and the next pass revalidates the entire
// workspace at once — competing with the queries it exists to make fast.
func jitteredTTL(base time.Duration) time.Duration {
	spread := base / 3
	//nolint:gosec // cache expiry, not a security decision
	return base - spread + time.Duration(rand.Int63n(int64(spread)*2))
}

// indexEntry is one repository's mapped index and its lifetime.
type indexEntry struct {
	mi      *search.MappedIndex
	expires time.Time
	// refs counts holders: one for the cache itself, plus one per query
	// currently reading it. The mapping is released when the count reaches
	// zero, which may be well after the entry has left the table.
	refs int
	// evicted records that the cache has let go, so the last query to finish
	// unmaps rather than leaving it mapped forever.
	evicted bool
}

type indexCache struct {
	mu      sync.Mutex
	entries map[string]*indexEntry
}

func newIndexCache() *indexCache {
	return &indexCache{entries: map[string]*indexEntry{}}
}

// acquire returns a fresh index and a function that must be called when the
// caller is done with it.
//
// The release function is not optional. Until it runs the mapping is pinned;
// after it runs the index may be unmapped at any moment and every slice in it
// becomes invalid memory.
func (c *indexCache) acquire(path string) (*search.Index, func(), bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[path]
	if !ok {
		return nil, nil, false
	}
	if time.Now().After(e.expires) {
		c.dropLocked(path, e)
		return nil, nil, false
	}
	e.refs++
	return e.mi.Index, func() { c.release(e) }, true
}

// release drops one holder's reference.
func (c *indexCache) release(e *indexEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e.refs--
	c.maybeCloseLocked(e)
}

// put installs a mapped index, replacing any previous one.
func (c *indexCache) put(path string, mi *search.MappedIndex) {
	if mi == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.entries[path]; ok {
		c.dropLocked(path, old)
	}
	if len(c.entries) >= maxIndexEntries {
		// Full of live entries. Decline rather than evict one a query may be
		// reading, and rather than start a treadmill — which is what made the
		// symbol cache useless before it was fixed.
		_ = mi.Close()
		return
	}
	c.entries[path] = &indexEntry{
		mi:      mi,
		expires: time.Now().Add(jitteredTTL(indexCacheTTL)),
		refs:    1, // the cache's own reference
	}
}

// dropLocked removes an entry and releases the cache's own reference.
// Callers must hold the mutex.
func (c *indexCache) dropLocked(path string, e *indexEntry) {
	if e.evicted {
		return
	}
	delete(c.entries, path)
	e.evicted = true
	e.refs--
	c.maybeCloseLocked(e)
}

// maybeCloseLocked unmaps once nobody holds the entry and the cache has let it
// go. Callers must hold the mutex.
func (c *indexCache) maybeCloseLocked(e *indexEntry) {
	if e.refs <= 0 && e.evicted && e.mi != nil {
		_ = e.mi.Close()
		e.mi = nil
	}
}

// fresh reports whether a usable index is already held, without taking a
// reference. Used by the warmer to skip work it does not need to do.
func (c *indexCache) fresh(path string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[path]
	return ok && time.Now().Before(e.expires)
}

// stats reports what the cache holds, for diagnostics and tests.
//
// The byte figure is heap only — the path list each index keeps — because the
// rest is mapped and belongs to the page cache, not to this process's budget.
func (c *indexCache) stats() (entries, bytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.entries {
		if e.mi != nil {
			bytes += e.mi.Bytes()
		}
	}
	return len(c.entries), bytes
}

// closeAll releases every mapping. Called when the server stops.
func (c *indexCache) closeAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for path, e := range c.entries {
		c.dropLocked(path, e)
	}
}

// searchOneRepo searches one repository, through the index when there is one.
//
// The index only ever decides which files to open. Matching, bounding and
// ordering are the same code either way, so an indexed search and an
// exhaustive one cannot disagree about a file they both read — which is the
// property internal/search's TestIndexedSearchMatchesExhaustive pins.
//
// Every path that cannot be reasoned about falls back to the full walk: no
// index yet, an index covering only part of the repository, a regex, a pattern
// too short for a trigram. Falling back is slower and always correct, which is
// the right direction for a filter to be wrong in.
func (s *Server) searchOneRepo(ctx context.Context, repo *RepoEntry, m *search.Matcher) (*search.Result, bool, error) {
	allowed := func(rel string) bool {
		_, ok := fileAllowed(rel, s.extraFileExts)
		return ok
	}

	ix, release, ok := s.indexCache.acquire(repo.Path)
	if !ok {
		res, err := searchRepo(ctx, repo.Path, m, allowed)
		return res, false, err
	}
	// Held for the whole call. Candidates copies the paths out of the mapping,
	// so in principle the search no longer needs it — but nothing downstream
	// should have to know that, and the cost of holding it is a counter.
	defer release()

	cands, narrowed := ix.Candidates(m)
	if !narrowed {
		res, err := searchRepo(ctx, repo.Path, m, allowed)
		return res, false, err
	}
	res, err := search.SearchRepoPaths(ctx, repo.Path, m, search.FilterCandidates(cands, m), ix.Truncated())
	return res, true, err
}

// buildIndex is the seam tests replace.
var buildIndex = search.BuildIndex

// indexFor makes sure one repository has a usable index.
func (s *Server) indexFor(ctx context.Context, repo *RepoEntry) {
	if s.indexCache.fresh(repo.Path) {
		return
	}
	mi, err := s.loadOrBuildIndex(ctx, repo)
	if err != nil {
		// Speculative work: a repository that cannot be indexed is searched
		// exhaustively instead, and reports its own error if it cannot be read
		// at all.
		return
	}
	s.indexCache.put(repo.Path, mi)
}
