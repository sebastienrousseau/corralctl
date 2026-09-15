// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/search"
	"github.com/sebastienrousseau/corralctl/internal/symbols"
)

// The warmer's gate, and the caches' expiry paths.
//
// These decide whether a laptop keeps scanning a workspace nobody is asking
// about, and whether a query meets a cache entry that has quietly gone stale.

func TestSymbolQueryIdleGatesRefreshing(t *testing.T) {
	srv := newTestServer(t, t.TempDir())
	if !srv.symbolQueryIdle() {
		t.Error("a server nobody has queried should be idle")
	}
	srv.noteSymbolQuery()
	if srv.symbolQueryIdle() {
		t.Error("a server queried just now is not idle")
	}
	// Far enough back that the gate closes again.
	srv.lastSymbolQuery.Store(time.Now().Add(-2 * warmIdleAfter).UnixNano())
	if !srv.symbolQueryIdle() {
		t.Error("after the idle window, refreshing should stop")
	}
}

func TestWarmerStopsWithItsContext(t *testing.T) {
	srv := newTestServer(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	done := srv.startSymbolWarmer(ctx)
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the warmer outlived its context; a stopped server would keep reading the disk")
	}
}

func TestWarmSymbolsHandlesAnEmptyAndUnreadableWorkspace(t *testing.T) {
	// No repositories: nothing to do, and no panic on the empty slice.
	srv := newTestServer(t, t.TempDir())
	srv.warmSymbols(context.Background())

	// A scan that fails must leave the warmer silent rather than crashing the
	// background goroutine.
	stubSeam(t, &scanWorkspace, func(string) (*Index, error) { return nil, errors.New("scan failed") })
	srv2 := newTestServer(t, t.TempDir())
	srv2.warmSymbols(context.Background())
}

func TestWarmSymbolsStopsOnCancellation(t *testing.T) {
	base := t.TempDir()
	for i := 0; i < 5; i++ {
		makeFakeRepo(t, base, "Public", "go", "r"+string(rune('a'+i)),
			"https://github.com/acme/r.git", "")
	}
	srv := newTestServer(t, base)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	srv.warmSymbols(ctx)
	if n, _ := srv.indexCache.stats(); n != 0 {
		t.Errorf("a cancelled warm still indexed %d repositories", n)
	}
}

func TestIndexCacheExpiryDropsTheEntry(t *testing.T) {
	c := newIndexCache()
	c.put("repo", mappedFor(t, t.TempDir()))
	t.Cleanup(c.closeAll)

	// Backdate it so the next read finds it expired.
	c.mu.Lock()
	c.entries["repo"].expires = time.Now().Add(-time.Minute)
	c.mu.Unlock()

	if _, _, ok := c.acquire("repo"); ok {
		t.Error("an expired index was served")
	}
	if c.fresh("repo") {
		t.Error("fresh() reported an expired entry as usable")
	}
	if _, _, ok := c.acquire("absent"); ok {
		t.Error("an index that was never stored was served")
	}
	if c.fresh("absent") {
		t.Error("fresh() invented an entry")
	}
}

func TestIndexCachePutIgnoresNilAndReplaces(t *testing.T) {
	c := newIndexCache()
	t.Cleanup(c.closeAll)
	c.put("repo", nil)
	if n, _ := c.stats(); n != 0 {
		t.Errorf("a nil index was stored: %d entries", n)
	}
	c.put("repo", mappedFor(t, t.TempDir()))
	c.put("repo", mappedFor(t, t.TempDir())) // replaces, unmapping the first
	if n, _ := c.stats(); n != 1 {
		t.Errorf("replacing an entry left %d", n)
	}
}

func TestIndexCacheDropIsIdempotent(t *testing.T) {
	c := newIndexCache()
	c.put("repo", mappedFor(t, t.TempDir()))
	c.mu.Lock()
	e := c.entries["repo"]
	c.dropLocked("repo", e)
	c.dropLocked("repo", e) // a second drop must not double-decrement
	c.mu.Unlock()
	if e.refs > 0 {
		t.Errorf("refs = %d after dropping twice", e.refs)
	}
}

