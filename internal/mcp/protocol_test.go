// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Protocol-level tests.
//
// These drive a real MCP client against a real server over the SDK's in-memory
// transport, so they exercise URI-template routing, argument decoding against
// the generated schema, annotation serialisation and error shape. The tests
// they replace called handler functions directly, which is how a resource
// template that matched nothing at all passed for several releases.

func TestToolsListExposesTheExpectedSet(t *testing.T) {
	h := newHarness(t, ServerOptions{Root: t.TempDir()})
	tools := h.tools()

	readOnly := []string{
		"corral_list_repos", "corral_find_repo", "corral_get_repo_metadata",
		"corral_status_summary", "corral_workspace_index", "corral_doctor",
	}
	for _, name := range readOnly {
		if tools[name] == nil {
			t.Errorf("read tool %s is not registered", name)
		}
	}
	// Write tools must not appear unless explicitly enabled.
	for _, name := range []string{
		"corral_sync_repo", "corral_clone_repo", "corral_delete_repo",
		"corral_create_worktree", "corral_release_worktree", "corral_prune_worktrees",
	} {
		if tools[name] != nil {
			t.Errorf("%s must not be registered on a read-only server", name)
		}
	}
}

// TestToolAnnotationsOnTheWire is the regression test for annotation defaults.
// An omitted destructiveHint serialises as true, so leaving annotations unset
// marked every read tool destructive and made corral_delete_repo's annotation
// indistinguishable from a directory listing's. Clients use these to decide
// whether to auto-approve, so the read tools paid a confirmation tax while
// deletion gave no warning at all.
func TestToolAnnotationsOnTheWire(t *testing.T) {
	h := newHarness(t, ServerOptions{
		Root: t.TempDir(), EnableMutations: true, EnableDestructiveMutations: true,
		AuditLogPath: filepath.Join(t.TempDir(), "audit.log"),
	})
	tools := h.tools()

	want := map[string]struct{ readOnly, destructive bool }{
		"corral_list_repos":        {true, false},
		"corral_find_repo":         {true, false},
		"corral_get_repo_metadata": {true, false},
		"corral_status_summary":    {true, false},
		"corral_workspace_index":   {true, false},
		"corral_doctor":            {true, false},
		"corral_sync_repo":         {false, false},
		"corral_clone_repo":        {false, false},
		"corral_create_worktree":   {false, false},
		"corral_release_worktree":  {false, false},
		"corral_prune_worktrees":   {false, false},
		"corral_delete_repo":       {false, true},
	}
	for name, exp := range want {
		tool := tools[name]
		if tool == nil {
			t.Errorf("%s not registered", name)
			continue
		}
		if tool.Annotations == nil {
			t.Errorf("%s has no annotations; clients then assume the unsafe defaults", name)
			continue
		}
		if tool.Annotations.ReadOnlyHint != exp.readOnly {
			t.Errorf("%s readOnlyHint = %t, want %t", name, tool.Annotations.ReadOnlyHint, exp.readOnly)
		}
		if tool.Annotations.DestructiveHint == nil {
			t.Errorf("%s destructiveHint absent; the spec default is true, which marks a read tool destructive", name)
		} else if *tool.Annotations.DestructiveHint != exp.destructive {
			t.Errorf("%s destructiveHint = %t, want %t", name, *tool.Annotations.DestructiveHint, exp.destructive)
		}
	}
}

