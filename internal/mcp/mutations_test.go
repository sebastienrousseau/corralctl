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
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/git"
)

// Mutation-tool tests.
//
// These drive the write tools through a real client session, with the git and
// filesystem seams stubbed so nothing is cloned, pulled or deleted for real.
// The delete guard cascade is the highest-risk code in this package: every
// refusal below corresponds to work that exists nowhere but the user's disk.

func stubSeam[T any](t *testing.T, target *T, replacement T) {
	t.Helper()
	old := *target
	*target = replacement
	t.Cleanup(func() { *target = old })
}

// safeGuards stubs the three delete guards to "nothing to protect", which is
// the baseline most tests want; individual tests override the one they exercise.
func safeGuards(t *testing.T) {
	t.Helper()
	clean := func(context.Context, string) (bool, string) { return false, "" }
	stubSeam(t, &hasDirtyWorkingTree, clean)
	stubSeam(t, &hasUnpushedCommits, clean)
	stubSeam(t, &hasIgnoredContent, clean)
}

func mutationHarness(t *testing.T, base string, destructive bool) (*harness, string) {
	t.Helper()
	auditPath := filepath.Join(t.TempDir(), "audit.log")
	return newHarness(t, ServerOptions{
		Root:                       base,
		EnableMutations:            true,
		EnableDestructiveMutations: destructive,
		AuditLogPath:               auditPath,
	}), auditPath
}

func auditRecords(t *testing.T, path string) []AuditRecord {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- test-controlled path
	if err != nil {
		return nil
	}
	var out []AuditRecord
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var rec AuditRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("audit log line is not JSON: %v (%s)", err, line)
		}
		out = append(out, rec)
	}
	return out
}

func TestSyncRepoPullsAndAudits(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "https://github.com/o/alpha.git", "")
	pulled := 0
	stubSeam(t, &gitPull, func(context.Context, string, git.PullOptions) error { pulled++; return nil })
	stubSeam(t, &markSynced, func(string) error { return nil })
	h, audit := mutationHarness(t, base, false)

	var got map[string]any
	h.callToolJSON("corral_sync_repo", map[string]any{"query": "alpha"}, &got)
	if got["result"] != "synced" {
		t.Errorf("result = %v, want synced", got["result"])
	}
	if pulled != 1 {
		t.Errorf("git pull called %d times, want 1", pulled)
	}

	// Intent is recorded before the mutation and completion after, so a crash
	// mid-tool still leaves evidence of what was attempted.
	recs := auditRecords(t, audit)
	if len(recs) != 2 {
		t.Fatalf("expected intent + completion records, got %d: %+v", len(recs), recs)
	}
	if recs[0].Phase != "intent" || recs[1].Phase != "completion" {
		t.Errorf("phases = %q,%q want intent,completion", recs[0].Phase, recs[1].Phase)
	}
	if recs[0].OperationID == "" || recs[0].OperationID != recs[1].OperationID {
		t.Errorf("records are not correlated: %+v", recs)
	}
}

func TestSyncRepoSurfacesPullFailure(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	stubSeam(t, &gitPull, func(context.Context, string, git.PullOptions) error {
		return errors.New("network unreachable")
	})
	h, _ := mutationHarness(t, base, false)

	text, isErr := h.callTool("corral_sync_repo", map[string]any{"query": "alpha"})
	if !isErr {
		t.Fatal("a failed pull must be reported as a tool error")
	}
	if !strings.Contains(text, "network unreachable") {
		t.Errorf("error should carry the cause, got: %s", text)
	}
}

func TestSyncRepoRefusesUnknownRepo(t *testing.T) {
	h, _ := mutationHarness(t, t.TempDir(), false)
	if _, isErr := h.callTool("corral_sync_repo", map[string]any{"query": "nope"}); !isErr {
		t.Error("syncing an unknown repository must be refused")
	}
}

func TestCloneRepoRefusesExistingTargetAndEscape(t *testing.T) {
	base := t.TempDir()
	existing := makeFakeRepo(t, base, "Public", "go", "taken", "", "")
	stubSeam(t, &gitClone, func(context.Context, string, string, git.CloneOptions) error { return nil })
	h, _ := mutationHarness(t, base, false)

	// Never silently overwrite.
	if _, isErr := h.callTool("corral_clone_repo", map[string]any{
		"url": "https://github.com/o/x.git", "target": existing,
	}); !isErr {
		t.Error("cloning over an existing target must be refused")
	}

	// Never escape the sandbox root.
	if _, isErr := h.callTool("corral_clone_repo", map[string]any{
		"url": "https://github.com/o/x.git", "target": "../outside",
	}); !isErr {
		t.Error("a target outside the root must be refused")
	}
}

