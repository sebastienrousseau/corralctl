// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWithMetadataTimeoutNilContext covers the defensive nil branch: these
// helpers are exported, so an embedder can reach them with a nil context,
// and context.WithTimeout panics on nil.
func TestWithMetadataTimeoutNilContext(t *testing.T) {
	// SA1012 forbids a literal nil Context, which is the entire point of
	// this test: withMetadataTimeout is reached from exported functions an
	// embedder can call with nil, and context.WithTimeout panics on one.
	//
	// The directive is staticcheck's own `//lint:ignore`, not golangci-lint's
	// `//nolint`. CI runs staticcheck standalone, which does not honour
	// //nolint — so a //nolint here passes golangci-lint locally and fails
	// CI, which is exactly what happened.
	//lint:ignore SA1012 passing nil is the behaviour under test
	ctx, cancel := withMetadataTimeout(nil)
	defer cancel()
	if ctx == nil {
		t.Fatal("withMetadataTimeout(nil) returned a nil context")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("withMetadataTimeout did not set a deadline")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > metadataTimeout {
		t.Fatalf("deadline %v is not within (0, %v]", remaining, metadataTimeout)
	}
}

// TestWithMetadataTimeoutPreservesCancellation asserts the derived context
// still unwinds when the caller's run is cancelled, which is the reason
// these calls take a context at all.
func TestWithMetadataTimeoutPreservesCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	ctx, cancel := withMetadataTimeout(parent)
	defer cancel()

	cancelParent()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling the parent did not cancel the metadata context")
	}
}

// TestMetadataCallsRespectCancellation covers the three helpers that used to
// run with no context and could block forever on an unresponsive filesystem.
func TestMetadataCallsRespectCancellation(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	if _, err := CurrentBranch(ctx, dir); err == nil {
		t.Error("CurrentBranch ignored a cancelled context")
	}
	if _, err := RemoteOrigin(ctx, dir); err == nil {
		t.Error("RemoteOrigin ignored a cancelled context")
	}
	if !IsEmpty(ctx, dir) {
		t.Error("IsEmpty should report true when the command cannot run")
	}
}

