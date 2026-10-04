// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/git"
)

func TestCorralDoctor(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "https://github.com/o/alpha.git", "")
	makeFakeRepo(t, base, "Private", "rust", "beta", "https://github.com/o/beta.git", "")

	h := newHarness(t, ServerOptions{Root: base})

	// Case 1: Dirty and unpushed issues detected
	stubSeam(t, &doctorHasLocalChanges, func(ctx context.Context, repoPath string) (bool, string) {
		if filepath.Base(repoPath) == "alpha" {
			return true, "modified files"
		}
		return false, ""
	})
	stubSeam(t, &doctorHasUnpublishedWork, func(ctx context.Context, repoPath string) (bool, string) {
		if filepath.Base(repoPath) == "beta" {
			return true, "unpushed commits"
		}
		return false, ""
	})
	stubSeam(t, &doctorListWorktrees, func(ctx context.Context, repoPath string) ([]git.WorktreeInfo, error) {
		if filepath.Base(repoPath) == "alpha" {
			return []git.WorktreeInfo{
				{Path: repoPath, Branch: "main"},
				{Path: filepath.Join(repoPath, ".git/corral-worktrees/feat"), Branch: "feat"},
				{Path: filepath.Join(repoPath, "bare"), Bare: true},
			}, nil
		}
		return nil, errors.New("list worktrees failed")
	})

	var out DoctorOutput
	h.callToolJSON("corral_doctor", map[string]any{}, &out)

	if out.TotalRepos != 2 {
		t.Errorf("TotalRepos = %d, want 2", out.TotalRepos)
	}
	if out.Healthy {
		t.Errorf("expected Healthy = false with dirty and unpushed repos")
	}
	if out.IssuesCount != 2 {
		t.Errorf("IssuesCount = %d, want 2", out.IssuesCount)
	}
	if len(out.DirtyRepos) != 1 || out.DirtyRepos[0] != "alpha" {
		t.Errorf("DirtyRepos = %+v, want [alpha]", out.DirtyRepos)
	}
	if len(out.UnpushedRepos) != 1 || out.UnpushedRepos[0] != "beta" {
		t.Errorf("UnpushedRepos = %+v, want [beta]", out.UnpushedRepos)
	}
	if len(out.Worktrees) != 1 {
		t.Errorf("Worktrees = %+v, want 1 entry", out.Worktrees)
	}

	// Case 2: Healthy workspace
	stubSeam(t, &doctorHasLocalChanges, func(ctx context.Context, repoPath string) (bool, string) {
		return false, ""
	})
	stubSeam(t, &doctorHasUnpublishedWork, func(ctx context.Context, repoPath string) (bool, string) {
		return false, ""
	})
	var cleanOut DoctorOutput
	h.callToolJSON("corral_doctor", map[string]any{"check_caches": true}, &cleanOut)
	if !cleanOut.Healthy {
		t.Errorf("expected clean workspace to be healthy")
	}
	if cleanOut.IssuesCount != 0 {
		t.Errorf("expected 0 issues, got %d", cleanOut.IssuesCount)
	}

	// Case 3: Scan workspace error
	errHarness := newHarness(t, ServerOptions{Root: filepath.Join(t.TempDir(), "nonexistent")})
	_, isErr := errHarness.callTool("corral_doctor", map[string]any{})
	if !isErr {
		t.Errorf("expected error when workspace scan fails")
	}
}