func TestCloneRepoSucceedsAndRedactsCredentials(t *testing.T) {
	base := t.TempDir()
	var gotURL string
	stubSeam(t, &gitClone, func(_ context.Context, url, _ string, _ git.CloneOptions) error {
		gotURL = url
		return nil
	})
	stubSeam(t, &mkdirMutation, func(string, os.FileMode) error { return nil })
	h, audit := mutationHarness(t, base, false)

	// #nosec G101 -- a fabricated credential; this test exists to prove such a
	// URL reaches git intact but never reaches the audit log.
	secret := "https://user:s3cr3t@github.com/o/x.git"
	var got map[string]any
	h.callToolJSON("corral_clone_repo", map[string]any{
		"url": secret, "target": filepath.Join(base, "Public", "go", "x"),
	}, &got)
	if gotURL != secret {
		t.Errorf("the real URL must reach git unchanged, got %q", gotURL)
	}

	// But the audit trail must not become a place credentials accumulate.
	b, err := os.ReadFile(audit) // #nosec G304 -- test-controlled path
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "s3cr3t") {
		t.Errorf("audit log leaked the clone credential:\n%s", b)
	}
	if !strings.Contains(string(b), "REDACTED") {
		t.Errorf("audit log should record a redacted URL:\n%s", b)
	}
}

func TestDeleteRepoRequiresBothGates(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	// Mutations on, destructive off.
	h, _ := mutationHarness(t, base, false)
	if h.tools()["corral_delete_repo"] != nil {
		t.Fatal("delete must not be registered without the destructive gate")
	}
}

// The guard cascade. Each of these corresponds to work that exists nowhere but
// the user's disk, so each must refuse rather than proceed.
func TestDeleteRepoGuardCascade(t *testing.T) {
	cases := []struct {
		name   string
		stub   func(t *testing.T)
		expect string
	}{
		{
			name: "uncommitted changes",
			stub: func(t *testing.T) {
				stubSeam(t, &hasDirtyWorkingTree, func(context.Context, string) (bool, string) {
					return true, "working tree has local changes"
				})
			},
			expect: "uncommitted",
		},
		{
			name: "unpublished commits",
			stub: func(t *testing.T) {
				stubSeam(t, &hasUnpushedCommits, func(context.Context, string) (bool, string) {
					return true, "2 commits reachable only from local refs or HEAD"
				})
			},
			expect: "unpublished",
		},
		{
			name: "gitignored content",
			stub: func(t *testing.T) {
				stubSeam(t, &hasIgnoredContent, func(context.Context, string) (bool, string) {
					return true, "1 gitignored path(s) present, e.g. .env"
				})
			},
			expect: "gitignored",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
			safeGuards(t)
			tc.stub(t)
			removed := 0
			stubSeam(t, &removeMutation, func(string) error { removed++; return nil })
			h, audit := mutationHarness(t, base, true)

			text, isErr := h.callTool("corral_delete_repo", map[string]any{"query": "alpha"})
			if !isErr {
				t.Fatalf("delete must refuse when %s is present", tc.name)
			}
			if !strings.Contains(text, tc.expect) {
				t.Errorf("refusal should say why (%q), got: %s", tc.expect, text)
			}
			if removed != 0 {
				t.Fatalf("nothing may be removed after a refusal, removeMutation called %d times", removed)
			}
			// A refusal is still auditable: it is evidence an agent tried.
			recs := auditRecords(t, audit)
			if len(recs) == 0 || recs[len(recs)-1].Result != "refused" {
				t.Errorf("refusal should be audited, got %+v", recs)
			}
		})
	}
}

func TestDeleteRepoSucceedsWhenClean(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	safeGuards(t)
	removed := ""
	stubSeam(t, &removeMutation, func(p string) error { removed = p; return nil })
	h, audit := mutationHarness(t, base, true)

	var got map[string]any
	h.callToolJSON("corral_delete_repo", map[string]any{"query": "alpha"}, &got)
	if got["result"] != "deleted" {
		t.Errorf("result = %v, want deleted", got["result"])
	}
	// The clone is staged aside before it is removed (SEC-5), so what
	// reaches removeMutation is the staged name — in the same directory,
	// derived from the original, and marked as machinery.
	if !strings.HasPrefix(filepath.Base(removed), stagePrefix+"alpha-") {
		t.Errorf("removed %q, want a staged copy of alpha", removed)
	}
	if !strings.HasSuffix(filepath.Dir(removed), filepath.Join("Public", "go")) {
		t.Errorf("staged outside the clone's own directory: %q", removed)
	}
	recs := auditRecords(t, audit)
	if len(recs) != 2 || recs[1].Result != "ok" {
		t.Errorf("expected intent + ok completion, got %+v", recs)
	}
}

