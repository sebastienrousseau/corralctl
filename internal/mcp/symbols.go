// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sebastienrousseau/corralctl/internal/sanitize"
	"github.com/sebastienrousseau/corralctl/internal/symbols"
)

// Cross-repository symbol lookup.
//
// This is the tool the rest of the index exists to make possible. Every
// competing code-context server answers "where is this symbol" for one
// open repository; corral is the only one that can answer it across every
// clone on the machine, because it is the only one that knows they are all
// there.

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
//
// It used to cap at 24 repositories and evict by earliest expiry. Both were
// wrong for the access pattern that dominates — an unfiltered query, which
// touches every repository in the workspace.
//
// A repository count bounds entries, not memory: measured on a 234-repository
// workspace, one repository held 200,000 symbols and the median held 2,307.
//
// Equal TTLs make "evict the earliest expiry" into FIFO, and FIFO against a
// sequential sweep of N items through a cache of M < N is the pessimal case:
// it evicts precisely what the next call asks for first, so the hit rate is
// zero however large M is, short of N. That is why the old cache did not merely
// help less than it could — it could not help at all with the call that needed
// it most, and each query left the cache no warmer than the last.
//
// A miss is expensive even when the on-disk cache holds the answer, because the
// fingerprint keying that cache is computed by walking and stat-ing every source
// file in the repository, so a miss pays the walk whether or not it avoids the
// parse. That is what put corral_find_symbol at a 20.1s p95, and what made it
// 60s on a busy machine.
//
// The budget is in symbols because that is what occupies memory. Measured on
// the same workspace: 539,879 symbols retained 260.5MB, or 506 bytes each —
// so the default below is about 126MB. It is deliberately not large enough to
// hold every workspace; what stops a cache that cannot fit everything from
// being useless is the admission policy, not the size.
const defaultMaxCachedSymbols = 250_000

// maxCachedSymbols is the budget in force. Overridable because the right
// number depends on how much memory the machine can spare, and a server
// indexing someone's whole workspace should not be the thing that decides.
var maxCachedSymbols = envInt("CORRAL_SYMBOL_CACHE_SYMBOLS", defaultMaxCachedSymbols)

// maxEntryShare caps how much of the budget a single repository may occupy.
//
// Without it one outlier evicts the workspace: the 200,000-symbol repository
// above is 80% of the default budget on its own, so caching it would drop
// almost everything else, and the next query would re-extract all of it. A
// repository over this share is simply not cached — it pays its own cost
// rather than charging it to the other 233.
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
//
// Deliberately not an LRU: the eviction policy is "drop everything expired,
// then drop the oldest", which is a few lines and behaves identically for
// the access pattern that actually occurs — an agent working through a
// handful of repositories in one session.
type symbolCache struct {
	mu      sync.Mutex
	entries map[string]symbolEntry
	// symbols tracks the total held, so the budget does not require walking
	// every entry on each insert.
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
		// Drop it here rather than leave it to the next insert, so the
		// counter cannot drift above what is actually reachable.
		c.symbols -= len(e.result.Symbols)
		delete(c.entries, path)
		return nil, false
	}
	return e.result, true
}

