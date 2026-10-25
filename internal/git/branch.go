// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package git

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
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

// HasBranch reports whether targetDir has a local branch with this name.
func HasBranch(ctx context.Context, targetDir, branch string) (bool, error) {
	if strings.TrimSpace(branch) == "" {
		return false, fmt.Errorf("invalid branch name %q", branch)
	}
	// The refs/heads/ prefix keeps a name git could read as an option, or
	// as a tag or remote ref, from meaning anything but a local branch.
	_, err := runGitOutput(ctx, targetDir, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// defaultBranchSources are where DefaultBranch looks, in order: the
// default the clone recorded for origin, then the branch HEAD is on.
var defaultBranchSources = []struct{ ref, prefix string }{
	{"refs/remotes/origin/HEAD", "origin/"},
	{"HEAD", ""},
}

// DefaultBranch returns the branch targetDir treats as its default: the
// one origin/HEAD names, else the one HEAD is on. A candidate counts only
// when it exists as a local branch, because that is what a mirror pushes.
func DefaultBranch(ctx context.Context, targetDir string) (string, error) {
	for _, src := range defaultBranchSources {
		out, err := runGitOutput(ctx, targetDir, "symbolic-ref", "--quiet", "--short", src.ref)
		name := strings.TrimPrefix(strings.TrimSpace(out), src.prefix)
		if err != nil || name == "" {
			continue
		}
		ok, err := HasBranch(ctx, targetDir, name)
		if err != nil {
			return "", err
		}
		if ok {
			return name, nil
		}
	}
	return "", errors.New("no local default branch: neither origin/HEAD nor HEAD names a local branch")
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

// ResolvedRemoteURL is the URL git actually uses for the named remote,
// after its url.<base>.insteadOf rewrites: where .git/config says
// "work:me/repo", this is what git expands it to. It reads configuration
// only and makes no network request.
func ResolvedRemoteURL(ctx context.Context, targetDir, remote string) (string, error) {
	out, err := runGitOutput(ctx, targetDir, "ls-remote", "--get-url", remote)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
