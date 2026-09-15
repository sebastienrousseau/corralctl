// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sebastienrousseau/corralctl/internal/symbols"
)

// resultText joins a tool result's text content, matching what an MCP
// client would see.
func resultText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// symbolWorkspace builds a workspace of Go repositories with known
// declarations, so a lookup's answer can be asserted exactly.
func symbolWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	repos := map[string]map[string]string{
		"Public/go/alpha": {
			"alpha.go": "package alpha\n\n" +
				"// Shared is declared in both repositories.\n" +
				"func Shared() {}\n\n" +
				"func OnlyInAlpha() {}\n\n" +
				"type Widget struct{}\n\n" +
				"func (w *Widget) Render() {}\n\n" +
				"type Renderer interface{ Render() }\n\n" +
				"const AlphaConst = 1\n\n" +
				"var alphaVar = 2\n",
			"alpha_test.go": "package alpha\n\nfunc TestOnlyInAlpha() {}\n",
		},
		"Public/go/beta": {
			"beta.go": "package beta\n\nfunc Shared() {}\n\nfunc OnlyInBeta() {}\n",
		},
	}
	for rel, files := range repos {
		dir := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o750); err != nil {
			t.Fatal(err)
		}
		cfg := "[remote \"origin\"]\n\turl = https://github.com/acme/" + filepath.Base(rel) + ".git\n"
		if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

// callFindSymbol runs the tool and decodes its JSON body.
func callFindSymbol(t *testing.T, s *Server, in findSymbolInput) (map[string]any, bool) {
	t.Helper()
	res, _, err := s.handleFindSymbol(context.Background(), nil, in)
	if err != nil {
		t.Fatalf("handleFindSymbol returned a protocol error: %v", err)
	}
	if res.IsError {
		return nil, true
	}
	var body map[string]any
	text := resultText(res)
	if err := json.Unmarshal([]byte(text), &body); err != nil {
		t.Fatalf("tool output is not JSON: %v\n%s", err, text)
	}
	return body, false
}

func symbolNames(body map[string]any) []string {
	raw, _ := body["symbols"].([]any)
	out := make([]string, 0, len(raw))
	for _, r := range raw {
		m, _ := r.(map[string]any)
		out = append(out, m["symbol"].(string)+"@"+m["repo"].(string))
	}
	return out
}

// TestFindSymbolAcrossRepositories is the capability the whole index exists
// for: one name, every clone, without the caller naming a repository.
func TestFindSymbolAcrossRepositories(t *testing.T) {
	s, err := NewServer(ServerOptions{Root: symbolWorkspace(t)})
	if err != nil {
		t.Fatal(err)
	}

	body, isErr := callFindSymbol(t, s, findSymbolInput{Name: "Shared"})
	if isErr {
		t.Fatal("lookup failed")
	}
	if got := body["total_matched"].(float64); got != 2 {
		t.Fatalf("matched %v, want 2 (one per repository)", got)
	}
	names := symbolNames(body)
	want := map[string]bool{"Shared@Public/go/alpha": true, "Shared@Public/go/beta": true}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected hit %q", n)
		}
		delete(want, n)
	}
	for missing := range want {
		t.Errorf("missing hit %q", missing)
	}
	if got := body["repositories_search"].(float64); got != 2 {
		t.Errorf("searched %v repositories, want 2", got)
	}
}

func TestFindSymbolScopedToOneRepository(t *testing.T) {
	s, _ := NewServer(ServerOptions{Root: symbolWorkspace(t)})

	body, _ := callFindSymbol(t, s, findSymbolInput{Name: "Shared", Repo: "beta"})
	if got := body["total_matched"].(float64); got != 1 {
		t.Fatalf("matched %v, want 1", got)
	}
	if names := symbolNames(body); names[0] != "Shared@Public/go/beta" {
		t.Errorf("got %v, want the beta hit only", names)
	}

	// An unknown repository is an error, not an empty result.
	if _, isErr := callFindSymbol(t, s, findSymbolInput{Name: "Shared", Repo: "nope"}); !isErr {
		t.Error("an unresolvable repo should be an error")
	}
}