// Input schemas are derived from Go structs by the SDK. Assert the parameters
// actually reach the wire, since a mistyped tag would silently produce a tool
// nothing can call correctly.
func TestToolInputSchemasAreGenerated(t *testing.T) {
	h := newHarness(t, ServerOptions{Root: t.TempDir()})
	tools := h.tools()

	schema, err := marshalToMap(tools["corral_list_repos"].InputSchema)
	if err != nil {
		t.Fatalf("input schema is not JSON: %v", err)
	}
	props, _ := schema["properties"].(map[string]any)
	for _, want := range []string{"visibility", "language", "name_contains", "synced_only", "limit", "offset", "response_format"} {
		if _, ok := props[want]; !ok {
			t.Errorf("corral_list_repos schema is missing %q", want)
		}
	}

	// A required field must be marked required, or a model may omit it.
	findSchema, err := marshalToMap(tools["corral_find_repo"].InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := findSchema["required"].([]any)
	found := false
	for _, r := range req {
		if r == "query" {
			found = true
		}
	}
	if !found {
		t.Errorf("corral_find_repo must mark 'query' required, got required=%v", req)
	}
}

func TestListReposFiltersAndPages(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "https://github.com/o/alpha.git", `{"last_synced_at":"2026-06-30T00:00:00Z"}`)
	makeFakeRepo(t, base, "Public", "rust", "beta", "", "")
	makeFakeRepo(t, base, "Private", "go", "gamma", "", "")
	h := newHarness(t, ServerOptions{Root: base})

	var byLang struct {
		Total int `json:"total_matched"`
	}
	h.callToolJSON("corral_list_repos", map[string]any{"language": "go"}, &byLang)
	if byLang.Total != 2 {
		t.Errorf("language filter: total_matched = %d, want 2", byLang.Total)
	}

	var byVis struct {
		Total int `json:"total_matched"`
	}
	h.callToolJSON("corral_list_repos", map[string]any{"visibility": "private"}, &byVis)
	if byVis.Total != 1 {
		t.Errorf("visibility filter: total_matched = %d, want 1", byVis.Total)
	}

	var synced struct {
		Total int `json:"total_matched"`
	}
	h.callToolJSON("corral_list_repos", map[string]any{"synced_only": true}, &synced)
	if synced.Total != 1 {
		t.Errorf("synced_only: total_matched = %d, want 1", synced.Total)
	}

	// Paging metadata must be present and honest.
	var page struct {
		Total      int              `json:"total_matched"`
		Returned   int              `json:"returned"`
		NextOffset int              `json:"next_offset"`
		Repos      []map[string]any `json:"repos"`
	}
	h.callToolJSON("corral_list_repos", map[string]any{"limit": 2}, &page)
	if page.Total != 3 || page.Returned != 2 || page.NextOffset != 2 {
		t.Errorf("paging = total %d returned %d next %d, want 3/2/2", page.Total, page.Returned, page.NextOffset)
	}
	// Concise is the default projection, so the expensive fields must be absent.
	if _, ok := page.Repos[0]["remote_url"]; ok {
		t.Error("default projection must be concise; remote_url leaked")
	}
	// And detailed must actually add them back. Assert against the fixture that
	// has an origin: remote_url is omitempty, so a repo without one would drop
	// the field regardless of projection.
	h.callToolJSON("corral_list_repos", map[string]any{"response_format": "detailed", "name_contains": "alpha"}, &page)
	if len(page.Repos) != 1 {
		t.Fatalf("expected exactly the alpha fixture, got %d", len(page.Repos))
	}
	if _, ok := page.Repos[0]["remote_url"]; !ok {
		t.Errorf("detailed projection should include remote_url, got %v", page.Repos[0])
	}
}

func TestFindRepoResolvesAndReportsAmbiguity(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	makeFakeRepo(t, base, "Private", "go", "alpha", "", "")
	makeFakeRepo(t, base, "Public", "rust", "beta", "", "")
	h := newHarness(t, ServerOptions{Root: base})

	var match map[string]any
	h.callToolJSON("corral_find_repo", map[string]any{"query": "beta"}, &match)
	if match["rel_path"] != "Public/rust/beta" {
		t.Errorf("unique match = %v", match["rel_path"])
	}

	text, isErr := h.callTool("corral_find_repo", map[string]any{"query": "alpha"})
	if !isErr {
		t.Fatal("an ambiguous query must return a tool error")
	}
	// The error must name the candidates, or the model cannot narrow.
	if !strings.Contains(text, "Public/go/alpha") || !strings.Contains(text, "Private/go/alpha") {
		t.Errorf("ambiguity error should list candidates, got: %s", text)
	}

	if _, isErr := h.callTool("corral_find_repo", map[string]any{"query": "nope"}); !isErr {
		t.Error("an unknown repository must return a tool error")
	}
}