// The IsRepository guard exists for a time-of-check/time-of-use race: the
// index is cached, so a directory can stop being a clone between the scan that
// found it and the delete that acts on it. Deleting a directory that is no
// longer a repository would bypass every other guard, all of which ask git.
//
// An earlier version of this test pointed at a directory with no .git at all —
// which the scanner never indexes, so the lookup failed first and the guard was
// never reached. It passed without exercising anything.
func TestDeleteRepoRefusesTargetThatStoppedBeingARepository(t *testing.T) {
	base := t.TempDir()
	repo := makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	safeGuards(t)
	removed := false
	stubSeam(t, &removeMutation, func(string) error { removed = true; return nil })
	h, auditPath := mutationHarness(t, base, true)

	// Populate the scan cache while it is still a repository.
	if _, isErr := h.callTool("corral_find_repo", map[string]any{"query": "alpha"}); isErr {
		t.Fatal("fixture should be findable before the race")
	}
	// Now it isn't one any more.
	if err := os.RemoveAll(filepath.Join(repo, ".git")); err != nil {
		t.Fatal(err)
	}

	text, isErr := h.callTool("corral_delete_repo", map[string]any{"query": "alpha"})
	if !isErr {
		t.Fatalf("delete should have been refused, got: %s", text)
	}
	if !strings.Contains(text, "is not a git repository") {
		t.Errorf("refusal %q should name the failed guard, not some earlier error", text)
	}
	if removed {
		t.Fatal("a directory that is not a git repository was removed")
	}
	recs := auditRecords(t, auditPath)
	if len(recs) == 0 || recs[len(recs)-1].Result != "refused" {
		t.Errorf("the refusal must be audited, got %+v", recs)
	}
}

// A mutation that cannot be audited must not happen: an unlogged mutation
// defeats the mechanism entirely.
func TestMutationRefusedWhenAuditFails(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	// Record rather than t.Fatal: this runs on the server's goroutine, and
	// t.Fatal there calls runtime.Goexit on the wrong goroutine — the handler
	// never returns and the client blocks until the test times out.
	pulled := false
	stubSeam(t, &gitPull, func(context.Context, string, git.PullOptions) error {
		pulled = true
		return nil
	})
	// An audit path that cannot be created: nested underneath a regular file,
	// so creating its parent directory fails with ENOTDIR.
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, ServerOptions{
		Root: base, EnableMutations: true,
		AuditLogPath: filepath.Join(blocker, "audit.log"),
	})
	if _, isErr := h.callTool("corral_sync_repo", map[string]any{"query": "alpha"}); !isErr {
		t.Error("a mutation whose intent cannot be audited must be refused")
	}
	if pulled {
		t.Error("the pull ran even though the intent could not be audited")
	}
}

// failAuditAfter makes the audit log unwritable from the nth Write onward, so a
// mutation can pass its intent record and then fail its completion record. That
// pairing is the whole point of the two-phase audit: the caller must be told
// both what went wrong and that the record of it is missing.
func failAuditAfter(t *testing.T, n int) {
	t.Helper()
	real := openAuditFile
	calls := 0
	openAuditFile = func(name string, flag int, perm os.FileMode) (auditFile, error) {
		calls++
		if calls >= n {
			return nil, errors.New("disk on fire")
		}
		return real(name, flag, perm)
	}
	t.Cleanup(func() { openAuditFile = real })
}