// put stores an extraction if it fits.
//
// Expired entries are dropped first — they are free to release. If the cache is
// still at budget after that, the new entry is NOT admitted: everything left is
// fresh, and evicting a fresh entry to make room is what turned a full-workspace
// sweep into a cache that never held anything. Declining instead gives a stable
// resident set, so a sweep that cannot fit still hits on the part that does.
func (c *symbolCache) put(path string, res *symbols.Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	incoming := len(res.Symbols)

	// One repository must not be able to evict the workspace.
	if incoming > maxCachedSymbols/maxEntryShare {
		return
	}

	// Replacing an entry must not double-count its symbols.
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
		// Full of fresh entries. Keep them.
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
//
// A zero, negative or unparseable value falls back rather than erroring: a
// typo in an environment variable should not stop the server from starting,
// and the cost of the fallback is a cache of the default size.
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

// symbolsFor extracts one repository's symbols through two caches.
//
// The in-memory one answers a repeated query within a session without
// touching the disk at all. The on-disk one is what makes the *first*
// query of a session fast, and is the reason the memory cache can stay
// small: a client that launches this server per session used to pay the
// full extraction every time.
func (s *Server) symbolsFor(ctx context.Context, repo *RepoEntry) (*symbols.Result, error) {
	if cached, ok := s.symbolCache.get(repo.Path); ok {
		return cached, nil
	}
	res, err := extractSymbols(ctx, repo.Path, s.symbolDisk)
	if err != nil {
		return nil, err
	}
	s.symbolCache.put(repo.Path, res)
	return res, nil
}

// repoFanOut bounds how many repositories are extracted at once.
//
// Higher than GOMAXPROCS because the work is dominated by waiting on the
// filesystem, and bounded because each repository's own extraction is
// already parallel underneath — an unbounded fan-out turns a disk queue
// into contention.
var repoFanOut = func() int {
	return clampFanOut(runtime.GOMAXPROCS(0) * 2)
}

// maxRepoFanOut caps the fan-out regardless of core count.
const maxRepoFanOut = 16

// clampFanOut holds a worker count inside [1, maxRepoFanOut].
//
// Separated from repoFanOut so the bounds can be asserted against the pure
// function rather than against whatever core count the test machine
// happens to have — a clamp only tested on a 10-core laptop is a clamp
// nobody has tested.
func clampFanOut(n int) int {
	if n < 1 {
		return 1
	}
	if n > maxRepoFanOut {
		return maxRepoFanOut
	}
	return n
}

// extractSymbols is indirected so tests can stub the expensive parse.
var extractSymbols = symbols.ExtractRepoCached

// findSymbolInput is the argument set for corral_find_symbol.
type findSymbolInput struct {
	Name         string `json:"name" jsonschema:"Symbol to find. Matched exactly and case-insensitively; a method also matches its 'Receiver.Name' form."`
	Kind         string `json:"kind,omitempty" jsonschema:"Restrict to one kind: func, method, type, interface, const or var."`
	Repo         string `json:"repo,omitempty" jsonschema:"Restrict to one repository, by the same identifier corral_find_repo accepts. Omit to search every clone in the workspace."`
	Substring    bool   `json:"substring,omitempty" jsonschema:"Match any symbol whose name contains the query, instead of matching it exactly."`
	ExportedOnly bool   `json:"exported_only,omitempty" jsonschema:"Return only symbols visible outside their package."`
	IncludeTests bool   `json:"include_tests,omitempty" jsonschema:"Include declarations from test files. Excluded by default because they usually outnumber everything else."`
	Limit        int    `json:"limit,omitempty" jsonschema:"Maximum symbols to return. Default 50, maximum 200."`
}

// repoOverviewInput is the argument set for corral_repo_overview.
type repoOverviewInput struct {
	Query string `json:"query" jsonschema:"Repository identifier: bare name, relative path, or any unique path suffix."`
}

// registerSymbolTools attaches the symbol tools. They are read-only and
// local, like the rest of the read set.
func (s *Server) registerSymbolTools() {
	langs := strings.Join(symbols.Languages(), ", ")

	addTool(s, &mcp.Tool{
		Name:        "corral_find_symbol",
		Title:       "Find where a symbol is defined",
		Annotations: readOnlyAnnotations(),
		Description: "Find where a function, method, type, interface, constant or variable is defined, across every repository in the Corral workspace — not just one. This is the tool to reach for when you know a name but not which repository it lives in. Returns file and line, not source: read the file at the location it gives you. Indexed languages: " + langs + ". Test declarations are excluded unless include_tests is set. Results are paginated and the response reports whether any repository's index was truncated.",
	}, s.handleFindSymbol)

	addTool(s, &mcp.Tool{
		Name:        "corral_repo_overview",
		Title:       "Summarise one repository",
		Annotations: readOnlyAnnotations(),
		Description: "Orient in a single repository in one call: its location and origin, how many source files it has, its declaration counts by kind, and its most significant exported types and functions. Cheaper and far smaller than listing the tree and reading files. Use it before corral_find_symbol when you do not yet know what a repository contains.",
	}, s.handleRepoOverview)
}

// handleFindSymbol resolves a symbol across one repository or all of them.
func (s *Server) handleFindSymbol(ctx context.Context, _ *mcp.CallToolRequest, in findSymbolInput) (*mcp.CallToolResult, FindSymbolOutput, error) {
	s.noteSymbolQuery()
	if strings.TrimSpace(in.Name) == "" {
		return nil, FindSymbolOutput{}, fmt.Errorf("name must not be empty")
	}

	query := symbols.Query{
		Name:         in.Name,
		Substring:    in.Substring,
		ExportedOnly: in.ExportedOnly,
		IncludeTests: in.IncludeTests,
	}
	if in.Kind != "" {
		kind, err := symbols.ParseKind(in.Kind)
		if err != nil {
			// A filter that cannot be honoured is an error, never an empty
			// result: the two are indistinguishable to the caller.
			return nil, FindSymbolOutput{}, err
		}
		query.Kind = kind
	}

	idx, err := s.scan()
	if err != nil {
		return nil, FindSymbolOutput{}, fmt.Errorf("scan workspace: %v", err)
	}

	targets := idx.Repos
	if in.Repo != "" {
		match, err := idx.Find(in.Repo)
		if err != nil {
			return nil, FindSymbolOutput{}, err
		}
		targets = []RepoEntry{*match}
	}

	// Repositories are searched concurrently.
	//
	// Extraction is dominated by walking the filesystem, not by parsing —
	// measured on a 187-repository workspace, the walk is roughly four
	// times the parse — and a walk spends most of its time waiting on the
	// kernel rather than using a core. Doing them one after another left
	// almost all of that wait unoverlapped.
	//
	// The fan-out is bounded because each repository's own extraction is
	// already parallel underneath: too many at once turns a disk queue
	// into contention and makes the whole thing slower.
	var (
		mu        sync.Mutex
		hits      []SymbolHit
		truncated []string
		scanned   int
		next      atomic.Int64
		wg        sync.WaitGroup
	)
	workers := repoFanOut()
	if workers > len(targets) {
		workers = len(targets)
	}

	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(targets) || ctx.Err() != nil {
					return
				}
				repo := &targets[i]
				res, err := s.symbolsFor(ctx, repo)
				if err != nil {
					// One unreadable repository must not fail the whole
					// lookup.
					continue
				}
				red := repo.Redacted()

				// Filtering happens outside the lock, so the shared state
				// is held only for the append.
				var local []SymbolHit
				for _, sym := range res.Symbols {
					if !query.Match(sym) {
						continue
					}
					local = append(local, SymbolHit{
						Repo:     red.RelPath,
						Symbol:   sanitize.Untrusted(sym.Qualified(), maxEntryName),
						Kind:     sym.Kind,
						File:     sanitize.Untrusted(sym.File, maxEntryPath),
						Line:     sym.Line,
						Exported: sym.Exported,
						Language: sym.Language,
						Test:     sym.Test,
					})
				}

				mu.Lock()
				scanned++
				if res.Truncated {
					truncated = append(truncated, red.RelPath)
				}
				hits = append(hits, local...)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		// A cancelled scan is reported *in the tool result*, not as a
		// transport error. MCP treats a returned Go error as the call
		// having failed, which would lose the partial count the agent can
		// still act on. This is the protocol's convention, not an
		// oversight.
		//nolint:nilerr // deliberate: cancellation is a tool result, not an error
		return toolError("cancelled after %d repositories", scanned), FindSymbolOutput{
			Query:              in.Name,
			RepositoriesSearch: scanned,
			Symbols:            []SymbolHit{},
			Note:               "Cancelled before every repository was searched; the counts are partial.",
		}, nil
	}
	// Workers finish in an arbitrary order, so this list is arbitrary
	// until the sort below. The truncation list needs its own ordering for
	// the same reason: two identical queries must not disagree.
	sort.Strings(truncated)

	// Exported before unexported, then by repository and location, so the
	// first page holds the answers most likely to be wanted.
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Exported != hits[j].Exported {
			return hits[i].Exported
		}
		if hits[i].Repo != hits[j].Repo {
			return hits[i].Repo < hits[j].Repo
		}
		if hits[i].File != hits[j].File {
			return hits[i].File < hits[j].File
		}
		return hits[i].Line < hits[j].Line
	})

	limit := in.Limit
	if limit <= 0 {
		limit = defaultPageSize
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	total := len(hits)
	if len(hits) > limit {
		hits = hits[:limit]
	}

	// Empty, not null: the schema says `array`, and "no match" is the answer
	// this tool gives most often.
	if hits == nil {
		hits = []SymbolHit{}
	}

	body := FindSymbolOutput{
		Query:              in.Name,
		RepositoriesSearch: scanned,
		TotalMatched:       total,
		Returned:           len(hits),
		Symbols:            hits,
	}
	if total == 0 {
		body.Note = "No match. Symbols come from " + strings.Join(symbols.Languages(), ", ") +
			" sources only; try substring:true, or include_tests:true if it is declared in a test."
	}
	if total > len(hits) {
		body.Note = "More matches than the limit; narrow with kind, repo or exported_only."
	}
	if len(truncated) > 0 {
		// A silently partial index is worse than a slow one: the caller
		// cannot tell a missing symbol from an absent one.
		body.IndexesTruncated = truncated
		body.IndexesTruncatedNote = "These repositories exceeded the per-repository file or symbol cap; their results are incomplete."
	}
	return jsonResult(body), body, nil
}