func TestStatusSummaryCounts(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "a", "", `{"last_synced_at":"2026-01-01T00:00:00Z"}`)
	makeFakeRepo(t, base, "Public", "go", "b", "", "")
	makeFakeRepo(t, base, "Private", "rust", "c", "", "")
	h := newHarness(t, ServerOptions{Root: base})

	var got struct {
		Total  int            `json:"total"`
		Synced int            `json:"synced"`
		ByVis  map[string]int `json:"by_visibility"`
	}
	h.callToolJSON("corral_status_summary", nil, &got)
	if got.Total != 3 || got.Synced != 1 {
		t.Errorf("total=%d synced=%d, want 3/1", got.Total, got.Synced)
	}
	if got.ByVis["Public"] != 2 || got.ByVis["Private"] != 1 {
		t.Errorf("by_visibility = %v", got.ByVis)
	}
}

// TestFileResourceReadsNestedPaths is the regression test for the routing bug:
// the template used RFC 6570 simple expansion, which does not match "/", so no
// file below a repository's top level resolved at all. Only a test that goes
// through the router can catch it.
func TestFileResourceReadsNestedPaths(t *testing.T) {
	base := t.TempDir()
	repo := makeFakeRepo(t, base, "Public", "go", "alpha", "https://github.com/o/alpha.git", "")
	mustWrite(t, filepath.Join(repo, "README.md"), "top level")
	mustWrite(t, filepath.Join(repo, "src", "main.go"), "package main")
	mustWrite(t, filepath.Join(repo, "src", "deep", "x.txt"), "deeper")
	h := newHarness(t, ServerOptions{Root: base})

	for path, want := range map[string]string{
		"README.md":      "top level",
		"src/main.go":    "package main",
		"src/deep/x.txt": "deeper",
	} {
		got, err := h.readResource("corral://repo/Public/alpha/file/" + path)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if !strings.Contains(got, want) {
			t.Errorf("%s: got %q, want it to contain %q", path, got, want)
		}
	}
}

// TestFileResourceRefusesCredentials is the other half of that change: fixing
// the routing without a denylist would have turned an unreachable resource into
// a working credential-exfiltration primitive, because .git/config sits inside
// the repository and the path sandbox permits it by design.
func TestFileResourceRefusesCredentials(t *testing.T) {
	base := t.TempDir()
	repo := makeFakeRepo(t, base, "Public", "go", "alpha", "https://github.com/o/alpha.git", "")
	mustWrite(t, filepath.Join(repo, ".git", "config"),
		"[remote \"origin\"]\n\turl = https://ghp_SECRETTOKEN@github.com/o/alpha.git\n")
	mustWrite(t, filepath.Join(repo, ".env"), "AWS_SECRET_ACCESS_KEY=hunter2")
	mustWrite(t, filepath.Join(repo, "deploy.pem"), "-----BEGIN PRIVATE KEY-----")
	mustWrite(t, filepath.Join(repo, "main.go"), "package main")
	h := newHarness(t, ServerOptions{Root: base})

	for _, rel := range []string{".git/config", ".git/../.git/config", ".env", "deploy.pem"} {
		got, err := h.readResource("corral://repo/Public/alpha/file/" + rel)
		if err == nil {
			t.Errorf("%s: expected refusal, got %d bytes", rel, len(got))
			continue
		}
		if strings.Contains(err.Error(), "ghp_SECRETTOKEN") || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("%s: the refusal leaked the secret: %v", rel, err)
		}
	}
	// An ordinary file still reads, so the denylist is not simply blocking all.
	if got, err := h.readResource("corral://repo/Public/alpha/file/main.go"); err != nil {
		t.Errorf("ordinary file must remain readable: %v", err)
	} else if !strings.Contains(got, "package main") {
		t.Errorf("unexpected content: %q", got)
	}
}

