// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package git

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// CreateWorktree creates a new linked Git worktree for targetDir at worktreePath.
// If branch is non-empty, a new branch with that name is created.
func CreateWorktree(ctx context.Context, targetDir, worktreePath, branch string) error {
	branch = strings.TrimSpace(branch)
	if strings.HasPrefix(branch, "-") {
		return fmt.Errorf("invalid branch name %q", branch)
	}
	worktreePath = filepath.Clean(worktreePath)
	if strings.HasPrefix(worktreePath, "-") {
		return fmt.Errorf("invalid worktree path %q", worktreePath)
	}
	args := []string{"worktree", "add"}
	if branch != "" {
		args = append(args, "-b", branch)
	}
	args = append(args, "--", worktreePath)
	_, err := runGitOutput(ctx, targetDir, args...)
	return err
}

// RemoveWorktree deletes a linked Git worktree. When force is false, it refuses
// if the worktree contains uncommitted or untracked changes.
func RemoveWorktree(ctx context.Context, targetDir, worktreePath string, force bool) error {
	worktreePath = filepath.Clean(worktreePath)
	if strings.HasPrefix(worktreePath, "-") {
		return fmt.Errorf("invalid worktree path %q", worktreePath)
	}
	if !force {
		if hasChanges, reason := HasLocalChanges(ctx, worktreePath); hasChanges {
			return fmt.Errorf("refusing to remove worktree: %s", reason)
		}
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, "--", worktreePath)
	_, err := runGitOutput(ctx, targetDir, args...)
	return err
}

// PruneWorktrees cleans up stale worktree administrative metadata in targetDir.
func PruneWorktrees(ctx context.Context, targetDir string) error {
	_, err := runGitOutput(ctx, targetDir, "worktree", "prune")
	return err
}

// WorktreeInfo describes one linked or main Git worktree.
type WorktreeInfo struct {
	Path   string `json:"path"`
	Commit string `json:"commit,omitempty"`
	Branch string `json:"branch,omitempty"`
	Bare   bool   `json:"bare,omitempty"`
}

// ListWorktrees lists all active linked and main Git worktrees for targetDir.
func ListWorktrees(ctx context.Context, targetDir string) ([]WorktreeInfo, error) {
	out, err := runGitOutput(ctx, targetDir, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	return parseWorktreeListPorcelain(out), nil
}

func parseWorktreeListPorcelain(out string) []WorktreeInfo {
	var list []WorktreeInfo
	var current *WorktreeInfo

	lines := strings.Split(out, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if current != nil {
				list = append(list, *current)
				current = nil
			}
			continue
		}
		if strings.HasPrefix(line, "worktree ") {
			current = &WorktreeInfo{Path: strings.TrimPrefix(line, "worktree ")}
			continue
		}
		switch {
		case strings.HasPrefix(line, "HEAD "):
			current.Commit = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimPrefix(line, "branch ")
			current.Branch = strings.TrimPrefix(ref, "refs/heads/")
		case line == "bare":
			current.Bare = true
		case line == "detached":
			current.Branch = "detached"
		}
	}
	if current != nil {
		list = append(list, *current)
	}
	return list
}
