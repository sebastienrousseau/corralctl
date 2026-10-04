// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gitutil "github.com/sebastienrousseau/corralctl/internal/git"
	corralmcp "github.com/sebastienrousseau/corralctl/internal/mcp"
)

type fakeFileInfo struct {
	name  string
	size  int64
	isDir bool
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return f.size }
func (f fakeFileInfo) Mode() fs.FileMode  { return 0755 }
func (f fakeFileInfo) ModTime() time.Time { return time.Now() }
func (f fakeFileInfo) IsDir() bool        { return f.isDir }
func (f fakeFileInfo) Sys() any           { return nil }

func TestDoctorFlagValidation(t *testing.T) {
	oldOutput := doctorOutput
	defer func() { doctorOutput = oldOutput }()

	doctorOutput = "yaml"
	err := doctorCmd.PreRunE(doctorCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--output must be text or json") {
		t.Fatalf("expected error on invalid output flag, got %v", err)
	}

	doctorOutput = "text"
	if err := doctorCmd.PreRunE(doctorCmd, nil); err != nil {
		t.Fatalf("unexpected error on text: %v", err)
	}

	doctorOutput = "json"
	if err := doctorCmd.PreRunE(doctorCmd, nil); err != nil {
		t.Fatalf("unexpected error on json: %v", err)
	}
}