func TestFileResourceRejectsTraversal(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	makeFakeRepo(t, base, "Public", "go", "sibling", "", "")
	mustWrite(t, filepath.Join(base, "Public", "go", "sibling", "secret.txt"), "sibling data")
	mustWrite(t, filepath.Join(base, "outside.txt"), "outside data")
	h := newHarness(t, ServerOptions{Root: base})

	for _, rel := range []string{"../../../outside.txt", "../sibling/secret.txt"} {
		if got, err := h.readResource("corral://repo/Public/alpha/file/" + rel); err == nil {
			t.Errorf("%s: traversal must be refused, got %q", rel, got)
		}
	}
}

func TestStateAndTreeResources(t *testing.T) {
	base := t.TempDir()
	repo := makeFakeRepo(t, base, "Public", "go", "alpha", "",
		`{"last_synced_at":"2026-06-30T00:00:00Z","last_synced_pushed_at":"2026-06-29T00:00:00Z"}`)
	mustWrite(t, filepath.Join(repo, "main.go"), "package main")
	makeFakeRepo(t, base, "Public", "go", "nostate", "", "")
	h := newHarness(t, ServerOptions{Root: base})

	got, err := h.readResource("corral://repo/Public/alpha/state")
	if err != nil {
		t.Fatalf("state resource: %v", err)
	}
	if !strings.Contains(got, "2026-06-30T00:00:00Z") {
		t.Errorf("state resource missing the sidecar timestamp: %s", got)
	}
	if _, err := h.readResource("corral://repo/Public/nostate/state"); err == nil {
		t.Error("a repo with no sidecar should report that, not succeed silently")
	}

	tree, err := h.readResource("corral://repo/Public/alpha/tree")
	if err != nil {
		t.Fatalf("tree resource: %v", err)
	}
	if !strings.Contains(tree, "main.go") {
		t.Errorf("tree should list main.go: %s", tree)
	}
	// .git is hidden from the listing so it does not swamp the output.
	if strings.Contains(tree, ".git/") {
		t.Errorf("tree must not list .git internals: %s", tree)
	}
}

func TestWorkspaceIndexResourceAndTool(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	makeFakeRepo(t, base, "Public", "go", "beta", "", "")
	h := newHarness(t, ServerOptions{Root: base})

	var idx struct {
		Total int `json:"total_matched"`
	}
	h.callToolJSON("corral_workspace_index", nil, &idx)
	if idx.Total != 2 {
		t.Errorf("workspace index total = %d, want 2", idx.Total)
	}
	if got, err := h.readResource("corral://workspace/index"); err != nil {
		t.Errorf("workspace index resource: %v", err)
	} else if !strings.Contains(got, "alpha") {
		t.Errorf("index resource missing repos: %s", got)
	}
}

func TestPromptsAreServed(t *testing.T) {
	h := newHarness(t, ServerOptions{Root: t.TempDir()})

	res := h.prompt("explain_workspace", nil)
	if len(res.Messages) == 0 {
		t.Fatal("explain_workspace returned no messages")
	}
	if !strings.Contains(promptText(res.Messages[0]), "corral_status_summary") {
		t.Error("explain_workspace should point at the summary tool")
	}

	res = h.prompt("identify_stale_repos", map[string]string{"threshold_days": "7"})
	if !strings.Contains(promptText(res.Messages[0]), "7") {
		t.Error("identify_stale_repos should honour threshold_days")
	}
}

