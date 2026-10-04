// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"bytes"
	"context"
	"errors"
	"testing"

	gitutil "github.com/sebastienrousseau/corralctl/internal/git"
	corralmcp "github.com/sebastienrousseau/corralctl/internal/mcp"
	"github.com/spf13/cobra"
)

func TestBranchValidation(t *testing.T) {
	for _, cmd := range []*cobra.Command{branchListCmd, branchCreateCmd, branchSwitchCmd} {
		branchOutput = "invalid"
		if err := cmd.PreRunE(cmd, nil); err == nil {
			t.Errorf("expected error for invalid output on %s", cmd.Use)
		}
		branchOutput = "json"
		if err := cmd.PreRunE(cmd, nil); err != nil {
			t.Errorf("unexpected error for json output on %s: %v", cmd.Use, err)
		}
		branchOutput = "text"
		if err := cmd.PreRunE(cmd, nil); err != nil {
			t.Errorf("unexpected error for text output on %s: %v", cmd.Use, err)
		}
		branchOutput = ""
		if err := cmd.PreRunE(cmd, nil); err != nil {
			t.Errorf("unexpected error for empty output on %s: %v", cmd.Use, err)
		}
	}
}

func resetBranchFlags() {
	branchRepoFilter = ""
	branchLanguageFilter = ""
	branchVisibilityFilter = ""
	branchAll = false
	branchStartPoint = ""
	branchDryRun = false
	branchOutput = ""
	branchJSON = false
}

func mockIndex() *corralmcp.Index {
	return &corralmcp.Index{
		Repos: []corralmcp.RepoEntry{
			{Name: "repo-go", Path: "/path/repo-go", Language: "Go", Visibility: "public"},
			{Name: "repo-rust", Path: "/path/repo-rust", Language: "Rust", Visibility: "private"},
			{Name: "repo-empty", Path: "/path/repo-empty", Language: "Python", Visibility: "internal"},
		},
	}
}

func TestFilterBranchTargets(t *testing.T) {
	defer resetBranchFlags()

	idx := mockIndex()

	// No filters
	resetBranchFlags()
	targets, err := filterBranchTargets(idx)
	if err != nil || len(targets) != 3 {
		t.Fatalf("expected 3 targets, got %d (err: %v)", len(targets), err)
	}

	// Specific repo found
	resetBranchFlags()
	branchRepoFilter = "repo-go"
	targets, err = filterBranchTargets(idx)
	if err != nil || len(targets) != 1 || targets[0].Name != "repo-go" {
		t.Fatalf("expected repo-go, got %+v (err: %v)", targets, err)
	}

	// Specific repo not found
	resetBranchFlags()
	branchRepoFilter = "non-existent"
	_, err = filterBranchTargets(idx)
	if err == nil {
		t.Fatal("expected error for non-existent repo filter")
	}

	// Language filter
	resetBranchFlags()
	branchLanguageFilter = "rust"
	targets, err = filterBranchTargets(idx)
	if err != nil || len(targets) != 1 || targets[0].Name != "repo-rust" {
		t.Fatalf("expected repo-rust, got %+v (err: %v)", targets, err)
	}

	// Visibility filter
	resetBranchFlags()
	branchVisibilityFilter = "public"
	targets, err = filterBranchTargets(idx)
	if err != nil || len(targets) != 1 || targets[0].Name != "repo-go" {
		t.Fatalf("expected repo-go for public, got %+v (err: %v)", targets, err)
	}
}