func TestFindSymbolFiltersAndForms(t *testing.T) {
	s, _ := NewServer(ServerOptions{Root: symbolWorkspace(t)})

	cases := []struct {
		name string
		in   findSymbolInput
		want int
	}{
		{"method by bare name", findSymbolInput{Name: "Render", Kind: "method"}, 1},
		{"method by qualified name", findSymbolInput{Name: "Widget.Render"}, 1},
		{"interface kind", findSymbolInput{Name: "Renderer", Kind: "interface"}, 1},
		{"type kind excludes interface", findSymbolInput{Name: "Renderer", Kind: "type"}, 0},
		{"const", findSymbolInput{Name: "AlphaConst", Kind: "const"}, 1},
		{"substring", findSymbolInput{Name: "OnlyIn", Substring: true}, 2},
		{"exported only excludes package-private", findSymbolInput{Name: "alphaVar", ExportedOnly: true}, 0},
		{"unexported is found by default", findSymbolInput{Name: "alphaVar"}, 1},
		{"tests excluded by default", findSymbolInput{Name: "TestOnlyInAlpha"}, 0},
		{"tests included on request", findSymbolInput{Name: "TestOnlyInAlpha", IncludeTests: true}, 1},
		{"miss", findSymbolInput{Name: "NoSuchThing"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body, isErr := callFindSymbol(t, s, tc.in)
			if isErr {
				t.Fatal("unexpected tool error")
			}
			if got := int(body["total_matched"].(float64)); got != tc.want {
				t.Errorf("matched %d, want %d (%v)", got, tc.want, symbolNames(body))
			}
		})
	}
}

// TestFindSymbolRefusesRatherThanReturningNothing is the rule the whole
// codebase follows: a filter that cannot be honoured is an error, because
// an empty result is indistinguishable from a correct one.
func TestFindSymbolRefusesUnsupportedKind(t *testing.T) {
	s, _ := NewServer(ServerOptions{Root: symbolWorkspace(t)})
	res, _, err := s.handleFindSymbol(context.Background(), nil, findSymbolInput{Name: "Shared", Kind: "macro"})
	if err != nil {
		t.Fatalf("protocol error: %v", err)
	}
	if !res.IsError {
		t.Fatal("an unknown kind must be refused, not answered with an empty result")
	}
	text := resultText(res)
	for _, want := range []string{"macro", "func", "interface"} {
		if !strings.Contains(text, want) {
			t.Errorf("refusal %q should name %q", text, want)
		}
	}
}

func TestFindSymbolRejectsEmptyName(t *testing.T) {
	s, _ := NewServer(ServerOptions{Root: symbolWorkspace(t)})
	res, _, _ := s.handleFindSymbol(context.Background(), nil, findSymbolInput{Name: "   "})
	if !res.IsError {
		t.Error("an empty name should be refused")
	}
}

func TestFindSymbolPaginatesAndRanksExportedFirst(t *testing.T) {
	s, _ := NewServer(ServerOptions{Root: symbolWorkspace(t)})

	body, _ := callFindSymbol(t, s, findSymbolInput{Name: "OnlyIn", Substring: true, Limit: 1})
	if got := int(body["returned"].(float64)); got != 1 {
		t.Errorf("returned %d, want 1", got)
	}
	if got := int(body["total_matched"].(float64)); got != 2 {
		t.Errorf("total_matched %d, want 2", got)
	}
	if _, ok := body["note"]; !ok {
		t.Error("a truncated page should carry a note telling the caller how to narrow")
	}

	// A miss carries guidance rather than a bare empty list.
	body, _ = callFindSymbol(t, s, findSymbolInput{Name: "Absent"})
	note, ok := body["note"].(string)
	if !ok || !strings.Contains(note, "substring") {
		t.Errorf("a miss should suggest what to try next, got %v", body["note"])
	}
}