func TestMutationToolsRequireTheirGates(t *testing.T) {
	base := t.TempDir()
	audit := filepath.Join(t.TempDir(), "audit.log")

	// Mutations off: no write tools at all.
	h := newHarness(t, ServerOptions{Root: base})
	if _, ok := h.tools()["corral_sync_repo"]; ok {
		t.Error("sync tool exposed without --enable-mutations")
	}

	// Mutations on, destructive off: delete must stay hidden.
	h = newHarness(t, ServerOptions{Root: base, EnableMutations: true, AuditLogPath: audit})
	tools := h.tools()
	if tools["corral_sync_repo"] == nil || tools["corral_clone_repo"] == nil {
		t.Error("sync/clone should be exposed with --enable-mutations")
	}
	if tools["corral_delete_repo"] != nil {
		t.Error("delete must require --enable-destructive-mutations as a second gate")
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRepoMetadataReturnsBranchAndState(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "https://github.com/o/alpha.git",
		`{"last_synced_at":"2026-06-30T00:00:00Z"}`)
	h := newHarness(t, ServerOptions{Root: base})

	var got struct {
		Repo struct {
			RelPath   string `json:"rel_path"`
			RemoteURL string `json:"remote_url"`
			State     *struct {
				LastSyncedAt string `json:"last_synced_at"`
			} `json:"state"`
		} `json:"repo"`
		CurrentBranch string `json:"current_branch"`
	}
	h.callToolJSON("corral_get_repo_metadata", map[string]any{"query": "alpha"}, &got)
	if got.Repo.RelPath != "Public/go/alpha" {
		t.Errorf("rel_path = %q", got.Repo.RelPath)
	}
	if got.Repo.RemoteURL != "https://github.com/o/alpha.git" {
		t.Errorf("remote_url = %q", got.Repo.RemoteURL)
	}
	if got.Repo.State == nil || got.Repo.State.LastSyncedAt == "" {
		t.Error("sidecar state should be included in the metadata payload")
	}

	if _, isErr := h.callTool("corral_get_repo_metadata", map[string]any{"query": "nope"}); !isErr {
		t.Error("unknown repository must be a tool error")
	}
}

// A workspace root that is itself a repository must not collapse the index, and
// paging must still report the real total. Guards the Scan fix at the protocol
// level, where a caller would actually notice.
func TestWorkspaceIndexPagesAndExcludesRoot(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a", "b", "c"} {
		makeFakeRepo(t, base, "Public", "go", n, "", "")
	}
	h := newHarness(t, ServerOptions{Root: base})

	var page struct {
		Total      int              `json:"total_matched"`
		Returned   int              `json:"returned"`
		NextOffset int              `json:"next_offset"`
		Repos      []map[string]any `json:"repos"`
	}
	h.callToolJSON("corral_workspace_index", map[string]any{"limit": 2}, &page)
	if page.Total != 3 {
		t.Errorf("total_matched = %d, want 3 (the root must not be an entry)", page.Total)
	}
	if page.Returned != 2 || page.NextOffset != 2 {
		t.Errorf("returned=%d next_offset=%d, want 2/2", page.Returned, page.NextOffset)
	}
	for _, r := range page.Repos {
		if r["rel_path"] == "." {
			t.Error("the workspace root leaked into the index")
		}
	}
	// The final page reports no continuation. Decode into a fresh value: the
	// key is *omitted* on the last page, so reusing `page` would leave the
	// previous call's next_offset in place and the assertion would be a lie.
	var last struct {
		Returned   int `json:"returned"`
		NextOffset int `json:"next_offset"`
	}
	h.callToolJSON("corral_workspace_index", map[string]any{"limit": 2, "offset": 2}, &last)
	if last.Returned != 1 || last.NextOffset != 0 {
		t.Errorf("last page: returned=%d next_offset=%d, want 1/0", last.Returned, last.NextOffset)
	}
}

func TestResourceErrorsForUnknownRepoAndBadURI(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	h := newHarness(t, ServerOptions{Root: base})

	for _, uri := range []string{
		"corral://repo/Public/ghost/tree",
		"corral://repo/Public/ghost/file/README.md",
		"corral://repo/Public/alpha/file/does-not-exist.txt",
	} {
		if _, err := h.readResource(uri); err == nil {
			t.Errorf("%s should have failed", uri)
		}
	}
}

