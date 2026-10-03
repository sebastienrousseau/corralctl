// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/git"
	corralmcp "github.com/sebastienrousseau/corralctl/internal/mcp"
	"github.com/spf13/cobra"
)

func resetWorktreeFlags() {
	worktreeRepoFilter = ""
	worktreePathFlag = ""
	worktreeForce = false
	worktreeOutput = "text"
	worktreeJSON = false
}

func TestWorktreeCommandsPreRunE(t *testing.T) {
	origOutput := worktreeOutput
	t.Cleanup(func() { worktreeOutput = origOutput })

	commands := []*cobra.Command{worktreeListCmd, worktreeCreateCmd, worktreeRemoveCmd}
	for _, cmd := range commands {
		worktreeOutput = "text"
		if err := cmd.PreRunE(cmd, nil); err != nil {
			t.Fatalf("%s: unexpected error for text: %v", cmd.Name(), err)
		}
		worktreeOutput = "json"
		if err := cmd.PreRunE(cmd, nil); err != nil {
			t.Fatalf("%s: unexpected error for json: %v", cmd.Name(), err)
		}
		worktreeOutput = ""
		if err := cmd.PreRunE(cmd, nil); err != nil {
			t.Fatalf("%s: unexpected error for empty: %v", cmd.Name(), err)
		}
		worktreeOutput = "yaml"
		if err := cmd.PreRunE(cmd, nil); err == nil {
			t.Fatalf("%s: expected error for yaml, got nil", cmd.Name())
		}
	}
}