// Every failure path in a mutation must surface as a tool error carrying the
// underlying cause — never a silent success, and never a dropped audit.
func TestMutationFailurePathsReportCause(t *testing.T) {
	// Shaped the way internal/git actually returns them: git names the
	// operation itself, so the MCP layer no longer repeats it. A bare
	// "boom" everywhere made that redundant prefix look load-bearing.
	boom := errors.New("boom")
	pullBoom := errors.New("git pull failed in alpha: boom")
	cloneBoom := errors.New("git clone failed in alpha: boom")

	cases := []struct {
		name       string
		tool       string
		args       map[string]any
		auditFails bool // fail the audit completion, not the intent
		stub       func(t *testing.T)
		wants      []string
	}{
		{
			name: "pull fails",
			tool: "corral_sync_repo", args: map[string]any{"query": "alpha"},
			stub: func(t *testing.T) {
				stubSeam(t, &gitPull, func(context.Context, string, git.PullOptions) error { return pullBoom })
			},
			wants: []string{"git pull failed", "boom"},
		},
		{
			name: "pull fails and the completion record is lost",
			tool: "corral_sync_repo", args: map[string]any{"query": "alpha"},
			auditFails: true,
			stub: func(t *testing.T) {
				stubSeam(t, &gitPull, func(context.Context, string, git.PullOptions) error { return pullBoom })
			},
			wants: []string{"git pull failed", "audit completion failed"},
		},
		{
			name: "state update fails",
			tool: "corral_sync_repo", args: map[string]any{"query": "alpha"},
			stub: func(t *testing.T) {
				stubSeam(t, &gitPull, func(context.Context, string, git.PullOptions) error { return nil })
				stubSeam(t, &markSynced, func(string) error { return boom })
			},
			wants: []string{"state update failed", "boom"},
		},
		{
			name: "sync succeeds but the completion record is lost",
			tool: "corral_sync_repo", args: map[string]any{"query": "alpha"},
			auditFails: true,
			stub: func(t *testing.T) {
				stubSeam(t, &gitPull, func(context.Context, string, git.PullOptions) error { return nil })
				stubSeam(t, &markSynced, func(string) error { return nil })
			},
			wants: []string{"audit completion failed"},
		},
		{
			name: "clone parent directory cannot be created",
			tool: "corral_clone_repo", args: map[string]any{"url": "https://example.com/o/new.git", "target": "Public/go/new"},
			stub: func(t *testing.T) {
				stubSeam(t, &mkdirMutation, func(string, os.FileMode) error { return boom })
			},
			wants: []string{"create target parent", "boom"},
		},
		{
			name: "clone fails",
			tool: "corral_clone_repo", args: map[string]any{"url": "https://example.com/o/new.git", "target": "Public/go/new"},
			stub: func(t *testing.T) {
				stubSeam(t, &mkdirMutation, func(string, os.FileMode) error { return nil })
				stubSeam(t, &gitClone, func(context.Context, string, string, git.CloneOptions) error { return cloneBoom })
			},
			wants: []string{"git clone failed", "boom"},
		},
		{
			name: "removal fails",
			tool: "corral_delete_repo", args: map[string]any{"query": "alpha"},
			stub: func(t *testing.T) {
				safeGuards(t)
				stubSeam(t, &removeMutation, func(string) error { return boom })
			},
			wants: []string{"remove failed", "boom"},
		},
		{
			name: "removal succeeds but the completion record is lost",
			tool: "corral_delete_repo", args: map[string]any{"query": "alpha"},
			auditFails: true,
			stub: func(t *testing.T) {
				safeGuards(t)
				stubSeam(t, &removeMutation, func(string) error { return nil })
			},
			wants: []string{"audit completion failed"},
		},
		{
			name: "a refusal whose audit record is lost still refuses",
			tool: "corral_delete_repo", args: map[string]any{"query": "alpha"},
			stub: func(t *testing.T) {
				safeGuards(t)
				stubSeam(t, &hasDirtyWorkingTree, func(context.Context, string) (bool, string) {
					return true, "M README.md"
				})
				stubSeam(t, &removeMutation, func(string) error {
					t.Error("a refused delete must not remove anything")
					return nil
				})
				failAuditAfter(t, 1)
			},
			wants: []string{"uncommitted changes present", "audit failed"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			makeFakeRepo(t, base, "Public", "go", "alpha", "https://github.com/o/alpha.git", "")
			h, _ := mutationHarness(t, base, true)
			tc.stub(t)
			if tc.auditFails {
				// The intent record (call 1) lands; the completion (call 2) does not.
				failAuditAfter(t, 2)
			}
			text, isErr := h.callTool(tc.tool, tc.args)
			if !isErr {
				t.Fatalf("%s should have failed, got: %s", tc.tool, text)
			}
			for _, want := range tc.wants {
				if !strings.Contains(text, want) {
					t.Errorf("error %q does not mention %q", text, want)
				}
			}
		})
	}
}

// A mutation on an unscannable root must refuse rather than act on a guess.
func TestMutationsRefuseAnUnscannableRoot(t *testing.T) {
	h, _ := mutationHarness(t, filepath.Join(t.TempDir(), "does-not-exist"), true)
	stubSeam(t, &gitPull, func(context.Context, string, git.PullOptions) error {
		t.Error("pull ran against an unscannable root")
		return nil
	})
	stubSeam(t, &removeMutation, func(string) error {
		t.Error("removal ran against an unscannable root")
		return nil
	})
	for _, tool := range []string{"corral_sync_repo", "corral_delete_repo"} {
		text, isErr := h.callTool(tool, map[string]any{"query": "alpha"})
		if !isErr {
			t.Errorf("%s returned success on an unscannable root: %s", tool, text)
		}
	}
}

// If the intent record cannot be written, nothing may happen — this is the
// same contract as the sync path, checked for clone because clone is the one
// mutation that does not require the target to already exist.
func TestCloneRefusedWhenIntentCannotBeAudited(t *testing.T) {
	base := t.TempDir()
	h, _ := mutationHarness(t, base, false)
	cloned := false
	stubSeam(t, &gitClone, func(context.Context, string, string, git.CloneOptions) error {
		cloned = true
		return nil
	})
	failAuditAfter(t, 1)

	text, isErr := h.callTool("corral_clone_repo", map[string]any{
		"url": "https://example.com/o/new.git", "target": "Public/go/new",
	})
	if !isErr {
		t.Fatalf("clone should have been refused, got: %s", text)
	}
	if !strings.Contains(text, "audit intent failed") {
		t.Errorf("error %q should name the audit failure", text)
	}
	if cloned {
		t.Error("the clone ran even though its intent could not be audited")
	}
}