// The file resource is bounded, so one enormous file cannot exhaust a client.
func TestFileResourceTruncatesLargeFiles(t *testing.T) {
	base := t.TempDir()
	repo := makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	big := strings.Repeat("x", maxFileBytes+2048)
	mustWrite(t, filepath.Join(repo, "big.txt"), big)
	h := newHarness(t, ServerOptions{Root: base})

	got, err := h.readResource("corral://repo/Public/alpha/file/big.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "truncated") {
		t.Error("an over-sized file should be reported as truncated")
	}
	if len(got) > maxFileBytes+512 {
		t.Errorf("returned %d bytes, want it bounded near %d", len(got), maxFileBytes)
	}
}

func TestServerAccessors(t *testing.T) {
	base := t.TempDir()
	audit := filepath.Join(t.TempDir(), "audit.log")

	ro, err := NewServer(ServerOptions{Root: base, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if ro.Root() != base {
		t.Errorf("Root() = %q", ro.Root())
	}
	if ro.MutationsEnabled() {
		t.Error("MutationsEnabled() should be false by default")
	}
	if ro.AuditLogPath() != "" {
		t.Errorf("a read-only server has no audit log, got %q", ro.AuditLogPath())
	}

	rw, err := NewServer(ServerOptions{Root: base, Version: "test", EnableMutations: true, AuditLogPath: audit})
	if err != nil {
		t.Fatal(err)
	}
	if !rw.MutationsEnabled() {
		t.Error("MutationsEnabled() should be true when enabled")
	}
	if rw.AuditLogPath() != audit {
		t.Errorf("AuditLogPath() = %q, want %q", rw.AuditLogPath(), audit)
	}
}

// ServeStdio delegates to the transport runner; the seam is stubbed so the test
// does not block on stdin.
func TestServeStdioDelegates(t *testing.T) {
	called := false
	stubSeam(t, &serveStdio, func(*mcp.Server) error { called = true; return nil })
	srv := newTestServer(t, t.TempDir())
	if err := srv.ServeStdio(); err != nil {
		t.Fatalf("ServeStdio: %v", err)
	}
	if !called {
		t.Error("ServeStdio should delegate to the stdio runner")
	}
}

func TestServeStdioWithMetricsAddr(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	called := false
	stubSeam(t, &serveStdio, func(*mcp.Server) error {
		called = true
		resp, err := http.Get("http://" + addr + MetricsEndpoint)
		if err != nil {
			t.Errorf("metrics request failed: %v", err)
			return nil
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("metrics status = %d, want %d", resp.StatusCode, http.StatusOK)
		}
		return nil
	})

	srv, err := NewServer(ServerOptions{
		Root:        t.TempDir(),
		MetricsAddr: addr,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.ServeStdio(); err != nil {
		t.Fatalf("ServeStdio: %v", err)
	}
	if !called {
		t.Error("expected serveStdio to be called")
	}
}

func TestStartMetricsServerListenError(t *testing.T) {
	oldListen := listenMetrics
	defer func() { listenMetrics = oldListen }()
	listenMetrics = func(network, address string) (net.Listener, error) {
		return nil, errors.New("simulated listen error")
	}

	srv, err := NewServer(ServerOptions{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	done := srv.startMetricsServer(context.Background(), "127.0.0.1:9099")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for startMetricsServer to return on listen error")
	}
}

type mockErrListener struct{}

func (mockErrListener) Accept() (net.Conn, error) { return nil, errors.New("simulated accept error") }
func (mockErrListener) Close() error               { return nil }
func (mockErrListener) Addr() net.Addr             { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9099} }

func TestStartMetricsServerServeError(t *testing.T) {
	oldListen := listenMetrics
	defer func() { listenMetrics = oldListen }()
	listenMetrics = func(network, address string) (net.Listener, error) {
		return mockErrListener{}, nil
	}

	srv, err := NewServer(ServerOptions{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	done := srv.startMetricsServer(context.Background(), "127.0.0.1:9099")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for startMetricsServer to return on serve error")
	}
}

// A workspace larger than the scan cap must say so. Before this was surfaced,
// an over-cap workspace looked complete to a caller, which is worse than an
// error: the agent confidently concludes a repository does not exist.
func TestOverCapWorkspaceIsReportedAsTruncated(t *testing.T) {
	base := t.TempDir()
	for _, n := range []string{"a", "b", "c"} {
		makeFakeRepo(t, base, "Public", "go", n, "", "")
	}
	old := maxIndexRepos
	maxIndexRepos = 2
	t.Cleanup(func() { maxIndexRepos = old })

	h := newHarness(t, ServerOptions{Root: base})
	for _, tool := range []string{"corral_list_repos", "corral_workspace_index"} {
		var got struct {
			Truncated bool `json:"workspace_truncated"`
		}
		h.callToolJSON(tool, map[string]any{}, &got)
		if !got.Truncated {
			t.Errorf("%s must report workspace_truncated when the scan cap is hit", tool)
		}
	}
}

// The tree resource is bounded the same way, and says so rather than silently
// returning a partial listing.
func TestTreeResourceIsBoundedAndDepthLimited(t *testing.T) {
	base := t.TempDir()
	repo := makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	// Deeper than the walk's depth limit.
	deep := filepath.Join(repo, "one", "two", "three")
	if err := os.MkdirAll(deep, 0o750); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(deep, "buried.txt"), "x")
	mustWrite(t, filepath.Join(repo, "one", "shallow.txt"), "x")
	h := newHarness(t, ServerOptions{Root: base})

	got, err := h.readResource("corral://repo/Public/alpha/tree")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "shallow.txt") {
		t.Error("entries within the depth limit should be listed")
	}
	if strings.Contains(got, "buried.txt") {
		t.Error("the tree walk should stop at its depth limit")
	}
}

// Malformed URIs are rejected twice over. The SDK's URI-template matcher
// refuses them first — a client sees a generic "Resource not found" rather
// than corral's specific message — so resolveURIRepo's own checks are
// defence-in-depth and have to be exercised directly.
func TestResolveURIRejectsMalformedURIs(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	srv := newTestServer(t, base)

	for _, tc := range []struct{ uri, want string }{
		{"https://example.com/repo/Public/alpha/tree", "unsupported scheme"},
		{"corral://repo/alpha", "missing owner/name"},
		{"corral://repo//", "missing owner/name"},
		{"corral://repo/Public/ghost", "no repository Public/ghost"},
	} {
		_, err := srv.resolveURIRepo(tc.uri)
		if err == nil {
			t.Errorf("%s should have failed", tc.uri)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q should mention %q", tc.uri, err, tc.want)
		}
	}

	// And the client-visible behaviour: a non-matching URI is simply not found.
	h := newHarness(t, ServerOptions{Root: base})
	if _, err := h.readResource("corral://repo/alpha/tree"); err == nil {
		t.Error("a URI that matches no template must fail")
	}
}

// A nested-group origin URL is the only thing that can resolve this repository:
// neither its visibility segment nor its parent directory is the owner.
func TestResourceResolvesRepoByNestedRemoteOwner(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "widget",
		"https://git.example.com/parent/team/widget.git", "")
	h := newHarness(t, ServerOptions{Root: base})

	for _, owner := range []string{"parent", "team"} {
		if _, err := h.readResource("corral://repo/" + owner + "/widget/tree"); err != nil {
			t.Errorf("owner %q should resolve via the origin URL: %v", owner, err)
		}
	}
	if _, err := h.readResource("corral://repo/stranger/widget/tree"); err == nil {
		t.Error("an unrelated owner must not resolve")
	}
}

// A misconfigured root (pointing at a file, or at nothing) must produce a clear
// tool error from every read path, not a panic and not an empty-looking
// workspace that an agent would read as "you have no repositories".
func TestEveryReadPathReportsAnUnscannableRoot(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "root-is-a-file")
	mustWrite(t, notADir, "x")

	for _, root := range []string{notADir, filepath.Join(t.TempDir(), "does-not-exist")} {
		h := newHarness(t, ServerOptions{Root: root})
		for _, tc := range []struct {
			tool string
			args map[string]any
		}{
			{"corral_list_repos", map[string]any{}},
			{"corral_workspace_index", map[string]any{}},
			{"corral_find_repo", map[string]any{"query": "alpha"}},
			{"corral_get_repo_metadata", map[string]any{"query": "alpha"}},
			{"corral_status_summary", map[string]any{}},
		} {
			text, isErr := h.callTool(tc.tool, tc.args)
			if !isErr {
				t.Errorf("%s on unscannable root %q returned success: %s", tc.tool, root, text)
			}
		}
		if _, err := h.readResource("corral://workspace/index"); err == nil {
			t.Errorf("the workspace index resource should fail on root %q", root)
		}
	}
}

func TestTreeResourceReportsItsOwnTruncation(t *testing.T) {
	base := t.TempDir()
	repo := makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	for _, n := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		mustWrite(t, filepath.Join(repo, n), "x")
	}
	old := maxTreeEntries
	maxTreeEntries = 2
	t.Cleanup(func() { maxTreeEntries = old })

	h := newHarness(t, ServerOptions{Root: base})
	got, err := h.readResource("corral://repo/Public/alpha/tree")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "truncated at 2 entries") {
		t.Errorf("a truncated tree must say so, and say at what bound; got:\n%s", got)
	}
}