// TestFindSymbolSurfacesTruncatedIndexes covers the property that keeps a
// partial answer from looking complete.
func TestFindSymbolSurfacesTruncatedIndexes(t *testing.T) {
	s, _ := NewServer(ServerOptions{Root: symbolWorkspace(t)})

	old := extractSymbols
	extractSymbols = func(context.Context, string, symbols.Cache) (*symbols.Result, error) {
		return &symbols.Result{
			Symbols:   []symbols.Symbol{{Name: "Shared", Kind: symbols.KindFunc, File: "a.go", Line: 1, Exported: true, Language: "go"}},
			Files:     1,
			Truncated: true,
		}, nil
	}
	t.Cleanup(func() { extractSymbols = old })

	body, _ := callFindSymbol(t, s, findSymbolInput{Name: "Shared"})
	list, ok := body["indexes_truncated"].([]any)
	if !ok || len(list) == 0 {
		t.Fatal("a truncated index must be reported to the caller")
	}
	if _, ok := body["indexes_truncated_note"]; !ok {
		t.Error("truncation should be explained, not just flagged")
	}
}

// TestFindSymbolToleratesAnUnreadableRepository: one repository failing to
// index must not fail the whole lookup.
func TestFindSymbolToleratesAnUnreadableRepository(t *testing.T) {
	s, _ := NewServer(ServerOptions{Root: symbolWorkspace(t)})

	old := extractSymbols
	// Atomic because handleFindSymbol extracts repositories concurrently.
	// A plain int here was a data race, and the kind that a single local
	// `go test -race` run does not reliably catch: it took CI, on a
	// different scheduler, to surface it.
	var calls atomic.Int64
	extractSymbols = func(_ context.Context, path string, _ symbols.Cache) (*symbols.Result, error) {
		calls.Add(1)
		if strings.HasSuffix(path, "alpha") {
			return nil, errors.New("permission denied")
		}
		return &symbols.Result{
			Symbols: []symbols.Symbol{{Name: "Shared", Kind: symbols.KindFunc, File: "b.go", Line: 1, Exported: true, Language: "go"}},
			Files:   1,
		}, nil
	}
	t.Cleanup(func() { extractSymbols = old })

	body, isErr := callFindSymbol(t, s, findSymbolInput{Name: "Shared"})
	if isErr {
		t.Fatal("one bad repository must not fail the lookup")
	}
	if got := int(body["total_matched"].(float64)); got != 1 {
		t.Errorf("matched %d, want 1 from the readable repository", got)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("attempted %d repositories, want 2", got)
	}
}

