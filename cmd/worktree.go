// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/git"
	corralmcp "github.com/sebastienrousseau/corralctl/internal/mcp"
	"github.com/spf13/cobra"
)

var (
	worktreeRepoFilter    string
	worktreePathFlag      string
	worktreeForce         bool
	worktreeOutput        string
	worktreeJSON          bool
	worktreePruneDryRun   bool
	worktreePruneTempOnly bool

	worktreeScan           = corralmcp.Scan
	worktreeCreateOp       = git.CreateWorktree
	worktreeRemoveOp       = git.RemoveWorktree
	worktreePruneOp        = git.PruneWorktrees
	worktreeListOp         = git.ListWorktrees
	worktreeMkdirAll       = os.MkdirAll
	worktreeStat           = os.Stat
	worktreeCurrentTimeSec = func() int64 { return time.Now().Unix() }
)

// WorktreeEntry describes one worktree in a repository for listing.
type WorktreeEntry struct {
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
	Commit string `json:"commit,omitempty"`
	Bare   bool   `json:"bare,omitempty"`
}

// WorktreeCreateResult describes the result of creating a worktree.
type WorktreeCreateResult struct {
	Repo   string `json:"repo"`
	Branch string `json:"branch,omitempty"`
	Path   string `json:"path"`
	Result string `json:"result"`
}

// WorktreeRemoveResult describes the result of removing a worktree.
type WorktreeRemoveResult struct {
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	Result string `json:"result"`
}

// WorktreePruneEntry describes one pruned or candidate worktree.
type WorktreePruneEntry struct {
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
	Reason string `json:"reason"`
	Pruned bool   `json:"pruned"`
}

// WorktreePruneResult summarizes worktrees cleaned up across repositories.
type WorktreePruneResult struct {
	TotalChecked int                  `json:"total_checked"`
	TotalPruned  int                  `json:"total_pruned"`
	DryRun       bool                 `json:"dry_run"`
	Worktrees    []WorktreePruneEntry `json:"worktrees"`
}

var worktreeCmd = &cobra.Command{
	Use:   "worktree",
	Short: "Manage linked Git worktrees across workspace repositories",
	Long:  "Create, list, and remove isolated Git worktrees for workspace repositories.",
}