// A path segment that is not valid percent-encoding must be rejected, not
// silently passed through to the filesystem.
func TestFileResourceRejectsBadPercentEncoding(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	h := newHarness(t, ServerOptions{Root: base})

	if _, err := h.readResource("corral://repo/Public/alpha/file/%zz.txt"); err == nil {
		t.Error("a malformed percent-escape must be rejected")
	}
}

// An unreadable file is an error, not empty content: an agent must not read
// "no permission" as "this file is blank".
// A file the reader cannot open must surface as an error rather than an empty
// document, so an agent is never told a file is blank when it was actually
// refused. Covered two ways, because neither alone is sufficient:
//
// The seam case runs on every platform. The real-filesystem case cannot: on
// Windows os.Chmod only toggles the read-only attribute, which does not stop a
// read, so chmod(0o000) there produces a perfectly readable file and the
// assertion passes vacuously — which is how this test failed CI on
// windows-latest while passing on macOS and Linux.
//
// The real-filesystem case still earns its place where the OS supports it: it
// is what proves the seam is standing in for something that genuinely happens,
// rather than for an error only the stub can produce.
func TestFileResourceReportsUnreadableFile(t *testing.T) {
	base := t.TempDir()
	repo := makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	secret := filepath.Join(repo, "locked.txt")
	mustWrite(t, secret, "content")

	t.Run("open refused", func(t *testing.T) {
		stubSeam(t, &openResource, func(string) (*os.File, error) {
			return nil, fs.ErrPermission
		})
		h := newHarness(t, ServerOptions{Root: base})
		_, err := h.readResource("corral://repo/Public/alpha/file/locked.txt")
		if err == nil {
			t.Fatal("an unopenable file must surface as an error")
		}
		if !strings.Contains(err.Error(), "open file") {
			t.Errorf("error %q should say which step failed", err)
		}
	})

	t.Run("permission bits", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("os.Chmod on Windows toggles the read-only attribute only; it cannot make a file unreadable by its owner")
		}
		if os.Geteuid() == 0 {
			t.Skip("running as root: permission bits do not apply")
		}
		if err := os.Chmod(secret, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(secret, 0o600) })

		h := newHarness(t, ServerOptions{Root: base})
		if _, err := h.readResource("corral://repo/Public/alpha/file/locked.txt"); err == nil {
			t.Error("an unreadable file must surface as an error")
		}
	})
}
