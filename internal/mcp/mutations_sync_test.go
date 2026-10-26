// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/sebastienrousseau/corralctl/internal/github"
)

// makeRealClone creates a git repository at base/Public/go/<name> with one
// commit on main, origin at originURL (none when it is empty), and
// origin/HEAD naming main, as a clone would have it. branch, when set, is checked out afterwards.
func makeRealClone(t *testing.T, base, name, originURL, branch string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := filepath.Join(base, "Public", "go", name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...) // #nosec G204 -- literal test arguments
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "--quiet", "--initial-branch=main")
	run("commit", "--allow-empty", "--quiet", "-m", "init")
	if originURL != "" {
		run("remote", "add", "origin", originURL)
	}
	run("update-ref", "refs/remotes/origin/main", "HEAD")
	run("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	if branch != "" {
		run("switch", "--quiet", "-c", branch)
	}
	return dir
}

// TestSyncRepoRefusesAFeatureBranch is the regression test for S-12:
// corral_sync_repo ran `git pull --rebase --autostash` on whatever branch
// was checked out, so an agent could rebase a user's feature branch. The
// CLI never pulls a clone that is off its default branch.
func TestSyncRepoRefusesAFeatureBranch(t *testing.T) {
	base := t.TempDir()
	makeRealClone(t, base, "alpha", "https://github.com/o/alpha.git", "feature")
	pulled := 0
	stubSeam(t, &gitPull, func(context.Context, string, git.PullOptions) error { pulled++; return nil })
	stubSeam(t, &markSynced, func(string) error { return nil })
	h, audit := mutationHarness(t, base, false)

	text, isErr := h.callTool("corral_sync_repo", map[string]any{"query": "alpha"})
	if !isErr || !strings.Contains(text, "on branch feature") {
		t.Fatalf("a clone on a feature branch must be refused, got isErr=%t: %s", isErr, text)
	}
	if pulled != 0 {
		t.Fatalf("git pull ran %d times on a refused clone", pulled)
	}
	recs := auditRecords(t, audit)
	if len(recs) != 1 || recs[0].Result != "refused" {
		t.Fatalf("a refusal must be audited: %+v", recs)
	}
}

// safeSyncGuards makes corral_sync_repo's guards pass, for tests about
// what happens after them on a fake repository.
func safeSyncGuards(t *testing.T) {
	t.Helper()
	stubSeam(t, &syncOriginMismatch, func(github.Repo, string) (string, bool) { return "", false })
	stubSeam(t, &originDefaultBranch, func(context.Context, string) (string, error) { return "main", nil })
	stubSeam(t, &syncBranchRefusal, func(context.Context, string, string) (string, bool) { return "", false })
}

// TestSyncRepoPullsTheDefaultBranchUnderTheLock: a clone on the branch
// origin/HEAD names passes every guard, and the pull runs holding the
// per-repository lock deletion takes, so the two cannot interleave.
func TestSyncRepoPullsTheDefaultBranchUnderTheLock(t *testing.T) {
	base := t.TempDir()
	makeRealClone(t, base, "alpha", "https://github.com/o/alpha.git", "")
	stubSeam(t, &markSynced, func(string) error { return nil })
	h, _ := mutationHarness(t, base, false)
	pulled, locked := 0, false
	stubSeam(t, &gitPull, func(_ context.Context, path string, _ git.PullOptions) error {
		pulled++
		lock := h.server.repoLocks.lock(path)
		if lock.TryLock() {
			lock.Unlock()
		} else {
			locked = true
		}
		return nil
	})
	if text, isErr := h.callTool("corral_sync_repo", map[string]any{"query": "alpha"}); isErr {
		t.Fatalf("a clone on its default branch was refused: %s", text)
	}
	if pulled != 1 || !locked {
		t.Fatalf("pulled=%d locked=%t, want one pull under the lock", pulled, locked)
	}
}

// TestSyncRepoGuardsRefuse covers every refusal in the cascade; none of
// them may reach the pull.
func TestSyncRepoGuardsRefuse(t *testing.T) {
	cases := []struct {
		name   string
		origin string
		stub   func(t *testing.T)
		want   string
	}{
		{"no origin", "", nil, "origin is not set"},
		{"an origin that changed after the index was read", "https://github.com/o/alpha.git", func(t *testing.T) {
			stubSeam(t, &syncOriginMismatch, func(github.Repo, string) (string, bool) {
				return "origin collision: target has github.com/x/y, expected github.com/o/alpha", true
			})
		}, "origin collision"},
		{"no origin/HEAD", "https://github.com/o/alpha.git", func(t *testing.T) {
			stubSeam(t, &originDefaultBranch, func(context.Context, string) (string, error) {
				return "", errors.New("origin/HEAD is not set")
			})
		}, "cannot tell the default branch"},
		{"an empty clone", "https://github.com/o/alpha.git", func(t *testing.T) {
			stubSeam(t, &syncBranchRefusal, func(context.Context, string, string) (string, bool) {
				return "empty repository (no commits yet)", true
			})
		}, "empty repository"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := t.TempDir()
			makeRealClone(t, base, "alpha", c.origin, "")
			stubSeam(t, &gitPull, func(context.Context, string, git.PullOptions) error {
				t.Error("a refused clone was pulled")
				return nil
			})
			if c.stub != nil {
				c.stub(t)
			}
			h, _ := mutationHarness(t, base, false)
			text, isErr := h.callTool("corral_sync_repo", map[string]any{"query": "alpha"})
			if !isErr || !strings.Contains(text, c.want) {
				t.Fatalf("isErr=%t text=%s, want a refusal naming %q", isErr, text, c.want)
			}
		})
	}
}

// TestSyncRepoRefusalWithALostAuditStillRefuses: a refusal whose audit
// record cannot be written is still a refusal, and says both.
func TestSyncRepoRefusalWithALostAuditStillRefuses(t *testing.T) {
	base := t.TempDir()
	makeRealClone(t, base, "alpha", "https://github.com/o/alpha.git", "feature")
	stubSeam(t, &gitPull, func(context.Context, string, git.PullOptions) error {
		t.Error("a refused clone was pulled")
		return nil
	})
	h, _ := mutationHarness(t, base, false)
	failAuditAfter(t, 1)
	text, isErr := h.callTool("corral_sync_repo", map[string]any{"query": "alpha"})
	if !isErr || !strings.Contains(text, "on branch feature") || !strings.Contains(text, "audit failed") {
		t.Fatalf("isErr=%t text=%s", isErr, text)
	}
}