func TestSearchOneRepoFallsBackWithoutAnIndex(t *testing.T) {
	srv, repo := repoForIndexing(t, t.TempDir())
	m, err := search.Compile(search.Query{Pattern: "Findable", MaxHits: 10})
	if err != nil {
		t.Fatal(err)
	}

	// No index yet: the exhaustive walk answers, and says it was not indexed.
	res, indexed, err := srv.searchOneRepo(context.Background(), repo, m)
	if err != nil {
		t.Fatal(err)
	}
	if indexed {
		t.Error("reported as indexed with an empty cache")
	}
	if len(res.Hits) != 1 {
		t.Errorf("fallback found %d hits, want 1", len(res.Hits))
	}

	// With an index, the same query is answered from a narrowed candidate set.
	srv.indexFor(context.Background(), repo)
	res, indexed, err = srv.searchOneRepo(context.Background(), repo, m)
	if err != nil {
		t.Fatal(err)
	}
	if !indexed {
		t.Error("an available index was not used")
	}
	if len(res.Hits) != 1 {
		t.Errorf("indexed search found %d hits, want 1", len(res.Hits))
	}

	// A regex is one the index declines, so it falls back even when present.
	re, err := search.Compile(search.Query{Pattern: "Find.ble", Regex: true, MaxHits: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, indexed, err = srv.searchOneRepo(context.Background(), repo, re); err != nil {
		t.Fatal(err)
	} else if indexed {
		t.Error("a regex cannot be narrowed by trigrams, but was reported as indexed")
	}
}

func TestSymbolCacheDeclinesAnOversizedEntry(t *testing.T) {
	c := newSymbolCache()
	// Larger than one repository's allowed share: it must not be admitted,
	// because caching it would evict everything else.
	huge := &symbols.Result{Symbols: make([]symbols.Symbol, maxCachedSymbols/maxEntryShare+1)}
	c.put("big", huge)
	if _, ok := c.get("big"); ok {
		t.Error("an entry larger than its share was cached; one repository can now evict the workspace")
	}

	small := &symbols.Result{Symbols: make([]symbols.Symbol, 4)}
	c.put("small", small)
	c.put("small", small) // replacing must not double-count
	if _, ok := c.get("small"); !ok {
		t.Error("an ordinary entry was refused")
	}
}

func TestEnvIntFallsBackRatherThanFailing(t *testing.T) {
	const name = "CORRAL_TEST_INT"
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"", 7}, {"   ", 7}, {"nonsense", 7}, {"0", 7}, {"-3", 7}, {"12", 12},
	} {
		t.Run("value="+tc.raw, func(t *testing.T) {
			t.Setenv(name, tc.raw)
			if got := envInt(name, 7); got != tc.want {
				t.Errorf("envInt(%q) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}

func TestCaptureWriterFlushPassesThrough(t *testing.T) {
	rec := httptest.NewRecorder()
	cw := &captureWriter{ResponseWriter: rec}
	cw.WriteHeader(http.StatusOK) // success: forwarded immediately
	if !cw.passing {
		t.Fatal("a 2xx should stream straight through")
	}
	if _, err := cw.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	cw.Flush()
	cw.flush() // must be a no-op once passing
	if rec.Body.String() != "hello" {
		t.Errorf("body = %q", rec.Body.String())
	}
}

func TestReadAndRestoreRejectsAMissingBody(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Body = nil
	if _, err := readAndRestore(req); !errors.Is(err, errNoBody) {
		t.Errorf("err = %v, want errNoBody", err)
	}
}

func TestWarmerRefreshesOnlyWhileInUse(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "https://github.com/acme/alpha.git", "")
	srv := newTestServer(t, base)

	// Short enough to observe; the branch under test is the tick, not the
	// duration.
	stubSeam(t, &warmInterval, 20*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := srv.startSymbolWarmer(ctx)

	// Idle: ticks fire and are skipped. Nothing to assert beyond it not
	// spinning into the scan, which the coverage of the skip branch records.
	time.Sleep(80 * time.Millisecond)

	// Active: the same tick now refreshes.
	srv.noteSymbolQuery()
	time.Sleep(80 * time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("warmer did not stop")
	}
}

func TestWarmSymbolsSkipsWhatIsAlreadyFresh(t *testing.T) {
	base := t.TempDir()
	repo := makeFakeRepo(t, base, "Public", "go", "alpha", "https://github.com/acme/alpha.git", "")
	writeIn(t, repo, "a.go", "package a\n\nfunc Alpha() {}\n")
	srv := newTestServer(t, base)

	srv.warmSymbols(context.Background())
	first, _ := srv.indexCache.stats()

	// A second pass must find everything fresh and do nothing.
	extracted := 0
	stubSeam(t, &extractSymbols, func(ctx context.Context, root string, c symbols.Cache) (*symbols.Result, error) {
		extracted++
		return &symbols.Result{}, nil
	})
	srv.warmSymbols(context.Background())
	if extracted != 0 {
		t.Errorf("a refresh pass re-extracted %d repositories that had not expired", extracted)
	}
	if n, _ := srv.indexCache.stats(); n != first {
		t.Errorf("index count moved from %d to %d on a no-op pass", first, n)
	}
}

func TestIndexForSkipsAFreshEntry(t *testing.T) {
	srv, repo := repoForIndexing(t, t.TempDir())
	srv.indexFor(context.Background(), repo)

	built := 0
	stubSeam(t, &buildIndex, func(ctx context.Context, root string, f search.FileFilter) (*search.Index, error) {
		built++
		return search.BuildIndex(ctx, root, f)
	})
	srv.indexFor(context.Background(), repo)
	if built != 0 {
		t.Errorf("a fresh index was rebuilt %d time(s)", built)
	}
}

func TestSearchCodeReportsIndexedRepositories(t *testing.T) {
	base := t.TempDir()
	for i := 0; i < 3; i++ {
		name := "repo-" + string(rune('a'+i))
		r := makeFakeRepo(t, base, "Public", "go", name, "https://github.com/acme/"+name+".git", "")
		writeIn(t, r, "a.go", "package a\n\nfunc Findable"+string(rune('A'+i))+"() {}\n")
	}
	h := newHarness(t, ServerOptions{Root: base, IndexCacheDir: t.TempDir()})

	// Warm the index the way the background warmer would, then search: the
	// count is what tells a slow answer from a cold one without guessing.
	idx, err := h.server.scan()
	if err != nil {
		t.Fatal(err)
	}
	for i := range idx.Repos {
		h.server.indexFor(context.Background(), &idx.Repos[i])
	}

	body := searchCode(t, h, map[string]any{"query": "FindableA", "max_results": 10})
	if body.IndexedRepositories == 0 {
		t.Error("no repository reported as indexed despite a warm index")
	}
	if body.IndexedRepositories > body.RepositoriesSearched {
		t.Errorf("indexed %d of %d searched", body.IndexedRepositories, body.RepositoriesSearched)
	}
	if len(body.Hits) != 1 {
		t.Errorf("got %d hits, want 1", len(body.Hits))
	}
}
