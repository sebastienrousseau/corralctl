// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	gitutil "github.com/sebastienrousseau/corralctl/internal/git"
	corralmcp "github.com/sebastienrousseau/corralctl/internal/mcp"
	"github.com/spf13/cobra"
)

var (
	doctorOutput             string
	doctorConfigFile         string
	doctorPruneWorktrees     bool
	doctorRefreshTags        bool
	doctorExclude            string
	doctorTimeout            = 30 * time.Second
	doctorScan               = corralmcp.Scan
	doctorHasLocalChanges    = gitutil.HasLocalChanges
	doctorHasUnpublishedWork = gitutil.HasUnpublishedWork
	doctorListWorktrees      = gitutil.ListWorktrees
	doctorPruneOp            = gitutil.PruneWorktrees
	doctorRemoveWorktreeOp   = gitutil.RemoveWorktree
	doctorDirSize            = calculateDirSize
	doctorUserCacheDir       = os.UserCacheDir
	doctorUserConfigDir      = os.UserConfigDir
	doctorStat               = os.Stat
	doctorReadFile           = os.ReadFile
)

// DoctorReport contains the comprehensive workspace diagnostic audit.
type DoctorReport struct {
	BaseDir         string                `json:"base_dir"`
	TotalRepos      int                   `json:"total_repos"`
	TotalSizeBytes  int64                 `json:"total_size_bytes"`
	TotalSize       string                `json:"total_size"`
	ByForge         map[string]int        `json:"by_forge"`
	ByLanguage      map[string]int        `json:"by_language"`
	ByVisibility    map[string]int        `json:"by_visibility"`
	DirtyRepos      []DoctorRepoIssue     `json:"dirty_repos,omitempty"`
	UnpushedRepos   []DoctorRepoIssue     `json:"unpushed_repos,omitempty"`
	Worktrees       []DoctorWorktreeEntry `json:"worktrees,omitempty"`
	PrunedWorktrees int                   `json:"pruned_worktrees,omitempty"`
	RefreshedTags   int                   `json:"refreshed_tags,omitempty"`
	CacheStatus     DoctorCacheStatus     `json:"cache_status"`
	ExcludedRepos   int                   `json:"excluded_repos,omitempty"`
	RulesApplied    bool                  `json:"rules_applied,omitempty"`
	Violations      []string              `json:"violations,omitempty"`
	Healthy         bool                  `json:"healthy"`
}

// DoctorRepoIssue represents a repository with uncommitted or unpushed work.
type DoctorRepoIssue struct {
	Name   string `json:"name"`
	Path   string `json:"path"`
	Detail string `json:"detail"`
}