// handleRepoOverview summarises one repository in a single call.
func (s *Server) handleRepoOverview(ctx context.Context, _ *mcp.CallToolRequest, in repoOverviewInput) (*mcp.CallToolResult, RepoOverviewOutput, error) {
	s.noteSymbolQuery()
	idx, err := s.scan()
	if err != nil {
		return nil, RepoOverviewOutput{}, fmt.Errorf("scan workspace: %v", err)
	}
	match, err := idx.Find(in.Query)
	if err != nil {
		return nil, RepoOverviewOutput{}, err
	}
	res, err := s.symbolsFor(ctx, match)
	if err != nil {
		return nil, RepoOverviewOutput{}, fmt.Errorf("index %s: %v", match.Redacted().RelPath, err)
	}

	byKind := map[symbols.Kind]int{}
	var exportedTypes, exportedFuncs []string
	for _, sym := range res.Symbols {
		if sym.Test {
			continue
		}
		byKind[sym.Kind]++
		if !sym.Exported {
			continue
		}
		name := sanitize.Untrusted(sym.Qualified(), maxEntryName)
		switch sym.Kind {
		case symbols.KindType, symbols.KindInterface:
			exportedTypes = append(exportedTypes, name)
		case symbols.KindFunc:
			exportedFuncs = append(exportedFuncs, name)
		}
	}
	sort.Strings(exportedTypes)
	sort.Strings(exportedFuncs)

	const maxListed = 25
	red := match.Redacted()
	body := RepoOverviewOutput{
		Repo:       red.RelPath,
		Path:       red.Path,
		RemoteURL:  red.RemoteURL,
		Language:   red.Language,
		Visibility: red.Visibility,
		Files:      res.Files,
		Declarations: DeclarationCounts{
			Func: byKind[symbols.KindFunc], Method: byKind[symbols.KindMethod],
			Type: byKind[symbols.KindType], Interface: byKind[symbols.KindInterface],
			Const: byKind[symbols.KindConst], Var: byKind[symbols.KindVar],
		},
		ExportedTypes:     emptyIfNil(capList(exportedTypes, maxListed)),
		ExportedFunctions: emptyIfNil(capList(exportedFuncs, maxListed)),
	}
	if res.Truncated {
		body.Truncated = true
		body.TruncatedNote = "This repository exceeded the per-repository file or symbol cap; the counts are a lower bound."
	}
	if len(exportedTypes) > maxListed || len(exportedFuncs) > maxListed {
		body.Note = fmt.Sprintf("Lists are capped at %d; use corral_find_symbol for the rest.", maxListed)
	}
	return jsonResult(body), body, nil
}

// capList truncates a list to at most n entries.
func capList(xs []string, n int) []string {
	if len(xs) > n {
		return xs[:n]
	}
	return xs
}

// newSymbolDiskCache builds the on-disk symbol cache for a server.
//
// "off" disables it. An empty path takes the platform default. A
// directory that cannot be created yields nil, and a nil cache is simply
// the uncached path — a machine where the cache directory is unwritable
// should be slower, not broken.
func newSymbolDiskCache(dir string) symbols.Cache {
	if dir == "off" {
		return nil
	}
	if dir == "" {
		dir = DefaultSymbolCacheDir()
	}
	c := symbols.NewDiskCache(dir)
	if c == nil {
		// Explicitly nil rather than a typed nil in an interface, which
		// would be non-nil to every caller that checks.
		return nil
	}
	return c
}

// DefaultSymbolCacheDir returns the platform-default location for the
// persisted symbol index.
//
// $XDG_CACHE_HOME/corral/symbols, falling back to ~/.cache/corral/symbols.
// The cache directory rather than the state directory the audit log uses:
// this is derived data that can be rebuilt from the workspace at any time,
// which is exactly the distinction XDG draws between the two.
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
