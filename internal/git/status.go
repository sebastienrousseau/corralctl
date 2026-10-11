// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package git

import (
	"context"
	"strconv"
	"strings"
	"time"
)

// RepoStatus captures the local operational and git hygiene posture of a repository clone.
type RepoStatus struct {
	IsDirty       bool          `json:"is_dirty"`
	HasUnpushed   bool          `json:"has_unpushed"`
	UnpushedCount int           `json:"unpushed_count"`
	HasStash      bool          `json:"has_stash"`
	HasWorktree   bool          `json:"has_worktree"`
	IsDiverged    bool          `json:"is_diverged"`
	HasConflict   bool          `json:"has_conflict"`
	LastCommitAge time.Duration `json:"last_commit_age"`
	IsDormant     bool          `json:"is_dormant"`
}

// InspectRepoStatus audits the local repository state at targetDir to determine
// in-flight modifications, divergence, active worktrees, and hygiene posture.
func InspectRepoStatus(ctx context.Context, targetDir string) (RepoStatus, error) {
	var st RepoStatus
	if _, err := Dir(targetDir); err != nil {
		return st, err
	}

	// 1. Uncommitted working-tree changes
	if dirty, _ := HasLocalChanges(ctx, targetDir); dirty {
		st.IsDirty = true
	}

	// 2. Unresolved merge/rebase conflicts
	if out, err := runGitOutput(ctx, targetDir, "diff", "--name-only", "--diff-filter=U"); err == nil {
		if strings.TrimSpace(out) != "" {
			st.HasConflict = true
		}
	}

	// 3. Stashes
	if _, err := runGitOutput(ctx, targetDir, "rev-parse", "--verify", "--quiet", "refs/stash"); err == nil {
		st.HasStash = true
	}

	// 4. Linked worktrees (more than 1 entry in worktree list)
	if wts, err := ListWorktrees(ctx, targetDir); err == nil && len(wts) > 1 {
		st.HasWorktree = true
	}

	// 5. Divergence & unpushed commits relative to tracking branch
	if out, err := runGitOutput(ctx, targetDir, "rev-list", "--left-right", "--count", "@{u}...HEAD"); err == nil {
		parts := strings.Fields(out)
		if len(parts) >= 2 {
			behind, _ := strconv.Atoi(parts[0])
			ahead, _ := strconv.Atoi(parts[1])
			if ahead > 0 {
				st.HasUnpushed = true
				st.UnpushedCount = ahead
			}
			if behind > 0 && ahead > 0 {
				st.IsDiverged = true
			}
		}
	} else {
		// Fall back to local-only commits across all branches if no tracking branch is configured
		if out, err := runGitOutput(ctx, targetDir, "rev-list", "--count", "--all", "--not", "--remotes"); err == nil {
			if cnt, _ := strconv.Atoi(strings.TrimSpace(out)); cnt > 0 {
				st.HasUnpushed = true
				st.UnpushedCount = cnt
			}
		}
	}

	// 6. Last commit timestamp
	if out, err := runGitOutput(ctx, targetDir, "log", "-1", "--format=%at"); err == nil {
		if sec, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64); err == nil && sec > 0 {
			commitTime := time.Unix(sec, 0)
			st.LastCommitAge = time.Since(commitTime)
			if st.LastCommitAge > 60*24*time.Hour {
				st.IsDormant = true
			}
		}
	}

	return st, nil
}