// Both clone failure modes, paired with a lost completion record: the caller
// must learn the operation failed AND that the audit trail is incomplete.
func TestCloneFailuresWithLostAuditRecord(t *testing.T) {
	// mkdir is corral's own failure and carries no git prefix; the clone
	// is git's and carries the one git writes.
	boom := errors.New("boom")
	cloneBoom := errors.New("git clone failed in alpha: boom")
	cases := []struct {
		name  string
		stub  func(t *testing.T)
		wants []string
	}{
		{
			name: "parent directory",
			stub: func(t *testing.T) {
				stubSeam(t, &mkdirMutation, func(string, os.FileMode) error { return boom })
			},
			wants: []string{"create target parent", "audit completion failed"},
		},
		{
			name: "clone itself",
			stub: func(t *testing.T) {
				stubSeam(t, &mkdirMutation, func(string, os.FileMode) error { return nil })
				stubSeam(t, &gitClone, func(context.Context, string, string, git.CloneOptions) error { return cloneBoom })
			},
			wants: []string{"git clone failed", "audit completion failed"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := mutationHarness(t, t.TempDir(), false)
			tc.stub(t)
			failAuditAfter(t, 2)
			text, isErr := h.callTool("corral_clone_repo", map[string]any{
				"url": "https://example.com/o/new.git", "target": "Public/go/new",
			})
			if !isErr {
				t.Fatalf("clone should have failed, got: %s", text)
			}
			for _, want := range tc.wants {
				if !strings.Contains(text, want) {
					t.Errorf("error %q does not mention %q", text, want)
				}
			}
		})
	}
}

// Each delete guard, when its own refusal record cannot be written, must still
// refuse and must say the audit failed too.
func TestDeleteRefusalsWithLostAuditRecord(t *testing.T) {
	guards := []struct {
		name string
		stub func(t *testing.T)
		want string
	}{
		{"unpushed commits", func(t *testing.T) {
			stubSeam(t, &hasUnpushedCommits, func(context.Context, string) (bool, string) {
				return true, "ahead 2"
			})
		}, "unpublished git state present"},
		{"gitignored content", func(t *testing.T) {
			stubSeam(t, &hasIgnoredContent, func(context.Context, string) (bool, string) {
				return true, ".env"
			})
		}, "gitignored content present"},
	}
	for _, g := range guards {
		t.Run(g.name, func(t *testing.T) {
			base := t.TempDir()
			makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
			safeGuards(t)
			g.stub(t)
			stubSeam(t, &removeMutation, func(string) error {
				t.Error("a refused delete must not remove anything")
				return nil
			})
			h, _ := mutationHarness(t, base, true)
			failAuditAfter(t, 1)

			text, isErr := h.callTool("corral_delete_repo", map[string]any{"query": "alpha"})
			if !isErr {
				t.Fatalf("delete should have been refused, got: %s", text)
			}
			for _, want := range []string{g.want, "audit failed"} {
				if !strings.Contains(text, want) {
					t.Errorf("error %q does not mention %q", text, want)
				}
			}
		})
	}
}

func TestWorktreeCreateAndAudits(t *testing.T) {
	base := t.TempDir()
	repoPath := makeFakeRepo(t, base, "Public", "go", "alpha", "https://github.com/o/alpha.git", "")
	realRepo, err := filepath.EvalSymlinks(repoPath)
	if err != nil {
		t.Fatal(err)
	}
	created := 0
	var gotRepo, gotTarget, gotBranch string
	stubSeam(t, &gitCreateWorktree, func(_ context.Context, repo, target, branch string) error {
		created++
		gotRepo = repo
		gotTarget = target
		gotBranch = branch
		return nil
	})
	h, audit := mutationHarness(t, base, false)

	var got map[string]any
	h.callToolJSON("corral_create_worktree", map[string]any{
		"repo":   "alpha",
		"branch": "feat/isolated",
	}, &got)

	if got["result"] != "created" {
		t.Errorf("result = %v, want created", got["result"])
	}
	wantTarget := filepath.Join(realRepo, ".git", "corral-worktrees", filepath.FromSlash("feat/isolated"))
	if gotTarget != wantTarget {
		t.Errorf("gitCreateWorktree target = %q, want %q", gotTarget, wantTarget)
	}
	if gotBranch != "feat/isolated" {
		t.Errorf("gitCreateWorktree branch = %q, want feat/isolated", gotBranch)
	}
	if gotRepo != realRepo {
		t.Errorf("gitCreateWorktree repo = %q, want %q", gotRepo, realRepo)
	}
	if created != 1 {
		t.Errorf("gitCreateWorktree called %d times, want 1", created)
	}

	recs := auditRecords(t, audit)
	if len(recs) != 2 {
		t.Fatalf("expected intent + completion records, got %d: %+v", len(recs), recs)
	}
	if recs[0].Phase != "intent" || recs[1].Phase != "completion" {
		t.Errorf("phases = %q,%q want intent,completion", recs[0].Phase, recs[1].Phase)
	}
	if recs[0].OperationID == "" || recs[0].OperationID != recs[1].OperationID {
		t.Errorf("records are not correlated: %+v", recs)
	}
	if recs[0].Tool != "corral_create_worktree" || recs[1].Tool != "corral_create_worktree" {
		t.Errorf("tool names incorrect in audit records: %+v", recs)
	}
}

