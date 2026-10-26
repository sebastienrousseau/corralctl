// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package git

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestHasBranch(t *testing.T) {
	ctx := context.Background()
	repo := mirrorRepo(t)
	mirrorGit(t, repo, "tag", "v1")

	if ok, err := HasBranch(ctx, repo, "main"); err != nil || !ok {
		t.Fatalf("main = %t, %v", ok, err)
	}
	// A tag of that name is not a branch.
	if ok, err := HasBranch(ctx, repo, "v1"); err != nil || ok {
		t.Fatalf("tag v1 = %t, %v", ok, err)
	}
	if ok, err := HasBranch(ctx, repo, "--all"); err != nil || ok {
		t.Fatalf("option-like name = %t, %v", ok, err)
	}
	if _, err := HasBranch(ctx, repo, " "); err == nil {
		t.Fatal("expected an empty name to be refused")
	}
	if _, err := HasBranch(ctx, filepath.Join(repo, "missing"), "main"); err == nil {
		t.Fatal("expected a missing directory to be an error, not a missing branch")
	}
}

func TestDefaultBranch(t *testing.T) {
	ctx := context.Background()
	upstream := mirrorRepo(t)
	mirrorGit(t, upstream, "branch", "trunk")
	mirrorGit(t, upstream, "symbolic-ref", "HEAD", "refs/heads/trunk")

	// A clone records the upstream default as origin/HEAD and checks it
	// out; moving HEAD elsewhere shows origin/HEAD wins.
	clone := filepath.Join(t.TempDir(), "clone")
	mirrorGit(t, upstream, "clone", "-q", upstream, clone)
	mirrorGit(t, clone, "switch", "-q", "main")
	if got, err := DefaultBranch(ctx, clone); err != nil || got != "trunk" {
		t.Fatalf("clone default = %q, %v", got, err)
	}

	// origin/HEAD naming a branch with no local copy falls back to HEAD.
	mirrorGit(t, clone, "branch", "-q", "-D", "trunk")
	if got, err := DefaultBranch(ctx, clone); err != nil || got != "main" {
		t.Fatalf("fallback default = %q, %v", got, err)
	}

	// No origin and a detached HEAD leave nothing to name.
	mirrorGit(t, upstream, "switch", "-q", "--detach", "main")
	if _, err := DefaultBranch(ctx, upstream); err == nil {
		t.Fatal("expected a detached repository without origin to have no default")
	}
}

func TestDefaultBranchReportsAFailedLookup(t *testing.T) {
	oldRun := runGitOutput
	t.Cleanup(func() { runGitOutput = oldRun })
	exitFatal := exec.Command("sh", "-c", "exit 128").Run()
	runGitOutput = func(_ context.Context, _ string, args ...string) (string, error) {
		if args[0] == "symbolic-ref" {
			return "origin/main\n", nil
		}
		return "", exitFatal
	}
	var exitErr *exec.ExitError
	if _, err := DefaultBranch(context.Background(), "/repo"); !errors.As(err, &exitErr) {
		t.Fatalf("expected the show-ref failure, got %v", err)
	}
}

// TestOriginDefaultBranch: origin/HEAD's branch, and an error rather than
// HEAD's branch when origin/HEAD is not set.
func TestOriginDefaultBranch(t *testing.T) {
	ctx := context.Background()
	upstream := mirrorRepo(t)
	mirrorGit(t, upstream, "branch", "trunk")
	mirrorGit(t, upstream, "symbolic-ref", "HEAD", "refs/heads/trunk")
	clone := filepath.Join(t.TempDir(), "clone")
	mirrorGit(t, upstream, "clone", "-q", upstream, clone)
	mirrorGit(t, clone, "switch", "-q", "main")
	if got, err := OriginDefaultBranch(ctx, clone); err != nil || got != "trunk" {
		t.Fatalf("origin default = %q, %v", got, err)
	}
	if got, err := OriginDefaultBranch(ctx, upstream); err == nil {
		t.Fatalf("a repository without origin/HEAD named %q", got)
	}
}
