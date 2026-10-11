// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package git

import (
	"context"
	"fmt"
	"strings"
)

// BranchInfo describes a local or remote Git branch.
type BranchInfo struct {
	Name    string `json:"name"`
	Current bool   `json:"current"`
	Commit  string `json:"commit,omitempty"`
	Remote  string `json:"remote,omitempty"`
}

// ListBranches lists branches in targetDir. If all is true, remote branches are included.
func ListBranches(ctx context.Context, targetDir string, all bool) ([]BranchInfo, error) {
	args := []string{"branch", "--list", "--format=%(HEAD) %(refname:short) %(objectname:short) %(upstream:short)"}
	if all {
		args = append(args, "-a")
	}
	out, err := runGitOutput(ctx, targetDir, args...)
	if err != nil {
		return nil, err
	}
	return parseBranchList(out), nil
}

func parseBranchList(out string) []BranchInfo {
	var list []BranchInfo
	lines := strings.Split(out, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		var isCurrent bool
		idx := 0
		if parts[0] == "*" {
			isCurrent = true
			idx = 1
		}
		if idx >= len(parts) {
			continue
		}
		info := BranchInfo{
			Current: isCurrent,
			Name:    parts[idx],
		}
		if idx+1 < len(parts) {
			info.Commit = parts[idx+1]
		}
		if idx+2 < len(parts) {
			info.Remote = parts[idx+2]
		}
		list = append(list, info)
	}
	return list
}

// CreateBranch creates a new branch in targetDir starting from startPoint (or HEAD if empty).
func CreateBranch(ctx context.Context, targetDir, branch, startPoint string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("invalid branch name %q", branch)
	}
	startPoint = strings.TrimSpace(startPoint)
	if strings.HasPrefix(startPoint, "-") {
		return fmt.Errorf("invalid start point %q", startPoint)
	}
	args := []string{"branch"}
	if startPoint != "" {
		args = append(args, "--", branch, startPoint)
	} else {
		args = append(args, "--", branch)
	}
	_, err := runGitOutput(ctx, targetDir, args...)
	return err
}

// SwitchBranch switches to an existing branch in targetDir.
func SwitchBranch(ctx context.Context, targetDir, branch string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" || strings.HasPrefix(branch, "-") {
		return fmt.Errorf("invalid branch name %q", branch)
	}
	_, err := runGitOutput(ctx, targetDir, "switch", "--", branch)
	return err
}
