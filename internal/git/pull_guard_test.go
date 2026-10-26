// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cloneWithUpstreamChange clones a fresh upstream and then pushes one more
// commit to it, so a pull of the clone has work to do.
func cloneWithUpstreamChange(t *testing.T) (target string) {
	t.Helper()
	upstream, workDir := setupTestRepo(t)
	t.Cleanup(func() { cleanup(t, upstream); cleanup(t, workDir) })
	target = filepath.Join(t.TempDir(), "clone")
	if err := Clone(context.Background(), upstream, target, CloneOptions{}); err != nil {
		t.Fatal(err)
	}
	runGit(t, target, "config", "user.name", "Test")
	runGit(t, target, "config", "user.email", "test@test.com")
	if err := os.WriteFile(filepath.Join(workDir, "upstream.txt"), []byte("u"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, "git", "-C", workDir, "add", "upstream.txt")
	run(t, "git", "-C", workDir, "commit", "-m", "upstream")
	run(t, "git", "-C", workDir, "push", "origin", "main")
	return target
}

func headOf(t *testing.T, dir string) string {
	t.Helper()
	out, err := runGitOutput(context.Background(), dir, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

// TestPullRefusesAnOperationInProgress reproduces A-05: with a revert
// staged but not committed, `git pull --rebase --autostash` stashed it,
// moved HEAD and left REVERT_HEAD pointing at the old base.
func TestPullRefusesAnOperationInProgress(t *testing.T) {
	target := cloneWithUpstreamChange(t)
	runGit(t, target, "revert", "--no-commit", "HEAD")
	before := headOf(t, target)

	err := Pull(context.Background(), target, PullOptions{})
	if !errors.Is(err, ErrPullRefused) || !strings.Contains(err.Error(), "REVERT_HEAD") {
		t.Fatalf("Pull = %v, want a refusal naming REVERT_HEAD", err)
	}
	if after := headOf(t, target); after != before {
		t.Errorf("HEAD moved from %s to %s under an in-progress revert", before, after)
	}
}

// TestPullRefusesLocalCommitsAhead pins the other half of A-05: commits
// that exist only in the clone were silently rebased (and re-committed
// unsigned, since Pull disables commit.gpgsign) by an unattended sync.
func TestPullRefusesLocalCommitsAhead(t *testing.T) {
	target := cloneWithUpstreamChange(t)
	if err := os.WriteFile(filepath.Join(target, "local.txt"), []byte("l"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, target, "add", "local.txt")
	runGit(t, target, "commit", "-m", "local")
	before := headOf(t, target)

	err := Pull(context.Background(), target, PullOptions{})
	if !errors.Is(err, ErrPullRefused) || !strings.Contains(err.Error(), "1 local commit") {
		t.Fatalf("Pull = %v, want a refusal naming the local commit", err)
	}
	if after := headOf(t, target); after != before {
		t.Errorf("local commit rewritten: %s became %s", before, after)
	}
}

func TestPullRefusesWithoutAnUpstream(t *testing.T) {
	target := cloneWithUpstreamChange(t)
	runGit(t, target, "branch", "--unset-upstream")
	err := Pull(context.Background(), target, PullOptions{})
	if !errors.Is(err, ErrPullRefused) || !strings.Contains(err.Error(), "upstream") {
		t.Fatalf("Pull = %v, want a refusal naming the missing upstream", err)
	}
}

func TestPullRefusesWhenTheGitDirCannotBeRead(t *testing.T) {
	err := Pull(context.Background(), t.TempDir(), PullOptions{})
	if !errors.Is(err, ErrPullRefused) {
		t.Fatalf("Pull outside a repository = %v, want a refusal", err)
	}
}

// TestPullGuardNamesEachMarker covers every marker, not only the one the
// revert reproduction creates.
func TestPullGuardNamesEachMarker(t *testing.T) {
	for _, m := range inProgressMarkers {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, m.name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if reason := inProgressReason(dir); !strings.Contains(reason, m.name) {
			t.Errorf("%s: reason %q does not name the marker", m.name, reason)
		}
	}
	if reason := inProgressReason(t.TempDir()); reason != "" {
		t.Errorf("clean git dir reported %q", reason)
	}
}

// TestPullReportsAGitFailureAfterTheGuard keeps the plain failure path
// distinct from a refusal: a clean clone whose remote has gone away
// passes the guard, and the pull itself fails.
func TestPullReportsAGitFailureAfterTheGuard(t *testing.T) {
	target := cloneWithUpstreamChange(t)
	runGit(t, target, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone.git"))
	err := Pull(context.Background(), target, PullOptions{})
	if err == nil || errors.Is(err, ErrPullRefused) || !strings.Contains(err.Error(), "git pull failed") {
		t.Fatalf("Pull = %v, want a git pull failure", err)
	}
}