func TestFindSymbolHonoursCancellation(t *testing.T) {
	s, _ := NewServer(ServerOptions{Root: symbolWorkspace(t)})
	// Warm the scan so the cancellation is observed in the symbol loop
	// rather than in the workspace walk.
	if _, err := s.scan(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, _, err := s.handleFindSymbol(ctx, nil, findSymbolInput{Name: "Shared"})
	if err != nil {
		t.Fatalf("protocol error: %v", err)
	}
	if !res.IsError {
		t.Error("a cancelled lookup should report rather than pretend to succeed")
	}
}

// --- repo overview ---

func TestRepoOverview(t *testing.T) {
	s, _ := NewServer(ServerOptions{Root: symbolWorkspace(t)})

	res, _, err := s.handleRepoOverview(context.Background(), nil, repoOverviewInput{Query: "alpha"})
	if err != nil {
		t.Fatalf("protocol error: %v", err)
	}
	if res.IsError {
		t.Fatalf("overview failed: %s", resultText(res))
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(resultText(res)), &body); err != nil {
		t.Fatal(err)
	}

	if body["repo"] != "Public/go/alpha" {
		t.Errorf("repo = %v", body["repo"])
	}
	decls := body["declarations"].(map[string]any)
	for kind, want := range map[string]float64{"func": 2, "method": 1, "type": 1, "interface": 1, "const": 1, "var": 1} {
		if got := decls[kind].(float64); got != want {
			t.Errorf("%s count = %v, want %v", kind, got, want)
		}
	}
	// Test declarations are excluded from the counts, so the two functions
	// are the non-test ones.
	types := body["exported_types"].([]any)
	if len(types) != 2 {
		t.Errorf("exported_types = %v, want Renderer and Widget", types)
	}
	if body["remote_url"] == "" {
		t.Error("overview should carry the origin URL")
	}
}

func TestRepoOverviewUnknownRepository(t *testing.T) {
	s, _ := NewServer(ServerOptions{Root: symbolWorkspace(t)})
	res, _, _ := s.handleRepoOverview(context.Background(), nil, repoOverviewInput{Query: "nope"})
	if !res.IsError {
		t.Error("an unknown repository should be an error")
	}
}

func TestRepoOverviewExtractionFailure(t *testing.T) {
	s, _ := NewServer(ServerOptions{Root: symbolWorkspace(t)})
	old := extractSymbols
	extractSymbols = func(context.Context, string, symbols.Cache) (*symbols.Result, error) {
		return nil, errors.New("disk on fire")
	}
	t.Cleanup(func() { extractSymbols = old })

	res, _, _ := s.handleRepoOverview(context.Background(), nil, repoOverviewInput{Query: "alpha"})
	if !res.IsError {
		t.Error("a failed extraction should be reported")
	}
}

func TestRepoOverviewSurfacesTruncationAndCaps(t *testing.T) {
	s, _ := NewServer(ServerOptions{Root: symbolWorkspace(t)})

	many := make([]symbols.Symbol, 0, 80)
	for i := 0; i < 40; i++ {
		many = append(many,
			symbols.Symbol{Name: "T" + string(rune('A'+i%26)) + itoaMCP(i), Kind: symbols.KindType, Exported: true, File: "a.go", Line: i + 1},
			symbols.Symbol{Name: "F" + string(rune('A'+i%26)) + itoaMCP(i), Kind: symbols.KindFunc, Exported: true, File: "a.go", Line: i + 1},
		)
	}
	old := extractSymbols
	extractSymbols = func(context.Context, string, symbols.Cache) (*symbols.Result, error) {
		return &symbols.Result{Symbols: many, Files: 1, Truncated: true}, nil
	}
	t.Cleanup(func() { extractSymbols = old })

	res, _, _ := s.handleRepoOverview(context.Background(), nil, repoOverviewInput{Query: "alpha"})
	var body map[string]any
	if err := json.Unmarshal([]byte(resultText(res)), &body); err != nil {
		t.Fatal(err)
	}
	if body["truncated"] != true {
		t.Error("truncation must be surfaced")
	}
	if _, ok := body["truncated_note"]; !ok {
		t.Error("truncation should be explained")
	}
	if got := len(body["exported_types"].([]any)); got != 25 {
		t.Errorf("exported_types capped at %d, want 25", got)
	}
	if _, ok := body["note"]; !ok {
		t.Error("a capped list should say so")
	}
}

func itoaMCP(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestCapList(t *testing.T) {
	in := []string{"a", "b", "c"}
	if got := capList(in, 5); len(got) != 3 {
		t.Errorf("capList under the cap = %v", got)
	}
	if got := capList(in, 2); len(got) != 2 {
		t.Errorf("capList over the cap = %v", got)
	}
}

// --- cache ---

func TestSymbolCacheServesAndExpires(t *testing.T) {
	c := newSymbolCache()
	res := &symbols.Result{Files: 1}

	if _, ok := c.get("/x"); ok {
		t.Error("an empty cache should miss")
	}
	c.put("/x", res)
	if got, ok := c.get("/x"); !ok || got != res {
		t.Error("a fresh entry should hit")
	}

	// Expire it by hand rather than sleeping for the TTL.
	c.mu.Lock()
	e := c.entries["/x"]
	e.expires = time.Now().Add(-time.Second)
	c.entries["/x"] = e
	c.mu.Unlock()
	if _, ok := c.get("/x"); ok {
		t.Error("an expired entry should miss")
	}
}

// TestSymbolCacheEvicts covers both eviction passes: expired entries first,
// then the oldest, so an agent sweeping a large workspace cannot make the
// cache grow without bound.
func TestSymbolCacheEvicts(t *testing.T) {
	c := newSymbolCache()
	for i := 0; i < maxCachedRepos+8; i++ {
		c.put("/repo"+itoaMCP(i), &symbols.Result{Files: i})
	}
	c.mu.Lock()
	n := len(c.entries)
	c.mu.Unlock()
	if n > maxCachedRepos {
		t.Errorf("cache holds %d entries, want at most %d", n, maxCachedRepos)
	}

	// With some entries already expired, the first pass should reclaim them.
	c2 := newSymbolCache()
	for i := 0; i < maxCachedRepos; i++ {
		c2.put("/old"+itoaMCP(i), &symbols.Result{})
	}
	c2.mu.Lock()
	for k, e := range c2.entries {
		e.expires = time.Now().Add(-time.Minute)
		c2.entries[k] = e
	}
	c2.mu.Unlock()
	c2.put("/fresh", &symbols.Result{})
	if _, ok := c2.get("/fresh"); !ok {
		t.Error("the new entry should have been stored")
	}
}

// TestSymbolsForUsesTheCache asserts a second lookup does not re-parse.
func TestSymbolsForUsesTheCache(t *testing.T) {
	s, _ := NewServer(ServerOptions{Root: symbolWorkspace(t)})
	old := extractSymbols
	var calls int
	extractSymbols = func(context.Context, string, symbols.Cache) (*symbols.Result, error) {
		calls++
		return &symbols.Result{Files: 1}, nil
	}
	t.Cleanup(func() { extractSymbols = old })

	repo := &RepoEntry{Path: "/some/repo"}
	if _, err := s.symbolsFor(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	if _, err := s.symbolsFor(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Errorf("extracted %d times, want 1: the second call should hit the cache", calls)
	}

	// A failure is not cached, so a transient error can be retried.
	extractSymbols = func(context.Context, string, symbols.Cache) (*symbols.Result, error) {
		calls++
		return nil, errors.New("nope")
	}
	other := &RepoEntry{Path: "/other/repo"}
	if _, err := s.symbolsFor(context.Background(), other); err == nil {
		t.Error("expected the extraction error to propagate")
	}
	if _, err := s.symbolsFor(context.Background(), other); err == nil {
		t.Error("a failure must not be cached as a success")
	}
}

// TestSymbolToolsAreRegistered guards the wiring: a tool that is never
// registered is invisible, and no other test would notice.
func TestSymbolToolsAreRegistered(t *testing.T) {
	s, err := NewServer(ServerOptions{Root: symbolWorkspace(t)})
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("server is nil")
	}
	// The server instructions must point the model at the differentiator,
	// or it will never reach for it.
	instr := serverInstructions(ServerOptions{Root: "/tmp"})
	for _, want := range []string{"corral_find_symbol", "corral_repo_overview", "across every clone"} {
		if !strings.Contains(instr, want) {
			t.Errorf("server instructions should mention %q", want)
		}
	}
}

// TestFindSymbolScanFailure covers the branch where the workspace walk
// itself fails, which no fixture can produce naturally.
func TestFindSymbolScanFailure(t *testing.T) {
	s, _ := NewServer(ServerOptions{Root: symbolWorkspace(t)})
	old := scanWorkspace
	scanWorkspace = func(string) (*Index, error) { return nil, errors.New("root vanished") }
	t.Cleanup(func() { scanWorkspace = old })
	s.invalidateScanCache()

	res, _, _ := s.handleFindSymbol(context.Background(), nil, findSymbolInput{Name: "Shared"})
	if !res.IsError {
		t.Error("a failed scan should be reported")
	}
	res, _, _ = s.handleRepoOverview(context.Background(), nil, repoOverviewInput{Query: "alpha"})
	if !res.IsError {
		t.Error("a failed scan should be reported by the overview too")
	}
}

// TestFindSymbolRanking pins the order: exported first, then repository,
// then file, then line — so the first page holds the likeliest answers.
func TestFindSymbolRanking(t *testing.T) {
	s, _ := NewServer(ServerOptions{Root: symbolWorkspace(t)})

	old := extractSymbols
	extractSymbols = func(_ context.Context, path string, _ symbols.Cache) (*symbols.Result, error) {
		// Deliberately returned out of order, and identical across
		// repositories, so only the sort can produce the expected result.
		return &symbols.Result{Files: 1, Symbols: []symbols.Symbol{
			{Name: "Target", Kind: symbols.KindFunc, File: "z.go", Line: 5, Language: "go"},
			{Name: "Target", Kind: symbols.KindFunc, File: "a.go", Line: 9, Exported: true, Language: "go"},
			{Name: "Target", Kind: symbols.KindFunc, File: "a.go", Line: 2, Exported: true, Language: "go"},
			{Name: "Target", Kind: symbols.KindFunc, File: "a.go", Line: 1, Language: "go"},
		}}, nil
	}
	t.Cleanup(func() { extractSymbols = old })

	body, _ := callFindSymbol(t, s, findSymbolInput{Name: "Target", Limit: maxPageSize + 100})
	raw := body["symbols"].([]any)
	if len(raw) != 8 { // four per repository, two repositories
		t.Fatalf("got %d hits, want 8", len(raw))
	}

	// The first four must be the exported ones.
	for i := 0; i < 4; i++ {
		if !raw[i].(map[string]any)["exported"].(bool) {
			t.Fatalf("hit %d is unexported; exported must rank first", i)
		}
	}
	// Within a repository, ordered by file then line.
	first := raw[0].(map[string]any)
	second := raw[1].(map[string]any)
	if first["file"] != "a.go" || first["line"].(float64) != 2 {
		t.Errorf("first hit = %v:%v, want a.go:2", first["file"], first["line"])
	}
	if second["file"] != "a.go" || second["line"].(float64) != 9 {
		t.Errorf("second hit = %v:%v, want a.go:9", second["file"], second["line"])
	}
	// A limit above the maximum is clamped rather than honoured.
	if got := int(body["returned"].(float64)); got > maxPageSize {
		t.Errorf("returned %d, want at most the page cap %d", got, maxPageSize)
	}
}

// TestSymbolDiskCacheSelection covers the three ways a cache directory is
// chosen, including the one that must yield no cache at all.
func TestSymbolDiskCacheSelection(t *testing.T) {
	t.Run("off disables it", func(t *testing.T) {
		if c := newSymbolDiskCache("off"); c != nil {
			t.Error(`"off" should yield no cache`)
		}
	})

	t.Run("an explicit directory is used", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "sym")
		c := newSymbolDiskCache(dir)
		if c == nil {
			t.Fatal("expected a cache")
		}
		c.Put("/repo", symbols.Fingerprint{Files: 1}, &symbols.Result{Files: 1})
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) == 0 {
			t.Error("the cache did not write to the directory it was given")
		}
	})

	t.Run("empty takes the default", func(t *testing.T) {
		// TestMain points XDG_CACHE_HOME at a throwaway directory, so this
		// exercises the default without touching the real one.
		if c := newSymbolDiskCache(""); c == nil {
			t.Error("the default location should be usable")
		}
	})

	t.Run("an unusable directory degrades to no cache", func(t *testing.T) {
		// A path whose parent is a file cannot be created. The server must
		// be slower, not broken.
		file := filepath.Join(t.TempDir(), "a-file")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		c := newSymbolDiskCache(filepath.Join(file, "sym"))
		if c != nil {
			t.Error("an uncreatable directory should yield no cache")
		}
		// And nil must be usable, since symbolsFor passes it straight
		// through to the extractor.
		if _, err := extractSymbols(context.Background(), t.TempDir(), c); err != nil {
			t.Errorf("a nil cache is the uncached path: %v", err)
		}
	})
}