func TestRunBranchList(t *testing.T) {
	defer resetBranchFlags()
	oldScan := branchScan
	oldList := branchListOp
	defer func() {
		branchScan = oldScan
		branchListOp = oldList
	}()

	ctx := context.Background()

	// Scan error
	branchScan = func(string) (*corralmcp.Index, error) {
		return nil, errors.New("scan failure")
	}
	if err := runBranchList(ctx, "/test"); err == nil {
		t.Fatal("expected scan error")
	}

	// Filter error
	branchScan = func(string) (*corralmcp.Index, error) {
		return mockIndex(), nil
	}
	branchRepoFilter = "missing-repo"
	if err := runBranchList(ctx, "/test"); err == nil {
		t.Fatal("expected filter error")
	}
	resetBranchFlags()

	// Empty targets
	branchScan = func(string) (*corralmcp.Index, error) {
		return &corralmcp.Index{}, nil
	}
	if err := runBranchList(ctx, "/test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Target with list error and no branches
	branchScan = func(string) (*corralmcp.Index, error) {
		return mockIndex(), nil
	}
	branchListOp = func(context.Context, string, bool) ([]gitutil.BranchInfo, error) {
		return nil, errors.New("git error")
	}
	if err := runBranchList(ctx, "/test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Success with JSON output
	branchListOp = func(ctx context.Context, target string, all bool) ([]gitutil.BranchInfo, error) {
		return []gitutil.BranchInfo{
			{Name: "main", Current: true, Commit: "1234567890abcdef", Remote: "origin/main"},
			{Name: "feat", Current: false, Commit: "short", Remote: ""},
			{Name: "bare-branch", Current: false},
		}, nil
	}
	branchJSON = true
	if err := runBranchList(ctx, "/test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Success with text output
	branchJSON = false
	branchOutput = "text"
	if err := runBranchList(ctx, "/test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Success with branch having remote but no commit
	branchListOp = func(ctx context.Context, target string, all bool) ([]gitutil.BranchInfo, error) {
		return []gitutil.BranchInfo{
			{Name: "orphan", Current: false, Remote: "origin/orphan"},
		}, nil
	}
	if err := runBranchList(ctx, "/test"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunBranchCreate(t *testing.T) {
	defer resetBranchFlags()
	oldScan := branchScan
	oldCreate := branchCreateOp
	defer func() {
		branchScan = oldScan
		branchCreateOp = oldCreate
	}()

	ctx := context.Background()

	// Scan error
	branchScan = func(string) (*corralmcp.Index, error) {
		return nil, errors.New("scan failure")
	}
	if err := runBranchCreate(ctx, "/test", "new-branch"); err == nil {
		t.Fatal("expected scan error")
	}

	// Filter error
	branchScan = func(string) (*corralmcp.Index, error) {
		return mockIndex(), nil
	}
	branchRepoFilter = "missing-repo"
	if err := runBranchCreate(ctx, "/test", "new-branch"); err == nil {
		t.Fatal("expected filter error")
	}
	resetBranchFlags()

	// Dry-run mode
	branchDryRun = true
	branchOutput = "text"
	if err := runBranchCreate(ctx, "/test", "new-branch"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Dry-run JSON
	branchOutput = "json"
	if err := runBranchCreate(ctx, "/test", "new-branch"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resetBranchFlags()

	// Empty targets
	branchScan = func(string) (*corralmcp.Index, error) {
		return &corralmcp.Index{}, nil
	}
	if err := runBranchCreate(ctx, "/test", "new-branch"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Real execution: one success, one failure
	branchScan = func(string) (*corralmcp.Index, error) {
		return &corralmcp.Index{
			Repos: []corralmcp.RepoEntry{
				{Name: "repo1", Path: "/path/1"},
				{Name: "repo2", Path: "/path/2"},
			},
		}, nil
	}
	branchCreateOp = func(ctx context.Context, targetDir, branch, startPoint string) error {
		if targetDir == "/path/2" {
			return errors.New("git branch failed")
		}
		return nil
	}
	branchOutput = "text"
	if err := runBranchCreate(ctx, "/test", "new-branch"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunBranchSwitch(t *testing.T) {
	defer resetBranchFlags()
	oldScan := branchScan
	oldSwitch := branchSwitchOp
	defer func() {
		branchScan = oldScan
		branchSwitchOp = oldSwitch
	}()

	ctx := context.Background()

	// Scan error
	branchScan = func(string) (*corralmcp.Index, error) {
		return nil, errors.New("scan failure")
	}
	if err := runBranchSwitch(ctx, "/test", "main"); err == nil {
		t.Fatal("expected scan error")
	}

	// Filter error
	branchScan = func(string) (*corralmcp.Index, error) {
		return mockIndex(), nil
	}
	branchRepoFilter = "missing-repo"
	if err := runBranchSwitch(ctx, "/test", "main"); err == nil {
		t.Fatal("expected filter error")
	}
	resetBranchFlags()

	// Dry-run mode
	branchDryRun = true
	branchOutput = "text"
	if err := runBranchSwitch(ctx, "/test", "main"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Dry-run JSON
	branchOutput = "json"
	if err := runBranchSwitch(ctx, "/test", "main"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resetBranchFlags()

	// Empty targets
	branchScan = func(string) (*corralmcp.Index, error) {
		return &corralmcp.Index{}, nil
	}
	if err := runBranchSwitch(ctx, "/test", "main"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Real execution: one success, one failure
	branchScan = func(string) (*corralmcp.Index, error) {
		return &corralmcp.Index{
			Repos: []corralmcp.RepoEntry{
				{Name: "repo1", Path: "/path/1"},
				{Name: "repo2", Path: "/path/2"},
			},
		}, nil
	}
	branchSwitchOp = func(ctx context.Context, targetDir, branch string) error {
		if targetDir == "/path/2" {
			return errors.New("git switch failed")
		}
		return nil
	}
	branchOutput = "text"
	if err := runBranchSwitch(ctx, "/test", "main"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBranchCobraCommands(t *testing.T) {
	defer resetBranchFlags()
	oldScan := branchScan
	oldList := branchListOp
	oldCreate := branchCreateOp
	oldSwitch := branchSwitchOp
	defer func() {
		branchScan = oldScan
		branchListOp = oldList
		branchCreateOp = oldCreate
		branchSwitchOp = oldSwitch
	}()

	testDir := t.TempDir()

	branchScan = func(string) (*corralmcp.Index, error) {
		return mockIndex(), nil
	}
	branchListOp = func(context.Context, string, bool) ([]gitutil.BranchInfo, error) {
		return []gitutil.BranchInfo{{Name: "main", Current: true}}, nil
	}
	branchCreateOp = func(context.Context, string, string, string) error {
		return nil
	}
	branchSwitchOp = func(context.Context, string, string) error {
		return nil
	}

	// Execute list
	rootCmd.SetArgs([]string{"branch", "list", testDir})
	var outBuf bytes.Buffer
	rootCmd.SetOut(&outBuf)
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("branch list failed: %v", err)
	}

	// Execute create with base_dir
	rootCmd.SetArgs([]string{"branch", "create", "test-br", testDir})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("branch create failed: %v", err)
	}

	// Execute switch with base_dir
	rootCmd.SetArgs([]string{"branch", "switch", "main", testDir})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("branch switch failed: %v", err)
	}
}
