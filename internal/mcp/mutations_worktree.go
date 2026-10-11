// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sebastienrousseau/corralctl/internal/sanitize"
)

// handleCreateWorktree creates an isolated worktree under .git/corral-worktrees/<branch>.
func (s *Server) handleCreateWorktree(ctx context.Context, _ *mcp.CallToolRequest, in createWorktreeInput) (*mcp.CallToolResult, MutationOutput, error) {
	if in.Repo == "" {
		return nil, MutationOutput{}, fmt.Errorf("repo is required")
	}
	branch := strings.TrimSpace(in.Branch)
	if branch == "" || strings.HasPrefix(branch, "-") || strings.Contains(branch, "..") {
		return nil, MutationOutput{}, fmt.Errorf("invalid branch name %q", in.Branch)
	}

	idx, err := s.scan()
	if err != nil {
		return nil, MutationOutput{}, fmt.Errorf("scan workspace: %v", err)
	}
	repo, err := idx.Find(in.Repo)
	if err != nil {
		return nil, MutationOutput{}, fmt.Errorf("%v", err)
	}
	safeRepo, err := idx.SafeMutationPath(repo.Path)
	if err != nil {
		return nil, MutationOutput{}, fmt.Errorf("%v", err)
	}

	worktreeDir := filepath.Join(safeRepo, ".git", "corral-worktrees", filepath.FromSlash(filepath.Clean(branch)))
	if _, err := statMutation(worktreeDir); err == nil {
		return nil, MutationOutput{}, fmt.Errorf("worktree already exists at %s", worktreeDir)
	}

	rec := AuditRecord{
		Tool:   "corral_create_worktree",
		Target: worktreeDir,
		Args:   map[string]any{"repo": in.Repo, "branch": branch},
	}
	rec, err = s.beginMutation(rec)
	if err != nil {
		return nil, MutationOutput{}, fmt.Errorf("audit intent failed: %v", err)
	}

	if err := mkdirMutation(filepath.Dir(worktreeDir), 0o750); err != nil {
		if auditErr := s.completeMutation(rec, "error", err.Error()); auditErr != nil {
			return nil, MutationOutput{}, fmt.Errorf("create worktree parent: %v; audit completion failed: %v", err, auditErr)
		}
		return nil, MutationOutput{}, fmt.Errorf("create worktree parent: %v", err)
	}

	createErr := gitCreateWorktree(ctx, safeRepo, worktreeDir, branch)
	if createErr != nil {
		if auditErr := s.completeMutation(rec, "error", createErr.Error()); auditErr != nil {
			return nil, MutationOutput{}, fmt.Errorf("%v; audit completion failed: %v", createErr, auditErr)
		}
		return nil, MutationOutput{}, fmt.Errorf("%v", createErr)
	}

	if err := s.completeMutation(rec, "ok", ""); err != nil {
		return nil, MutationOutput{}, fmt.Errorf("audit completion failed: %v", err)
	}
	out := MutationOutput{
		Tool:   "corral_create_worktree",
		Repo:   repo.RelPath,
		Target: worktreeDir,
		Result: "created",
	}
	return jsonResult(out), out, nil
}

// handleReleaseWorktree removes an agent worktree and prunes worktree metadata.
func (s *Server) handleReleaseWorktree(ctx context.Context, _ *mcp.CallToolRequest, in releaseWorktreeInput) (*mcp.CallToolResult, MutationOutput, error) {
	if in.Repo == "" {
		return nil, MutationOutput{}, fmt.Errorf("repo is required")
	}

	idx, err := s.scan()
	if err != nil {
		return nil, MutationOutput{}, fmt.Errorf("scan workspace: %v", err)
	}
	repo, err := idx.Find(in.Repo)
	if err != nil {
		return nil, MutationOutput{}, fmt.Errorf("%v", err)
	}
	safeRepo, err := idx.SafeMutationPath(repo.Path)
	if err != nil {
		return nil, MutationOutput{}, fmt.Errorf("%v", err)
	}

	allowedBase := filepath.Join(safeRepo, ".git", "corral-worktrees")
	var targetWorktree string
	if in.Branch != "" {
		branch := strings.TrimSpace(in.Branch)
		if branch == "" || strings.HasPrefix(branch, "-") || strings.Contains(branch, "..") {
			return nil, MutationOutput{}, fmt.Errorf("invalid branch name %q", in.Branch)
		}
		targetWorktree = filepath.Join(allowedBase, filepath.FromSlash(filepath.Clean(branch)))
	} else if in.Path != "" {
		targetWorktree = filepath.Clean(in.Path)
	} else {
		return nil, MutationOutput{}, fmt.Errorf("either branch or path must be specified")
	}

	rel, err := filepath.Rel(allowedBase, targetWorktree)
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		return nil, MutationOutput{}, fmt.Errorf("worktree path %q is not inside %s", targetWorktree, allowedBase)
	}

	rec := AuditRecord{
		Tool:   "corral_release_worktree",
		Target: targetWorktree,
		Args:   map[string]any{"repo": in.Repo, "branch": in.Branch, "force": in.Force},
	}
	rec, err = s.beginMutation(rec)
	if err != nil {
		return nil, MutationOutput{}, fmt.Errorf("audit intent failed: %v", err)
	}

	if !in.Force {
		if hasChanges, reason := hasLocalChanges(ctx, targetWorktree); hasChanges {
			msg := fmt.Sprintf("refusing to release dirty worktree: %s (pass force=true to override)", reason)
			if auditErr := s.auditRefusal(rec, msg); auditErr != nil {
				return nil, MutationOutput{}, fmt.Errorf("%s; audit refusal failed: %v", msg, auditErr)
			}
			return nil, MutationOutput{}, fmt.Errorf("%s", msg)
		}
	}

	removeErr := gitRemoveWorktree(ctx, safeRepo, targetWorktree, in.Force)
	if removeErr != nil {
		if auditErr := s.completeMutation(rec, "error", removeErr.Error()); auditErr != nil {
			return nil, MutationOutput{}, fmt.Errorf("%v; audit completion failed: %v", removeErr, auditErr)
		}
		return nil, MutationOutput{}, fmt.Errorf("%v", removeErr)
	}
	_ = gitPruneWorktrees(ctx, safeRepo)

	if err := s.completeMutation(rec, "ok", ""); err != nil {
		return nil, MutationOutput{}, fmt.Errorf("audit completion failed: %v", err)
	}
	out := MutationOutput{
		Tool:   "corral_release_worktree",
		Repo:   repo.RelPath,
		Target: targetWorktree,
		Result: "released",
	}
	return jsonResult(out), out, nil
}