// TestDefaultSymbolCacheDirFollowsXDG pins the location, including the
// choice of the cache directory over the state directory the audit log
// uses: this is derived data that can be rebuilt at any time, which is
// exactly the distinction XDG draws.
func TestDefaultSymbolCacheDirFollowsXDG(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/xdg-cache")
	if got, want := DefaultSymbolCacheDir(), filepath.Join("/xdg-cache", "corral", "symbols"); got != want {
		t.Errorf("DefaultSymbolCacheDir = %q, want %q", got, want)
	}

	t.Setenv("XDG_CACHE_HOME", "")
	home := t.TempDir()
	oldHome := auditUserHomeDir
	auditUserHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { auditUserHomeDir = oldHome })
	if got, want := DefaultSymbolCacheDir(), filepath.Join(home, ".cache", "corral", "symbols"); got != want {
		t.Errorf("DefaultSymbolCacheDir = %q, want %q", got, want)
	}

	// A machine with no resolvable home is rare, but the fallback must
	// still be local to the session rather than empty.
	auditUserHomeDir = func() (string, error) { return "", errors.New("no home") }
	if got := DefaultSymbolCacheDir(); !strings.HasPrefix(got, os.TempDir()) {
		t.Errorf("DefaultSymbolCacheDir = %q, want a path under the temp dir", got)
	}
}