func TestDoctorScanError(t *testing.T) {
	oldScan := doctorScan
	defer func() { doctorScan = oldScan }()

	doctorScan = func(root string) (*corralmcp.Index, error) {
		return nil, errors.New("simulated scan error")
	}

	err := doctorCmd.RunE(doctorCmd, []string{t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "simulated scan error") {
		t.Fatalf("expected scan error to propagate, got %v", err)
	}
}

func TestDoctorCollectAndPrintReport(t *testing.T) {
	oldScan := doctorScan
	oldLocal := doctorHasLocalChanges
	oldUnpub := doctorHasUnpublishedWork
	oldWorktrees := doctorListWorktrees
	oldDirSize := doctorDirSize
	oldCacheDir := doctorUserCacheDir
	oldStat := doctorStat
	oldOutput := doctorOutput

	defer func() {
		doctorScan = oldScan
		doctorHasLocalChanges = oldLocal
		doctorHasUnpublishedWork = oldUnpub
		doctorListWorktrees = oldWorktrees
		doctorDirSize = oldDirSize
		doctorUserCacheDir = oldCacheDir
		doctorStat = oldStat
		doctorOutput = oldOutput
	}()

	dir := t.TempDir()
	repo1Path := filepath.Join(dir, "Public", "Go", "alpha")
	repo2Path := filepath.Join(dir, "Private", "Python", "beta")
	repo3Path := filepath.Join(dir, "Forks", "gamma")

	doctorScan = func(root string) (*corralmcp.Index, error) {
		return &corralmcp.Index{
			Repos: []corralmcp.RepoEntry{
				{
					Name:       "alpha",
					Path:       repo1Path,
					RemoteURL:  "https://github.com/org/alpha.git",
					Language:   "Go",
					Visibility: "Public",
				},
				{
					Name:       "beta",
					Path:       repo2Path,
					RemoteURL:  "git@gitlab.com:org/beta.git",
					Language:   "Python",
					Visibility: "Private",
				},
				{
					Name:       "gamma",
					Path:       repo3Path,
					RemoteURL:  "",
					Language:   "",
					Visibility: "",
				},
			},
		}, nil
	}

	doctorHasLocalChanges = func(ctx context.Context, targetDir string) (bool, string) {
		if strings.Contains(targetDir, "alpha") {
			return true, "working tree has local changes"
		}
		return false, ""
	}

	doctorHasUnpublishedWork = func(ctx context.Context, targetDir string) (bool, string) {
		if strings.Contains(targetDir, "beta") {
			return true, "1 commits reachable only from local refs"
		}
		return false, ""
	}

	doctorListWorktrees = func(ctx context.Context, targetDir string) ([]gitutil.WorktreeInfo, error) {
		if strings.Contains(targetDir, "alpha") {
			return []gitutil.WorktreeInfo{
				{Path: targetDir, Commit: "main"},
				{Path: filepath.Join(targetDir, ".git", "corral-worktrees", "wt-1"), Branch: "feature-wt", Commit: "abc1234"},
				{Path: filepath.Join(targetDir, ".git", "corral-worktrees", "wt-2"), Branch: "", Commit: "def5678"},
			}, nil
		}
		return nil, errors.New("no worktrees")
	}

	doctorDirSize = func(dir string) int64 {
		return 500 * 1024 * 1024 // 500 MB
	}

	doctorUserCacheDir = func() (string, error) {
		return filepath.Join(dir, "cache"), nil
	}

	doctorStat = func(name string) (fs.FileInfo, error) {
		if strings.Contains(name, "index") || strings.Contains(name, "symbols") {
			return fakeFileInfo{name: "cache", size: 1024, isDir: true}, nil
		}
		return nil, os.ErrNotExist
	}

	// 1. Text Output
	doctorOutput = "text"
	rescueStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := doctorCmd.RunE(doctorCmd, []string{dir})
	_ = w.Close()
	os.Stdout = rescueStdout

	if err != nil {
		t.Fatalf("unexpected error on text run: %v", err)
	}

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	outText := buf.String()

	if !strings.Contains(outText, "Corral Workspace Health Audit") {
		t.Errorf("missing header in output: %s", outText)
	}
	if !strings.Contains(outText, "github.com:") || !strings.Contains(outText, "gitlab.com:") {
		t.Errorf("expected forges in output: %s", outText)
	}
	if !strings.Contains(outText, "Active Worktrees: 2 linked worktrees") {
		t.Errorf("expected worktrees section in output: %s", outText)
	}
	if !strings.Contains(outText, "Status: WARNING") {
		t.Errorf("expected WARNING status: %s", outText)
	}

	// 2. JSON Output
	doctorOutput = "json"
	r, w, _ = os.Pipe()
	os.Stdout = w

	err = doctorCmd.RunE(doctorCmd, []string{dir})
	_ = w.Close()
	os.Stdout = rescueStdout

	if err != nil {
		t.Fatalf("unexpected error on json run: %v", err)
	}

	buf.Reset()
	_, _ = io.Copy(&buf, r)
	jsonText := buf.String()

	if !strings.Contains(jsonText, `"healthy": false`) {
		t.Errorf("expected healthy: false in json output: %s", jsonText)
	}
}

func TestDoctorHealthyWorkspace(t *testing.T) {
	oldScan := doctorScan
	oldLocal := doctorHasLocalChanges
	oldUnpub := doctorHasUnpublishedWork
	oldWorktrees := doctorListWorktrees
	oldDirSize := doctorDirSize
	oldCacheDir := doctorUserCacheDir
	oldStat := doctorStat
	oldOutput := doctorOutput

	defer func() {
		doctorScan = oldScan
		doctorHasLocalChanges = oldLocal
		doctorHasUnpublishedWork = oldUnpub
		doctorListWorktrees = oldWorktrees
		doctorDirSize = oldDirSize
		doctorUserCacheDir = oldCacheDir
		doctorStat = oldStat
		doctorOutput = oldOutput
	}()

	dir := t.TempDir()
	doctorScan = func(root string) (*corralmcp.Index, error) {
		return &corralmcp.Index{
			Repos: []corralmcp.RepoEntry{
				{
					Name:       "clean-repo",
					Path:       filepath.Join(dir, "clean-repo"),
					RemoteURL:  "https://github.com/owner/clean-repo.git",
					Language:   "Go",
					Visibility: "Public",
				},
			},
		}, nil
	}

	doctorHasLocalChanges = func(ctx context.Context, targetDir string) (bool, string) {
		return false, ""
	}
	doctorHasUnpublishedWork = func(ctx context.Context, targetDir string) (bool, string) {
		return false, ""
	}
	doctorListWorktrees = func(ctx context.Context, targetDir string) ([]gitutil.WorktreeInfo, error) {
		return nil, nil
	}
	doctorDirSize = func(dir string) int64 { return 1024 }
	doctorUserCacheDir = func() (string, error) { return "", errors.New("no cache dir") }
	doctorStat = func(name string) (fs.FileInfo, error) { return nil, os.ErrNotExist }

	doctorOutput = "text"
	rescueStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := doctorCmd.RunE(doctorCmd, []string{dir})
	_ = w.Close()
	os.Stdout = rescueStdout

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	outText := buf.String()

	if !strings.Contains(outText, "Status: OK (all repositories clean and synced)") {
		t.Errorf("expected OK status, got: %s", outText)
	}
}

func TestDoctorHelpers(t *testing.T) {
	// 1. formatHumanSize
	if got := formatHumanSize(500); got != "500 B" {
		t.Errorf("formatHumanSize(500) = %q, want 500 B", got)
	}
	if got := formatHumanSize(1536); got != "1.5 KB" {
		t.Errorf("formatHumanSize(1536) = %q, want 1.5 KB", got)
	}
	if got := formatHumanSize(2 * 1024 * 1024); got != "2.0 MB" {
		t.Errorf("formatHumanSize(2MB) = %q, want 2.0 MB", got)
	}

	// 2. inferForge
	if got := inferForge(""); got != "local" {
		t.Errorf("inferForge(\"\") = %q, want local", got)
	}
	if got := inferForge("https://github.com/org/repo"); got != "github.com" {
		t.Errorf("inferForge(github url) = %q, want github.com", got)
	}
	if got := inferForge("git@gitlab.com:org/repo.git"); got != "gitlab.com" {
		t.Errorf("inferForge(scp url) = %q, want gitlab.com", got)
	}
	if got := inferForge("git://bitbucket.org/org/repo"); got != "bitbucket.org" {
		t.Errorf("inferForge(git proto) = %q, want bitbucket.org", got)
	}
	if got := inferForge("ftp://user@host/path"); got != "host" {
		t.Errorf("inferForge(ftp) = %q, want host", got)
	}
	if got := inferForge("not-a-valid-url-at-all"); got != "other" {
		t.Errorf("inferForge(other) = %q, want other", got)
	}

	// 3. calculateDirSize on real temp directory
	tmp := t.TempDir()
	f1 := filepath.Join(tmp, "a.txt")
	_ = os.WriteFile(f1, []byte("hello"), 0600)
	if size := calculateDirSize(tmp); size != 5 {
		t.Errorf("calculateDirSize(tmp) = %d, want 5", size)
	}
	if size := calculateDirSize("/path/does/not/exist"); size != 0 {
		t.Errorf("calculateDirSize(missing) = %d, want 0", size)
	}
}

func TestDoctorWorktreePruningAndStale(t *testing.T) {
	oldScan := doctorScan
	oldList := doctorListWorktrees
	oldStat := doctorStat
	oldPrune := doctorPruneOp
	oldRemove := doctorRemoveWorktreeOp
	oldOutput := doctorOutput
	oldPruneFlag := doctorPruneWorktrees

	defer func() {
		doctorScan = oldScan
		doctorListWorktrees = oldList
		doctorStat = oldStat
		doctorPruneOp = oldPrune
		doctorRemoveWorktreeOp = oldRemove
		doctorOutput = oldOutput
		doctorPruneWorktrees = oldPruneFlag
	}()

	dir := t.TempDir()
	doctorScan = func(root string) (*corralmcp.Index, error) {
		return &corralmcp.Index{
			Repos: []corralmcp.RepoEntry{
				{Name: "alpha", Path: filepath.Join(dir, "alpha")},
			},
		}, nil
	}

	doctorListWorktrees = func(ctx context.Context, targetDir string) ([]gitutil.WorktreeInfo, error) {
		return []gitutil.WorktreeInfo{
			{Path: filepath.Join(dir, "alpha"), Bare: false}, // main repo
			{Path: filepath.Join(dir, "missing-wt"), Branch: ""}, // missing path, empty branch
			{Path: "/tmp/scratchpad/temp-wt", Branch: "feat/temp"}, // temp path
		}, nil
	}

	doctorStat = func(name string) (fs.FileInfo, error) {
		if strings.Contains(name, "missing") {
			return nil, os.ErrNotExist
		}
		return fakeFileInfo{name: "temp-wt", isDir: true}, nil
	}

	prunedRepos := 0
	removedWorktrees := 0
	doctorPruneOp = func(ctx context.Context, dir string) error {
		prunedRepos++
		return nil
	}
	doctorRemoveWorktreeOp = func(ctx context.Context, dir, path string, force bool) error {
		removedWorktrees++
		return nil
	}

	// 1. Without --prune-worktrees
	doctorPruneWorktrees = false
	doctorOutput = "text"

	rescueStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := doctorCmd.RunE(doctorCmd, []string{dir})
	_ = w.Close()
	os.Stdout = rescueStdout
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	out := buf.String()

	if !strings.Contains(out, "[STALE: path does not exist on disk]") {
		t.Errorf("expected missing path stale marker, got: %s", out)
	}
	if !strings.Contains(out, "[STALE: ephemeral scratchpad location]") {
		t.Errorf("expected temp path stale marker, got: %s", out)
	}
	if prunedRepos != 0 || removedWorktrees != 0 {
		t.Fatalf("expected no pruning without flag, got %d pruned, %d removed", prunedRepos, removedWorktrees)
	}

	// 2. With --prune-worktrees
	doctorPruneWorktrees = true
	r, w, _ = os.Pipe()
	os.Stdout = w

	err = doctorCmd.RunE(doctorCmd, []string{dir})
	_ = w.Close()
	os.Stdout = rescueStdout
	if err != nil {
		t.Fatalf("unexpected error with pruning: %v", err)
	}
	buf.Reset()
	_, _ = io.Copy(&buf, r)
	out = buf.String()

	if !strings.Contains(out, "Worktree Cleanup:") || !strings.Contains(out, "Pruned: 2 stale worktrees") {
		t.Errorf("expected prune summary, got: %s", out)
	}
	if prunedRepos != 1 || removedWorktrees != 1 {
		t.Fatalf("expected 1 repo pruned and 1 temp removed, got %d pruned, %d removed", prunedRepos, removedWorktrees)
	}
}

func TestDoctorCustomRulesAndViolations(t *testing.T) {
	oldScan := doctorScan
	oldReadFile := doctorReadFile
	oldConfigDir := doctorUserConfigDir
	oldConfigFile := doctorConfigFile
	oldOutput := doctorOutput

	defer func() {
		doctorScan = oldScan
		doctorReadFile = oldReadFile
		doctorUserConfigDir = oldConfigDir
		doctorConfigFile = oldConfigFile
		doctorOutput = oldOutput
	}()

	dir := t.TempDir()
	doctorScan = func(root string) (*corralmcp.Index, error) {
		return &corralmcp.Index{
			Repos: []corralmcp.RepoEntry{
				{Name: "alpha", Path: filepath.Join(dir, "alpha"), RemoteURL: ""},
			},
		}, nil
	}

	// 1. Config read error (non-not-exist error)
	doctorConfigFile = "/custom/config.json"
	doctorReadFile = func(name string) ([]byte, error) {
		return nil, errors.New("disk failure")
	}
	if err := doctorCmd.RunE(doctorCmd, []string{dir}); err == nil {
		t.Fatal("expected config read error, got nil")
	}

	// 2. Config json parse error
	doctorReadFile = func(name string) ([]byte, error) {
		return []byte("invalid json {{{"), nil
	}
	if err := doctorCmd.RunE(doctorCmd, []string{dir}); err == nil {
		t.Fatal("expected json unmarshal error, got nil")
	}

	// 3. Rule violations triggered
	zero := 0
	rulesJSON := `{
		"doctor": {
			"disk_budget_bytes": 100,
			"max_dirty_repos": 0,
			"max_unpushed_repos": 0,
			"max_worktrees": 0,
			"require_caches": true,
			"warn_on_missing_origin": true
		}
	}`
	doctorReadFile = func(name string) ([]byte, error) {
		return []byte(rulesJSON), nil
	}

	// Mock state with 1 dirty repo, 1 unpushed repo, 1 worktree, cache invalid
	oldLocal := doctorHasLocalChanges
	oldUnpub := doctorHasUnpublishedWork
	oldWorktrees := doctorListWorktrees
	oldDirSize := doctorDirSize
	defer func() {
		doctorHasLocalChanges = oldLocal
		doctorHasUnpublishedWork = oldUnpub
		doctorListWorktrees = oldWorktrees
		doctorDirSize = oldDirSize
	}()

	doctorScan = func(root string) (*corralmcp.Index, error) {
		return &corralmcp.Index{
			Repos: []corralmcp.RepoEntry{
				{Name: "alpha", Path: filepath.Join(dir, "alpha"), RemoteURL: ""},
				{Name: "beta", Path: filepath.Join(dir, "beta"), RemoteURL: ""},
			},
		}, nil
	}

	doctorHasLocalChanges = func(ctx context.Context, targetDir string) (bool, string) {
		if strings.Contains(targetDir, "alpha") {
			return true, "uncommitted changes"
		}
		return false, ""
	}
	doctorHasUnpublishedWork = func(ctx context.Context, targetDir string) (bool, string) {
		if strings.Contains(targetDir, "beta") {
			return true, "ahead of origin"
		}
		return false, ""
	}
	doctorListWorktrees = func(ctx context.Context, targetDir string) ([]gitutil.WorktreeInfo, error) {
		return []gitutil.WorktreeInfo{
			{Path: filepath.Join(dir, "alpha-wt"), Branch: "wt"},
		}, nil
	}
	doctorDirSize = func(dir string) int64 { return 2048 }

	doctorOutput = "text"
	rescueStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := doctorCmd.RunE(doctorCmd, []string{dir})
	_ = w.Close()
	os.Stdout = rescueStdout
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	out := buf.String()

	if !strings.Contains(out, "Health Rules:") || !strings.Contains(out, "rule violations") {
		t.Errorf("expected rule violations in output, got: %s", out)
	}
	if !strings.Contains(out, "disk usage") || !strings.Contains(out, "exceeds budget") {
		t.Errorf("expected disk budget violation, got: %s", out)
	}
	if !strings.Contains(out, "cache integrity check failed") {
		t.Errorf("expected cache violation, got: %s", out)
	}
	if !strings.Contains(out, "has no remote origin configured") {
		t.Errorf("expected missing origin violation, got: %s", out)
	}

	// 4. Default candidates check (customPath empty) with os.ErrNotExist and configDir error
	doctorConfigFile = ""
	doctorUserConfigDir = func() (string, error) {
		return "", errors.New("no config dir")
	}
	doctorReadFile = func(name string) ([]byte, error) {
		return nil, os.ErrNotExist
	}
	rules, err := loadDoctorRules(dir, "")
	if err != nil || rules != nil {
		t.Fatalf("expected nil rules on not-exist, got rules=%v, err=%v", rules, err)
	}

	// 5. Config loaded with satisfied rules (no violations)
	doctorReadFile = func(name string) ([]byte, error) {
		return []byte(`{"doctor": {"disk_budget_bytes": 100000000}}`), nil
	}
	doctorHasLocalChanges = func(ctx context.Context, targetDir string) (bool, string) { return false, "" }
	doctorHasUnpublishedWork = func(ctx context.Context, targetDir string) (bool, string) { return false, "" }
	doctorListWorktrees = func(ctx context.Context, targetDir string) ([]gitutil.WorktreeInfo, error) { return nil, nil }
	doctorScan = func(root string) (*corralmcp.Index, error) {
		return &corralmcp.Index{
			Repos: []corralmcp.RepoEntry{
				{Name: "alpha", Path: filepath.Join(dir, "alpha"), RemoteURL: "https://github.com/org/alpha"},
			},
		}, nil
	}

	r, w, _ = os.Pipe()
	os.Stdout = w

	err = doctorCmd.RunE(doctorCmd, []string{dir})
	_ = w.Close()
	os.Stdout = rescueStdout
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	buf.Reset()
	_, _ = io.Copy(&buf, r)
	out = buf.String()

	if !strings.Contains(out, "All threshold rules satisfied") {
		t.Errorf("expected all threshold rules satisfied, got: %s", out)
	}
	_ = zero
}


