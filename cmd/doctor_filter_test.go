// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gitutil "github.com/sebastienrousseau/corralctl/internal/git"
	corralmcp "github.com/sebastienrousseau/corralctl/internal/mcp"
)

func TestParseExcludePatterns(t *testing.T) {
	if got := parseExcludePatterns(""); got != nil {
		t.Fatalf("expected nil for empty input, got %v", got)
	}
	if got := parseExcludePatterns("   ,  , "); got != nil {
		t.Fatalf("expected nil for whitespace input, got %v", got)
	}
	got := parseExcludePatterns(" alpha, beta , ,gamma ")
	want := []string{"alpha", "beta", "gamma"}
	if len(got) != len(want) {
		t.Fatalf("len mismatch: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("at index %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestIsRepoExcluded(t *testing.T) {
	repo := corralmcp.RepoEntry{
		Name:    "winget-pkgs",
		Path:    "/Volumes/HD/Code/Arrange/other/winget-pkgs",
		RelPath: "Arrange/other/winget-pkgs",
	}

	if isRepoExcluded(repo, nil) {
		t.Errorf("expected false for empty patterns")
	}

	// Exact name match
	if !isRepoExcluded(repo, []string{"winget-pkgs"}) {
		t.Errorf("expected true for exact name match")
	}

	// Exact relative path match
	if !isRepoExcluded(repo, []string{"Arrange/other/winget-pkgs"}) {
		t.Errorf("expected true for exact rel path match")
	}

	// Relative path prefix match
	if !isRepoExcluded(repo, []string{"Arrange/other"}) {
		t.Errorf("expected true for rel path prefix match")
	}

	// Relative path suffix match
	if !isRepoExcluded(repo, []string{"other/winget-pkgs"}) {
		t.Errorf("expected true for rel path suffix match")
	}

	// Path substring match
	if !isRepoExcluded(repo, []string{"Arrange"}) {
		t.Errorf("expected true for path directory substring match")
	}

	// Glob pattern match
	if !isRepoExcluded(repo, []string{"winget-*"}) {
		t.Errorf("expected true for glob pattern match")
	}

	// Non-matching pattern
	if isRepoExcluded(repo, []string{"unrelated-repo", "another/path"}) {
		t.Errorf("expected false for non-matching patterns")
	}
}

func TestInspectRepoHygiene(t *testing.T) {
	oldLocal := doctorHasLocalChanges
	oldUnpub := doctorHasUnpublishedWork
	defer func() {
		doctorHasLocalChanges = oldLocal
		doctorHasUnpublishedWork = oldUnpub
	}()

	repo := corralmcp.RepoEntry{
		Name: "test-repo",
		Path: "/path/to/test-repo",
	}

	// 1. Dirty repo
	doctorHasLocalChanges = func(ctx context.Context, targetDir string) (bool, string) {
		return true, "working tree has local changes"
	}
	doctorHasUnpublishedWork = func(ctx context.Context, targetDir string) (bool, string) {
		return false, ""
	}
	report := &DoctorReport{}
	inspectRepoHygiene(context.Background(), repo, report)
	if len(report.DirtyRepos) != 1 || report.DirtyRepos[0].Detail != "working tree has local changes" {
		t.Errorf("expected 1 dirty repo issue, got %v", report.DirtyRepos)
	}
	if len(report.UnpushedRepos) != 0 {
		t.Errorf("expected 0 unpushed repos, got %v", report.UnpushedRepos)
	}

	// 2. Unpushed repo (not dirty)
	doctorHasLocalChanges = func(ctx context.Context, targetDir string) (bool, string) {
		return false, ""
	}
	doctorHasUnpublishedWork = func(ctx context.Context, targetDir string) (bool, string) {
		return true, "3 commits unpushed"
	}
	report = &DoctorReport{}
	inspectRepoHygiene(context.Background(), repo, report)
	if len(report.DirtyRepos) != 0 {
		t.Errorf("expected 0 dirty repos, got %v", report.DirtyRepos)
	}
	if len(report.UnpushedRepos) != 1 || report.UnpushedRepos[0].Detail != "3 commits unpushed" {
		t.Errorf("expected 1 unpushed repo issue, got %v", report.UnpushedRepos)
	}

	// 3. Clean repo
	doctorHasUnpublishedWork = func(ctx context.Context, targetDir string) (bool, string) {
		return false, ""
	}
	report = &DoctorReport{}
	inspectRepoHygiene(context.Background(), repo, report)
	if len(report.DirtyRepos) != 0 || len(report.UnpushedRepos) != 0 {
		t.Errorf("expected clean report, got dirty=%v, unpushed=%v", report.DirtyRepos, report.UnpushedRepos)
	}
}

func TestInspectRepoWorktrees(t *testing.T) {
	oldList := doctorListWorktrees
	oldStat := doctorStat
	oldRemove := doctorRemoveWorktreeOp
	oldPrune := doctorPruneOp
	defer func() {
		doctorListWorktrees = oldList
		doctorStat = oldStat
		doctorRemoveWorktreeOp = oldRemove
		doctorPruneOp = oldPrune
	}()

	repo := corralmcp.RepoEntry{
		Name: "test-repo",
		Path: "/path/to/test-repo",
	}

	// 1. Error listing worktrees returns early
	doctorListWorktrees = func(ctx context.Context, targetDir string) ([]gitutil.WorktreeInfo, error) {
		return nil, errors.New("worktree list failed")
	}
	report := &DoctorReport{}
	inspectRepoWorktrees(context.Background(), repo, report, false)
	if len(report.Worktrees) != 0 {
		t.Errorf("expected 0 worktrees on list error, got %v", report.Worktrees)
	}

	// 2. Bare worktree and main worktree are skipped
	doctorListWorktrees = func(ctx context.Context, targetDir string) ([]gitutil.WorktreeInfo, error) {
		return []gitutil.WorktreeInfo{
			{Path: repo.Path, Bare: false},
			{Path: "/path/to/bare", Bare: true},
		}, nil
	}
	report = &DoctorReport{}
	inspectRepoWorktrees(context.Background(), repo, report, false)
	if len(report.Worktrees) != 0 {
		t.Errorf("expected 0 worktrees for bare and main paths, got %v", report.Worktrees)
	}

	// 3. Missing worktree and ephemeral worktree with prune
	tempDir := filepath.Join(t.TempDir(), "scratchpad", "wt")
	doctorListWorktrees = func(ctx context.Context, targetDir string) ([]gitutil.WorktreeInfo, error) {
		return []gitutil.WorktreeInfo{
			{Path: "/nonexistent/worktree/path", Branch: "feat/missing", Commit: "1234567"},
			{Path: tempDir, Branch: "", Commit: "abcdef1"},
		}, nil
	}
	doctorStat = func(name string) (os.FileInfo, error) {
		if name == tempDir {
			return fakeFileInfo{name: "wt", isDir: true}, nil
		}
		return fakeFileInfo{}, errors.New("not found")
	}

	removed := false
	doctorRemoveWorktreeOp = func(ctx context.Context, repoDir, worktreePath string, force bool) error {
		removed = true
		return nil
	}
	pruned := false
	doctorPruneOp = func(ctx context.Context, repoDir string) error {
		pruned = true
		return nil
	}

	report = &DoctorReport{}
	inspectRepoWorktrees(context.Background(), repo, report, true)
	if len(report.Worktrees) != 2 {
		t.Fatalf("expected 2 worktree entries, got %d", len(report.Worktrees))
	}
	if !report.Worktrees[0].Stale || report.Worktrees[0].Reason != "path does not exist on disk" {
		t.Errorf("expected missing path reason, got %+v", report.Worktrees[0])
	}
	if !report.Worktrees[1].Stale || report.Worktrees[1].Reason != "ephemeral scratchpad location" {
		t.Errorf("expected ephemeral scratchpad reason, got %+v", report.Worktrees[1])
	}
	if !removed {
		t.Errorf("expected doctorRemoveWorktreeOp to be called")
	}
	if !pruned {
		t.Errorf("expected doctorPruneOp to be called")
	}
	if report.PrunedWorktrees != 2 {
		t.Errorf("expected 2 pruned worktrees, got %d", report.PrunedWorktrees)
	}
}

func TestDoctorExcludeFlag(t *testing.T) {
	oldScan := doctorScan
	oldLocal := doctorHasLocalChanges
	oldUnpub := doctorHasUnpublishedWork
	oldWorktrees := doctorListWorktrees
	oldDirSize := doctorDirSize
	oldExclude := doctorExclude
	oldOutput := doctorOutput
	defer func() {
		doctorScan = oldScan
		doctorHasLocalChanges = oldLocal
		doctorHasUnpublishedWork = oldUnpub
		doctorListWorktrees = oldWorktrees
		doctorDirSize = oldDirSize
		doctorExclude = oldExclude
		doctorOutput = oldOutput
	}()

	dir := t.TempDir()
	doctorScan = func(root string) (*corralmcp.Index, error) {
		return &corralmcp.Index{
			Repos: []corralmcp.RepoEntry{
				{Name: "alpha", Path: filepath.Join(dir, "alpha"), Language: "go", RemoteURL: "https://github.com/org/alpha"},
				{Name: "winget-pkgs", Path: filepath.Join(dir, "winget-pkgs"), Language: "powershell", RemoteURL: "https://github.com/microsoft/winget-pkgs"},
			},
		}, nil
	}
	doctorHasLocalChanges = func(ctx context.Context, targetDir string) (bool, string) { return false, "" }
	doctorHasUnpublishedWork = func(ctx context.Context, targetDir string) (bool, string) { return false, "" }
	doctorListWorktrees = func(ctx context.Context, targetDir string) ([]gitutil.WorktreeInfo, error) { return nil, nil }
	doctorDirSize = func(dir string) int64 { return 1024 }

	doctorExclude = "winget-pkgs"
	report, err := collectDoctorReport(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if report.TotalRepos != 2 {
		t.Errorf("expected 2 total repos, got %d", report.TotalRepos)
	}
	if report.ExcludedRepos != 1 {
		t.Errorf("expected 1 excluded repo, got %d", report.ExcludedRepos)
	}
	if report.ByForge["github.com"] != 1 {
		t.Errorf("expected 1 github repo in forge stats, got %d", report.ByForge["github.com"])
	}

	rescueStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	printDoctorText(report)
	_ = w.Close()
	os.Stdout = rescueStdout

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	out := buf.String()

	if !strings.Contains(out, "Excluded: 1 repos") {
		t.Errorf("expected 'Excluded: 1 repos' in text output, got: %s", out)
	}
	if !strings.Contains(out, "Clean:    1 repos") {
		t.Errorf("expected 'Clean:    1 repos' in text output, got: %s", out)
	}
}

func TestDoctorTimeoutFlag(t *testing.T) {
	oldScan := doctorScan
	oldLocal := doctorHasLocalChanges
	oldUnpub := doctorHasUnpublishedWork
	oldWorktrees := doctorListWorktrees
	oldDirSize := doctorDirSize
	oldTimeout := doctorTimeout
	defer func() {
		doctorScan = oldScan
		doctorHasLocalChanges = oldLocal
		doctorHasUnpublishedWork = oldUnpub
		doctorListWorktrees = oldWorktrees
		doctorDirSize = oldDirSize
		doctorTimeout = oldTimeout
	}()

	dir := t.TempDir()
	doctorScan = func(root string) (*corralmcp.Index, error) {
		return &corralmcp.Index{
			Repos: []corralmcp.RepoEntry{
				{Name: "repo1", Path: filepath.Join(dir, "repo1"), Language: "go", RemoteURL: "https://github.com/org/repo1"},
			},
		}, nil
	}
	doctorDirSize = func(dir string) int64 { return 512 }
	doctorListWorktrees = func(ctx context.Context, targetDir string) ([]gitutil.WorktreeInfo, error) { return nil, nil }
	doctorHasUnpublishedWork = func(ctx context.Context, targetDir string) (bool, string) { return false, "" }

	deadlineObserved := false
	doctorHasLocalChanges = func(ctx context.Context, targetDir string) (bool, string) {
		if _, ok := ctx.Deadline(); ok {
			deadlineObserved = true
		}
		return false, ""
	}

	// 1. With positive timeout
	doctorTimeout = 50 * time.Millisecond
	_, err := collectDoctorReport(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !deadlineObserved {
		t.Errorf("expected deadline on context when timeout > 0")
	}

	// 2. With zero timeout (disabled)
	deadlineObserved = false
	doctorTimeout = 0
	_, err = collectDoctorReport(context.Background(), dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if deadlineObserved {
		t.Errorf("expected no deadline on context when timeout is 0")
	}
}
