// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"text/template"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/sebastienrousseau/corralctl/internal/github"
	"github.com/sebastienrousseau/corralctl/internal/tui"
)

func TestRunInteractiveOutcomes(t *testing.T) {
	oldSelector, oldExit, oldTTY := runSelector, osExit, isTerminal
	t.Cleanup(func() { runSelector, osExit, isTerminal = oldSelector, oldExit, oldTTY })
	isTerminal = func(fd uintptr) bool { return false }
	exitCode := 0
	osExit = func(code int) { exitCode = code }
	opts := defaultRunOptions(t.TempDir())
	opts.Interactive = true

	runSelector = func(context.Context, string, github.FetchOptions, tui.FetchFunc) ([]github.Repo, bool, error) {
		return nil, false, errors.New("selector failed")
	}
	Run(context.Background(), opts)
	if exitCode != 1 {
		t.Fatalf("selector failure exit = %d", exitCode)
	}

	exitCode = 0
	runSelector = func(context.Context, string, github.FetchOptions, tui.FetchFunc) ([]github.Repo, bool, error) {
		return nil, false, nil
	}
	Run(context.Background(), opts)
	if exitCode != 0 {
		t.Fatalf("selector cancel exit = %d", exitCode)
	}

	runSelector = func(context.Context, string, github.FetchOptions, tui.FetchFunc) ([]github.Repo, bool, error) {
		return nil, true, nil
	}
	Run(context.Background(), opts)

	runSelector = func(context.Context, string, github.FetchOptions, tui.FetchFunc) ([]github.Repo, bool, error) {
		return []github.Repo{{Name: "repo", Owner: "acme", FullName: "acme/repo", Visibility: "Public", Language: "Go"}}, true, nil
	}
	opts.DryRun = true
	Run(context.Background(), opts)
}

func TestSummaryAndLayoutBranches(t *testing.T) {
	var summary Summary
	for _, result := range []RepoResult{
		{Action: "CLONE", Moved: true}, {Action: "SYNC"}, {Action: "SKIP"},
		{Action: "ERROR"}, {Action: "DRY-RUN"},
	} {
		summary.add(result)
	}
	if summary.Moved != 1 || summary.Cloned != 1 || summary.Synced != 1 || summary.Skipped != 1 || summary.Failed != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	custom := RunOptions{Layout: "{{.Name}}"}
	if effectiveLayout(custom, github.Repo{}) != "{{.Name}}" {
		t.Fatal("custom layout ignored")
	}
	if repoRemoteIdentity(github.Repo{Owner: "Acme", Name: "Repo"}) != "github.com/acme/repo" {
		t.Fatal("owner/name identity not normalized")
	}
	if repoRemoteIdentity(github.Repo{Name: "repo"}) != "" {
		t.Fatal("ownerless repo must have no identity")
	}
	if firstNonEmpty("", "") != "" || firstNonEmpty("", "x") != "x" {
		t.Fatal("firstNonEmpty failed")
	}
	tmpl := template.Must(template.New("bad").Parse("{{call .Name}}"))
	if _, err := executeLayout(tmpl, github.Repo{Name: "not-a-function"}, "owner"); err == nil {
		t.Fatal("expected template execution error")
	}
}