func TestCorralPruneWorktrees(t *testing.T) {
	base := t.TempDir()
	alphaPath := makeFakeRepo(t, base, "Public", "go", "alpha", "https://github.com/o/alpha.git", "")
	makeFakeRepo(t, base, "Private", "rust", "beta", "https://github.com/o/beta.git", "")

	h, _ := mutationHarness(t, base, false)

	missingPath := "/var/custom/missing-worktree"
	existingTempPath := filepath.Join(alphaPath, ".git", "corral-worktrees", "temp-1")
	existingNonTempPath := "/var/custom/other-worktree"

	stubSeam(t, &statPath, func(path string) (os.FileInfo, error) {
		if path == missingPath {
			return nil, os.ErrNotExist
		}
		return os.Stat(base)
	})

	stubSeam(t, &gitListWorktrees, func(ctx context.Context, repoPath string) ([]git.WorktreeInfo, error) {
		if strings.HasSuffix(filepath.ToSlash(repoPath), "Public/go/alpha") {
			return []git.WorktreeInfo{
				{Path: repoPath, Branch: "main"},
				{Path: repoPath + "-bare", Bare: true},
				{Path: missingPath, Branch: "gone"},
				{Path: existingTempPath, Branch: "temp-1"},
				{Path: existingNonTempPath, Branch: "other"},
			}, nil
		}
		return nil, errors.New("list failure")
	})

	removed := []string{}
	stubSeam(t, &gitRemoveWorktree, func(ctx context.Context, targetDir, worktreePath string, force bool) error {
		removed = append(removed, worktreePath)
		return nil
	})
	prunedCalls := 0
	stubSeam(t, &gitPruneWorktrees, func(ctx context.Context, targetDir string) error {
		prunedCalls++
		return nil
	})

	// 1. Dry run
	var dryOut PruneWorktreesOutput
	h.callToolJSON("corral_prune_worktrees", map[string]any{
		"repo":    "alpha",
		"dry_run": true,
	}, &dryOut)

	if !dryOut.DryRun {
		t.Errorf("expected DryRun = true")
	}
	if dryOut.PrunedCount != 3 { // missingPath, existingTempPath, existingNonTempPath
		t.Errorf("PrunedCount = %d, want 3", dryOut.PrunedCount)
	}
	if len(removed) != 0 {
		t.Errorf("dry run should not remove any worktrees")
	}

	// 2. Temp-only dry run
	var tempOnlyOut PruneWorktreesOutput
	h.callToolJSON("corral_prune_worktrees", map[string]any{
		"repo":      "alpha",
		"dry_run":   true,
		"temp_only": true,
	}, &tempOnlyOut)

	if tempOnlyOut.PrunedCount != 1 { // only existingTempPath is temp and not missing
		t.Errorf("temp-only PrunedCount = %d, want 1", tempOnlyOut.PrunedCount)
	}

	// 3. Real prune
	var realOut PruneWorktreesOutput
	h.callToolJSON("corral_prune_worktrees", map[string]any{
		"repo":  "alpha",
		"force": true,
	}, &realOut)

	if realOut.PrunedCount != 3 {
		t.Errorf("real prune PrunedCount = %d, want 3", realOut.PrunedCount)
	}
	if len(removed) != 2 { // existingTempPath and existingNonTempPath (missingPath is not on disk so git remove is skipped)
		t.Errorf("removed count = %d, want 2", len(removed))
	}
	if prunedCalls < 1 {
		t.Errorf("expected gitPruneWorktrees calls")
	}

	// 4. Removal error branch
	stubSeam(t, &gitRemoveWorktree, func(ctx context.Context, targetDir, worktreePath string, force bool) error {
		return errors.New("cannot remove worktree")
	})
	var errOut PruneWorktreesOutput
	h.callToolJSON("corral_prune_worktrees", map[string]any{
		"repo": "alpha",
	}, &errOut)
	var foundErr bool
	for _, p := range errOut.Pruned {
		if !p.Pruned && p.Reason != "" {
			foundErr = true
		}
	}
	if !foundErr {
		t.Errorf("expected removal error recorded in pruned items: %+v", errOut)
	}

	// 5. Repo not found error
	_, isErr := h.callTool("corral_prune_worktrees", map[string]any{"repo": "nonexistent"})
	if !isErr {
		t.Errorf("expected error for nonexistent repo filter")
	}

	// 6. Scan failure
	errHarness, _ := mutationHarness(t, filepath.Join(t.TempDir(), "nonexistent"), false)
	_, isErr = errHarness.callTool("corral_prune_worktrees", map[string]any{})
	if !isErr {
		t.Errorf("expected scan error on nonexistent root")
	}

	// 7. Audit failure
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	failingAuditHarness := newHarness(t, ServerOptions{
		Root:            base,
		EnableMutations: true,
		AuditLogPath:    filepath.Join(blocker, "audit.log"),
	})
	_, isErr = failingAuditHarness.callTool("corral_prune_worktrees", map[string]any{})
	if !isErr {
		t.Errorf("expected audit error when log file cannot be created")
	}

	// 8. List error on repo without filter (hits listErr != nil continue branch)
	var allReposOut PruneWorktreesOutput
	h.callToolJSON("corral_prune_worktrees", map[string]any{"dry_run": true}, &allReposOut)

	// 9. SafeMutationPath failure
	realRel := relSafePath
	calls := 0
	stubSeam(t, &relSafePath, func(b, target string) (string, error) {
		calls++
		if calls > 1 {
			return "", errors.New("rel failure")
		}
		return realRel(b, target)
	})
	var safeFailOut PruneWorktreesOutput
	h.callToolJSON("corral_prune_worktrees", map[string]any{"dry_run": true}, &safeFailOut)

	// 10. Audit completion failure
	failAuditAfter(t, 2)
	text, isErr := h.callTool("corral_prune_worktrees", map[string]any{"repo": "alpha", "dry_run": true})
	if !isErr || !strings.Contains(text, "audit completion failed") {
		t.Errorf("prune audit completion failure: isErr=%v, text=%s", isErr, text)
	}
}

func TestIsTempWorktreePath(t *testing.T) {
	if !isTempWorktreePath("/tmp/foo") {
		t.Error("expected /tmp/foo to be temp")
	}
	if !isTempWorktreePath("/private/tmp/foo") {
		t.Error("expected /private/tmp/foo to be temp")
	}
	if !isTempWorktreePath("/repo/scratchpad/foo") {
		t.Error("expected /repo/scratchpad/foo to be temp")
	}
	if !isTempWorktreePath("/repo/.git/corral-worktrees/foo") {
		t.Error("expected /repo/.git/corral-worktrees/foo to be temp")
	}
	if isTempWorktreePath("/home/user/code/normal-repo") {
		t.Error("expected normal repo not to be temp")
	}
}

