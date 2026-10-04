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
	if err := os.MkdirAll(hooksDir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, hook := range []string{"post-merge", "post-checkout", "pre-rebase"} {
		path := filepath.Join(hooksDir, hook)
		//nolint:gosec // G306: test hook script must be executable
		if err := os.WriteFile(path, []byte(hookScript), 0o700); err != nil {
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
	if err := os.MkdirAll(dotGit, 0o750); err != nil {
		t.Fatal(err)
	}

	headPath := filepath.Join(dotGit, "HEAD")

	// 1. Normal branch
	if err := os.WriteFile(headPath, []byte("ref: refs/heads/feat/fast-refs\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	branch, err := CurrentBranch(context.Background(), dir)
	if err != nil || branch != "feat/fast-refs" {
		t.Errorf("CurrentBranch() = %q, %v; want 'feat/fast-refs', nil", branch, err)
	}

	// 2. Detached HEAD (SHA-1)
	sha1 := "e508898123456789abcdef0123456789abcdef01"
	if err := os.WriteFile(headPath, []byte(sha1+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	branch, err = CurrentBranch(context.Background(), dir)
	if err != nil || branch != "HEAD" {
		t.Errorf("CurrentBranch(sha1) = %q, %v; want 'HEAD', nil", branch, err)
	}

	// 3. Detached HEAD (SHA-256)
	sha256 := strings.Repeat("a", 64)
	if err := os.WriteFile(headPath, []byte(sha256+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	branch, err = CurrentBranch(context.Background(), dir)
	if err != nil || branch != "HEAD" {
		t.Errorf("CurrentBranch(sha256) = %q, %v; want 'HEAD', nil", branch, err)
	}

	// 4. Literal HEAD
	if err := os.WriteFile(headPath, []byte("HEAD\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	branch, err = CurrentBranch(context.Background(), dir)
	if err != nil || branch != "HEAD" {
		t.Errorf("CurrentBranch('HEAD') = %q, %v; want 'HEAD', nil", branch, err)
	}

	// 5. Corrupt / empty ref
	if err := os.WriteFile(headPath, []byte("ref: refs/heads/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b, ok := readDirectBranch(dir); ok || b != "" {
		t.Errorf("expected readDirectBranch to fail on empty ref, got %q, %v", b, ok)
	}

	// 6. Non-hex 40-char string
	nonHex40 := "z" + strings.Repeat("0", 39)
	if err := os.WriteFile(headPath, []byte(nonHex40+"\n"), 0o600); err != nil {
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
	if err := os.MkdirAll(filepath.Join(missingHeadDir, ".git"), 0o750); err != nil {
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

func TestHeadCommitAndDirectRefReading(t *testing.T) {
	dir := t.TempDir()
	dotGit := filepath.Join(dir, ".git")
	if err := os.MkdirAll(dotGit, 0o750); err != nil {
		t.Fatal(err)
	}
	headPath := filepath.Join(dotGit, "HEAD")

	// 1. Cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := HeadCommit(ctx, dir); err == nil {
		t.Error("HeadCommit should fail on cancelled context")
	}

	// 2. Detached HEAD (SHA-1)
	sha1 := "e508898123456789abcdef0123456789abcdef01"
	if err := os.WriteFile(headPath, []byte(sha1+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commit, err := HeadCommit(context.Background(), dir)
	if err != nil || commit != sha1 {
		t.Errorf("HeadCommit(sha1) = %q, %v; want %q, nil", commit, err, sha1)
	}

	// 3. Detached HEAD (SHA-256)
	sha256 := strings.Repeat("f", 64)
	if err := os.WriteFile(headPath, []byte(sha256+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commit, err = HeadCommit(context.Background(), dir)
	if err != nil || commit != sha256 {
		t.Errorf("HeadCommit(sha256) = %q, %v; want %q, nil", commit, err, sha256)
	}

	// 4. Symbolic HEAD pointing to loose ref
	looseDir := filepath.Join(dotGit, "refs", "heads")
	if err := os.MkdirAll(looseDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(headPath, []byte("ref: refs/heads/feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	looseSHA := "1234567890abcdef1234567890abcdef12345678"
	if err := os.WriteFile(filepath.Join(looseDir, "feature"), []byte(looseSHA+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commit, err = HeadCommit(context.Background(), dir)
	if err != nil || commit != looseSHA {
		t.Errorf("HeadCommit(loose) = %q, %v; want %q, nil", commit, err, looseSHA)
	}

	// 5. Symbolic HEAD pointing to packed ref
	if err := os.WriteFile(headPath, []byte("ref: refs/heads/packed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	packedSHA := "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	packedContent := "# pack-refs with: peeled sorted\n\n" +
		"^peeled\n" +
		"singleword\n" +
		packedSHA + " refs/heads/packed\n"
	if err := os.WriteFile(filepath.Join(dotGit, "packed-refs"), []byte(packedContent), 0o600); err != nil {
		t.Fatal(err)
	}
	commit, err = HeadCommit(context.Background(), dir)
	if err != nil || commit != packedSHA {
		t.Errorf("HeadCommit(packed) = %q, %v; want %q, nil", commit, err, packedSHA)
	}

	// 6. Linked worktree reading commondir for packed-refs
	wtDir := t.TempDir()
	wtGitDir := filepath.Join(dir, ".git", "worktrees", "wt1")
	if err := os.MkdirAll(wtGitDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtDir, ".git"), []byte("gitdir: "+wtGitDir+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtGitDir, "HEAD"), []byte("ref: refs/heads/packed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtGitDir, "commondir"), []byte("../..\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commit, err = HeadCommit(context.Background(), wtDir)
	if err != nil || commit != packedSHA {
		t.Errorf("HeadCommit(commondir) = %q, %v; want %q, nil", commit, err, packedSHA)
	}

	// 7. Unborn HEAD / IsEmpty detection
	unbornDir := t.TempDir()
	unbornDotGit := filepath.Join(unbornDir, ".git")
	if err := os.MkdirAll(unbornDotGit, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unbornDotGit, "HEAD"), []byte("ref: refs/heads/unborn\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if empty, ok := directIsEmpty(unbornDir); !ok || !empty {
		t.Errorf("directIsEmpty(unborn) = %v, %v; want true, true", empty, ok)
	}
	if !IsEmpty(context.Background(), unbornDir) {
		t.Error("IsEmpty(unborn) want true")
	}

	// Also test directIsEmpty on repo with commit
	if empty, ok := directIsEmpty(dir); !ok || empty {
		t.Errorf("directIsEmpty(dir with commit) = %v, %v; want false, true", empty, ok)
	}

	// directIsEmpty on detached SHA
	detachedDir := t.TempDir()
	detachedDotGit := filepath.Join(detachedDir, ".git")
	_ = os.MkdirAll(detachedDotGit, 0o750)
	_ = os.WriteFile(filepath.Join(detachedDotGit, "HEAD"), []byte(sha1+"\n"), 0o600)
	if empty, ok := directIsEmpty(detachedDir); !ok || empty {
		t.Errorf("directIsEmpty(detached) = %v, %v; want false, true", empty, ok)
	}

	// directIsEmpty on commondir with packed-refs
	if empty, ok := directIsEmpty(wtDir); !ok || empty {
		t.Errorf("directIsEmpty(wtDir) = %v, %v; want false, true", empty, ok)
	}

	// directIsEmpty errors on missing dir or missing HEAD
	if _, ok := directIsEmpty("/not/a/dir"); ok {
		t.Error("directIsEmpty should return ok=false for non-existent dir")
	}
	emptyDotGitDir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(emptyDotGitDir, ".git"), 0o750)
	if _, ok := directIsEmpty(emptyDotGitDir); ok {
		t.Error("directIsEmpty should return ok=false for missing HEAD")
	}

	// 8. Invalid formats and traversal attempts
	if err := os.WriteFile(headPath, []byte("ref: \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, ok := readDirectHeadCommit(dir); ok || c != "" {
		t.Errorf("expected readDirectHeadCommit to fail on empty ref: got %q, %v", c, ok)
	}
	if _, ok := directIsEmpty(dir); ok {
		t.Error("directIsEmpty should return ok=false for empty ref prefix")
	}

	if err := os.WriteFile(headPath, []byte("ref: ../../outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, ok := readDirectHeadCommit(dir); ok || c != "" {
		t.Errorf("expected readDirectHeadCommit to reject traversal: got %q, %v", c, ok)
	}

	if err := os.WriteFile(headPath, []byte("corrupt-content\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, ok := readDirectHeadCommit(dir); ok || c != "" {
		t.Errorf("expected readDirectHeadCommit to reject corrupt content: got %q, %v", c, ok)
	}
	if _, ok := directIsEmpty(dir); ok {
		t.Error("directIsEmpty should return ok=false for corrupt content")
	}

	if c, ok := readDirectHeadCommit("/definitely/not/a/repo"); ok || c != "" {
		t.Errorf("expected readDirectHeadCommit to fail on missing dir: got %q, %v", c, ok)
	}

	// 9. Fallback on real repo
	bareDir, workDir := setupTestRepo(t)
	defer cleanup(t, bareDir)
	defer cleanup(t, workDir)

	realCommit, err := HeadCommit(context.Background(), workDir)
	if err != nil || len(realCommit) != 40 {
		t.Fatalf("HeadCommit on real repo failed: %v, commit=%q", err, realCommit)
	}

	// Fallback when readFile fails
	oldReadFile := readFile
	readFile = func(string) ([]byte, error) { return nil, os.ErrNotExist }
	fallbackCommit, err := HeadCommit(context.Background(), workDir)
	readFile = oldReadFile
	if err != nil || fallbackCommit != realCommit {
		t.Errorf("HeadCommit fallback = %q, %v; want %q, nil", fallbackCommit, err, realCommit)
	}

	// Error path when rev-parse fails
	if _, err := HeadCommit(context.Background(), "/nonexistent/repo"); err == nil {
		t.Error("expected error for non-existent repo, got nil")
	}

	// 10. Direct testing of readPackedRef
	if _, ok := readPackedRef("/nonexistent/packed-refs", "refs/heads/foo"); ok {
		t.Error("readPackedRef should fail on missing file")
	}

	// 11. readFile error in readDirectHeadCommit and directIsEmpty
	missingHeadRepo := t.TempDir()
	_ = os.MkdirAll(filepath.Join(missingHeadRepo, ".git"), 0o750)
	if _, ok := readDirectHeadCommit(missingHeadRepo); ok {
		t.Error("readDirectHeadCommit should fail on missing HEAD")
	}

	// 12. directIsEmpty with commondir having packed-refs
	wtWithPackedDir := t.TempDir()
	wtGitPackedDir := filepath.Join(wtWithPackedDir, ".git-wt", "wt2")
	_ = os.MkdirAll(wtGitPackedDir, 0o750)
	_ = os.WriteFile(filepath.Join(wtWithPackedDir, ".git"), []byte("gitdir: "+wtGitPackedDir+"\n"), 0o600)
	_ = os.WriteFile(filepath.Join(wtGitPackedDir, "HEAD"), []byte("ref: refs/heads/somebranch\n"), 0o600)
	_ = os.WriteFile(filepath.Join(wtGitPackedDir, "commondir"), []byte(dotGit+"\n"), 0o600)
	// dotGit already has packed-refs from step 5!
	// so directIsEmpty should return (false, false) because common packed-refs exists
	if _, ok := directIsEmpty(wtWithPackedDir); ok {
		t.Error("directIsEmpty should return ok=false when commondir has packed-refs")
	}

	// 13. directIsEmpty when loose ref exists but is corrupt (not empty, not ok)
	corruptRepo := t.TempDir()
	corruptDotGit := filepath.Join(corruptRepo, ".git")
	_ = os.MkdirAll(filepath.Join(corruptDotGit, "refs", "heads"), 0o750)
	_ = os.WriteFile(filepath.Join(corruptDotGit, "HEAD"), []byte("ref: refs/heads/corrupt\n"), 0o600)
	_ = os.WriteFile(filepath.Join(corruptDotGit, "refs", "heads", "corrupt"), []byte("bad-sha\n"), 0o600)
	if _, ok := directIsEmpty(corruptRepo); ok {
		t.Error("directIsEmpty should return ok=false when loose ref exists but commit cannot be read")
	}

	// 14. readPackedRef targetRef not found
	if _, ok := readPackedRef(filepath.Join(dotGit, "packed-refs"), "refs/heads/missing-packed-ref"); ok {
		t.Error("readPackedRef should fail when targetRef is not found in packed-refs")
	}

	// 15. directIsEmpty on worktree with relative commondir and no packed-refs
	wtRelDir := t.TempDir()
	wtGitRelDir := filepath.Join(dir, ".git", "worktrees", "wt-rel")
	_ = os.MkdirAll(wtGitRelDir, 0o750)
	_ = os.WriteFile(filepath.Join(wtRelDir, ".git"), []byte("gitdir: "+wtGitRelDir+"\n"), 0o600)
	_ = os.WriteFile(filepath.Join(wtGitRelDir, "HEAD"), []byte("ref: refs/heads/unborn\n"), 0o600)
	commonWithoutPacked := t.TempDir()
	commonDotGit := filepath.Join(commonWithoutPacked, ".git")
	_ = os.MkdirAll(commonDotGit, 0o750)
	relPath, _ := filepath.Rel(wtGitRelDir, commonDotGit)
	_ = os.WriteFile(filepath.Join(wtGitRelDir, "commondir"), []byte(relPath+"\n"), 0o600)
	if empty, ok := directIsEmpty(wtRelDir); !ok || !empty {
		t.Errorf("directIsEmpty(wtRelDir) = %v, %v; want true, true", empty, ok)
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
	if err := os.WriteFile(dirtyFile, []byte("uncommitted"), 0o600); err != nil {
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

	// 6. ListWorktrees includes main and linked worktree
	wts, err := ListWorktrees(context.Background(), workDir)
	if err != nil {
		t.Fatalf("ListWorktrees failed: %v", err)
	}
	if len(wts) < 2 {
		t.Fatalf("expected at least 2 worktrees, got %d", len(wts))
	}

	if err := RemoveWorktree(context.Background(), workDir, wtPath2, false); err != nil {
		t.Fatalf("RemoveWorktree clean failed: %v", err)
	}
}

func TestParseWorktreeListPorcelain(t *testing.T) {
	raw := `worktree /path/to/main
HEAD abcdef1234567890
branch refs/heads/main

worktree /path/to/bare
bare

worktree /path/to/detached
HEAD 123456abcdef7890
detached

`
	wts := parseWorktreeListPorcelain(raw)
	if len(wts) != 3 {
		t.Fatalf("expected 3 worktrees, got %d", len(wts))
	}
	if wts[0].Path != "/path/to/main" || wts[0].Branch != "main" || wts[0].Commit != "abcdef1234567890" {
		t.Errorf("unexpected wt[0]: %+v", wts[0])
	}
	if wts[1].Path != "/path/to/bare" || !wts[1].Bare {
		t.Errorf("unexpected wt[1]: %+v", wts[1])
	}
	if wts[2].Path != "/path/to/detached" || wts[2].Branch != "detached" {
		t.Errorf("unexpected wt[2]: %+v", wts[2])
	}

	// Non-existent directory returns error
	if _, err := ListWorktrees(context.Background(), "/nonexistent/path"); err == nil {
		t.Error("expected error listing worktrees for non-existent path")
	}

	// Empty input
	if empty := parseWorktreeListPorcelain(""); len(empty) != 0 {
		t.Errorf("expected 0 for empty, got %d", len(empty))
	}

	// Input without trailing newline
	single := parseWorktreeListPorcelain("worktree /path/to/extra\nbranch refs/heads/extra")
	if len(single) != 1 || single[0].Path != "/path/to/extra" || single[0].Branch != "extra" {
		t.Errorf("unexpected single worktree: %+v", single)
	}
}