// pruneWorktreesInput is the argument set for corral_prune_worktrees.
type pruneWorktreesInput struct {
	Repo     string `json:"repo,omitempty" jsonschema:"Filter pruning to a specific repository name or relative path."`
	DryRun   bool   `json:"dry_run,omitempty" jsonschema:"When true, reports which worktrees would be pruned without removing them."`
	TempOnly bool   `json:"temp_only,omitempty" jsonschema:"When true, only prune worktrees located in temporary or scratchpad paths."`
	Force    bool   `json:"force,omitempty" jsonschema:"When true, force remove worktrees even if local modifications exist."`
}

// WorktreePrunedItem describes one worktree cleaned up by corral_prune_worktrees.
type WorktreePrunedItem struct {
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
	Reason string `json:"reason"`
	Pruned bool   `json:"pruned"`
}

// PruneWorktreesOutput is the structured content returned by corral_prune_worktrees.
type PruneWorktreesOutput struct {
	TotalChecked int                  `json:"total_checked"`
	PrunedCount  int                  `json:"pruned_count"`
	DryRun       bool                 `json:"dry_run"`
	Pruned       []WorktreePrunedItem `json:"pruned"`
}

func isTempWorktreePath(p string) bool {
	slashPath := filepath.ToSlash(p)
	tmp := filepath.ToSlash(os.TempDir())
	if strings.HasPrefix(slashPath, tmp) || strings.HasPrefix(slashPath, "/tmp/") || strings.HasPrefix(slashPath, "/private/tmp/") {
		return true
	}
	return strings.Contains(slashPath, ".git/corral-worktrees") || strings.Contains(slashPath, "scratchpad")
}

func (s *Server) handlePruneWorktrees(ctx context.Context, _ *mcp.CallToolRequest, in pruneWorktreesInput) (*mcp.CallToolResult, PruneWorktreesOutput, error) {
	idx, err := s.scan()
	if err != nil {
		return nil, PruneWorktreesOutput{}, fmt.Errorf("scan workspace: %v", err)
	}

	targets := idx.Repos
	if in.Repo != "" {
		match, findErr := idx.Find(in.Repo)
		if findErr != nil {
			return nil, PruneWorktreesOutput{}, fmt.Errorf("%v", findErr)
		}
		targets = []RepoEntry{*match}
	}

	rec := AuditRecord{
		Tool:   "corral_prune_worktrees",
		Target: s.opts.Root,
		Args:   map[string]any{"repo": in.Repo, "dry_run": in.DryRun, "temp_only": in.TempOnly, "force": in.Force},
	}
	rec, err = s.beginMutation(rec)
	if err != nil {
		return nil, PruneWorktreesOutput{}, fmt.Errorf("audit intent failed: %v", err)
	}

	output := PruneWorktreesOutput{
		DryRun: in.DryRun,
		Pruned: []WorktreePrunedItem{},
	}

	for _, repo := range targets {
		safeRepo, safeErr := idx.SafeMutationPath(repo.Path)
		if safeErr != nil {
			continue
		}
		wts, listErr := gitListWorktrees(ctx, safeRepo)
		if listErr != nil {
			continue
		}
		for _, wt := range wts {
			output.TotalChecked++
			if wt.Bare || wt.Path == safeRepo || wt.Path == repo.Path {
				continue
			}

			_, statErr := statPath(wt.Path)
			isMissing := statErr != nil
			isTemp := isTempWorktreePath(wt.Path)

			if in.TempOnly && !isTemp {
				continue
			}

			var reason string
			switch {
			case isMissing:
				reason = "stale worktree (path missing on disk)"
			case isTemp:
				reason = "temporary worktree path"
			default:
				reason = "linked worktree pruned"
			}

			item := WorktreePrunedItem{
				Repo:   repo.Name,
				Path:   sanitize.Untrusted(wt.Path, 1024),
				Branch: wt.Branch,
				Reason: reason,
			}

			if in.DryRun {
				item.Pruned = false
				output.Pruned = append(output.Pruned, item)
				output.PrunedCount++
			} else {
				if !isMissing {
					if rmErr := gitRemoveWorktree(ctx, safeRepo, wt.Path, in.Force); rmErr != nil {
						item.Reason = fmt.Sprintf("failed to remove: %v", rmErr)
						item.Pruned = false
						output.Pruned = append(output.Pruned, item)
						continue
					}
				}
				_ = gitPruneWorktrees(ctx, safeRepo)
				item.Pruned = true
				output.Pruned = append(output.Pruned, item)
				output.PrunedCount++
			}
		}
	}

	if err := s.completeMutation(rec, "ok", ""); err != nil {
		return nil, PruneWorktreesOutput{}, fmt.Errorf("audit completion failed: %v", err)
	}
	return jsonResult(output), output, nil
}
