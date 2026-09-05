// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/sebastienrousseau/corralctl/internal/github"
)

// TestProcessRepoSkipsEmptyRemote guards the fix for the six sync
// failures the maintainer surfaced against sebastienrousseau/
// audiotextpro, llmkey, rust-lib-template, payment-initiation,
// SmartPay, paydrive — all of which had been initialised on GitHub
// but never pushed to, so the local clone was in an unborn-HEAD
// state and `git pull` bombed with "no such ref was fetched".
//
// After the fix, processRepo must return SKIP with a specific message
// BEFORE calling gitPull, so the user sees "empty repository (no
// commits yet)" instead of a raw git error.
func TestProcessRepoSkipsEmptyRemote(t *testing.T) {
	oldGitIsEmpty := gitIsEmpty
	oldGitPull := gitPull
	oldGitCurrentBranch := gitCurrentBranch
	defer func() {
		gitIsEmpty = oldGitIsEmpty
		gitPull = oldGitPull
		gitCurrentBranch = oldGitCurrentBranch
	}()

	gitIsEmpty = func(_ context.Context, targetDir string) bool { return true }
	pullCalls := 0
	gitPull = func(ctx context.Context, targetDir string, opts git.PullOptions) error {
		pullCalls++
		return nil
	}
	gitCurrentBranch = func(_ context.Context, targetDir string) (string, error) { return "main", nil }

	baseDir, err := os.MkdirTemp("", "engine_empty_test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(baseDir) }()

	repo := github.Repo{
		Name:          "empty_upstream",
		Language:      "Go",
		Visibility:    "Public",
		DefaultBranch: "main",
		CloneURL:      "https://github.com/owner/repo.git",
	}
	targetDir := filepath.Join(baseDir, "Public", "Go", "empty_upstream")
	makeCloneAt(t, targetDir, "https://github.com/owner/repo.git")
	job := Job{Repo: repo, Target: targetDir}

	msg := processRepo(context.Background(), "owner", "https", true, false, git.CloneOptions{}, SyncOptions{}, job)
	if msg.Action != "SKIP" {
		t.Fatalf("expected SKIP for empty repo, got %s (%s)", msg.Action, msg.Message)
	}
	if !strings.Contains(msg.Message, "empty repository") {
		t.Errorf("expected message to mention 'empty repository', got %q", msg.Message)
	}
	if pullCalls != 0 {
		t.Errorf("gitPull must NOT be called for an empty repo, was called %d times", pullCalls)
	}
}

// TestProcessRepoStillSyncsNonEmpty guards against a regression that
// would over-eagerly SKIP everyone's normal syncs. gitIsEmpty must
// return false for a healthy repo and the pull must proceed.
func TestProcessRepoStillSyncsNonEmpty(t *testing.T) {
	oldGitIsEmpty := gitIsEmpty
	oldGitPull := gitPull
	oldGitCurrentBranch := gitCurrentBranch
	defer func() {
		gitIsEmpty = oldGitIsEmpty
		gitPull = oldGitPull
		gitCurrentBranch = oldGitCurrentBranch
	}()

	gitIsEmpty = func(_ context.Context, targetDir string) bool { return false }
	pullCalls := 0
	gitPull = func(ctx context.Context, targetDir string, opts git.PullOptions) error {
		pullCalls++
		return nil
	}
	gitCurrentBranch = func(_ context.Context, targetDir string) (string, error) { return "main", nil }

	baseDir, err := os.MkdirTemp("", "engine_nonempty_test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(baseDir) }()

	repo := github.Repo{
		Name:          "healthy",
		Language:      "Go",
		Visibility:    "Public",
		DefaultBranch: "main",
		CloneURL:      "https://github.com/owner/repo.git",
	}
	targetDir := filepath.Join(baseDir, "Public", "Go", "healthy")
	makeCloneAt(t, targetDir, "https://github.com/owner/repo.git")
	job := Job{Repo: repo, Target: targetDir}

	msg := processRepo(context.Background(), "owner", "https", true, false, git.CloneOptions{}, SyncOptions{}, job)
	if msg.Action != "SYNC" {
		t.Fatalf("expected SYNC for non-empty repo, got %s (%s)", msg.Action, msg.Message)
	}
	if pullCalls != 1 {
		t.Errorf("gitPull must be called exactly once for a non-empty repo, was called %d times", pullCalls)
	}
}