func TestWorktreeReleaseAndAudits(t *testing.T) {
	base := t.TempDir()
	repoPath := makeFakeRepo(t, base, "Public", "go", "alpha", "https://github.com/o/alpha.git", "")
	realRepo, err := filepath.EvalSymlinks(repoPath)
	if err != nil {
		t.Fatal(err)
	}
	stubSeam(t, &hasLocalChanges, func(context.Context, string) (bool, string) { return false, "" })
	var removedTarget string
	var removedForce bool
	stubSeam(t, &gitRemoveWorktree, func(_ context.Context, repo, target string, force bool) error {
		removedTarget = target
		removedForce = force
		return nil
	})
	pruned := 0
	stubSeam(t, &gitPruneWorktrees, func(context.Context, string) error {
		pruned++
		return nil
	})

	h, audit := mutationHarness(t, base, false)

	// Release by branch name
	var got map[string]any
	h.callToolJSON("corral_release_worktree", map[string]any{
		"repo":   "alpha",
		"branch": "feat/isolated",
	}, &got)
	if got["result"] != "released" {
		t.Errorf("result = %v, want released", got["result"])
	}
	wantTarget := filepath.Join(realRepo, ".git", "corral-worktrees", filepath.FromSlash("feat/isolated"))
	if removedTarget != wantTarget {
		t.Errorf("removed target = %q, want %q", removedTarget, wantTarget)
	}
	if removedForce != false {
		t.Errorf("removed force = %v, want false", removedForce)
	}
	if pruned != 1 {
		t.Errorf("pruned called %d times, want 1", pruned)
	}

	recs := auditRecords(t, audit)
	if len(recs) != 2 {
		t.Fatalf("expected intent + completion records, got %d: %+v", len(recs), recs)
	}

	// Release by explicit path with force
	h.callToolJSON("corral_release_worktree", map[string]any{
		"repo":  "alpha",
		"path":  wantTarget,
		"force": true,
	}, &got)
	if got["result"] != "released" {
		t.Errorf("result = %v, want released", got["result"])
	}
	if removedForce != true {
		t.Errorf("removed force = %v, want true", removedForce)
	}
}

func TestWorktreeInputValidationAndRefusals(t *testing.T) {
	base := t.TempDir()
	repoPath := makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	realRepo, err := filepath.EvalSymlinks(repoPath)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := mutationHarness(t, base, false)

	// Create input validation
	createCases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"empty repo", map[string]any{"repo": "", "branch": "feat"}, "repo is required"},
		{"empty branch", map[string]any{"repo": "alpha", "branch": ""}, "invalid branch name"},
		{"branch with dash prefix", map[string]any{"repo": "alpha", "branch": "-bad"}, "invalid branch name"},
		{"branch with dotdot", map[string]any{"repo": "alpha", "branch": "feat/../bad"}, "invalid branch name"},
		{"repo not found", map[string]any{"repo": "missing", "branch": "feat"}, "no repository matches"},
	}
	for _, tc := range createCases {
		t.Run("create_"+tc.name, func(t *testing.T) {
			text, isErr := h.callTool("corral_create_worktree", tc.args)
			if !isErr {
				t.Fatalf("expected error, got: %s", text)
			}
			if !strings.Contains(text, tc.want) {
				t.Errorf("error %q does not contain %q", text, tc.want)
			}
		})
	}

	// Worktree already exists
	t.Run("create_already_exists", func(t *testing.T) {
		existing := filepath.Join(realRepo, ".git", "corral-worktrees", "existing")
		if err := os.MkdirAll(existing, 0o750); err != nil {
			t.Fatal(err)
		}
		text, isErr := h.callTool("corral_create_worktree", map[string]any{
			"repo": "alpha", "branch": "existing",
		})
		if !isErr {
			t.Fatalf("expected error for existing worktree, got: %s", text)
		}
		if !strings.Contains(text, "worktree already exists") {
			t.Errorf("error %q does not contain 'worktree already exists'", text)
		}
	})

	// Release input validation
	releaseCases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"empty repo", map[string]any{"repo": "", "branch": "feat"}, "repo is required"},
		{"missing branch and path", map[string]any{"repo": "alpha"}, "either branch or path must be specified"},
		{"invalid branch with prefix dash", map[string]any{"repo": "alpha", "branch": "-bad"}, "invalid branch name"},
		{"invalid branch with dotdot", map[string]any{"repo": "alpha", "branch": "foo/../bar"}, "invalid branch name"},
		{"invalid branch empty spaces", map[string]any{"repo": "alpha", "branch": "   "}, "invalid branch name"},
		{"repo not found", map[string]any{"repo": "missing", "branch": "feat"}, "no repository matches"},
		{"path outside worktrees dir", map[string]any{"repo": "alpha", "path": filepath.Join(base, "other")}, "is not inside"},
		{"path traversal", map[string]any{"repo": "alpha", "path": filepath.Join(realRepo, ".git", "corral-worktrees", "..", "config")}, "is not inside"},
		{"path is base itself", map[string]any{"repo": "alpha", "path": filepath.Join(realRepo, ".git", "corral-worktrees")}, "is not inside"},
	}
	for _, tc := range releaseCases {
		t.Run("release_"+tc.name, func(t *testing.T) {
			text, isErr := h.callTool("corral_release_worktree", tc.args)
			if !isErr {
				t.Fatalf("expected error, got: %s", text)
			}
			if !strings.Contains(text, tc.want) {
				t.Errorf("error %q does not contain %q", text, tc.want)
			}
		})
	}
}