func TestDiscoverExistingReposBranches(t *testing.T) {
	base := t.TempDir()
	valid := filepath.Join(base, "a")
	invalid := filepath.Join(base, "b")
	for _, dir := range []string{valid, invalid} {
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(valid, ".git", "config"), []byte("[remote \"origin\"]\nurl = https://github.com/acme/a.git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	skipped := filepath.Join(base, "node_modules", "nested")
	if err := os.MkdirAll(filepath.Join(skipped, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skipped, ".git", "config"), []byte("[remote \"origin\"]\nurl = https://github.com/acme/skipped.git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := discoverExistingRepos(base)
	if !reflect.DeepEqual(got["github.com/acme/a"], []string{valid}) {
		t.Fatalf("discovery = %v", got)
	}
	if _, ok := got["github.com/acme/skipped"]; ok {
		t.Fatal("dependency tree was traversed")
	}
	if got := discoverExistingRepos(filepath.Join(base, "missing")); len(got) != 0 {
		t.Fatalf("missing root discovery = %v", got)
	}
}

func TestNormalizeLayoutDirCaseFailureBranches(t *testing.T) {
	base := t.TempDir()
	visibility := filepath.Join(base, "Public")
	if err := os.Mkdir(visibility, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(visibility, "not-a-dir"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// A pre-existing temporary target forces the first rename to fail.
	source := filepath.Join(visibility, "GO")
	if err := os.Mkdir(source, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(visibility, "Go.corral-rename-tmp"), 0o750); err != nil {
		t.Fatal(err)
	}
	normalizeLayoutDirCase(base, []github.Repo{{Language: "Go"}})
}

func TestProcessRepoRelocationAndOriginFailures(t *testing.T) {
	base := t.TempDir()
	parentFile := filepath.Join(base, "blocked")
	if err := os.WriteFile(parentFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	result := processRepo(context.Background(), "acme", "https", false, false, git.CloneOptions{}, SyncOptions{}, Job{
		Repo: github.Repo{Name: "repo"}, Target: filepath.Join(parentFile, "repo"),
	})
	if result.Action != "ERROR" || !strings.Contains(result.Message, "creating target") {
		t.Fatalf("mkdir failure result: %+v", result)
	}

	result = processRepo(context.Background(), "acme", "https", false, true, git.CloneOptions{}, SyncOptions{}, Job{
		Repo: github.Repo{Name: "repo"}, Target: filepath.Join(base, "new"), Existing: filepath.Join(base, "old"),
	})
	if result.Action != "DRY-RUN" || !result.Moved {
		t.Fatalf("dry relocation result: %+v", result)
	}

	result = processRepo(context.Background(), "acme", "https", false, false, git.CloneOptions{}, SyncOptions{}, Job{
		Repo: github.Repo{Name: "repo"}, Target: filepath.Join(base, "new2"), Existing: filepath.Join(base, "missing-old"),
	})
	if result.Action != "ERROR" || !strings.Contains(result.Message, "failed moving") {
		t.Fatalf("rename failure result: %+v", result)
	}

	target := filepath.Join(base, "collision")
	if err := os.MkdirAll(filepath.Join(target, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	result = processRepo(context.Background(), "acme", "https", false, false, git.CloneOptions{}, SyncOptions{}, Job{
		Repo: github.Repo{Name: "repo", Owner: "acme"}, Target: target,
	})
	if result.Action != "ERROR" || !strings.Contains(result.Message, "cannot verify") {
		t.Fatalf("origin read failure result: %+v", result)
	}
	if err := os.WriteFile(filepath.Join(target, ".git", "config"), []byte("[remote \"origin\"]\nurl = https://github.com/other/repo.git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result = processRepo(context.Background(), "acme", "https", false, false, git.CloneOptions{}, SyncOptions{}, Job{
		Repo: github.Repo{Name: "repo", Owner: "acme"}, Target: target,
	})
	if result.Action != "ERROR" || !strings.Contains(result.Message, "origin collision") {
		t.Fatalf("origin collision result: %+v", result)
	}
}

func TestStateAdditionalFailures(t *testing.T) {
	dir := t.TempDir()
	// The sidecar lives inside the git directory, so force the unreadable
	// case there rather than in the working tree.
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(gitDir, StateFileName), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := readCloneState(dir); err == nil {
		t.Fatal("expected state read error")
	}
	invalidTime := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := writeCloneState(dir, cloneState{LastSyncedAt: invalidTime}); err == nil {
		t.Fatal("expected state marshal error")
	}
	if err := writeCloneState(filepath.Join(dir, "missing"), cloneState{}); err == nil {
		t.Fatal("expected state temp-file error")
	}
}

func TestRunInteractiveFetchClosureAndTTY(t *testing.T) {
	oldSelector, oldFetch, oldTTY, oldExit, oldProgram := runSelector, fetchRepos, isTerminal, osExit, runProgram
	t.Cleanup(func() {
		runSelector, fetchRepos, isTerminal, osExit, runProgram = oldSelector, oldFetch, oldTTY, oldExit, oldProgram
	})
	fetchRepos = func(context.Context, string, github.FetchOptions) ([]github.Repo, error) {
		return []github.Repo{{Name: "repo", Visibility: "Public", Language: "Go"}}, nil
	}
	runSelector = func(ctx context.Context, owner string, opts github.FetchOptions, fetch tui.FetchFunc) ([]github.Repo, bool, error) {
		repos, err := fetch()
		return repos, true, err
	}
	isTerminal = func(uintptr) bool { return true }
	runProgram = func(*tea.Program) (tea.Model, error) { return nil, nil }
	osExit = func(int) {}
	opts := defaultRunOptions(t.TempDir())
	opts.Interactive, opts.DryRun = true, true
	Run(context.Background(), opts)
}

func TestRunLayoutFailureResults(t *testing.T) {
	oldFetch, oldExit, oldTTY := fetchRepos, osExit, isTerminal
	t.Cleanup(func() { fetchRepos, osExit, isTerminal = oldFetch, oldExit, oldTTY })
	isTerminal = func(uintptr) bool { return false }
	exitCode := 0
	osExit = func(code int) { exitCode = code }
	fetchRepos = func(context.Context, string, github.FetchOptions) ([]github.Repo, error) { return nil, nil }
	opts := defaultRunOptions(t.TempDir())
	opts.Layout = "{{"
	Run(context.Background(), opts)
	if exitCode != 1 {
		t.Fatalf("invalid layout exit = %d", exitCode)
	}

	exitCode = 0
	fetchRepos = func(context.Context, string, github.FetchOptions) ([]github.Repo, error) {
		return []github.Repo{{Name: "not-a-function"}}, nil
	}
	opts.Layout = "{{call .Name}}"
	Run(context.Background(), opts)
	if exitCode != 1 {
		t.Fatalf("layout execution exit = %d", exitCode)
	}
}

func TestNDJSONOrphanAndCancellationEncodeFailures(t *testing.T) {
	oldFetch, oldExit, oldTTY := fetchRepos, osExit, isTerminal
	t.Cleanup(func() { fetchRepos, osExit, isTerminal = oldFetch, oldExit, oldTTY })
	fetchRepos = func(context.Context, string, github.FetchOptions) ([]github.Repo, error) { return nil, nil }
	isTerminal = func(uintptr) bool { return false }
	osExit = func(int) {}
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "Public", "Go", "orphan", ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "Public", "Go", "orphan", ".git", "config"), []byte("[remote \"origin\"]\nurl = https://github.com/owner/orphan.git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := defaultRunOptions(base)
	opts.Output, opts.Orphans = OutputNDJSON, true
	Run(context.Background(), opts)

	restore := withRedirectedStdout(t)
	Run(context.Background(), opts)
	restore()

	restore = withRedirectedStdout(t)
	emitCancellation(OutputNDJSON, false, json.NewEncoder(os.Stdout), context.Canceled)
	restore()
}

func TestInjectedFilesystemFailures(t *testing.T) {
	oldWalk, oldRead, oldStat, oldMkdir, oldRename := walkDir, readDir, statPath, mkdirAll, renamePath
	t.Cleanup(func() {
		walkDir, readDir, statPath, mkdirAll, renamePath = oldWalk, oldRead, oldStat, oldMkdir, oldRename
	})
	base := t.TempDir()
	child := filepath.Join(base, "child")
	if err := os.Mkdir(child, 0o750); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	walkDir = func(root string, fn fs.WalkDirFunc) error {
		_ = fn(child, entries[0], errors.New("walk failed"))
		_ = fn(filepath.Join(child, "file"), nil, errors.New("file failed"))
		return nil
	}
	_ = discoverExistingRepos(base)

	readDir = func(path string) ([]os.DirEntry, error) {
		if path == base {
			return entries, nil
		}
		return nil, errors.New("read failed")
	}
	normalizeLayoutDirCase(base, []github.Repo{{Language: "Go"}})

	nestedBase := t.TempDir()
	if err := os.Mkdir(filepath.Join(nestedBase, "Public"), 0o750); err != nil {
		t.Fatal(err)
	}
	nestedEntries, err := os.ReadDir(nestedBase)
	if err != nil {
		t.Fatal(err)
	}
	readDir = func(path string) ([]os.DirEntry, error) {
		if path == nestedBase {
			return nestedEntries, nil
		}
		return nil, errors.New("nested read failed")
	}
	normalizeLayoutDirCase(nestedBase, []github.Repo{{Language: "Go"}})

	readDir = oldRead
	caseBase := t.TempDir()
	if err := os.MkdirAll(filepath.Join(caseBase, "Public", "GO"), 0o750); err != nil {
		t.Fatal(err)
	}
	renames := 0
	renamePath = func(string, string) error {
		renames++
		if renames == 2 {
			return errors.New("second rename failed")
		}
		return nil
	}
	normalizeLayoutDirCase(caseBase, []github.Repo{{Language: "Go"}})
	if renames != 3 {
		t.Fatalf("rename calls = %d, want failure plus revert", renames)
	}

	statPath = func(string) (os.FileInfo, error) { return nil, errors.New("stat failed") }
	result := processRepo(context.Background(), "owner", "https", false, false, git.CloneOptions{}, SyncOptions{}, Job{
		Repo: github.Repo{Name: "repo"}, Target: filepath.Join(base, "target"), Existing: child,
	})
	if result.Action != "ERROR" || !strings.Contains(result.Message, "checking target") {
		t.Fatalf("stat failure = %+v", result)
	}
	statPath = oldStat
	mkdirAll = func(string, os.FileMode) error { return errors.New("mkdir failed") }
	result = processRepo(context.Background(), "owner", "https", false, false, git.CloneOptions{}, SyncOptions{}, Job{
		Repo: github.Repo{Name: "repo"}, Target: filepath.Join(base, "target"), Existing: child,
	})
	if result.Action != "ERROR" || !strings.Contains(result.Message, "relocation target") {
		t.Fatalf("relocation mkdir failure = %+v", result)
	}
}

type fakeStateTemp struct {
	name     string
	writeErr error
	closeErr error
}

func (f *fakeStateTemp) Write([]byte) (int, error) { return 0, f.writeErr }
func (f *fakeStateTemp) Close() error              { return f.closeErr }
func (f *fakeStateTemp) Name() string              { return f.name }

func TestWriteCloneStateIOFailures(t *testing.T) {
	oldCreate, oldRename := createStateTemp, renameStateFile
	t.Cleanup(func() { createStateTemp, renameStateFile = oldCreate, oldRename })
	dir := t.TempDir()
	// statePath resolves the git directory before any temp file is created.
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	createStateTemp = func(string, string) (stateTempFile, error) {
		return &fakeStateTemp{name: filepath.Join(dir, "tmp"), writeErr: errors.New("write failed")}, nil
	}
	if err := writeCloneState(dir, cloneState{}); err == nil || !strings.Contains(err.Error(), "write failed") {
		t.Fatalf("write error = %v", err)
	}
	createStateTemp = func(string, string) (stateTempFile, error) {
		return &fakeStateTemp{name: filepath.Join(dir, "tmp"), closeErr: errors.New("close failed")}, nil
	}
	if err := writeCloneState(dir, cloneState{}); err == nil || !strings.Contains(err.Error(), "close failed") {
		t.Fatalf("close error = %v", err)
	}
	createStateTemp = oldCreate
	renameStateFile = func(string, string) error { return errors.New("rename failed") }
	if err := writeCloneState(dir, cloneState{}); err == nil || !strings.Contains(err.Error(), "rename failed") {
		t.Fatalf("rename error = %v", err)
	}
}
