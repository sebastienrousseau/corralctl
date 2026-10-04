// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	gitutil "github.com/sebastienrousseau/corralctl/internal/git"
	corralmcp "github.com/sebastienrousseau/corralctl/internal/mcp"
	"github.com/spf13/cobra"
)

var (
	branchRepoFilter       string
	branchLanguageFilter   string
	branchVisibilityFilter string
	branchAll              bool
	branchStartPoint       string
	branchDryRun           bool
	branchOutput           string
	branchJSON             bool

	branchScan     = corralmcp.Scan
	branchListOp   = gitutil.ListBranches
	branchCreateOp = gitutil.CreateBranch
	branchSwitchOp = gitutil.SwitchBranch
)

// RepoBranchList describes the branches found in a single repository.
type RepoBranchList struct {
	Repo     string               `json:"repo"`
	Branches []gitutil.BranchInfo `json:"branches"`
}

// BranchOpResult describes the result of a branch operation on a single repository.
type BranchOpResult struct {
	Repo    string `json:"repo"`
	Branch  string `json:"branch"`
	Action  string `json:"action"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// BranchBatchResult summarizes the results of a branch operation across repositories.
type BranchBatchResult struct {
	TotalRepos   int              `json:"total_repos"`
	SuccessCount int              `json:"success_count"`
	FailureCount int              `json:"failure_count"`
	DryRun       bool             `json:"dry_run"`
	Results      []BranchOpResult `json:"results"`
}

var branchCmd = &cobra.Command{
	Use:   "branch",
	Short: "Manage Git branches across workspace repositories",
	Long:  "List, create, and switch Git branches across multiple repositories with filtering.",
}

var branchListCmd = &cobra.Command{
	Use:   "list [base_dir]",
	Short: "List branches across workspace repositories",
	Long:  "List local and remote Git branches across workspace repositories matching criteria.",
	Args:  cobra.MaximumNArgs(1),
	PreRunE: func(cmd *cobra.Command, args []string) error {
		if branchOutput != "" && branchOutput != "text" && branchOutput != "json" {
			return errors.New("--output must be text or json")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		root := resolvedBaseDir(args)
		return runBranchList(cmdContext(cmd), root)
	},
}

var branchCreateCmd = &cobra.Command{
	Use:   "create <branch-name> [base_dir]",
	Short: "Create a new branch across workspace repositories",
	Long:  "Create a new Git branch across workspace repositories matching criteria.",
	Args:  cobra.RangeArgs(1, 2),
	PreRunE: func(cmd *cobra.Command, args []string) error {
		if branchOutput != "" && branchOutput != "text" && branchOutput != "json" {
			return errors.New("--output must be text or json")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		branchName := args[0]
		rootArgs := []string{}
		if len(args) > 1 {
			rootArgs = []string{args[1]}
		}
		root := resolvedBaseDir(rootArgs)
		return runBranchCreate(cmdContext(cmd), root, branchName)
	},
}

var branchSwitchCmd = &cobra.Command{
	Use:     "switch <branch-name> [base_dir]",
	Aliases: []string{"checkout"},
	Short:   "Switch to a branch across workspace repositories",
	Long:    "Switch HEAD to an existing Git branch across workspace repositories matching criteria.",
	Args:    cobra.RangeArgs(1, 2),
	PreRunE: func(cmd *cobra.Command, args []string) error {
		if branchOutput != "" && branchOutput != "text" && branchOutput != "json" {
			return errors.New("--output must be text or json")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		branchName := args[0]
		rootArgs := []string{}
		if len(args) > 1 {
			rootArgs = []string{args[1]}
		}
		root := resolvedBaseDir(rootArgs)
		return runBranchSwitch(cmdContext(cmd), root, branchName)
	},
}

func filterBranchTargets(idx *corralmcp.Index) ([]corralmcp.RepoEntry, error) {
	targets := idx.Repos
	if branchRepoFilter != "" {
		match, err := idx.Find(branchRepoFilter)
		if err != nil {
			return nil, fmt.Errorf("repository %q: %w", branchRepoFilter, err)
		}
		targets = []corralmcp.RepoEntry{*match}
	}

	var filtered []corralmcp.RepoEntry
	for _, repo := range targets {
		if branchLanguageFilter != "" && !strings.EqualFold(repo.Language, branchLanguageFilter) {
			continue
		}
		if branchVisibilityFilter != "" && !strings.EqualFold(repo.Visibility, branchVisibilityFilter) {
			continue
		}
		filtered = append(filtered, repo)
	}
	return filtered, nil
}

func runBranchList(ctx context.Context, root string) error {
	idx, err := branchScan(root)
	if err != nil {
		return err
	}

	targets, err := filterBranchTargets(idx)
	if err != nil {
		return err
	}

	results := make([]RepoBranchList, 0, len(targets))
	for _, repo := range targets {
		branches, listErr := branchListOp(ctx, repo.Path, branchAll)
		if listErr != nil {
			continue
		}
		results = append(results, RepoBranchList{
			Repo:     repo.Name,
			Branches: branches,
		})
	}

	if branchJSON || branchOutput == "json" {
		return writeJSON(os.Stdout, results)
	}

	if len(targets) == 0 {
		fmt.Println("No repositories found.")
		return nil
	}

	var totalBranches int
	for _, r := range results {
		totalBranches += len(r.Branches)
	}
	if totalBranches == 0 {
		fmt.Println("No branches found.")
		return nil
	}

	for _, r := range results {
		fmt.Printf("=== %s ===\n", r.Repo)
		for _, b := range r.Branches {
			prefix := "  "
			if b.Current {
				prefix = "* "
			}
			details := b.Commit
			if len(details) > 8 {
				details = details[:8]
			}
			if b.Remote != "" {
				if details != "" {
					details += " -> " + b.Remote
				} else {
					details = b.Remote
				}
			}
			if details != "" {
				fmt.Printf("%s%-30s (%s)\n", prefix, b.Name, details)
			} else {
				fmt.Printf("%s%s\n", prefix, b.Name)
			}
		}
	}
	return nil
}

func runBranchCreate(ctx context.Context, root, branchName string) error {
	idx, err := branchScan(root)
	if err != nil {
		return err
	}

	targets, err := filterBranchTargets(idx)
	if err != nil {
		return err
	}

	batch := BranchBatchResult{
		TotalRepos: len(targets),
		DryRun:     branchDryRun,
		Results:    make([]BranchOpResult, 0, len(targets)),
	}

	for _, repo := range targets {
		if branchDryRun {
			batch.SuccessCount++
			batch.Results = append(batch.Results, BranchOpResult{
				Repo:   repo.Name,
				Branch: branchName,
				Action: "create",
				Status: "planned",
			})
			continue
		}

		err := branchCreateOp(ctx, repo.Path, branchName, branchStartPoint)
		if err != nil {
			batch.FailureCount++
			batch.Results = append(batch.Results, BranchOpResult{
				Repo:    repo.Name,
				Branch:  branchName,
				Action:  "create",
				Status:  "failed",
				Message: err.Error(),
			})
		} else {
			batch.SuccessCount++
			batch.Results = append(batch.Results, BranchOpResult{
				Repo:   repo.Name,
				Branch: branchName,
				Action: "create",
				Status: "created",
			})
		}
	}

	if branchJSON || branchOutput == "json" {
		return writeJSON(os.Stdout, batch)
	}

	if len(targets) == 0 {
		fmt.Println("No repositories found.")
		return nil
	}

	actionStr := "Created"
	if branchDryRun {
		actionStr = "Would create"
	}

	for _, res := range batch.Results {
		if res.Status == "failed" {
			fmt.Printf("FAILED %s in %s: %s\n", res.Branch, res.Repo, res.Message)
		} else {
			fmt.Printf("%s branch %s in %s\n", actionStr, res.Branch, res.Repo)
		}
	}
	fmt.Printf("\nSummary: %d total, %d succeeded, %d failed.\n",
		batch.TotalRepos, batch.SuccessCount, batch.FailureCount)
	return nil
}

func runBranchSwitch(ctx context.Context, root, branchName string) error {
	idx, err := branchScan(root)
	if err != nil {
		return err
	}

	targets, err := filterBranchTargets(idx)
	if err != nil {
		return err
	}

	batch := BranchBatchResult{
		TotalRepos: len(targets),
		DryRun:     branchDryRun,
		Results:    make([]BranchOpResult, 0, len(targets)),
	}

	for _, repo := range targets {
		if branchDryRun {
			batch.SuccessCount++
			batch.Results = append(batch.Results, BranchOpResult{
				Repo:   repo.Name,
				Branch: branchName,
				Action: "switch",
				Status: "planned",
			})
			continue
		}

		err := branchSwitchOp(ctx, repo.Path, branchName)
		if err != nil {
			batch.FailureCount++
			batch.Results = append(batch.Results, BranchOpResult{
				Repo:    repo.Name,
				Branch:  branchName,
				Action:  "switch",
				Status:  "failed",
				Message: err.Error(),
			})
		} else {
			batch.SuccessCount++
			batch.Results = append(batch.Results, BranchOpResult{
				Repo:   repo.Name,
				Branch: branchName,
				Action: "switch",
				Status: "switched",
			})
		}
	}

	if branchJSON || branchOutput == "json" {
		return writeJSON(os.Stdout, batch)
	}

	if len(targets) == 0 {
		fmt.Println("No repositories found.")
		return nil
	}

	actionStr := "Switched to"
	if branchDryRun {
		actionStr = "Would switch to"
	}

	for _, res := range batch.Results {
		if res.Status == "failed" {
			fmt.Printf("FAILED %s in %s: %s\n", res.Branch, res.Repo, res.Message)
		} else {
			fmt.Printf("%s branch %s in %s\n", actionStr, res.Branch, res.Repo)
		}
	}
	fmt.Printf("\nSummary: %d total, %d succeeded, %d failed.\n",
		batch.TotalRepos, batch.SuccessCount, batch.FailureCount)
	return nil
}

func init() {
	branchListCmd.Flags().StringVar(&branchRepoFilter, "repo", "", "Filter to a specific repository")
	branchListCmd.Flags().StringVar(&branchLanguageFilter, "language", "", "Filter repositories by language")
	branchListCmd.Flags().StringVar(&branchVisibilityFilter, "visibility", "", "Filter repositories by visibility")
	branchListCmd.Flags().BoolVarP(&branchAll, "all", "a", false, "List remote-tracking branches as well as local branches")
	branchListCmd.Flags().StringVar(&branchOutput, "output", "text", "Output format (text, json)")
	branchListCmd.Flags().BoolVar(&branchJSON, "json", false, "Output as JSON (shorthand for --output json)")

	branchCreateCmd.Flags().StringVar(&branchRepoFilter, "repo", "", "Filter to a specific repository")
	branchCreateCmd.Flags().StringVar(&branchLanguageFilter, "language", "", "Filter repositories by language")
	branchCreateCmd.Flags().StringVar(&branchVisibilityFilter, "visibility", "", "Filter repositories by visibility")
	branchCreateCmd.Flags().StringVar(&branchStartPoint, "start-point", "", "Starting point for new branch")
	branchCreateCmd.Flags().BoolVar(&branchDryRun, "dry-run", false, "Simulate branch creation without modifying repositories")
	branchCreateCmd.Flags().StringVar(&branchOutput, "output", "text", "Output format (text, json)")
	branchCreateCmd.Flags().BoolVar(&branchJSON, "json", false, "Output as JSON (shorthand for --output json)")

	branchSwitchCmd.Flags().StringVar(&branchRepoFilter, "repo", "", "Filter to a specific repository")
	branchSwitchCmd.Flags().StringVar(&branchLanguageFilter, "language", "", "Filter repositories by language")
	branchSwitchCmd.Flags().StringVar(&branchVisibilityFilter, "visibility", "", "Filter repositories by visibility")
	branchSwitchCmd.Flags().BoolVar(&branchDryRun, "dry-run", false, "Simulate branch switch without modifying repositories")
	branchSwitchCmd.Flags().StringVar(&branchOutput, "output", "text", "Output format (text, json)")
	branchSwitchCmd.Flags().BoolVar(&branchJSON, "json", false, "Output as JSON (shorthand for --output json)")

	branchCmd.AddCommand(branchListCmd, branchCreateCmd, branchSwitchCmd)
	rootCmd.AddCommand(branchCmd)
}