func TestWorktreeDirtyRefusal(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	stubSeam(t, &hasLocalChanges, func(context.Context, string) (bool, string) {
		return true, "uncommitted changes in foo.go"
	})
	h, audit := mutationHarness(t, base, false)

	// Refusal without force
	text, isErr := h.callTool("corral_release_worktree", map[string]any{
		"repo":   "alpha",
		"branch": "feat",
	})
	if !isErr {
		t.Fatalf("dirty worktree release should be refused, got: %s", text)
	}
	if !strings.Contains(text, "refusing to release dirty worktree: uncommitted changes in foo.go") {
		t.Errorf("refusal %q does not state reason", text)
	}
	recs := auditRecords(t, audit)
	if len(recs) != 2 || recs[1].Result != "refused" {
		t.Fatalf("expected intent + refusal records, got %+v", recs)
	}

	// Refusal with broken audit
	failAuditAfter(t, 2)
	text, isErr = h.callTool("corral_release_worktree", map[string]any{
		"repo":   "alpha",
		"branch": "feat",
	})
	if !isErr {
		t.Fatalf("dirty worktree release should be refused, got: %s", text)
	}
	if !strings.Contains(text, "audit refusal failed") {
		t.Errorf("refusal %q does not report failed audit", text)
	}
}

func TestWorktreeAuditAndFilesystemFailures(t *testing.T) {
	boom := errors.New("disk boom")

	// Unscannable root
	t.Run("unscannable_root", func(t *testing.T) {
		h, _ := mutationHarness(t, filepath.Join(t.TempDir(), "does-not-exist"), false)
		for _, tool := range []string{"corral_create_worktree", "corral_release_worktree"} {
			text, isErr := h.callTool(tool, map[string]any{"repo": "alpha", "branch": "feat"})
			if !isErr {
				t.Errorf("%s succeeded on unscannable root: %s", tool, text)
			}
			if !strings.Contains(text, "scan workspace") {
				t.Errorf("%s error %q does not mention scan workspace", tool, text)
			}
		}
	})

	// SafeMutationPath failure
	t.Run("safe_mutation_path_failure", func(t *testing.T) {
		base := t.TempDir()
		makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
		h, _ := mutationHarness(t, base, false)

		real := relSafePath
		calls := 0
		stubSeam(t, &relSafePath, func(b, target string) (string, error) {
			calls++
			if calls > 1 {
				return "", errors.New("rel failure")
			}
			return real(b, target)
		})

		text, isErr := h.callTool("corral_create_worktree", map[string]any{"repo": "alpha", "branch": "feat"})
		if !isErr || !strings.Contains(text, "rel failure") {
			t.Errorf("create SafeMutationPath failure: isErr=%v, text=%s", isErr, text)
		}

		calls = 0
		text, isErr = h.callTool("corral_release_worktree", map[string]any{"repo": "alpha", "branch": "feat"})
		if !isErr || !strings.Contains(text, "rel failure") {
			t.Errorf("release SafeMutationPath failure: isErr=%v, text=%s", isErr, text)
		}
	})

	// Audit intent failures
	t.Run("audit_intent_failure", func(t *testing.T) {
		base := t.TempDir()
		makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
		h, _ := mutationHarness(t, base, false)

		failAuditAfter(t, 1)
		text, isErr := h.callTool("corral_create_worktree", map[string]any{"repo": "alpha", "branch": "feat"})
		if !isErr || !strings.Contains(text, "audit intent failed") {
			t.Errorf("create intent audit failure: isErr=%v, text=%s", isErr, text)
		}

		failAuditAfter(t, 1)
		text, isErr = h.callTool("corral_release_worktree", map[string]any{"repo": "alpha", "branch": "feat"})
		if !isErr || !strings.Contains(text, "audit intent failed") {
			t.Errorf("release intent audit failure: isErr=%v, text=%s", isErr, text)
		}
	})

	// Create: mkdir failure
	t.Run("create_mkdir_failure", func(t *testing.T) {
		base := t.TempDir()
		makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
		h, _ := mutationHarness(t, base, false)
		stubSeam(t, &mkdirMutation, func(string, os.FileMode) error { return boom })

		text, isErr := h.callTool("corral_create_worktree", map[string]any{"repo": "alpha", "branch": "feat"})
		if !isErr || !strings.Contains(text, "create worktree parent") {
			t.Errorf("create mkdir failure: isErr=%v, text=%s", isErr, text)
		}

		// with lost audit record
		failAuditAfter(t, 2)
		text, isErr = h.callTool("corral_create_worktree", map[string]any{"repo": "alpha", "branch": "feat"})
		if !isErr || !strings.Contains(text, "create worktree parent") || !strings.Contains(text, "audit completion failed") {
			t.Errorf("create mkdir with lost audit: isErr=%v, text=%s", isErr, text)
		}
	})

	// Create: gitCreateWorktree failure
	t.Run("create_git_failure", func(t *testing.T) {
		base := t.TempDir()
		makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
		h, _ := mutationHarness(t, base, false)
		stubSeam(t, &mkdirMutation, func(string, os.FileMode) error { return nil })
		stubSeam(t, &gitCreateWorktree, func(context.Context, string, string, string) error { return boom })

		text, isErr := h.callTool("corral_create_worktree", map[string]any{"repo": "alpha", "branch": "feat"})
		if !isErr || !strings.Contains(text, "disk boom") {
			t.Errorf("create git failure: isErr=%v, text=%s", isErr, text)
		}

		// with lost audit record
		failAuditAfter(t, 2)
		text, isErr = h.callTool("corral_create_worktree", map[string]any{"repo": "alpha", "branch": "feat"})
		if !isErr || !strings.Contains(text, "disk boom") || !strings.Contains(text, "audit completion failed") {
			t.Errorf("create git with lost audit: isErr=%v, text=%s", isErr, text)
		}
	})

	// Create: audit completion failure on success
	t.Run("create_audit_completion_failure", func(t *testing.T) {
		base := t.TempDir()
		makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
		h, _ := mutationHarness(t, base, false)
		stubSeam(t, &mkdirMutation, func(string, os.FileMode) error { return nil })
		stubSeam(t, &gitCreateWorktree, func(context.Context, string, string, string) error { return nil })

		failAuditAfter(t, 2)
		text, isErr := h.callTool("corral_create_worktree", map[string]any{"repo": "alpha", "branch": "feat"})
		if !isErr || !strings.Contains(text, "audit completion failed") {
			t.Errorf("create audit completion failure: isErr=%v, text=%s", isErr, text)
		}
	})

	// Release: gitRemoveWorktree failure
	t.Run("release_git_failure", func(t *testing.T) {
		base := t.TempDir()
		makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
		h, _ := mutationHarness(t, base, false)
		stubSeam(t, &hasLocalChanges, func(context.Context, string) (bool, string) { return false, "" })
		stubSeam(t, &gitRemoveWorktree, func(context.Context, string, string, bool) error { return boom })

		text, isErr := h.callTool("corral_release_worktree", map[string]any{"repo": "alpha", "branch": "feat"})
		if !isErr || !strings.Contains(text, "disk boom") {
			t.Errorf("release git failure: isErr=%v, text=%s", isErr, text)
		}

		// with lost audit record
		failAuditAfter(t, 2)
		text, isErr = h.callTool("corral_release_worktree", map[string]any{"repo": "alpha", "branch": "feat"})
		if !isErr || !strings.Contains(text, "disk boom") || !strings.Contains(text, "audit completion failed") {
			t.Errorf("release git with lost audit: isErr=%v, text=%s", isErr, text)
		}
	})

	// Release: audit completion failure on success
	t.Run("release_audit_completion_failure", func(t *testing.T) {
		base := t.TempDir()
		makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
		h, _ := mutationHarness(t, base, false)
		stubSeam(t, &hasLocalChanges, func(context.Context, string) (bool, string) { return false, "" })
		stubSeam(t, &gitRemoveWorktree, func(context.Context, string, string, bool) error { return nil })
		stubSeam(t, &gitPruneWorktrees, func(context.Context, string) error { return nil })

		failAuditAfter(t, 2)
		text, isErr := h.callTool("corral_release_worktree", map[string]any{"repo": "alpha", "branch": "feat"})
		if !isErr || !strings.Contains(text, "audit completion failed") {
			t.Errorf("release audit completion failure: isErr=%v, text=%s", isErr, text)
		}
	})
}
