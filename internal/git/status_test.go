// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestInspectRepoStatusClean(t *testing.T) {
	ctx := context.Background()
	bareDir, workDir := setupTestRepo(t)
	defer cleanup(t, bareDir)
	defer cleanup(t, workDir)

	st, err := InspectRepoStatus(ctx, workDir)
	if err != nil {
		t.Fatalf("InspectRepoStatus failed: %v", err)
	}
	if st.IsDirty {
		t.Errorf("expected clean working tree, got dirty")
	}
	if st.HasUnpushed {
		t.Errorf("expected no unpushed commits, got %d", st.UnpushedCount)
	}
	if st.HasStash {
		t.Errorf("expected no stash, got stash")
	}
	if st.HasWorktree {
		t.Errorf("expected no extra worktree, got worktree")
	}
}

func TestInspectRepoStatusDirtyAndStash(t *testing.T) {
	ctx := context.Background()
	bareDir, workDir := setupTestRepo(t)
	defer cleanup(t, bareDir)
	defer cleanup(t, workDir)

	// Dirty file
	dirtyFile := filepath.Join(workDir, "dirty.txt")
	if err := os.WriteFile(dirtyFile, []byte("change"), 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := InspectRepoStatus(ctx, workDir)
	if err != nil {
		t.Fatalf("InspectRepoStatus failed: %v", err)
	}
	if !st.IsDirty {
		t.Errorf("expected dirty working tree, got clean")
	}

	// Stash it
	run(t, "git", "-C", workDir, "add", "dirty.txt")
	run(t, "git", "-C", workDir, "stash")

	st, err = InspectRepoStatus(ctx, workDir)
	if err != nil {
		t.Fatalf("InspectRepoStatus failed: %v", err)
	}
	if !st.HasStash {
		t.Errorf("expected stash, got none")
	}
}

func TestInspectRepoStatusUnpushedAndWorktree(t *testing.T) {
	ctx := context.Background()
	bareDir, workDir := setupTestRepo(t)
	defer cleanup(t, bareDir)
	defer cleanup(t, workDir)

	// Local commit without pushing
	f := filepath.Join(workDir, "commit.txt")
	if err := os.WriteFile(f, []byte("commit"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, "git", "-C", workDir, "add", "commit.txt")
	run(t, "git", "-C", workDir, "commit", "-m", "unpushed")

	st, err := InspectRepoStatus(ctx, workDir)
	if err != nil {
		t.Fatalf("InspectRepoStatus failed: %v", err)
	}
	if !st.HasUnpushed || st.UnpushedCount != 1 {
		t.Errorf("expected 1 unpushed commit, got %d (has: %v)", st.UnpushedCount, st.HasUnpushed)
	}

	// Add worktree
	wtDir, err := os.MkdirTemp("", "git_status_wt")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(t, wtDir)

	if err := CreateWorktree(ctx, workDir, wtDir, "wt-branch"); err != nil {
		t.Fatalf("CreateWorktree failed: %v", err)
	}

	st, err = InspectRepoStatus(ctx, workDir)
	if err != nil {
		t.Fatalf("InspectRepoStatus failed: %v", err)
	}
	if !st.HasWorktree {
		t.Errorf("expected active worktree, got false")
	}
}

func TestInspectRepoStatusFallbackAndDormant(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "git_status_standalone")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(t, dir)

	run(t, "git", "-C", dir, "init")
	run(t, "git", "-C", dir, "config", "user.name", "Test")
	run(t, "git", "-C", dir, "config", "user.email", "test@test.com")

	f := filepath.Join(dir, "init.txt")
	if err := os.WriteFile(f, []byte("init"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, "git", "-C", dir, "add", "init.txt")

	cmd := exec.Command("git", "-C", dir, "commit", "-m", "init", "--date", "2020-01-01T00:00:00Z") // #nosec G204 -- test repository directory
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_DATE=2020-01-01T00:00:00Z")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit failed: %v (%s)", err, string(out))
	}

	st, err := InspectRepoStatus(ctx, dir)
	if err != nil {
		t.Fatalf("InspectRepoStatus failed: %v", err)
	}
	if !st.HasUnpushed || st.UnpushedCount != 1 {
		t.Errorf("expected 1 local unpushed commit in standalone repo, got %d", st.UnpushedCount)
	}
	if !st.IsDormant {
		t.Errorf("expected dormant repo for commit in 2020, got false")
	}
}

func TestInspectRepoStatusDivergedAndConflict(t *testing.T) {
	ctx := context.Background()
	bareDir, workDir := setupTestRepo(t)
	defer cleanup(t, bareDir)
	defer cleanup(t, workDir)

	// Second clone that pushes a commit to origin
	workDir2, err := os.MkdirTemp("", "git_status_second_clone")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(t, workDir2)
	run(t, "git", "clone", bareDir, workDir2)
	run(t, "git", "-C", workDir2, "config", "user.name", "Test2")
	run(t, "git", "-C", workDir2, "config", "user.email", "test2@test.com")

	f2 := filepath.Join(workDir2, "remote_commit.txt")
	if err := os.WriteFile(f2, []byte("remote"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, "git", "-C", workDir2, "add", "remote_commit.txt")
	run(t, "git", "-C", workDir2, "commit", "-m", "remote commit")
	run(t, "git", "-C", workDir2, "push", "origin", "main")

	// Local commit on workDir1 without pulling
	f1 := filepath.Join(workDir, "local_commit.txt")
	if err := os.WriteFile(f1, []byte("local"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, "git", "-C", workDir, "add", "local_commit.txt")
	run(t, "git", "-C", workDir, "commit", "-m", "local commit")

	// Fetch origin in workDir1 so tracking branch moves, making it diverged
	run(t, "git", "-C", workDir, "fetch", "origin")

	st, err := InspectRepoStatus(ctx, workDir)
	if err != nil {
		t.Fatalf("InspectRepoStatus failed: %v", err)
	}
	if !st.IsDiverged {
		t.Errorf("expected diverged status, got false")
	}

	// Trigger conflict on a feature branch
	run(t, "git", "-C", workDir, "checkout", "-b", "feature")
	fConflict := filepath.Join(workDir, "conflict_file.txt")
	if err := os.WriteFile(fConflict, []byte("feature branch content"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, "git", "-C", workDir, "add", "conflict_file.txt")
	run(t, "git", "-C", workDir, "commit", "-m", "feature change")

	run(t, "git", "-C", workDir, "checkout", "main")
	if err := os.WriteFile(fConflict, []byte("main branch content"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, "git", "-C", workDir, "add", "conflict_file.txt")
	run(t, "git", "-C", workDir, "commit", "-m", "main change")

	cmdMerge := exec.Command("git", "-C", workDir, "merge", "feature") // #nosec G204 -- test repository directory
	cmdMerge.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	_ = cmdMerge.Run()

	st, err = InspectRepoStatus(ctx, workDir)
	if err != nil {
		t.Fatalf("InspectRepoStatus failed: %v", err)
	}
	if !st.HasConflict {
		t.Errorf("expected conflict status, got false")
	}
}

func TestInspectRepoStatusNonRepo(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	if _, err := InspectRepoStatus(ctx, dir); err == nil {
		t.Errorf("expected error inspecting non-git directory, got nil")
	}
}