var worktreeListCmd = &cobra.Command{
	Use:   "list [base_dir]",
	Short: "List linked Git worktrees across workspace repositories",
	Args:  cobra.MaximumNArgs(1),
	PreRunE: func(cmd *cobra.Command, args []string) error {
		if worktreeOutput != "" && worktreeOutput != "text" && worktreeOutput != "json" {
			return errors.New("--output must be text or json")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		root := resolvedBaseDir(args)
		return runWorktreeList(cmdContext(cmd), root)
	},
}

func runWorktreeList(ctx context.Context, root string) error {
	idx, err := worktreeScan(root)
	if err != nil {
		return err
	}

	targets := idx.Repos
	if worktreeRepoFilter != "" {
		match, findErr := idx.Find(worktreeRepoFilter)
		if findErr != nil {
			return fmt.Errorf("repository %q: %w", worktreeRepoFilter, findErr)
		}
		targets = []corralmcp.RepoEntry{*match}
	}

	var allEntries []WorktreeEntry
	for _, repo := range targets {
		wts, listErr := worktreeListOp(ctx, repo.Path)
		if listErr != nil {
			continue
		}
		for _, wt := range wts {
			allEntries = append(allEntries, WorktreeEntry{
				Repo:   repo.Name,
				Path:   wt.Path,
				Branch: wt.Branch,
				Commit: wt.Commit,
				Bare:   wt.Bare,
			})
		}
	}

	if worktreeJSON || worktreeOutput == "json" {
		if allEntries == nil {
			allEntries = []WorktreeEntry{}
		}
		return writeJSON(os.Stdout, allEntries)
	}

	if len(targets) == 0 {
		fmt.Println("No repositories found.")
		return nil
	}

	if len(allEntries) == 0 {
		fmt.Println("No worktrees found.")
		return nil
	}

	for _, entry := range allEntries {
		branchDisplay := entry.Branch
		if branchDisplay == "" {
			if entry.Bare {
				branchDisplay = "(bare)"
			} else {
				branchDisplay = "(detached)"
			}
		}
		commitDisplay := entry.Commit
		if len(commitDisplay) > 8 {
			commitDisplay = commitDisplay[:8]
		}
		fmt.Printf("%-20s %-20s %-10s %s\n", entry.Repo, branchDisplay, commitDisplay, entry.Path)
	}
	return nil
}

var worktreeCreateCmd = &cobra.Command{
	Use:   "create <repo> [branch] [base_dir]",
	Short: "Create an isolated linked Git worktree for a repository",
	Args:  cobra.RangeArgs(1, 3),
	PreRunE: func(cmd *cobra.Command, args []string) error {
		if worktreeOutput != "" && worktreeOutput != "text" && worktreeOutput != "json" {
			return errors.New("--output must be text or json")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		repoName := args[0]
		branch := ""
		if len(args) > 1 {
			branch = args[1]
		}
		rootArgs := []string{}
		if len(args) > 2 {
			rootArgs = []string{args[2]}
		}
		root := resolvedBaseDir(rootArgs)
		return runWorktreeCreate(cmdContext(cmd), root, repoName, branch)
	},
}

func runWorktreeCreate(ctx context.Context, root, repoName, branch string) error {
	idx, err := worktreeScan(root)
	if err != nil {
		return err
	}
	repo, err := idx.Find(repoName)
	if err != nil {
		return fmt.Errorf("repository %q: %w", repoName, err)
	}

	targetPath := worktreePathFlag
	if targetPath == "" {
		branchSuffix := branch
		if branchSuffix == "" {
			branchSuffix = "worktree"
		}
		safeBranch := strings.ReplaceAll(branchSuffix, "/", "-")
		base := filepath.Join(repo.Path, ".git", "corral-worktrees")
		if err := worktreeMkdirAll(base, 0o700); err != nil {
			return fmt.Errorf("create worktree directory: %w", err)
		}
		targetPath = filepath.Join(base, fmt.Sprintf("%s-%d", safeBranch, worktreeCurrentTimeSec()))
	}

	if err := worktreeCreateOp(ctx, repo.Path, targetPath, branch); err != nil {
		return fmt.Errorf("create worktree failed: %w", err)
	}

	res := WorktreeCreateResult{
		Repo:   repo.Name,
		Branch: branch,
		Path:   targetPath,
		Result: "created",
	}

	if worktreeJSON || worktreeOutput == "json" {
		return writeJSON(os.Stdout, res)
	}

	fmt.Printf("Created worktree for %s:\n", repo.Name)
	fmt.Printf("  Branch: %s\n", branch)
	fmt.Printf("  Path:   %s\n", targetPath)
	return nil
}

var worktreeRemoveCmd = &cobra.Command{
	Use:   "remove <repo> <path>",
	Short: "Remove a linked Git worktree",
	Args:  cobra.ExactArgs(2),
	PreRunE: func(cmd *cobra.Command, args []string) error {
		if worktreeOutput != "" && worktreeOutput != "text" && worktreeOutput != "json" {
			return errors.New("--output must be text or json")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		repoName := args[0]
		wtPath := args[1]
		root := resolvedBaseDir(nil)
		return runWorktreeRemove(cmdContext(cmd), root, repoName, wtPath)
	},
}

func runWorktreeRemove(ctx context.Context, root, repoName, wtPath string) error {
	idx, err := worktreeScan(root)
	if err != nil {
		return err
	}
	repo, err := idx.Find(repoName)
	if err != nil {
		return fmt.Errorf("repository %q: %w", repoName, err)
	}

	if err := worktreeRemoveOp(ctx, repo.Path, wtPath, worktreeForce); err != nil {
		return fmt.Errorf("remove worktree failed: %w", err)
	}
	_ = worktreePruneOp(ctx, repo.Path)

	res := WorktreeRemoveResult{
		Repo:   repo.Name,
		Path:   wtPath,
		Result: "removed",
	}

	if worktreeJSON || worktreeOutput == "json" {
		return writeJSON(os.Stdout, res)
	}

	fmt.Printf("Removed worktree: %s\n", wtPath)
	return nil
}

// isTemporaryPath reports whether a worktree path resides in an ephemeral or scratchpad location.
func isTemporaryPath(p string) bool {
	slashPath := filepath.ToSlash(filepath.Clean(p))
	tmp := filepath.ToSlash(os.TempDir())
	if strings.HasPrefix(slashPath, tmp) || strings.HasPrefix(slashPath, "/tmp/") || strings.HasPrefix(slashPath, "/private/tmp/") {
		return true
	}
	if strings.Contains(slashPath, ".git/corral-worktrees") || strings.Contains(slashPath, "scratchpad") {
		return true
	}
	return false
}

var worktreePruneCmd = &cobra.Command{
	Use:   "prune [base_dir]",
	Short: "Prune stale, orphaned, and temporary linked Git worktrees",
	Long:  "Detect and clean up linked Git worktrees whose paths no longer exist on disk or reside in ephemeral scratchpad locations.",
	Args:  cobra.MaximumNArgs(1),
	PreRunE: func(cmd *cobra.Command, args []string) error {
		if worktreeOutput != "" && worktreeOutput != "text" && worktreeOutput != "json" {
			return errors.New("--output must be text or json")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		root := resolvedBaseDir(args)
		return runWorktreePrune(cmdContext(cmd), root)
	},
}

func runWorktreePrune(ctx context.Context, root string) error {
	idx, err := worktreeScan(root)
	if err != nil {
		return err
	}

	targets := idx.Repos
	if worktreeRepoFilter != "" {
		match, findErr := idx.Find(worktreeRepoFilter)
		if findErr != nil {
			return fmt.Errorf("repository %q: %w", worktreeRepoFilter, findErr)
		}
		targets = []corralmcp.RepoEntry{*match}
	}

	res := WorktreePruneResult{
		DryRun:    worktreePruneDryRun,
		Worktrees: []WorktreePruneEntry{},
	}

	for _, repo := range targets {
		wts, listErr := worktreeListOp(ctx, repo.Path)
		if listErr != nil {
			continue
		}
		repoNeedsPrune := false
		for _, wt := range wts {
			if wt.Bare || wt.Path == repo.Path {
				continue
			}
			res.TotalChecked++

			_, statErr := worktreeStat(wt.Path)
			isMissing := statErr != nil
			isTemp := isTemporaryPath(wt.Path)

			var reason string
			switch {
			case isMissing:
				reason = "path does not exist on disk"
			case isTemp:
				reason = "ephemeral scratchpad location"
			case !worktreePruneTempOnly:
				reason = "linked worktree pruned"
			default:
				continue
			}

			entry := WorktreePruneEntry{
				Repo:   repo.Name,
				Path:   wt.Path,
				Branch: wt.Branch,
				Reason: reason,
			}

			if worktreePruneDryRun {
				entry.Pruned = false
			} else {
				if isTemp && !isMissing {
					_ = worktreeRemoveOp(ctx, repo.Path, wt.Path, worktreeForce)
				}
				repoNeedsPrune = true
				entry.Pruned = true
				res.TotalPruned++
			}
			res.Worktrees = append(res.Worktrees, entry)
		}
		if repoNeedsPrune && !worktreePruneDryRun {
			_ = worktreePruneOp(ctx, repo.Path)
		}
	}

	if worktreeJSON || worktreeOutput == "json" {
		return writeJSON(os.Stdout, res)
	}

	if len(targets) == 0 {
		fmt.Println("No repositories found.")
		return nil
	}

	if len(res.Worktrees) == 0 {
		fmt.Println("No stale or temporary worktrees found.")
		return nil
	}

	action := "Pruned"
	if worktreePruneDryRun {
		action = "Would prune"
	}

	for _, wt := range res.Worktrees {
		fmt.Printf("%s %s (%s) in %s: %s\n", action, wt.Path, wt.Branch, wt.Repo, wt.Reason)
	}
	fmt.Printf("\nSummary: %d checked, %d %s across %d repositories.\n",
		res.TotalChecked, len(res.Worktrees), strings.ToLower(action), len(targets))
	return nil
}

func init() {
	worktreeListCmd.Flags().StringVar(&worktreeRepoFilter, "repo", "", "Filter worktrees to a specific repository")
	worktreeListCmd.Flags().StringVar(&worktreeOutput, "output", "text", "Output format (text, json)")
	worktreeListCmd.Flags().BoolVar(&worktreeJSON, "json", false, "Output as JSON (shorthand for --output json)")

	worktreeCreateCmd.Flags().StringVar(&worktreePathFlag, "path", "", "Custom target path for worktree")
	worktreeCreateCmd.Flags().StringVar(&worktreeOutput, "output", "text", "Output format (text, json)")
	worktreeCreateCmd.Flags().BoolVar(&worktreeJSON, "json", false, "Output as JSON (shorthand for --output json)")

	worktreeRemoveCmd.Flags().BoolVarP(&worktreeForce, "force", "f", false, "Force removal even if untracked/uncommitted changes exist")
	worktreeRemoveCmd.Flags().StringVar(&worktreeOutput, "output", "text", "Output format (text, json)")
	worktreeRemoveCmd.Flags().BoolVar(&worktreeJSON, "json", false, "Output as JSON (shorthand for --output json)")

	worktreePruneCmd.Flags().StringVar(&worktreeRepoFilter, "repo", "", "Filter to a specific repository")
	worktreePruneCmd.Flags().BoolVar(&worktreePruneDryRun, "dry-run", false, "Simulate pruning without removing worktrees")
	worktreePruneCmd.Flags().BoolVarP(&worktreeForce, "force", "f", false, "Force removal of temporary worktrees even if modified")
	worktreePruneCmd.Flags().BoolVar(&worktreePruneTempOnly, "temp-only", false, "Only prune temporary scratchpad worktrees")
	worktreePruneCmd.Flags().StringVar(&worktreeOutput, "output", "text", "Output format (text, json)")
	worktreePruneCmd.Flags().BoolVar(&worktreeJSON, "json", false, "Output as JSON (shorthand for --output json)")

	worktreeCmd.AddCommand(worktreeListCmd, worktreeCreateCmd, worktreeRemoveCmd, worktreePruneCmd)
	rootCmd.AddCommand(worktreeCmd)
}