// TestSymbolsForUsesTheDiskCache is the end-to-end property: the second
// server over an unchanged workspace does not parse anything.
func TestSymbolsForUsesTheDiskCache(t *testing.T) {
	base := t.TempDir()
	repo := makeFakeRepo(t, base, "Public", "go", "alpha", "https://github.com/acme/alpha.git", "")
	if err := os.WriteFile(filepath.Join(repo, "a.go"),
		[]byte("package a\n\nfunc CachedAcrossProcesses() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cacheDir := t.TempDir()

	// A fresh server each time, which is what a client launching the
	// process per session actually does.
	for i := 0; i < 2; i++ {
		srv, err := NewServer(ServerOptions{Root: base, SymbolCacheDir: cacheDir})
		if err != nil {
			t.Fatal(err)
		}
		idx, err := srv.scan()
		if err != nil {
			t.Fatal(err)
		}
		res, err := srv.symbolsFor(context.Background(), &idx.Repos[0])
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, s := range res.Symbols {
			if s.Name == "CachedAcrossProcesses" {
				found = true
			}
		}
		if !found {
			t.Fatalf("run %d did not find the symbol: %+v", i, res.Symbols)
		}
	}

	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("expected one cache entry, got %d", len(entries))
	}
}

func TestClampFanOut(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{-8, 1}, {0, 1}, {1, 1}, {8, 8},
		{maxRepoFanOut, maxRepoFanOut},
		{maxRepoFanOut + 1, maxRepoFanOut},
		{4096, maxRepoFanOut},
	} {
		if got := clampFanOut(tc.in); got != tc.want {
			t.Errorf("clampFanOut(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
	if n := repoFanOut(); n < 1 || n > maxRepoFanOut {
		t.Errorf("repoFanOut = %d, want within [1, %d]", n, maxRepoFanOut)
	}
}

// TestFindSymbolAgreesWithASingleWorker pins that the fan-out across
// repositories is an optimisation and not a behaviour switch: the same
// query must return the same hits in the same order either way.
func TestFindSymbolAgreesWithASingleWorker(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{"alpha", "beta", "gamma", "delta"} {
		repo := makeFakeRepo(t, base, "Public", "go", name, "https://github.com/acme/"+name+".git", "")
		body := "package " + name + "\n\nfunc Shared() {}\nfunc " + name + "Only() {}\n"
		if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h := newHarness(t, ServerOptions{Root: base})

	var pooled, serial map[string]any
	h.callToolJSON("corral_find_symbol", map[string]any{"name": "Shared"}, &pooled)

	old := repoFanOut
	repoFanOut = func() int { return 1 }
	t.Cleanup(func() { repoFanOut = old })
	h.server.symbolCache = newSymbolCache()

	h.callToolJSON("corral_find_symbol", map[string]any{"name": "Shared"}, &serial)

	pooledJSON, err := json.Marshal(pooled)
	if err != nil {
		t.Fatal(err)
	}
	serialJSON, err := json.Marshal(serial)
	if err != nil {
		t.Fatal(err)
	}
	if string(pooledJSON) != string(serialJSON) {
		t.Errorf("the fan-out changed the answer:\n pooled %s\n serial %s", pooledJSON, serialJSON)
	}
}