// TestGitHooksAreDisarmedDuringOperations proves that repository hooks
// are disarmed and cannot execute during Clone or Pull.
func TestGitHooksAreDisarmedDuringOperations(t *testing.T) {
	bareDir, workDir := setupTestRepo(t)
	defer cleanup(t, bareDir)
	defer cleanup(t, workDir)

	targetDir := t.TempDir()
	if err := Clone(context.Background(), bareDir, targetDir, CloneOptions{}); err != nil {
		t.Fatalf("Clone failed: %v", err)
	}

	marker := filepath.Join(targetDir, "hook_ran.marker")
	hookScript := "#!/bin/sh\ntouch \"" + marker + "\"\n"
	hooksDir := filepath.Join(targetDir, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, hook := range []string{"post-merge", "post-checkout", "pre-rebase"} {
		path := filepath.Join(hooksDir, hook)
		if err := os.WriteFile(path, []byte(hookScript), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	newFile := filepath.Join(workDir, "new.txt")
	if err := os.WriteFile(newFile, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, "git", "-C", workDir, "add", "new.txt")
	run(t, "git", "-C", workDir, "commit", "-m", "update")
	run(t, "git", "-C", workDir, "push", "origin", "main")

	if err := Pull(context.Background(), targetDir, PullOptions{}); err != nil {
		t.Fatalf("Pull failed: %v", err)
	}

	if _, err := os.Stat(marker); err == nil {
		t.Errorf("hook executed during Pull despite core.hooksPath=/dev/null hardening")
	}
}

func TestDirectBranchReading(t *testing.T) {
	dir := t.TempDir()
	dotGit := filepath.Join(dir, ".git")
	if err := os.MkdirAll(dotGit, 0o755); err != nil {
		t.Fatal(err)
	}

	headPath := filepath.Join(dotGit, "HEAD")

	// 1. Normal branch
	if err := os.WriteFile(headPath, []byte("ref: refs/heads/feat/fast-refs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	branch, err := CurrentBranch(context.Background(), dir)
	if err != nil || branch != "feat/fast-refs" {
		t.Errorf("CurrentBranch() = %q, %v; want 'feat/fast-refs', nil", branch, err)
	}

	// 2. Detached HEAD (SHA-1)
	sha1 := "e508898123456789abcdef0123456789abcdef01"
	if err := os.WriteFile(headPath, []byte(sha1+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	branch, err = CurrentBranch(context.Background(), dir)
	if err != nil || branch != "HEAD" {
		t.Errorf("CurrentBranch(sha1) = %q, %v; want 'HEAD', nil", branch, err)
	}

	// 3. Detached HEAD (SHA-256)
	sha256 := strings.Repeat("a", 64)
	if err := os.WriteFile(headPath, []byte(sha256+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	branch, err = CurrentBranch(nil, dir)
	if err != nil || branch != "HEAD" {
		t.Errorf("CurrentBranch(sha256) = %q, %v; want 'HEAD', nil", branch, err)
	}

	// 4. Literal HEAD
	if err := os.WriteFile(headPath, []byte("HEAD\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	branch, err = CurrentBranch(context.Background(), dir)
	if err != nil || branch != "HEAD" {
		t.Errorf("CurrentBranch('HEAD') = %q, %v; want 'HEAD', nil", branch, err)
	}

	// 5. Corrupt / empty ref
	if err := os.WriteFile(headPath, []byte("ref: refs/heads/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, ok := readDirectBranch(dir); ok || b != "" {
		t.Errorf("expected readDirectBranch to fail on empty ref, got %q, %v", b, ok)
	}

	// 6. Non-hex 40-char string
	nonHex40 := "z" + strings.Repeat("0", 39)
	if err := os.WriteFile(headPath, []byte(nonHex40+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b, ok := readDirectBranch(dir); ok || b != "" {
		t.Errorf("expected readDirectBranch to fail on non-hex string, got %q, %v", b, ok)
	}

	// 7. Non-existent gitdir
	if b, ok := readDirectBranch("/definitely/not/a/git/dir"); ok || b != "" {
		t.Errorf("expected readDirectBranch to fail on missing dir, got %q, %v", b, ok)
	}

	// 8. Missing HEAD inside .git dir
	missingHeadDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(missingHeadDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if b, ok := readDirectBranch(missingHeadDir); ok || b != "" {
		t.Errorf("expected readDirectBranch to fail on missing HEAD, got %q, %v", b, ok)
	}

	// 9. Fallback to rev-parse when direct read fails on real repo
	bareDir, workDir := setupTestRepo(t)
	defer cleanup(t, bareDir)
	defer cleanup(t, workDir)

	oldReadFile := readFile
	readFile = func(string) ([]byte, error) { return nil, os.ErrNotExist }
	branch, err = CurrentBranch(context.Background(), workDir)
	readFile = oldReadFile
	if err != nil || branch != "main" {
		t.Errorf("CurrentBranch fallback = %q, %v; want 'main', nil", branch, err)
	}
}

func TestWorktreeLifecycle(t *testing.T) {
	bareDir, workDir := setupTestRepo(t)
	defer cleanup(t, bareDir)
	defer cleanup(t, workDir)

	wtPath := filepath.Join(t.TempDir(), "wt-test")

	// 1. Create worktree on a new branch
	if err := CreateWorktree(context.Background(), workDir, wtPath, "agent-branch"); err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}
	branch, err := CurrentBranch(context.Background(), wtPath)
	if err != nil || branch != "agent-branch" {
		t.Fatalf("worktree branch = %q, %v; want 'agent-branch', nil", branch, err)
	}

	// 2. Add uncommitted change and assert non-forced removal is refused
	dirtyFile := filepath.Join(wtPath, "dirty.txt")
	if err := os.WriteFile(dirtyFile, []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveWorktree(context.Background(), workDir, wtPath, false); err == nil {
		t.Fatal("expected RemoveWorktree without force to refuse dirty worktree")
	}

	// 3. Forced removal succeeds
	if err := RemoveWorktree(context.Background(), workDir, wtPath, true); err != nil {
		t.Fatalf("RemoveWorktree with force failed: %v", err)
	}

	// 4. Prune worktrees
	if err := PruneWorktrees(context.Background(), workDir); err != nil {
		t.Fatalf("PruneWorktrees failed: %v", err)
	}

	// 5. Create worktree without branch
	wtPath2 := filepath.Join(t.TempDir(), "wt-detached")
	if err := CreateWorktree(context.Background(), workDir, wtPath2, ""); err != nil {
		t.Fatalf("CreateWorktree with empty branch failed: %v", err)
	}
	if err := RemoveWorktree(context.Background(), workDir, wtPath2, false); err != nil {
		t.Fatalf("RemoveWorktree clean failed: %v", err)
	}
}


