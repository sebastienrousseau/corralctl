// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sebastienrousseau/corralctl/internal/git"
)

// failingAudit makes every audit write fail, which is how the "audit
// failed" arm of each mutation handler is reached. An unloggable mutation
// must surface to the agent, never proceed quietly.
func failingAudit(t *testing.T) {
	t.Helper()
	stubSeam(t, &openAuditFile, func(string, int, os.FileMode) (auditFile, error) {
		return nil, errors.New("audit device unavailable")
	})
}

// ---------------------------------------------------------------------------
// audit rotation
// ---------------------------------------------------------------------------

func TestAuditLogFinalRenameFailureIsReported(t *testing.T) {
	restoreAuditSeams(t)
	withSmallAuditLog(t, 1)
	path := filepath.Join(t.TempDir(), "mutations.log")
	auditor := NewAuditor(path)

	if err := auditor.Write(AuditRecord{Tool: "corral_sync_repo", Result: "ok"}); err != nil {
		t.Fatal(err)
	}

	// Fail only the rename that retires the active log, letting the
	// generation shuffle above it succeed. Without this distinction the
	// earlier rename fails first and this branch is never reached.
	real := renameAuditFile
	renameAuditFile = func(from, to string) error {
		if from == path {
			return errors.New("rename exploded")
		}
		return real(from, to)
	}

	err := auditor.Write(AuditRecord{Tool: "corral_sync_repo", Result: "ok"})
	if err == nil || !strings.Contains(err.Error(), "rotate audit log:") {
		t.Fatalf("expected the active-log rename failure to surface, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// index
// ---------------------------------------------------------------------------

func TestMarkStateSyncedRequiresRepository(t *testing.T) {
	err := markStateSynced(t.TempDir())
	if err == nil {
		t.Fatal("marking a non-repository as synced should fail")
	}
	if !strings.Contains(err.Error(), "resolve git dir") {
		t.Fatalf("error does not name the cause: %v", err)
	}
}

func TestSafeMutationPathReportsRelativisationFailure(t *testing.T) {
	base := t.TempDir()
	repo := makeFakeRepo(t, base, "Public", "go", "alpha", "", "")

	// SafePath relativises first and must keep working; only the second
	// call — SafeMutationPath's own root check — fails. Counting the calls
	// isolates that branch instead of failing the sandbox check itself.
	real := relSafePath
	calls := 0
	stubSeam(t, &relSafePath, func(base, target string) (string, error) {
		calls++
		if calls > 1 {
			return "", errors.New("relativisation exploded")
		}
		return real(base, target)
	})

	idx := &Index{Root: base}
	if _, err := idx.SafeMutationPath(repo); err == nil {
		t.Fatal("expected the relativisation failure to surface")
	} else if !strings.Contains(err.Error(), "resolving path") {
		t.Fatalf("error does not name the cause: %v", err)
	}
}

// ---------------------------------------------------------------------------
// mutations
// ---------------------------------------------------------------------------

func TestAuditWithoutAuditorIsRefused(t *testing.T) {
	// The mutation tools are only registered when an auditor exists, so
	// this should be unreachable — but a nil-deref here would be a
	// mutation with no record, which is the one outcome the audit
	// mechanism exists to prevent.
	err := (&Server{}).audit(AuditRecord{Tool: "corral_sync_repo"})
	if err == nil {
		t.Fatal("auditing with no auditor configured should fail")
	}
	if !strings.Contains(err.Error(), "no auditor configured") {
		t.Fatalf("error does not name the cause: %v", err)
	}
}

func TestSyncRepoRejectsUnknownQuery(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	h, _ := mutationHarness(t, base, false)

	out, isErr := h.callTool("corral_sync_repo", map[string]any{"query": "does-not-exist"})
	if !isErr {
		t.Fatalf("unknown repository was not refused: %s", out)
	}
	if !strings.Contains(out, "no repository matches") {
		t.Fatalf("refusal does not name the cause: %s", out)
	}
}

func TestSyncRepoReportsStateAndAuditFailureTogether(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	stubSeam(t, &gitPull, func(context.Context, string, git.PullOptions) error { return nil })
	stubSeam(t, &markSynced, func(string) error { return errors.New("sidecar write failed") })
	h, _ := mutationHarness(t, base, false)

	// The intent record must land before the failure, so audit only starts
	// failing once the sync is under way.
	failingAudit(t)

	out, isErr := h.callTool("corral_sync_repo", map[string]any{"query": "alpha"})
	if !isErr {
		t.Fatalf("a failed state update was reported as success: %s", out)
	}
	if !strings.Contains(out, "audit") {
		t.Fatalf("the audit failure was swallowed: %s", out)
	}
}

func TestDeleteRepoRejectsUnknownQuery(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	safeGuards(t)
	h, _ := mutationHarness(t, base, true)

	out, isErr := h.callTool("corral_delete_repo", map[string]any{"query": "does-not-exist"})
	if !isErr {
		t.Fatalf("unknown repository was not refused: %s", out)
	}
	if !strings.Contains(out, "no repository matches") {
		t.Fatalf("refusal does not name the cause: %s", out)
	}
}

func TestDeleteRepoRefusesUnsandboxablePath(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	safeGuards(t)
	stubSeam(t, &evalSafePath, func(string) (string, error) { return "", errors.New("symlink resolution exploded") })
	stubSeam(t, &relSafePath, func(string, string) (string, error) { return "..", nil })
	h, _ := mutationHarness(t, base, true)

	out, isErr := h.callTool("corral_delete_repo", map[string]any{"query": "alpha"})
	if !isErr {
		t.Fatalf("a path that escapes the root was not refused: %s", out)
	}
	if !strings.Contains(out, "escapes root") {
		t.Fatalf("refusal does not name the cause: %s", out)
	}
}

func TestDeleteRepoReportsRefusalAuditFailure(t *testing.T) {
	base := t.TempDir()
	// A directory that is not a git repository trips the first guard, so
	// the refusal path runs — and the audit of that refusal then fails.
	dir := filepath.Join(base, "Public", "go", "alpha")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	safeGuards(t)
	stubSeam(t, &gitIsRepository, func(string) bool { return false })
	h, _ := mutationHarness(t, base, true)
	failingAudit(t)

	out, isErr := h.callTool("corral_delete_repo", map[string]any{"query": "alpha"})
	if !isErr {
		t.Fatalf("delete of a non-repository was not refused: %s", out)
	}
	if !strings.Contains(out, "audit failed") {
		t.Fatalf("the audit failure was swallowed: %s", out)
	}
}

func TestDeleteRepoReportsIntentAuditFailure(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	safeGuards(t)
	removed := false
	stubSeam(t, &removeMutation, func(string) error { removed = true; return nil })
	h, _ := mutationHarness(t, base, true)
	failingAudit(t)

	out, isErr := h.callTool("corral_delete_repo", map[string]any{"query": "alpha"})
	if !isErr {
		t.Fatalf("an unloggable delete was allowed: %s", out)
	}
	if !strings.Contains(out, "audit intent failed") {
		t.Fatalf("refusal does not name the cause: %s", out)
	}
	if removed {
		t.Fatal("the clone was deleted even though the intent could not be recorded")
	}
}

func TestDeleteRepoReportsRemovalAndAuditFailureTogether(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	safeGuards(t)
	stubSeam(t, &removeMutation, func(string) error { return errors.New("device busy") })
	h, _ := mutationHarness(t, base, true)

	// Let the intent record land, then break auditing so the completion
	// record fails alongside the removal.
	calls := 0
	real := openAuditFile
	stubSeam(t, &openAuditFile, func(name string, flag int, perm os.FileMode) (auditFile, error) {
		calls++
		if calls > 1 {
			return nil, errors.New("audit device unavailable")
		}
		return real(name, flag, perm)
	})

	out, isErr := h.callTool("corral_delete_repo", map[string]any{"query": "alpha"})
	if !isErr {
		t.Fatalf("a failed removal was reported as success: %s", out)
	}
	if !strings.Contains(out, "remove failed") || !strings.Contains(out, "audit completion failed") {
		t.Fatalf("the result hides one of the two failures: %s", out)
	}
}

func TestCloneRepoReportsCompletionAuditFailure(t *testing.T) {
	base := t.TempDir()
	stubSeam(t, &gitClone, func(context.Context, string, string, git.CloneOptions) error { return nil })
	h, _ := mutationHarness(t, base, false)

	calls := 0
	real := openAuditFile
	stubSeam(t, &openAuditFile, func(name string, flag int, perm os.FileMode) (auditFile, error) {
		calls++
		if calls > 1 {
			return nil, errors.New("audit device unavailable")
		}
		return real(name, flag, perm)
	})

	out, isErr := h.callTool("corral_clone_repo", map[string]any{
		"url":    "https://github.com/acme/alpha.git",
		"target": filepath.Join(base, "Public", "go", "alpha"),
	})
	if !isErr {
		t.Fatalf("a clone whose completion could not be recorded was reported clean: %s", out)
	}
	if !strings.Contains(out, "audit completion failed") {
		t.Fatalf("result does not name the cause: %s", out)
	}
}

// ---------------------------------------------------------------------------
// resources
// ---------------------------------------------------------------------------

func TestWorkspaceIndexResourceReportsMarshalFailure(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	stubSeam(t, &marshalResource, func(any, string, string) ([]byte, error) {
		return nil, errors.New("marshal exploded")
	})
	h := newHarness(t, ServerOptions{Root: base})

	if _, err := h.readResource("corral://workspace/index"); err == nil {
		t.Fatal("expected the marshal failure to surface")
	}
}

func TestRepoStateResourceSurfacesFailures(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", `{"last_synced_at":"2026-01-01T00:00:00Z"}`)
	h := newHarness(t, ServerOptions{Root: base})

	if _, err := h.readResource("corral://repo/Public/missing/state"); err == nil {
		t.Fatal("state of an unknown repository should fail")
	}

	stubSeam(t, &marshalResource, func(any, string, string) ([]byte, error) {
		return nil, errors.New("marshal exploded")
	})
	if _, err := h.readResource("corral://repo/Public/alpha/state"); err == nil {
		t.Fatal("expected the marshal failure to surface")
	}
}

func TestRepoTreeResourceSurfacesFailures(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	h := newHarness(t, ServerOptions{Root: base})

	if _, err := h.readResource("corral://repo/Public/missing/tree"); err == nil {
		t.Fatal("tree of an unknown repository should fail")
	}

	stubSeam(t, &walkResource, func(string, fs.WalkDirFunc) error {
		return errors.New("walk exploded")
	})
	if _, err := h.readResource("corral://repo/Public/alpha/tree"); err == nil {
		t.Fatal("expected the walk failure to surface")
	}
}

func TestRepoTreeResourceSkipsUnreadableEntriesAndRoot(t *testing.T) {
	base := t.TempDir()
	repo := makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, ServerOptions{Root: base})

	// A walk that reports an error for one entry must not abort the whole
	// listing: an unreadable directory is a gap in the tree, not a failure
	// of the resource.
	real := walkResource
	stubSeam(t, &walkResource, func(root string, fn fs.WalkDirFunc) error {
		return real(root, func(path string, d fs.DirEntry, err error) error {
			if d != nil && d.Name() == "README.md" {
				return fn(path, d, errors.New("unreadable"))
			}
			return fn(path, d, err)
		})
	})

	out, err := h.readResource("corral://repo/Public/alpha/tree")
	if err != nil {
		t.Fatalf("listing failed on an unreadable entry: %v", err)
	}
	if strings.Contains(out, "README.md") {
		t.Fatalf("an entry the walk could not read was listed anyway:\n%s", out)
	}
}

func TestRepoTreeResourceStopsOnCancellation(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	srv, err := NewServer(ServerOptions{Root: base, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = srv.handleRepoTreeResource(ctx, &mcp.ReadResourceRequest{
		Params: &mcp.ReadResourceParams{URI: "corral://repo/Public/alpha/tree"},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled listing returned %v, want context.Canceled", err)
	}
}

func TestRepoFileResourceSurfacesFailures(t *testing.T) {
	base := t.TempDir()
	repo := makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, ServerOptions{Root: base})

	if _, err := h.readResource("corral://repo/Public/missing/file/README.md"); err == nil {
		t.Fatal("file read from an unknown repository should fail")
	}

	stubSeam(t, &readResourceAll, func(io.Reader) ([]byte, error) {
		return nil, errors.New("read exploded")
	})
	if _, err := h.readResource("corral://repo/Public/alpha/file/README.md"); err == nil {
		t.Fatal("expected the read failure to surface")
	}
}

func TestResolveURIRepoRejectsMalformedURI(t *testing.T) {
	srv, err := NewServer(ServerOptions{Root: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := srv.resolveURIRepo("corral://repo/%zz/name/state"); err == nil {
		t.Fatal("a URI that cannot be parsed should be rejected")
	}
}

func TestResolveURIRepoSurfacesScanFailure(t *testing.T) {
	srv, err := NewServer(ServerOptions{Root: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	stubSeam(t, &scanWorkspace, func(string) (*Index, error) { return nil, errors.New("scan exploded") })
	if _, err := srv.resolveURIRepo("corral://repo/Public/alpha/state"); err == nil {
		t.Fatal("expected the scan failure to surface")
	}
}

func TestOwnerMatchesURLRequiresBothOperands(t *testing.T) {
	if ownerMatchesURL("", "acme") {
		t.Fatal("an empty remote URL matched an owner")
	}
	if ownerMatchesURL("https://github.com/acme/alpha.git", "") {
		t.Fatal("an empty owner matched a remote URL")
	}
	if !ownerMatchesURL("https://github.com/acme/alpha.git", "acme") {
		t.Fatal("a genuine owner failed to match")
	}
}

func TestExtractFilePathRejectsUndecodablePath(t *testing.T) {
	if _, err := extractFilePath("corral://repo/o/n/file/%zz"); err == nil {
		t.Fatal("a percent-escape that cannot be decoded should be rejected")
	} else if !strings.Contains(err.Error(), "decoding path segment") {
		t.Fatalf("error does not name the cause: %v", err)
	}
}

// ---------------------------------------------------------------------------
// tools
// ---------------------------------------------------------------------------

func TestCurrentBranchReadsRealRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "trunk"},
		{"config", "user.name", "Test"},
		{"config", "user.email", "test@test.com"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) // #nosec G204 -- fixed fixture arguments
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-m", "init"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) // #nosec G204 -- fixed fixture arguments
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}

	if got := currentBranch(context.Background(), dir); got != "trunk" {
		t.Fatalf("currentBranch = %q, want %q", got, "trunk")
	}
}

func TestCurrentBranchReportsUnstartableGit(t *testing.T) {
	// An empty PATH means git cannot be found at all, so the failure is a
	// lookup error rather than a non-zero exit — the branch that has no
	// stderr to report.
	t.Setenv("PATH", "")
	if got := currentBranch(context.Background(), t.TempDir()); got != "" {
		t.Fatalf("currentBranch = %q, want empty when git cannot run", got)
	}
}

func TestSyncRepoRefusesUnsandboxablePath(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	stubSeam(t, &evalSafePath, func(string) (string, error) { return "", errors.New("symlink resolution exploded") })
	stubSeam(t, &relSafePath, func(string, string) (string, error) { return "..", nil })
	h, _ := mutationHarness(t, base, false)

	out, isErr := h.callTool("corral_sync_repo", map[string]any{"query": "alpha"})
	if !isErr {
		t.Fatalf("a path that escapes the root was not refused: %s", out)
	}
	if !strings.Contains(out, "escapes root") {
		t.Fatalf("refusal does not name the cause: %s", out)
	}
}

func TestSyncRepoReportsStateFailureWhenCompletionAuditAlsoFails(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	stubSeam(t, &gitPull, func(context.Context, string, git.PullOptions) error { return nil })
	stubSeam(t, &markSynced, func(string) error { return errors.New("sidecar write failed") })
	h, _ := mutationHarness(t, base, false)

	// The intent record must succeed so the handler reaches the sync; only
	// the completion record fails. Failing both would stop at the intent
	// check and never reach the branch under test.
	calls := 0
	real := openAuditFile
	stubSeam(t, &openAuditFile, func(name string, flag int, perm os.FileMode) (auditFile, error) {
		calls++
		if calls > 1 {
			return nil, errors.New("audit device unavailable")
		}
		return real(name, flag, perm)
	})

	out, isErr := h.callTool("corral_sync_repo", map[string]any{"query": "alpha"})
	if !isErr {
		t.Fatalf("a failed state update was reported as success: %s", out)
	}
	if !strings.Contains(out, "state update failed") || !strings.Contains(out, "audit completion failed") {
		t.Fatalf("the result hides one of the two failures: %s", out)
	}
}

func TestRepoTreeResourceStopsDescendingAtDepthTwo(t *testing.T) {
	base := t.TempDir()
	repo := makeFakeRepo(t, base, "Public", "go", "alpha", "", "")

	// A file at depth two is listed; a directory at depth two is not
	// descended into, so nothing below it appears. The listing is an
	// orientation pass, not a full tree.
	deep := filepath.Join(repo, "pkg", "inner")
	if err := os.MkdirAll(deep, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "pkg", "depth2.go"), []byte("package pkg"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "depth3.go"), []byte("package inner"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := newHarness(t, ServerOptions{Root: base})
	out, err := h.readResource("corral://repo/Public/alpha/tree")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "pkg/depth2.go") {
		t.Fatalf("a file at depth two was omitted:\n%s", out)
	}
	if strings.Contains(out, "depth3.go") {
		t.Fatalf("the listing descended past depth two:\n%s", out)
	}
}

func TestRepoFileResourceRejectsUndecodablePath(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	srv, err := NewServer(ServerOptions{Root: base, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}

	// The bad escape has to sit in the query string, not the path: url.Parse
	// validates path escapes and would reject the URI before the handler
	// sees it, but it stores RawQuery verbatim. extractFilePath takes
	// everything after "/file/", query included, so this is what an
	// undecodable path segment actually looks like by the time it reaches
	// the unescape call.
	//
	// Called directly rather than through the client session because the
	// URI template matcher does not route it.
	_, err = srv.handleRepoFileResource(context.Background(), &mcp.ReadResourceRequest{
		Params: &mcp.ReadResourceParams{URI: "corral://repo/Public/alpha/file/README.md?ref=%zz"},
	})
	if err == nil {
		t.Fatal("a file URI with an undecodable segment should be rejected")
	}
	if !strings.Contains(err.Error(), "decoding path segment") {
		t.Fatalf("error does not name the cause: %v", err)
	}
}

func TestServeStdioDefaultRunsOverStdin(t *testing.T) {
	// serveStdioDefault is the production binding behind the serveStdio
	// seam. Point stdin at an already-closed pipe so the transport reads
	// EOF immediately and Run returns, rather than blocking on a terminal.
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeEnd.Close(); err != nil {
		t.Fatal(err)
	}
	originalStdin := os.Stdin
	os.Stdin = readEnd
	t.Cleanup(func() {
		os.Stdin = originalStdin
		_ = readEnd.Close()
	})

	srv, err := NewServer(ServerOptions{Root: t.TempDir(), Version: "test"})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() { done <- serveStdioDefault(srv.mcp) }()

	select {
	case <-done:
		// Returning at all is the assertion: an EOF on stdin must end the
		// server loop rather than hang it. Whether that surfaces as nil or
		// as an EOF error is the SDK's business, not ours.
	case <-time.After(10 * time.Second):
		t.Fatal("serveStdioDefault did not return after stdin reached EOF")
	}
}