func TestWorktreeListCommand(t *testing.T) {
	root := t.TempDir()
	origScan, origList := worktreeScan, worktreeListOp
	t.Cleanup(func() {
		worktreeScan, worktreeListOp = origScan, origList
		resetWorktreeFlags()
	})

	// 1. Scan error
	resetWorktreeFlags()
	worktreeScan = func(string) (*corralmcp.Index, error) {
		return nil, errors.New("simulated scan error")
	}
	if err := worktreeListCmd.RunE(worktreeListCmd, []string{root}); err == nil {
		t.Fatal("expected scan error, got nil")
	}

	// 2. Empty workspace
	resetWorktreeFlags()
	worktreeScan = func(string) (*corralmcp.Index, error) {
		return &corralmcp.Index{Root: root, Repos: []corralmcp.RepoEntry{}}, nil
	}
	var runErr error
	out := captureStdout(t, func() {
		runErr = worktreeListCmd.RunE(worktreeListCmd, []string{root})
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !strings.Contains(out, "No repositories found.") {
		t.Fatalf("expected No repositories found, got: %s", out)
	}

	// Empty workspace in JSON mode
	worktreeJSON = true
	out = captureStdout(t, func() {
		runErr = worktreeListCmd.RunE(worktreeListCmd, []string{root})
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	var entries []WorktreeEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil || len(entries) != 0 {
		t.Fatalf("expected empty entries array, got: %v (%s)", err, out)
	}

	// 3. Workspace with repos but list error / no worktrees
	resetWorktreeFlags()
	repoADir := filepath.Join(root, "repo-a")
	idx := &corralmcp.Index{
		Root: root,
		Repos: []corralmcp.RepoEntry{
			{Name: "repo-a", RelPath: "repo-a", Path: repoADir},
		},
	}
	worktreeScan = func(string) (*corralmcp.Index, error) { return idx, nil }
	worktreeListOp = func(ctx context.Context, dir string) ([]git.WorktreeInfo, error) {
		return nil, errors.New("git error")
	}
	out = captureStdout(t, func() {
		runErr = worktreeListCmd.RunE(worktreeListCmd, []string{root})
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !strings.Contains(out, "No worktrees found.") {
		t.Fatalf("expected No worktrees found, got: %s", out)
	}

	// 4. Populated worktrees in text and JSON mode
	resetWorktreeFlags()
	worktreeListOp = func(ctx context.Context, dir string) ([]git.WorktreeInfo, error) {
		return []git.WorktreeInfo{
			{Path: repoADir, Branch: "main", Commit: "1234567890abcdef"},
			{Path: "/path/to/bare", Bare: true},
			{Path: "/path/to/detached", Commit: "abcdef1234567890"},
		}, nil
	}

	out = captureStdout(t, func() {
		runErr = worktreeListCmd.RunE(worktreeListCmd, []string{root})
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !strings.Contains(out, "repo-a") || !strings.Contains(out, "main") || !strings.Contains(out, "(bare)") || !strings.Contains(out, "(detached)") {
		t.Fatalf("unexpected list output:\n%s", out)
	}

	// JSON mode via --output json
	resetWorktreeFlags()
	worktreeOutput = "json"
	out = captureStdout(t, func() {
		runErr = worktreeListCmd.RunE(worktreeListCmd, []string{root})
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil || len(entries) != 3 {
		t.Fatalf("failed to decode JSON entries: %v, count: %d\nOutput: %s", err, len(entries), out)
	}

	// 5. Repo filter
	resetWorktreeFlags()
	worktreeRepoFilter = "repo-a"
	out = captureStdout(t, func() {
		runErr = worktreeListCmd.RunE(worktreeListCmd, []string{root})
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !strings.Contains(out, "repo-a") {
		t.Fatalf("expected repo-a in output: %s", out)
	}

	// Non-existent repo filter error
	worktreeRepoFilter = "non-existent"
	if err := worktreeListCmd.RunE(worktreeListCmd, []string{root}); err == nil {
		t.Fatal("expected error for non-existent repo, got nil")
	}
}

func TestWorktreeCreateCommand(t *testing.T) {
	root := t.TempDir()
	origScan, origCreate, origTime := worktreeScan, worktreeCreateOp, worktreeCurrentTimeSec
	t.Cleanup(func() {
		worktreeScan, worktreeCreateOp, worktreeCurrentTimeSec = origScan, origCreate, origTime
		resetWorktreeFlags()
	})

	repoADir := filepath.Join(root, "repo-a")
	idx := &corralmcp.Index{
		Root: root,
		Repos: []corralmcp.RepoEntry{
			{Name: "repo-a", RelPath: "repo-a", Path: repoADir},
		},
	}
	worktreeScan = func(string) (*corralmcp.Index, error) { return idx, nil }
	worktreeCurrentTimeSec = func() int64 { return 1700000000 }

	// 1. Scan error
	worktreeScan = func(string) (*corralmcp.Index, error) {
		return nil, errors.New("simulated scan error")
	}
	if err := worktreeCreateCmd.RunE(worktreeCreateCmd, []string{"repo-a"}); err == nil {
		t.Fatal("expected scan error, got nil")
	}
	worktreeScan = func(string) (*corralmcp.Index, error) { return idx, nil }

	// 2. Non-existent repo error
	if err := worktreeCreateCmd.RunE(worktreeCreateCmd, []string{"ghost"}); err == nil {
		t.Fatal("expected error for ghost repo, got nil")
	}

	// 3. Successful create with branch (default path)
	var capturedDir, capturedPath, capturedBranch string
	worktreeCreateOp = func(ctx context.Context, dir, path, branch string) error {
		capturedDir, capturedPath, capturedBranch = dir, path, branch
		return nil
	}

	resetWorktreeFlags()
	var runErr error
	out := captureStdout(t, func() {
		runErr = worktreeCreateCmd.RunE(worktreeCreateCmd, []string{"repo-a", "feature/test", root})
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !strings.Contains(out, "Created worktree for repo-a") || !strings.Contains(out, "feature/test") {
		t.Fatalf("unexpected create output: %s", out)
	}
	if capturedDir != repoADir || capturedBranch != "feature/test" {
		t.Fatalf("unexpected call args: %s, %s, %s", capturedDir, capturedPath, capturedBranch)
	}

	// 4. Successful create with custom path and JSON output
	resetWorktreeFlags()
	customPath := filepath.Join(t.TempDir(), "custom-wt")
	worktreePathFlag = customPath
	worktreeOutput = "json"
	out = captureStdout(t, func() {
		runErr = worktreeCreateCmd.RunE(worktreeCreateCmd, []string{"repo-a"})
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	var createRes WorktreeCreateResult
	if err := json.Unmarshal([]byte(out), &createRes); err != nil {
		t.Fatalf("failed to decode JSON result: %v\nOutput: %s", err, out)
	}
	if createRes.Repo != "repo-a" || createRes.Path != customPath || createRes.Result != "created" {
		t.Fatalf("unexpected result: %+v", createRes)
	}

	// 5. Create error from git
	resetWorktreeFlags()
	worktreeCreateOp = func(ctx context.Context, dir, path, branch string) error {
		return errors.New("git branch already exists")
	}
	if err := worktreeCreateCmd.RunE(worktreeCreateCmd, []string{"repo-a", "existing"}); err == nil {
		t.Fatal("expected create error, got nil")
	}

	// 6. Successful create with empty branch (exercises branchSuffix == "")
	worktreeCreateOp = func(ctx context.Context, dir, path, branch string) error {
		capturedDir, capturedPath, capturedBranch = dir, path, branch
		return nil
	}
	resetWorktreeFlags()
	out = captureStdout(t, func() {
		runErr = worktreeCreateCmd.RunE(worktreeCreateCmd, []string{"repo-a", ""})
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	if !strings.Contains(out, "Created worktree") {
		t.Fatalf("expected 'Created worktree' in output, got: %s", out)
	}
	if !strings.Contains(capturedPath, "worktree-") {
		t.Fatalf("expected worktree- prefix in path, got: %s", capturedPath)
	}

	// 7. MkdirAll failure
	origMkdir := worktreeMkdirAll
	defer func() { worktreeMkdirAll = origMkdir }()
	worktreeMkdirAll = func(string, os.FileMode) error {
		return errors.New("permission denied")
	}
	resetWorktreeFlags()
	if err := worktreeCreateCmd.RunE(worktreeCreateCmd, []string{"repo-a"}); err == nil {
		t.Fatal("expected error from mkdir, got nil")
	}
	worktreeMkdirAll = origMkdir

	// 8. Default worktreeCurrentTimeSec execution
	origTimeVal := origTime()
	if origTimeVal <= 0 {
		t.Fatalf("expected positive timestamp from default time func, got %d", origTimeVal)
	}
}

func TestWorktreeRemoveCommand(t *testing.T) {
	root := t.TempDir()
	origScan, origRemove, origPrune := worktreeScan, worktreeRemoveOp, worktreePruneOp
	t.Cleanup(func() {
		worktreeScan, worktreeRemoveOp, worktreePruneOp = origScan, origRemove, origPrune
		resetWorktreeFlags()
	})

	repoADir := filepath.Join(root, "repo-a")
	idx := &corralmcp.Index{
		Root: root,
		Repos: []corralmcp.RepoEntry{
			{Name: "repo-a", RelPath: "repo-a", Path: repoADir},
		},
	}
	worktreeScan = func(string) (*corralmcp.Index, error) { return idx, nil }

	// 1. Scan error
	worktreeScan = func(string) (*corralmcp.Index, error) {
		return nil, errors.New("simulated scan error")
	}
	if err := worktreeRemoveCmd.RunE(worktreeRemoveCmd, []string{"repo-a", "/some/path"}); err == nil {
		t.Fatal("expected scan error, got nil")
	}
	worktreeScan = func(string) (*corralmcp.Index, error) { return idx, nil }

	// 2. Non-existent repo error
	if err := worktreeRemoveCmd.RunE(worktreeRemoveCmd, []string{"ghost", "/some/path"}); err == nil {
		t.Fatal("expected ghost repo error, got nil")
	}

	// 3. Remove error (e.g. uncommitted changes without force)
	worktreeRemoveOp = func(ctx context.Context, dir, path string, force bool) error {
		if !force {
			return errors.New("refusing to remove worktree: uncommitted changes")
		}
		return nil
	}
	resetWorktreeFlags()
	if err := worktreeRemoveCmd.RunE(worktreeRemoveCmd, []string{"repo-a", "/dirty/path"}); err == nil {
		t.Fatal("expected error without force, got nil")
	}

	// 4. Successful remove with force in text mode
	var pruned bool
	worktreePruneOp = func(ctx context.Context, dir string) error {
		pruned = true
		return nil
	}
	worktreeRemoveOp = func(ctx context.Context, dir, path string, force bool) error {
		return nil
	}
	resetWorktreeFlags()
	worktreeForce = true
	var runErr error
	out := captureStdout(t, func() {
		runErr = worktreeRemoveCmd.RunE(worktreeRemoveCmd, []string{"repo-a", "/clean/path"})
	})
	if runErr != nil {
		t.Fatalf("unexpected remove error: %v", runErr)
	}
	if !strings.Contains(out, "Removed worktree: /clean/path") {
		t.Fatalf("unexpected remove output: %s", out)
	}
	if !pruned {
		t.Fatal("expected worktreePruneOp to be called")
	}

	// 5. Successful remove in JSON mode
	resetWorktreeFlags()
	worktreeJSON = true
	out = captureStdout(t, func() {
		runErr = worktreeRemoveCmd.RunE(worktreeRemoveCmd, []string{"repo-a", "/clean/path"})
	})
	if runErr != nil {
		t.Fatalf("unexpected remove error in json: %v", runErr)
	}
	var removeRes WorktreeRemoveResult
	if err := json.Unmarshal([]byte(out), &removeRes); err != nil {
		t.Fatalf("failed to decode JSON result: %v\nOutput: %s", err, out)
	}
	if removeRes.Repo != "repo-a" || removeRes.Result != "removed" {
		t.Fatalf("unexpected remove result: %+v", removeRes)
	}
}