// DoctorWorktreeEntry represents an active linked Git worktree.
type DoctorWorktreeEntry struct {
	Repo   string `json:"repo"`
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
	Commit string `json:"commit,omitempty"`
	Stale  bool   `json:"stale,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// DoctorCacheStatus details the on-disk state of persistent caches.
type DoctorCacheStatus struct {
	IndexCachePath   string `json:"index_cache_path"`
	IndexCacheValid  bool   `json:"index_cache_valid"`
	IndexCacheBytes  int64  `json:"index_cache_size_bytes"`
	SymbolCachePath  string `json:"symbol_cache_path"`
	SymbolCacheValid bool   `json:"symbol_cache_valid"`
	SymbolCacheBytes int64  `json:"symbol_cache_size_bytes"`
}

func calculateDirSize(dir string) int64 {
	var total int64
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

func formatHumanSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

func inferForge(remoteURL string) string {
	raw := strings.TrimSpace(remoteURL)
	if raw == "" {
		return "local"
	}
	if parsed, err := url.Parse(raw); err == nil && parsed.Hostname() != "" {
		return strings.ToLower(parsed.Hostname())
	}
	if colon := strings.Index(raw, ":"); colon >= 0 {
		hostPart := raw[:colon]
		if at := strings.LastIndex(hostPart, "@"); at >= 0 {
			hostPart = hostPart[at+1:]
		}
		if hostPart != "" {
			return strings.ToLower(hostPart)
		}
	}
	return "other"
}

func inspectCaches() DoctorCacheStatus {
	cacheRoot, err := doctorUserCacheDir()
	if err != nil {
		cacheRoot = filepath.Join(os.TempDir(), "corral-cache")
	}
	indexDir := filepath.Join(cacheRoot, "corral", "index")
	symbolDir := filepath.Join(cacheRoot, "corral", "symbols")

	status := DoctorCacheStatus{
		IndexCachePath:  indexDir,
		SymbolCachePath: symbolDir,
	}

	if info, err := doctorStat(indexDir); err == nil && info.IsDir() {
		status.IndexCacheValid = true
		status.IndexCacheBytes = doctorDirSize(indexDir)
	}

	if info, err := doctorStat(symbolDir); err == nil && info.IsDir() {
		status.SymbolCacheValid = true
		status.SymbolCacheBytes = doctorDirSize(symbolDir)
	}

	return status
}

func collectDoctorReport(ctx context.Context, root string) (*DoctorReport, error) {
	idx, err := doctorScan(root)
	if err != nil {
		return nil, fmt.Errorf("scanning workspace: %w", err)
	}

	report := &DoctorReport{
		BaseDir:      root,
		TotalRepos:   len(idx.Repos),
		ByForge:      make(map[string]int),
		ByLanguage:   make(map[string]int),
		ByVisibility: make(map[string]int),
		CacheStatus:  inspectCaches(),
	}

	if doctorRefreshTags {
		report.RefreshedTags = refreshWorkspaceFinderTags(idx.Repos)
	}

	excludePatterns := parseExcludePatterns(doctorExclude)

	for _, repo := range idx.Repos {
		if isRepoExcluded(repo, excludePatterns) {
			report.ExcludedRepos++
			continue
		}

		forge := inferForge(repo.RemoteURL)
		report.ByForge[forge]++

		lang := repo.Language
		if lang == "" {
			lang = "unknown"
		}
		report.ByLanguage[lang]++

		vis := strings.ToLower(repo.Visibility)
		if vis == "" {
			vis = "unknown"
		}
		report.ByVisibility[vis]++

		report.TotalSizeBytes += doctorDirSize(repo.Path)

		repoCtx := ctx
		var cancel context.CancelFunc
		if doctorTimeout > 0 {
			repoCtx, cancel = context.WithTimeout(ctx, doctorTimeout)
		}

		inspectRepoHygiene(repoCtx, repo, report)
		inspectRepoWorktrees(repoCtx, repo, report, doctorPruneWorktrees)

		if cancel != nil {
			cancel()
		}
	}

	report.TotalSize = formatHumanSize(report.TotalSizeBytes)

	rules, err := loadDoctorRules(root, doctorConfigFile)
	if err != nil {
		return nil, err
	}
	evaluateDoctorRules(report, rules, idx.Repos)

	report.Healthy = len(report.DirtyRepos) == 0 && len(report.UnpushedRepos) == 0 && len(report.Violations) == 0
	return report, nil
}

func printDoctorText(r *DoctorReport) {
	fmt.Printf("Corral Workspace Health Audit\n")
	fmt.Printf("=============================\n")
	fmt.Printf("Workspace: %s (%d repos, %s)\n\n", r.BaseDir, r.TotalRepos, r.TotalSize)

	fmt.Printf("Repositories by Forge:\n")
	for _, forge := range sortedKeys(r.ByForge) {
		fmt.Printf("  %-16s %d\n", forge+":", r.ByForge[forge])
	}
	fmt.Printf("\n")

	fmt.Printf("Repositories by Ecosystem:\n")
	for _, lang := range sortedKeys(r.ByLanguage) {
		fmt.Printf("  %-16s %d\n", lang+":", r.ByLanguage[lang])
	}
	fmt.Printf("\n")

	cleanCount := r.TotalRepos - r.ExcludedRepos - len(r.DirtyRepos) - len(r.UnpushedRepos)
	fmt.Printf("Git Hygiene:\n")
	fmt.Printf("  Clean:    %d repos\n", cleanCount)
	if r.ExcludedRepos > 0 {
		fmt.Printf("  Excluded: %d repos\n", r.ExcludedRepos)
	}
	if len(r.DirtyRepos) > 0 {
		fmt.Printf("  Dirty:    %d repos\n", len(r.DirtyRepos))
		for _, d := range r.DirtyRepos {
			fmt.Printf("    - %s: %s\n", d.Name, d.Detail)
		}
	}
	if len(r.UnpushedRepos) > 0 {
		fmt.Printf("  Unpushed: %d repos\n", len(r.UnpushedRepos))
		for _, u := range r.UnpushedRepos {
			fmt.Printf("    - %s: %s\n", u.Name, u.Detail)
		}
	}
	fmt.Printf("\n")

	if len(r.Worktrees) > 0 {
		fmt.Printf("Active Worktrees: %d linked worktrees\n", len(r.Worktrees))
		for _, wt := range r.Worktrees {
			branch := wt.Branch
			if branch == "" {
				branch = wt.Commit
			}
			staleSuffix := ""
			if wt.Stale {
				staleSuffix = fmt.Sprintf(" [STALE: %s]", wt.Reason)
			}
			fmt.Printf("  - %s: %s [%s]%s\n", wt.Repo, wt.Path, branch, staleSuffix)
		}
		fmt.Printf("\n")
	}

	if r.PrunedWorktrees > 0 {
		fmt.Printf("Worktree Cleanup:\n  Pruned: %d stale worktrees\n\n", r.PrunedWorktrees)
	}

	if r.RefreshedTags > 0 {
		fmt.Printf("Finder Tags:\n  Refreshed: %d repositories\n\n", r.RefreshedTags)
	}

	fmt.Printf("Cache Integrity:\n")
	idxStatus := "NOT CREATED"
	if r.CacheStatus.IndexCacheValid {
		idxStatus = fmt.Sprintf("OK (%s)", formatHumanSize(r.CacheStatus.IndexCacheBytes))
	}
	symStatus := "NOT CREATED"
	if r.CacheStatus.SymbolCacheValid {
		symStatus = fmt.Sprintf("OK (%s)", formatHumanSize(r.CacheStatus.SymbolCacheBytes))
	}
	fmt.Printf("  Index Cache:  %s (%s)\n", idxStatus, r.CacheStatus.IndexCachePath)
	fmt.Printf("  Symbol Cache: %s (%s)\n\n", symStatus, r.CacheStatus.SymbolCachePath)

	if r.RulesApplied {
		fmt.Printf("Health Rules:\n")
		if len(r.Violations) == 0 {
			fmt.Printf("  Status: All threshold rules satisfied\n\n")
		} else {
			fmt.Printf("  Violations: %d rule violations\n", len(r.Violations))
			for _, v := range r.Violations {
				fmt.Printf("    - %s\n", v)
			}
			fmt.Printf("\n")
		}
	}

	if r.Healthy {
		fmt.Printf("Status: OK (all repositories clean and synced)\n")
	} else {
		issues := len(r.DirtyRepos) + len(r.UnpushedRepos) + len(r.Violations)
		fmt.Printf("Status: WARNING (attention required for %d issues)\n", issues)
	}
}

func sortedKeys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

var doctorCmd = &cobra.Command{
	Use:   "doctor [base_dir]",
	Short: "Audit workspace health, git hygiene, worktrees, disk usage, and cache integrity",
	Args:  cobra.MaximumNArgs(1),
	PreRunE: func(cmd *cobra.Command, args []string) error {
		if doctorOutput != "text" && doctorOutput != "json" {
			return errors.New("--output must be text or json")
		}
		if doctorTimeout < 0 {
			return errors.New("--timeout must be >= 0")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		root := resolvedBaseDir(args)
		report, err := collectDoctorReport(cmdContext(cmd), root)
		if err != nil {
			return err
		}
		if doctorOutput == "json" {
			return writeJSON(os.Stdout, report)
		}
		printDoctorText(report)
		return nil
	},
}

func init() {
	doctorCmd.Flags().StringVar(&doctorOutput, "output", "text", "output format: text or json")
	doctorCmd.Flags().StringVar(&doctorConfigFile, "config", "", "path to custom .corral.json configuration file")
	doctorCmd.Flags().BoolVar(&doctorPruneWorktrees, "prune-worktrees", false, "automatically prune stale and temporary linked worktrees")
	doctorCmd.Flags().BoolVar(&doctorRefreshTags, "refresh-tags", false, "refresh macOS Finder tags and folder color labels for all workspace repositories")
	doctorCmd.Flags().StringVar(&doctorExclude, "exclude", "", "comma-separated repository names or path patterns to exclude from audit")
	doctorCmd.Flags().DurationVar(&doctorTimeout, "timeout", 30*time.Second, "deadline per repository check (0 to disable)")
	rootCmd.AddCommand(doctorCmd)
}

