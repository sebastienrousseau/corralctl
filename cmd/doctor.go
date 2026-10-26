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
	StagedDeletions []string              `json:"staged_deletions,omitempty"`
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
		if gitutil.IsDeletionStaging(repo.Path) {
			report.StagedDeletions = append(report.StagedDeletions, repo.Path)
			continue
		}
		if isRepoExcluded(repo, excludePatterns) {
			report.ExcludedRepos++
			continue
		}
		auditDoctorRepo(ctx, repo, report)
	}

	// A clone staged for deletion is not a repository of the workspace;
	// it is listed on its own and never removed here, since a delete that
	// stopped midway may have stopped for a reason.
	report.TotalRepos -= len(report.StagedDeletions)
	report.TotalSize = formatHumanSize(report.TotalSizeBytes)

	rules, err := loadDoctorRules(root, doctorConfigFile)
	if err != nil {
		return nil, err
	}
	evaluateDoctorRules(report, rules, idx.Repos)

	report.Healthy = doctorIssueCount(report) == 0
	return report, nil
}

// auditDoctorRepo adds one repository to the report's breakdowns and
// runs its hygiene and worktree checks under the per-repository deadline.
func auditDoctorRepo(ctx context.Context, repo corralmcp.RepoEntry, report *DoctorReport) {
	report.ByForge[inferForge(repo.RemoteURL)]++
	report.ByLanguage[orUnknown(repo.Language)]++
	report.ByVisibility[orUnknown(strings.ToLower(repo.Visibility))]++
	report.TotalSizeBytes += doctorDirSize(repo.Path)

	if doctorTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, doctorTimeout)
		defer cancel()
	}
	inspectRepoHygiene(ctx, repo, report)
	inspectRepoWorktrees(ctx, repo, report, doctorPruneWorktrees)
}

// orUnknown names an empty breakdown key.
func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func printDoctorText(r *DoctorReport) {
	fmt.Printf("Corral Workspace Health Audit\n")
	fmt.Printf("=============================\n")
	fmt.Printf("Workspace: %s (%d repos, %s)\n\n", r.BaseDir, r.TotalRepos, r.TotalSize)
	printDoctorCounts("Repositories by Forge:", r.ByForge)
	printDoctorCounts("Repositories by Ecosystem:", r.ByLanguage)
	printDoctorHygiene(r)
	printDoctorStaged(r)
	printDoctorWorktrees(r)
	if r.PrunedWorktrees > 0 {
		fmt.Printf("Worktree Cleanup:\n  Pruned: %d stale worktrees\n\n", r.PrunedWorktrees)
	}
	if r.RefreshedTags > 0 {
		fmt.Printf("Finder Tags:\n  Refreshed: %d repositories\n\n", r.RefreshedTags)
	}
	printDoctorCaches(r.CacheStatus)
	printDoctorRules(r)
	printDoctorStatus(r)
}

// printDoctorCounts prints one titled breakdown in key order.
func printDoctorCounts(title string, counts map[string]int) {
	fmt.Printf("%s\n", title)
	for _, k := range sortedKeys(counts) {
		fmt.Printf("  %-16s %d\n", k+":", counts[k])
	}
	fmt.Printf("\n")
}

// printDoctorIssues prints a labelled list of repository issues, if any.
func printDoctorIssues(label string, issues []DoctorRepoIssue) {
	if len(issues) == 0 {
		return
	}
	fmt.Printf("  %-9s %d repos\n", label, len(issues))
	for _, d := range issues {
		fmt.Printf("    - %s: %s\n", d.Name, d.Detail)
	}
}

func printDoctorHygiene(r *DoctorReport) {
	cleanCount := r.TotalRepos - r.ExcludedRepos - len(r.DirtyRepos) - len(r.UnpushedRepos)
	fmt.Printf("Git Hygiene:\n")
	fmt.Printf("  Clean:    %d repos\n", cleanCount)
	if r.ExcludedRepos > 0 {
		fmt.Printf("  Excluded: %d repos\n", r.ExcludedRepos)
	}
	printDoctorIssues("Dirty:", r.DirtyRepos)
	printDoctorIssues("Unpushed:", r.UnpushedRepos)
	fmt.Printf("\n")
}

func printDoctorWorktrees(r *DoctorReport) {
	if len(r.Worktrees) == 0 {
		return
	}
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

// cacheStatusText renders one cache's state for the text report.
func cacheStatusText(valid bool, size int64) string {
	if !valid {
		return "NOT CREATED"
	}
	return fmt.Sprintf("OK (%s)", formatHumanSize(size))
}

func printDoctorCaches(c DoctorCacheStatus) {
	fmt.Printf("Cache Integrity:\n")
	fmt.Printf("  Index Cache:  %s (%s)\n", cacheStatusText(c.IndexCacheValid, c.IndexCacheBytes), c.IndexCachePath)
	fmt.Printf("  Symbol Cache: %s (%s)\n\n", cacheStatusText(c.SymbolCacheValid, c.SymbolCacheBytes), c.SymbolCachePath)
}

func printDoctorRules(r *DoctorReport) {
	if !r.RulesApplied {
		return
	}
	fmt.Printf("Health Rules:\n")
	if len(r.Violations) == 0 {
		fmt.Printf("  Status: All threshold rules satisfied\n\n")
		return
	}
	fmt.Printf("  Violations: %d rule violations\n", len(r.Violations))
	for _, v := range r.Violations {
		fmt.Printf("    - %s\n", v)
	}
	fmt.Printf("\n")
}

func printDoctorStatus(r *DoctorReport) {
	if r.Healthy {
		fmt.Printf("Status: OK (all repositories clean and synced)\n")
		return
	}
	fmt.Printf("Status: WARNING (attention required for %d issues)\n", doctorIssueCount(r))
}

// doctorIssueCount is what keeps a report from being healthy.
func doctorIssueCount(r *DoctorReport) int {
	return len(r.DirtyRepos) + len(r.UnpushedRepos) + len(r.Violations) + len(r.StagedDeletions)
}

// printDoctorStaged lists directories a delete renamed aside and never
// removed. They are reported, never removed: each may be the only copy of
// what it holds.
func printDoctorStaged(r *DoctorReport) {
	if len(r.StagedDeletions) == 0 {
		return
	}
	fmt.Printf("Interrupted Deletions: %d left by a delete that did not finish\n", len(r.StagedDeletions))
	for _, p := range r.StagedDeletions {
		fmt.Printf("  - %s\n", p)
	}
	fmt.Printf("  corralctl does not remove these: inspect each, then delete it or rename it back.\n\n")
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
