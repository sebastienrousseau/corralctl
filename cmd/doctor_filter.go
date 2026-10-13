// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"context"
	"path/filepath"
	"strings"

	corralmcp "github.com/sebastienrousseau/corralctl/internal/mcp"
)

// parseExcludePatterns parses a comma-separated list of exclusion patterns,
// returning normalized, non-empty pattern strings.
func parseExcludePatterns(csv string) []string {
	if strings.TrimSpace(csv) == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	var result []string
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

// isRepoExcluded reports whether repo matches any pattern in patterns by name,
// relative path, directory substring, or glob expression.
func isRepoExcluded(repo corralmcp.RepoEntry, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}
	slashPath := filepath.ToSlash(repo.Path)
	slashRel := filepath.ToSlash(repo.RelPath)
	for _, pattern := range patterns {
		if repo.Name == pattern {
			return true
		}
		if slashRel != "" && (slashRel == pattern || strings.HasPrefix(slashRel, pattern+"/") || strings.HasSuffix(slashRel, "/"+pattern)) {
			return true
		}
		if strings.Contains(slashPath, "/"+pattern+"/") || strings.HasSuffix(slashPath, "/"+pattern) {
			return true
		}
		if matched, err := filepath.Match(pattern, repo.Name); err == nil && matched {
			return true
		}
	}
	return false
}

// inspectRepoHygiene audits local working-tree changes and unpublished work for repo,
// appending discovered issues to report.
func inspectRepoHygiene(ctx context.Context, repo corralmcp.RepoEntry, report *DoctorReport) {
	dirty, detail := doctorHasLocalChanges(ctx, repo.Path)
	if dirty {
		report.DirtyRepos = append(report.DirtyRepos, DoctorRepoIssue{
			Name:   repo.Name,
			Path:   repo.Path,
			Detail: detail,
		})
	}

	unpushed, unpushedDetail := doctorHasUnpublishedWork(ctx, repo.Path)
	if unpushed && !dirty {
		report.UnpushedRepos = append(report.UnpushedRepos, DoctorRepoIssue{
			Name:   repo.Name,
			Path:   repo.Path,
			Detail: unpushedDetail,
		})
	}
}

// inspectRepoWorktrees inspects linked worktrees for repo, recording active and stale
// worktrees in report, and optionally pruning stale worktrees when prune is true.
func inspectRepoWorktrees(ctx context.Context, repo corralmcp.RepoEntry, report *DoctorReport, prune bool) {
	wtList, err := doctorListWorktrees(ctx, repo.Path)
	if err != nil {
		return
	}
	repoPruned := false
	for _, wt := range wtList {
		if wt.Bare || wt.Path == repo.Path {
			continue
		}
		entry := DoctorWorktreeEntry{
			Repo:   repo.Name,
			Path:   wt.Path,
			Branch: wt.Branch,
			Commit: wt.Commit,
		}
		_, statErr := doctorStat(wt.Path)
		isMissing := statErr != nil
		isTemp := isTemporaryPath(wt.Path)
		if isMissing || isTemp {
			entry.Stale = true
			if isMissing {
				entry.Reason = "path does not exist on disk"
			} else {
				entry.Reason = "ephemeral scratchpad location"
			}
			if prune {
				if isTemp && !isMissing {
					_ = doctorRemoveWorktreeOp(ctx, repo.Path, wt.Path, true)
				}
				repoPruned = true
				report.PrunedWorktrees++
			}
		}
		report.Worktrees = append(report.Worktrees, entry)
	}
	if repoPruned && prune {
		_ = doctorPruneOp(ctx, repo.Path)
	}
}
