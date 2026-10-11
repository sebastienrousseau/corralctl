// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/symbols"
)

// symbolCacheTTL is how long an extracted repository stays fresh.
//
// Longer than the workspace scan's five seconds, because the inputs move at
// different speeds: the set of repositories changes when someone clones,
// while a repository's declarations change when someone edits. Parsing is
// also far more expensive than stat-ing, so a short TTL would spend most of
// an agent's session re-parsing files that had not changed.
const symbolCacheTTL = 2 * time.Minute

// The symbol cache is bounded by symbols held, not by repositories held, and
// it declines to evict a fresh entry in order to admit another.
const defaultMaxCachedSymbols = 250_000

// maxCachedSymbols is the budget in force. Overridable because the right
// number depends on how much memory the machine can spare, and a server
// indexing someone's whole workspace should not be the thing that decides.
var maxCachedSymbols = envInt("CORRAL_SYMBOL_CACHE_SYMBOLS", defaultMaxCachedSymbols)

// maxEntryShare caps how much of the budget a single repository may occupy.
//
// Without it one outlier evicts the workspace: a 200,000-symbol repository
// is 80% of the default budget on its own, so caching it would drop
// almost everything else. A repository over this share is simply not cached.
const maxEntryShare = 4

// maxCachedRepos is a ceiling on entries regardless of how small each is, so a
// workspace of thousands of tiny repositories cannot grow the map without
// bound.
const maxCachedRepos = 2048

// symbolEntry is one repository's cached extraction.
type symbolEntry struct {
	result  *symbols.Result
	expires time.Time
}

// symbolCache is a small TTL cache keyed by absolute repository path.
type symbolCache struct {
	mu      sync.Mutex
	entries map[string]symbolEntry
	symbols int
}

func newSymbolCache() *symbolCache {
	return &symbolCache{entries: map[string]symbolEntry{}}
}

// get returns a cached extraction if it is still fresh.
func (c *symbolCache) get(path string) (*symbols.Result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[path]
	if !ok {
		return nil, false
	}
	if time.Now().After(e.expires) {
		c.symbols -= len(e.result.Symbols)
		delete(c.entries, path)
		return nil, false
	}
	return e.result, true
}

// put stores an extraction if it fits.
func (c *symbolCache) put(path string, res *symbols.Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	incoming := len(res.Symbols)

	if incoming > maxCachedSymbols/maxEntryShare {
		return
	}

	if old, ok := c.entries[path]; ok {
		c.symbols -= len(old.result.Symbols)
		delete(c.entries, path)
	}

	if c.overBudget(incoming) {
		for k, e := range c.entries {
			if now.After(e.expires) {
				c.symbols -= len(e.result.Symbols)
				delete(c.entries, k)
			}
		}
	}
	if c.overBudget(incoming) {
		return
	}

	c.entries[path] = symbolEntry{result: res, expires: now.Add(symbolCacheTTL)}
	c.symbols += incoming
}

// overBudget reports whether admitting n more symbols would exceed either
// bound.
func (c *symbolCache) overBudget(incoming int) bool {
	return c.symbols+incoming > maxCachedSymbols || len(c.entries) >= maxCachedRepos
}

// envInt reads a positive integer from the environment, falling back to def.
func envInt(name string, def int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// newSymbolDiskCache builds the on-disk symbol cache for a server.
func newSymbolDiskCache(dir string) symbols.Cache {
	if dir == "off" {
		return nil
	}
	if dir == "" {
		dir = DefaultSymbolCacheDir()
	}
	c := symbols.NewDiskCache(dir)
	if c == nil {
		return nil
	}
	return c
}

// DefaultSymbolCacheDir returns the platform-default location for the
// persisted symbol index.
func DefaultSymbolCacheDir() string {
	if cache := os.Getenv("XDG_CACHE_HOME"); cache != "" {
		return filepath.Join(cache, "corral", "symbols")
	}
	home, err := auditUserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "corral", "symbols")
	}
	return filepath.Join(home, ".cache", "corral", "symbols")
}
