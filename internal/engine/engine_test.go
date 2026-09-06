// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/sebastienrousseau/corralctl/internal/github"
)

func defaultRunOptions(baseDir string) RunOptions {
	return RunOptions{
		Owner:       "owner",
		BaseDir:     baseDir,
		Concurrency: 1,
		Protocol:    "https",
		DoSync:      true,
		Output:      OutputText,
		Fetch: github.FetchOptions{
			Limit: 100,
		},
	}
}

func TestEngineRunError(t *testing.T) {
	oldFetchRepos := fetchRepos
	defer func() { fetchRepos = oldFetchRepos }()
	fetchRepos = func(ctx context.Context, owner string, opts github.FetchOptions) ([]github.Repo, error) {
		return nil, fmt.Errorf("mock error")
	}

	var exitCode int
	oldOsExit := osExit
	defer func() { osExit = oldOsExit }()
	osExit = func(code int) { exitCode = code }

	opts := defaultRunOptions("dir")
	Run(context.Background(), opts)
	if exitCode != 1 {
		t.Errorf("Expected exit code 1, got %d", exitCode)
	}
}

func TestEngineRunInvalidConfig(t *testing.T) {
	var exitCode int
	oldOsExit := osExit
	defer func() { osExit = oldOsExit }()
	osExit = func(code int) { exitCode = code }

	opts := defaultRunOptions("dir")
	opts.Concurrency = 0
	Run(context.Background(), opts)
	if exitCode != 1 {
		t.Errorf("Expected exit code 1 for invalid concurrency, got %d", exitCode)
	}

	exitCode = 0
	opts = defaultRunOptions("dir")
	opts.Fetch.Limit = -1
	Run(context.Background(), opts)
	if exitCode != 1 {
		t.Errorf("Expected exit code 1 for negative limit, got %d", exitCode)
	}
}

func TestEngineRunHeadlessErrors(t *testing.T) {
	oldOsExit := osExit
	exitCode := 0
	osExit = func(code int) { exitCode = code }
	t.Cleanup(func() { osExit = oldOsExit })

	oldFetchRepos := fetchRepos
	defer func() { fetchRepos = oldFetchRepos }()
	fetchRepos = func(ctx context.Context, owner string, opts github.FetchOptions) ([]github.Repo, error) {
		return []github.Repo{
			{Name: "repo_error", Language: "Go", Visibility: "Public", DefaultBranch: "main"},
			{Name: "repo_skip", Language: "Go", Visibility: "Public", DefaultBranch: "main"},
		}, nil
	}

	oldGitClone := gitClone
	defer func() { gitClone = oldGitClone }()
	gitClone = func(ctx context.Context, url, targetDir string, opts git.CloneOptions) error {
		if strings.Contains(targetDir, "repo_error") {
			return fmt.Errorf("err")
		}
		return nil
	}

	baseDir, _ := os.MkdirTemp("", "engine_test")
	defer func() { _ = os.RemoveAll(baseDir) }()

	_ = os.MkdirAll(filepath.Join(baseDir, "Public", "Go", "repo_skip", ".git"), 0o750)

	oldIsTerminal := isTerminal
	defer func() { isTerminal = oldIsTerminal }()
	isTerminal = func(fd uintptr) bool { return false }

	opts := defaultRunOptions(baseDir)
	opts.Fetch.Limit = 2
	opts.DoSync = false
	Run(context.Background(), opts)
	if exitCode != 1 {
		t.Errorf("Expected exit code 1 for a failed repository, got %d", exitCode)
	}
}

func TestEngineRunSuccess(t *testing.T) {
	oldFetchRepos := fetchRepos
	defer func() { fetchRepos = oldFetchRepos }()
	fetchRepos = func(ctx context.Context, owner string, opts github.FetchOptions) ([]github.Repo, error) {
		return []github.Repo{
			{Name: "repo1", Language: "Go", Visibility: "Public", DefaultBranch: "main"},
			{Name: "repo2", Language: "Go", Visibility: "Public", DefaultBranch: "main"},
		}, nil
	}

	var exitCode int
	oldOsExit := osExit
	defer func() { osExit = oldOsExit }()
	osExit = func(code int) { exitCode = code }

	oldIsTerminal := isTerminal
	defer func() { isTerminal = oldIsTerminal }()
	isTerminal = func(fd uintptr) bool { return true }

	oldRunProgram := runProgram
	defer func() { runProgram = oldRunProgram }()
	runProgram = func(p *tea.Program) (tea.Model, error) {
		return nil, nil
	}

	baseDir, _ := os.MkdirTemp("", "engine_test")
	defer func() { _ = os.RemoveAll(baseDir) }()

	opts := defaultRunOptions(baseDir)
	opts.Fetch.Limit = 2
	opts.DryRun = true
	opts.Orphans = true
	Run(context.Background(), opts)
	if exitCode != 0 {
		t.Errorf("Expected exit code 0, got %d", exitCode)
	}

	isTerminal = func(fd uintptr) bool { return false }
	Run(context.Background(), opts)

	isTerminal = func(fd uintptr) bool { return true }
	runProgram = func(p *tea.Program) (tea.Model, error) {
		return nil, fmt.Errorf("tui err")
	}
	Run(context.Background(), opts)
}

func TestProcessRepo(t *testing.T) {
	baseDir, _ := os.MkdirTemp("", "engine_test")
	defer func() { _ = os.RemoveAll(baseDir) }()

	repo := github.Repo{
		Name:          "repo1",
		Language:      "Go",
		Visibility:    "Public",
		DefaultBranch: "main",
		CloneURL:      "https://github.com/owner/repo1.git",
		SSHURL:        "ssh://clone",
	}
	targetDir := filepath.Join(baseDir, "Public", "Go", "repo1")
	job := Job{Repo: repo, Target: targetDir}

	msg := processRepo(context.Background(), "owner", "https", true, true, git.CloneOptions{}, SyncOptions{}, job)
	if msg.Action != "DRY-RUN" || msg.Message != "git clone" {
		t.Errorf("Expected dry run clone, got %v", msg)
	}

	makeCloneAt(t, targetDir, "https://github.com/owner/repo1.git")

	msg = processRepo(context.Background(), "owner", "https", true, true, git.CloneOptions{}, SyncOptions{}, job)
	if msg.Action != "DRY-RUN" || msg.Message != "git pull" {
		t.Errorf("Expected dry run pull, got %v", msg)
	}

	msg = processRepo(context.Background(), "owner", "https", false, false, git.CloneOptions{}, SyncOptions{}, job)
	if msg.Action != "SKIP" {
		t.Errorf("Expected SKIP, got %v", msg)
	}

	_ = os.RemoveAll(targetDir)
	_ = os.MkdirAll(targetDir, 0o750)
	msg = processRepo(context.Background(), "owner", "https", false, false, git.CloneOptions{}, SyncOptions{}, job)
	if msg.Action != "SKIP" || !strings.Contains(msg.Message, "not a git repo") {
		t.Errorf("Expected SKIP for non-git repo, got %v", msg)
	}

	_ = os.RemoveAll(targetDir)
	msg = processRepo(context.Background(), "owner", "ssh", false, true, git.CloneOptions{}, SyncOptions{}, job)
	if msg.Action != "DRY-RUN" {
		t.Errorf("Expected DRY-RUN for ssh, got %v", msg)
	}
}

func TestProcessRepoFull(t *testing.T) {
	baseDir, _ := os.MkdirTemp("", "engine_test")
	defer func() { _ = os.RemoveAll(baseDir) }()

	oldGitClone := gitClone
	oldGitPull := gitPull
	oldGitCurrentBranch := gitCurrentBranch
	oldGitRemoteOrigin := gitRemoteOrigin
	oldGitIsEmpty := gitIsEmpty
	defer func() {
		gitClone = oldGitClone
		gitPull = oldGitPull
		gitCurrentBranch = oldGitCurrentBranch
		gitRemoteOrigin = oldGitRemoteOrigin
		gitIsEmpty = oldGitIsEmpty
	}()

	gitClone = func(ctx context.Context, url, targetDir string, opts git.CloneOptions) error { return nil }
	gitPull = func(ctx context.Context, targetDir string, opts git.PullOptions) error { return nil }
	gitCurrentBranch = func(_ context.Context, targetDir string) (string, error) { return "main", nil }
	gitRemoteOrigin = func(targetDir string) (string, error) { return "https://github.com/owner/repo1.git", nil }
	// Assume the fake .git directory in this test is populated;
	// gitIsEmpty is a v0.0.13 addition that would otherwise SKIP the
	// SYNC path before gitPull is exercised.
	gitIsEmpty = func(_ context.Context, targetDir string) bool { return false }

	repo := github.Repo{
		Name:          "repo1",
		Language:      "Go",
		Visibility:    "Public",
		DefaultBranch: "main",
		CloneURL:      "https://github.com/owner/repo1.git",
		SSHURL:        "ssh://clone",
	}
	targetDir := filepath.Join(baseDir, "Public", "Go", "repo1")
	job := Job{Repo: repo, Target: targetDir}

	msg := processRepo(context.Background(), "owner", "https", true, false, git.CloneOptions{}, SyncOptions{}, job)
	if msg.Action != "CLONE" {
		t.Errorf("Expected CLONE, got %v", msg)
	}

	gitClone = func(ctx context.Context, url, targetDir string, opts git.CloneOptions) error {
		return fmt.Errorf("err")
	}
	_ = os.RemoveAll(targetDir)
	msg = processRepo(context.Background(), "owner", "https", true, false, git.CloneOptions{}, SyncOptions{}, job)
	if msg.Action != "ERROR" {
		t.Errorf("Expected ERROR, got %v", msg)
	}

	_ = os.MkdirAll(filepath.Join(targetDir, ".git"), 0o750)
	msg = processRepo(context.Background(), "owner", "https", true, false, git.CloneOptions{}, SyncOptions{}, job)
	if msg.Action != "SYNC" {
		t.Errorf("Expected SYNC, got %v", msg)
	}

	gitPull = func(ctx context.Context, targetDir string, opts git.PullOptions) error { return fmt.Errorf("err") }
	msg = processRepo(context.Background(), "owner", "https", true, false, git.CloneOptions{}, SyncOptions{}, job)
	if msg.Action != "ERROR" {
		t.Errorf("Expected ERROR, got %v", msg)
	}

	gitCurrentBranch = func(_ context.Context, targetDir string) (string, error) { return "feat", nil }
	msg = processRepo(context.Background(), "owner", "https", true, false, git.CloneOptions{}, SyncOptions{}, job)
	if msg.Action != "SKIP" {
		t.Errorf("Expected SKIP, got %v", msg)
	}

	repo.SSHURL = ""
	job = Job{Repo: repo, Target: targetDir}
	_ = os.RemoveAll(targetDir)
	msg = processRepo(context.Background(), "owner", "ssh", false, true, git.CloneOptions{}, SyncOptions{}, job)
	if msg.Action != "DRY-RUN" || !strings.Contains(msg.Message, "git clone") {
		t.Errorf("Expected DRY-RUN for ssh fallback, got %v", msg)
	}

	detectOrphans("owner", baseDir, []github.Repo{{Name: "other"}})
}

func TestProcessRepoCanceled(t *testing.T) {
	baseDir, _ := os.MkdirTemp("", "engine_test")
	defer func() { _ = os.RemoveAll(baseDir) }()

	repo := github.Repo{
		Name:          "repo1",
		Language:      "Go",
		Visibility:    "Public",
		DefaultBranch: "main",
		CloneURL:      "https://github.com/owner/repo1.git",
	}
	targetDir := filepath.Join(baseDir, "Public", "Go", "repo1")
	job := Job{Repo: repo, Target: targetDir}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	msg := processRepo(ctx, "owner", "https", true, false, git.CloneOptions{}, SyncOptions{}, job)
	if msg.Action != "ERROR" || !strings.Contains(msg.Message, "canceled") {
		t.Fatalf("expected canceled error result, got %#v", msg)
	}
}

func TestDetectOrphansError(t *testing.T) {
	detectOrphans("owner", "/invalid_dir_that_does_not_exist", []github.Repo{})

	baseDir, _ := os.MkdirTemp("", "engine_test")
	defer func() { _ = os.RemoveAll(baseDir) }()

	_ = os.MkdirAll(filepath.Join(baseDir, "Public", "Go", "repo1", ".git"), 0o750)

	oldGitRemoteOrigin := gitRemoteOrigin
	defer func() { gitRemoteOrigin = oldGitRemoteOrigin }()
	gitRemoteOrigin = func(targetDir string) (string, error) {
		return "", fmt.Errorf("err")
	}

	detectOrphans("owner", baseDir, []github.Repo{})

	gitRemoteOrigin = func(targetDir string) (string, error) {
		return "https://github.com/someone_else/repo.git", nil
	}
	detectOrphans("owner", baseDir, []github.Repo{})

	gitRemoteOrigin = func(targetDir string) (string, error) {
		return "git@github.com:owner/repo.git", nil
	}
	detectOrphans("owner", baseDir, []github.Repo{})

	// The local directory name ("repo1") is unknown, but the remote URL resolves
	// to a known repository, so it must NOT be flagged as an orphan.
	gitRemoteOrigin = func(targetDir string) (string, error) {
		return "https://github.com/owner/actualname.git", nil
	}
	detectOrphans("owner", baseDir, []github.Repo{{Name: "actualname"}})
}

func TestCleanupEmptyFoldersError(t *testing.T) {
	cleanupEmptyFolders("/invalid_dir_that_does_not_exist", []github.Repo{{Language: "Go"}})
}

func TestRepoNameFromURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://github.com/owner/repo.git", "repo"},
		{"git@github.com:owner/repo.git", "repo"},
		{"https://github.com/owner/repo", "repo"},
		{"plainname", "plainname"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := repoNameFromURL(tt.in); got != tt.want {
			t.Errorf("repoNameFromURL(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSkipDiscoveryDirectory(t *testing.T) {
	for _, name := range []string{".git", ".cache", ".next", ".venv", "DerivedData", "Pods", "build", "dist", "node_modules", "target", "vendor", "venv"} {
		if !skipDiscoveryDirectory(name) {
			t.Errorf("expected %q to be skipped", name)
		}
	}
	if skipDiscoveryDirectory("source") {
		t.Fatal("source directory should not be skipped")
	}
}

func TestFirstPath(t *testing.T) {
	if firstPath(nil) != "" || firstPath([]string{"one", "two"}) != "one" {
		t.Fatal("firstPath returned an unexpected value")
	}
}

func TestRunReportsDuplicateCloneLocations(t *testing.T) {
	base := t.TempDir()
	for _, name := range []string{"one", "two"} {
		dir := filepath.Join(base, name, ".git")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		config := "[remote \"origin\"]\nurl = https://github.com/owner/repo.git\n"
		if err := os.WriteFile(filepath.Join(dir, "config"), []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	oldFetch, oldExit := fetchRepos, osExit
	t.Cleanup(func() { fetchRepos, osExit = oldFetch, oldExit })
	fetchRepos = func(context.Context, string, github.FetchOptions) ([]github.Repo, error) {
		return []github.Repo{{Name: "repo", Owner: "owner", FullName: "owner/repo", Language: "Go", Visibility: "Public"}}, nil
	}
	exitCode := 0
	osExit = func(code int) { exitCode = code }
	opts := defaultRunOptions(base)
	opts.DryRun = true
	Run(context.Background(), opts)
	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
}

func TestNormalizeLanguage(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "other"},
		{"Go", "go"},
		{"C#", "csharp"},
		{"C++", "cpp"},
		{"Jupyter Notebook", "jupyter_notebook"},
		{"C/C++", "c_c++"},
	}

	for _, tt := range tests {
		result := normalizeLanguage(tt.input)
		if result != tt.expected {
			t.Errorf("normalizeLanguage(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestRepositoryBucketUsesWebForGitHubPages(t *testing.T) {
	tests := []struct {
		repo github.Repo
		want string
	}{
		{repo: github.Repo{Name: "site.github.io", Language: "JavaScript"}, want: "Web"},
		{repo: github.Repo{Name: "SITE.GITHUB.IO", Language: "SCSS"}, want: "Web"},
		{repo: github.Repo{Name: "site", Language: "Python"}, want: "Python"},
	}

	for _, tt := range tests {
		if got := repositoryBucket(tt.repo); got != tt.want {
			t.Errorf("repositoryBucket(%q, %q) = %q, want %q", tt.repo.Name, tt.repo.Language, got, tt.want)
		}
	}
}

func TestCanonicalLanguageAndCollection(t *testing.T) {
	tests := []struct{ language, want string }{
		{"C", "C"}, {"C++", "Cpp"}, {"C#", "CSharp"}, {"CSS", "CSS"},
		{"Dockerfile", "Docker"}, {"Go", "Go"}, {"HTML", "HTML"},
		{"JavaScript", "JavaScript"}, {"Jupyter Notebook", "Python"}, {"Lua", "Lua"},
		{"Objective-C", "Objective-C"}, {"Objective-C++", "Objective-Cpp"}, {"", "Other"},
		{"PHP", "PHP"}, {"Python", "Python"}, {"Ruby", "Ruby"}, {"Rust", "Rust"},
		{"SCSS", "SCSS"}, {"Shell", "Shell"}, {"Solidity", "Solidity"}, {"Stylus", "Stylus"},
		{"Swift", "Swift"}, {"TeX", "TeX"}, {"TypeScript", "TypeScript"},
		{"Vim Script", "VimScript"}, {"F#", "F#"},
	}
	for _, tt := range tests {
		if got := canonicalLanguage(tt.language); got != tt.want {
			t.Errorf("canonicalLanguage(%q) = %q, want %q", tt.language, got, tt.want)
		}
	}

	if got := repositoryCollection(github.Repo{Fork: true, Visibility: "Private"}); got != "Forks" {
		t.Errorf("fork collection = %q", got)
	}
	if got := repositoryCollection(github.Repo{Visibility: "private"}); got != "Private" {
		t.Errorf("private collection = %q", got)
	}
	if got := repositoryCollection(github.Repo{Visibility: "Public"}); got != "Public" {
		t.Errorf("public collection = %q", got)
	}
	for _, name := range []string{"public", "PRIVATE", "Forks", "work"} {
		if canonicalCollectionName(name) == "" {
			t.Errorf("canonicalCollectionName(%q) was empty", name)
		}
	}
	if got := canonicalCollectionName("Clients"); got != "" {
		t.Errorf("unknown collection = %q", got)
	}
}

func TestEnsureAppleCollections(t *testing.T) {
	base := t.TempDir()
	if err := ensureAppleCollections(base); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Public", "Private", "Work", "Forks"} {
		if info, err := os.Stat(filepath.Join(base, name)); err != nil || !info.IsDir() {
			t.Errorf("collection %s was not created", name)
		}
	}

	oldMkdir := mkdirAll
	mkdirAll = func(string, os.FileMode) error { return errors.New("mkdir") }
	t.Cleanup(func() { mkdirAll = oldMkdir })
	if err := ensureAppleCollections(base); err == nil {
		t.Fatal("expected collection creation error")
	}
}

func TestRunReportsCollectionCreationFailure(t *testing.T) {
	oldFetch, oldMkdir, oldExit := fetchRepos, mkdirAll, osExit
	t.Cleanup(func() { fetchRepos, mkdirAll, osExit = oldFetch, oldMkdir, oldExit })
	fetchRepos = func(context.Context, string, github.FetchOptions) ([]github.Repo, error) {
		return nil, nil
	}
	mkdirAll = func(string, os.FileMode) error { return errors.New("collections") }
	exitCode := 0
	osExit = func(code int) { exitCode = code }
	Run(context.Background(), defaultRunOptions(t.TempDir()))
	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
}

func TestRunDryRunDoesNotMigrateLayout(t *testing.T) {
	base := t.TempDir()
	legacy := filepath.Join(base, "Public", "go", "repo")
	if err := os.MkdirAll(filepath.Join(legacy, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	config := "[remote \"origin\"]\nurl = https://github.com/owner/repo.git\n"
	if err := os.WriteFile(filepath.Join(legacy, ".git", "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	oldFetch := fetchRepos
	t.Cleanup(func() { fetchRepos = oldFetch })
	fetchRepos = func(context.Context, string, github.FetchOptions) ([]github.Repo, error) {
		return []github.Repo{{Name: "repo", Owner: "owner", FullName: "owner/repo", Language: "Go", Visibility: "Public"}}, nil
	}
	opts := defaultRunOptions(base)
	opts.DryRun = true
	Run(context.Background(), opts)

	entries, err := os.ReadDir(filepath.Join(base, "Public"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "go" {
		t.Fatalf("dry run changed bucket names: %v", entries)
	}
	if _, err := os.Stat(filepath.Join(base, "Work")); !os.IsNotExist(err) {
		t.Fatal("dry run created collection folders")
	}
}

func TestMigrateLegacy(t *testing.T) {
	baseDir, err := os.MkdirTemp("", "engine_test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(baseDir) }()

	legacyDir := filepath.Join(baseDir, "go", "myrepo")
	// Migration now requires evidence that the directory really is this
	// repository's clone: a .git directory and a matching origin remote. A bare
	// directory sharing the repo's name is no longer sufficient, because that
	// let unrelated user folders be relocated silently.
	_ = os.MkdirAll(filepath.Join(legacyDir, ".git"), 0o750)
	cfg := "[remote \"origin\"]\n\turl = https://github.com/acme/myrepo.git\n"
	if err := os.WriteFile(filepath.Join(legacyDir, ".git", "config"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	repos := []github.Repo{{
		Name: "myrepo", Owner: "acme", FullName: "acme/myrepo",
		Language: "Go", Visibility: "Public",
	}}
	migrateLegacy(baseDir, repos)

	targetDir := filepath.Join(baseDir, "Public", "Go", "myrepo")
	if _, err := os.Stat(targetDir); os.IsNotExist(err) {
		t.Errorf("Expected %s to exist after migration", targetDir)
	}
}

// quitModel is a minimal tea.Model that quits immediately on Init so the
// default runProgram closure (p.Run()) can be exercised without a real TTY.
type quitModel struct{}

func (quitModel) Init() tea.Cmd                       { return tea.Quit }
func (quitModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return quitModel{}, tea.Quit }
func (quitModel) View() string                        { return "" }

// TestRunProgramDefault exercises the default runProgram closure (engine.go:85)
// by running a program that quits immediately with the renderer disabled.
func TestRunProgramDefault(t *testing.T) {
	p := tea.NewProgram(
		quitModel{},
		tea.WithoutRenderer(),
		tea.WithInput(strings.NewReader("")),
		tea.WithOutput(io.Discard),
	)
	if _, err := runProgram(p); err != nil {
		t.Fatalf("runProgram returned error: %v", err)
	}
}

// TestEngineRunNilContext covers the ctx == nil branch and the remaining
// validation branches (empty owner, empty base dir, bad protocol).
func TestEngineRunValidation(t *testing.T) {
	var exitCode int
	oldOsExit := osExit
	defer func() { osExit = oldOsExit }()
	osExit = func(code int) { exitCode = code }

	oldIsTerminal := isTerminal
	defer func() { isTerminal = oldIsTerminal }()
	isTerminal = func(fd uintptr) bool { return false }

	// Empty owner.
	opts := defaultRunOptions("dir")
	opts.Owner = ""
	exitCode = 0
	Run(context.Background(), opts)
	if exitCode != 1 {
		t.Errorf("expected exit 1 for empty owner, got %d", exitCode)
	}

	// Empty base directory.
	opts = defaultRunOptions("")
	exitCode = 0
	Run(context.Background(), opts)
	if exitCode != 1 {
		t.Errorf("expected exit 1 for empty base dir, got %d", exitCode)
	}

	// Invalid protocol.
	opts = defaultRunOptions("dir")
	opts.Protocol = "ftp"
	exitCode = 0
	Run(context.Background(), opts)
	if exitCode != 1 {
		t.Errorf("expected exit 1 for bad protocol, got %d", exitCode)
	}
}

// TestEngineRunNilContextAndDefaultOutput covers the ctx == nil branch and the
// Output == "" defaulting branch using a nil context and unset output.
func TestEngineRunNilContextAndDefaultOutput(t *testing.T) {
	oldFetchRepos := fetchRepos
	defer func() { fetchRepos = oldFetchRepos }()
	fetchRepos = func(ctx context.Context, owner string, opts github.FetchOptions) ([]github.Repo, error) {
		return []github.Repo{}, nil
	}

	oldIsTerminal := isTerminal
	defer func() { isTerminal = oldIsTerminal }()
	isTerminal = func(fd uintptr) bool { return false }

	baseDir, err := os.MkdirTemp("", "engine_test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(baseDir) }()

	opts := defaultRunOptions(baseDir)
	opts.Output = "" // exercise default-output branch
	// A typed nil context exercises the ctx == nil guard without tripping
	// staticcheck's SA1012 (which only flags an untyped nil literal).
	var nilCtx context.Context
	Run(nilCtx, opts)
}

// TestEngineRunJSON covers the OutputJSON aggregated payload branch and the
// add CLONE/SYNC accumulation cases.
func TestEngineRunJSON(t *testing.T) {
	oldFetchRepos := fetchRepos
	oldGitClone := gitClone
	oldGitPull := gitPull
	oldGitCurrentBranch := gitCurrentBranch
	defer func() {
		fetchRepos = oldFetchRepos
		gitClone = oldGitClone
		gitPull = oldGitPull
		gitCurrentBranch = oldGitCurrentBranch
	}()

	fetchRepos = func(ctx context.Context, owner string, opts github.FetchOptions) ([]github.Repo, error) {
		return []github.Repo{
			{Name: "repo_clone", Language: "Go", Visibility: "Public", DefaultBranch: "main"},
			{Name: "repo_sync", Language: "Go", Visibility: "Public", DefaultBranch: "main"},
		}, nil
	}
	gitClone = func(ctx context.Context, url, targetDir string, opts git.CloneOptions) error { return nil }
	gitPull = func(ctx context.Context, targetDir string, opts git.PullOptions) error { return nil }
	gitCurrentBranch = func(_ context.Context, targetDir string) (string, error) { return "main", nil }

	oldIsTerminal := isTerminal
	defer func() { isTerminal = oldIsTerminal }()
	isTerminal = func(fd uintptr) bool { return false }

	baseDir, err := os.MkdirTemp("", "engine_test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(baseDir) }()

	// Pre-create an existing git repo so repo_sync triggers a SYNC action.
	if err := os.MkdirAll(filepath.Join(baseDir, "Public", "Go", "repo_sync", ".git"), 0o750); err != nil {
		t.Fatal(err)
	}

	opts := defaultRunOptions(baseDir)
	opts.Output = OutputJSON
	Run(context.Background(), opts)
}

// TestEngineRunNDJSON covers the OutputNDJSON per-result encode branch.
func TestEngineRunNDJSON(t *testing.T) {
	oldFetchRepos := fetchRepos
	oldGitClone := gitClone
	defer func() {
		fetchRepos = oldFetchRepos
		gitClone = oldGitClone
	}()

	fetchRepos = func(ctx context.Context, owner string, opts github.FetchOptions) ([]github.Repo, error) {
		return []github.Repo{
			{Name: "repo_a", Language: "Go", Visibility: "Public", DefaultBranch: "main"},
		}, nil
	}
	gitClone = func(ctx context.Context, url, targetDir string, opts git.CloneOptions) error { return nil }

	oldIsTerminal := isTerminal
	defer func() { isTerminal = oldIsTerminal }()
	isTerminal = func(fd uintptr) bool { return false }

	baseDir, err := os.MkdirTemp("", "engine_test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(baseDir) }()

	opts := defaultRunOptions(baseDir)
	opts.Output = OutputNDJSON
	Run(context.Background(), opts)
}

// TestEngineRunLimitWarning covers the "fetched exactly N" warning branch.
func TestEngineRunLimitWarning(t *testing.T) {
	oldFetchRepos := fetchRepos
	oldGitClone := gitClone
	defer func() {
		fetchRepos = oldFetchRepos
		gitClone = oldGitClone
	}()

	fetchRepos = func(ctx context.Context, owner string, opts github.FetchOptions) ([]github.Repo, error) {
		return []github.Repo{
			{Name: "repo_a", Language: "Go", Visibility: "Public", DefaultBranch: "main"},
		}, nil
	}
	gitClone = func(ctx context.Context, url, targetDir string, opts git.CloneOptions) error { return nil }

	oldIsTerminal := isTerminal
	defer func() { isTerminal = oldIsTerminal }()
	isTerminal = func(fd uintptr) bool { return false }

	baseDir, err := os.MkdirTemp("", "engine_test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(baseDir) }()

	opts := defaultRunOptions(baseDir)
	opts.Fetch.Limit = 1 // equals number of repos -> warning
	Run(context.Background(), opts)
}

// TestEngineRunCanceled covers the ctx-cancel enqueue and worker/consumer
// select branches by running with an already-canceled context.
func TestEngineRunCanceled(t *testing.T) {
	oldFetchRepos := fetchRepos
	oldGitClone := gitClone
	oldOsExit := osExit
	defer func() {
		fetchRepos = oldFetchRepos
		gitClone = oldGitClone
		osExit = oldOsExit
	}()

	// Run now exits with the cancellation code on every canceled run; the
	// test would terminate the process otherwise.
	var lastExit int
	osExit = func(code int) { lastExit = code }

	repos := make([]github.Repo, 0, 50)
	for i := 0; i < 50; i++ {
		repos = append(repos, github.Repo{
			Name:          fmt.Sprintf("repo_%d", i),
			Language:      "Go",
			Visibility:    "Public",
			DefaultBranch: "main",
		})
	}
	fetchRepos = func(ctx context.Context, owner string, opts github.FetchOptions) ([]github.Repo, error) {
		return repos, nil
	}
	gitClone = func(ctx context.Context, url, targetDir string, opts git.CloneOptions) error { return nil }

	oldIsTerminal := isTerminal
	defer func() { isTerminal = oldIsTerminal }()
	isTerminal = func(fd uintptr) bool { return false }

	baseDir, err := os.MkdirTemp("", "engine_test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(baseDir) }()

	opts := defaultRunOptions(baseDir)
	opts.Fetch.Limit = len(repos)

	// The cancel-handling select branches in the worker, the post-process
	// results send, and the enqueue loop are inherently racy (both select
	// cases may be ready). Run repeatedly with an already-canceled context to
	// deterministically exercise all of them.
	for i := 0; i < 200; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		Run(ctx, opts)
	}

	if lastExit != cancelExitCode {
		t.Errorf("expected osExit(%d) on cancellation, got %d", cancelExitCode, lastExit)
	}
}

// TestEngineRunCanceledJSON asserts that the aggregated json payload carries
// summary.canceled = true when ctx is cancelled mid-run, so a scripted
// consumer reading the json doesn't mistake an aborted run for a clean one.
func TestEngineRunCanceledJSON(t *testing.T) {
	oldFetchRepos := fetchRepos
	oldGitClone := gitClone
	oldOsExit := osExit
	oldIsTerminal := isTerminal
	defer func() {
		fetchRepos = oldFetchRepos
		gitClone = oldGitClone
		osExit = oldOsExit
		isTerminal = oldIsTerminal
	}()

	fetchRepos = func(ctx context.Context, owner string, opts github.FetchOptions) ([]github.Repo, error) {
		return []github.Repo{
			{Name: "r1", Language: "Go", Visibility: "Public", DefaultBranch: "main"},
		}, nil
	}
	gitClone = func(ctx context.Context, url, targetDir string, opts git.CloneOptions) error { return nil }
	isTerminal = func(fd uintptr) bool { return false }

	var lastExit int
	osExit = func(code int) { lastExit = code }

	baseDir := t.TempDir()

	// Capture stdout so we can parse the aggregated json payload.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = w

	opts := defaultRunOptions(baseDir)
	opts.Output = OutputJSON
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	Run(ctx, opts)

	_ = w.Close()
	os.Stdout = oldStdout
	captured, _ := io.ReadAll(r)

	if lastExit != cancelExitCode {
		t.Errorf("expected osExit(%d) on cancellation, got %d", cancelExitCode, lastExit)
	}
	if !strings.Contains(string(captured), `"canceled": true`) {
		t.Errorf("expected canceled:true in json summary, got: %s", string(captured))
	}
}

// TestEngineRunCanceledNDJSON asserts that ndjson output emits a terminal
// {"action":"CANCELED",...} record so a `corralctl | jq` pipeline sees the
// cancel rather than just stopping mid-stream.
func TestEngineRunCanceledNDJSON(t *testing.T) {
	oldFetchRepos := fetchRepos
	oldGitClone := gitClone
	oldOsExit := osExit
	oldIsTerminal := isTerminal
	defer func() {
		fetchRepos = oldFetchRepos
		gitClone = oldGitClone
		osExit = oldOsExit
		isTerminal = oldIsTerminal
	}()

	fetchRepos = func(ctx context.Context, owner string, opts github.FetchOptions) ([]github.Repo, error) {
		return []github.Repo{
			{Name: "r1", Language: "Go", Visibility: "Public", DefaultBranch: "main"},
		}, nil
	}
	gitClone = func(ctx context.Context, url, targetDir string, opts git.CloneOptions) error { return nil }
	isTerminal = func(fd uintptr) bool { return false }
	osExit = func(code int) {}

	baseDir := t.TempDir()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = w

	opts := defaultRunOptions(baseDir)
	opts.Output = OutputNDJSON
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	Run(ctx, opts)

	_ = w.Close()
	os.Stdout = oldStdout
	captured, _ := io.ReadAll(r)

	if !strings.Contains(string(captured), `"action":"CANCELED"`) {
		t.Errorf("expected terminal CANCELED ndjson record, got: %s", string(captured))
	}
}

// TestEngineRunCanceledTextSilentOnTTY guarantees we never regress the
// interactive UX: when the user hits Ctrl-C in the TUI, the engine must
// emit nothing extra (the TUI itself handles the visual exit).
func TestEngineRunCanceledTextSilentOnTTY(t *testing.T) {
	oldFetchRepos := fetchRepos
	oldGitClone := gitClone
	oldOsExit := osExit
	oldIsTerminal := isTerminal
	oldRunProgram := runProgram
	defer func() {
		fetchRepos = oldFetchRepos
		gitClone = oldGitClone
		osExit = oldOsExit
		isTerminal = oldIsTerminal
		runProgram = oldRunProgram
	}()

	fetchRepos = func(ctx context.Context, owner string, opts github.FetchOptions) ([]github.Repo, error) {
		return []github.Repo{
			{Name: "r1", Language: "Go", Visibility: "Public", DefaultBranch: "main"},
		}, nil
	}
	gitClone = func(ctx context.Context, url, targetDir string, opts git.CloneOptions) error { return nil }
	isTerminal = func(fd uintptr) bool { return true }
	runProgram = func(p *tea.Program) (tea.Model, error) { return nil, nil }
	osExit = func(code int) {}

	baseDir := t.TempDir()

	// Redirect log output so we can assert it's empty (no "operation
	// canceled" line on the TTY path).
	var logBuf strings.Builder
	oldLogOut := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(oldLogOut)

	opts := defaultRunOptions(baseDir)
	opts.Output = OutputText
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	Run(ctx, opts)

	if strings.Contains(logBuf.String(), "operation canceled") {
		t.Errorf("TTY path must stay silent on cancel; logged: %q", logBuf.String())
	}
}

// TestEngineRunCanceledTextNoisyOnNonTTY is the inverse: scripted callers
// piping the text output should get one log line documenting the cancel.
// Diagnostics are written to stderr so stdout remains safe for structured output.
func TestEngineRunCanceledTextNoisyOnNonTTY(t *testing.T) {
	oldFetchRepos := fetchRepos
	oldGitClone := gitClone
	oldOsExit := osExit
	oldIsTerminal := isTerminal
	oldLogOut := log.Writer()
	defer func() {
		fetchRepos = oldFetchRepos
		gitClone = oldGitClone
		osExit = oldOsExit
		isTerminal = oldIsTerminal
		log.SetOutput(oldLogOut)
	}()

	fetchRepos = func(ctx context.Context, owner string, opts github.FetchOptions) ([]github.Repo, error) {
		return []github.Repo{
			{Name: "r1", Language: "Go", Visibility: "Public", DefaultBranch: "main"},
		}, nil
	}
	gitClone = func(ctx context.Context, url, targetDir string, opts git.CloneOptions) error { return nil }
	isTerminal = func(fd uintptr) bool { return false }
	osExit = func(code int) {}

	baseDir := t.TempDir()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = w

	opts := defaultRunOptions(baseDir)
	opts.Output = OutputText
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	Run(ctx, opts)

	_ = w.Close()
	os.Stderr = oldStderr
	captured, _ := io.ReadAll(r)

	if !strings.Contains(string(captured), "operation canceled") {
		t.Errorf("non-TTY text path must log on cancel; got: %q", string(captured))
	}
}

// withRedirectedStdout temporarily replaces os.Stdout with the write end of a
// pipe whose read end is immediately closed, so any write to stdout fails. It
// returns a restore function.
func withRedirectedStdout(t *testing.T) func() {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	return func() {
		os.Stdout = old
		_ = w.Close()
	}
}

// TestEngineRunJSONEncodeError covers the OutputJSON encode-failure branch
// (which calls osExit) by directing stdout to a broken pipe.
func TestEngineRunJSONEncodeError(t *testing.T) {
	oldFetchRepos := fetchRepos
	oldGitClone := gitClone
	defer func() {
		fetchRepos = oldFetchRepos
		gitClone = oldGitClone
	}()
	fetchRepos = func(ctx context.Context, owner string, opts github.FetchOptions) ([]github.Repo, error) {
		return []github.Repo{{Name: "repo_a", Language: "Go", Visibility: "Public", DefaultBranch: "main"}}, nil
	}
	gitClone = func(ctx context.Context, url, targetDir string, opts git.CloneOptions) error { return nil }

	oldIsTerminal := isTerminal
	defer func() { isTerminal = oldIsTerminal }()
	isTerminal = func(fd uintptr) bool { return false }

	var exitCode int
	oldOsExit := osExit
	defer func() { osExit = oldOsExit }()
	osExit = func(code int) { exitCode = code }

	baseDir, err := os.MkdirTemp("", "engine_test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(baseDir) }()

	opts := defaultRunOptions(baseDir)
	opts.Output = OutputJSON

	restore := withRedirectedStdout(t)
	Run(context.Background(), opts)
	restore()

	if exitCode != 1 {
		t.Errorf("expected exit 1 on json encode failure, got %d", exitCode)
	}
}

// TestEngineRunNDJSONEncodeError covers the OutputNDJSON per-result
// encode-failure branch by directing stdout to a broken pipe.
func TestEngineRunNDJSONEncodeError(t *testing.T) {
	oldOsExit := osExit
	exitCode := 0
	osExit = func(code int) { exitCode = code }
	t.Cleanup(func() { osExit = oldOsExit })

	oldFetchRepos := fetchRepos
	oldGitClone := gitClone
	defer func() {
		fetchRepos = oldFetchRepos
		gitClone = oldGitClone
	}()
	fetchRepos = func(ctx context.Context, owner string, opts github.FetchOptions) ([]github.Repo, error) {
		return []github.Repo{{Name: "repo_a", Language: "Go", Visibility: "Public", DefaultBranch: "main"}}, nil
	}
	gitClone = func(ctx context.Context, url, targetDir string, opts git.CloneOptions) error { return nil }

	oldIsTerminal := isTerminal
	defer func() { isTerminal = oldIsTerminal }()
	isTerminal = func(fd uintptr) bool { return false }

	baseDir, err := os.MkdirTemp("", "engine_test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(baseDir) }()

	opts := defaultRunOptions(baseDir)
	opts.Output = OutputNDJSON

	restore := withRedirectedStdout(t)
	Run(context.Background(), opts)
	restore()

	if exitCode != 1 {
		t.Errorf("expected exit 1 on ndjson encode failure, got %d", exitCode)
	}
}

// TestMigrateLegacyFailures covers the MkdirAll-failure and Rename-failure
// WARN branches of migrateLegacy.
func TestMigrateLegacyFailures(t *testing.T) {
	// MkdirAll failure: make the target's parent path component a regular file.
	t.Run("mkdirall_fails", func(t *testing.T) {
		baseDir, err := os.MkdirTemp("", "engine_test")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.RemoveAll(baseDir) }()

		// Legacy dir: <base>/go/myrepo
		if err := os.MkdirAll(filepath.Join(baseDir, "go", "myrepo"), 0o750); err != nil {
			t.Fatal(err)
		}
		// Target parent is <base>/Public/go. Create <base>/Public as a regular
		// file so MkdirAll of the parent fails.
		if err := os.WriteFile(filepath.Join(baseDir, "Public"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}

		repos := []github.Repo{{Name: "myrepo", Language: "Go", Visibility: "Public"}}
		migrateLegacy(baseDir, repos) // should log WARN and continue
	})

	// Rename failure: pre-create a non-empty destination directory so os.Rename
	// of the legacy dir onto it fails.
	t.Run("rename_fails", func(t *testing.T) {
		baseDir, err := os.MkdirTemp("", "engine_test")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.RemoveAll(baseDir) }()

		// Legacy dir: <base>/go/myrepo
		if err := os.MkdirAll(filepath.Join(baseDir, "go", "myrepo"), 0o750); err != nil {
			t.Fatal(err)
		}
		// Target dir: <base>/Public/go/myrepo, pre-populated so rename fails.
		target := filepath.Join(baseDir, "Public", "Go", "myrepo")
		if err := os.MkdirAll(target, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, "keep.txt"), []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}

		repos := []github.Repo{{Name: "myrepo", Language: "Go", Visibility: "Public"}}
		migrateLegacy(baseDir, repos) // should log WARN about failed migration
	})
}

// TestProcessRepoMkdirFail covers the failed-creating-target-directory branch
// in processRepo by making the target's parent a regular file.
func TestProcessRepoMkdirFail(t *testing.T) {
	baseDir, err := os.MkdirTemp("", "engine_test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(baseDir) }()

	// Create <base>/Public/go as a regular file so MkdirAll of the target
	// parent (<base>/Public/go) fails.
	if err := os.MkdirAll(filepath.Join(baseDir, "Public"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseDir, "Public", "Go"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	repo := github.Repo{
		Name:          "repo1",
		Language:      "Go",
		Visibility:    "Public",
		DefaultBranch: "main",
		CloneURL:      "https://github.com/owner/repo1.git",
	}
	targetDir := filepath.Join(baseDir, "Public", "Go", "repo1")
	job := Job{Repo: repo, Target: targetDir}

	msg := processRepo(context.Background(), "owner", "https", false, false, git.CloneOptions{}, SyncOptions{}, job)
	if msg.Action != "ERROR" || !strings.Contains(msg.Message, "failed creating target directory") {
		t.Fatalf("expected mkdir error result, got %#v", msg)
	}
}

func TestCleanupEmptyFolders(t *testing.T) {
	baseDir, err := os.MkdirTemp("", "engine_test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(baseDir) }()

	// The duplicate Go language exercises the seen-dedup path.
	repos := []github.Repo{{Language: "Go"}, {Language: "Rust"}, {Language: "Go"}}

	// Empty legacy language dir for a known language: removed.
	emptyLang := filepath.Join(baseDir, "go")
	_ = os.MkdirAll(emptyLang, 0o750)

	// Non-empty legacy language dir: os.Remove leaves it alone.
	nonEmptyLang := filepath.Join(baseDir, "rust")
	_ = os.MkdirAll(nonEmptyLang, 0o750)
	_ = os.WriteFile(filepath.Join(nonEmptyLang, "leftover"), []byte("x"), 0o600)

	// Unrelated dir that is not a repo language: must be left untouched.
	unrelated := filepath.Join(baseDir, ".claude")
	_ = os.MkdirAll(unrelated, 0o750)

	cleanupEmptyFolders(baseDir, repos)

	if _, err := os.Stat(emptyLang); !os.IsNotExist(err) {
		t.Errorf("Expected empty language dir %s to be removed", emptyLang)
	}
	if _, err := os.Stat(nonEmptyLang); os.IsNotExist(err) {
		t.Errorf("Expected non-empty language dir %s to be kept", nonEmptyLang)
	}
	if _, err := os.Stat(unrelated); os.IsNotExist(err) {
		t.Errorf("Expected unrelated dir %s to be kept", unrelated)
	}
}

func TestNormalizeLayoutDirCase(t *testing.T) {
	baseDir, err := os.MkdirTemp("", "engine_test")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.RemoveAll(baseDir) }()

	// Existing lowercase collection and language directories.
	mixed := filepath.Join(baseDir, "public", "javascript", "repo1")
	if err := os.MkdirAll(mixed, 0o750); err != nil {
		t.Fatal(err)
	}

	// Already-canonical bucket: idempotent no-op.
	already := filepath.Join(baseDir, "public", "Go", "repo2")
	if err := os.MkdirAll(already, 0o750); err != nil {
		t.Fatal(err)
	}

	// Unrelated dir whose name doesn't match any fetched language: untouched.
	unrelated := filepath.Join(baseDir, "public", "Configurations", "stuff")
	if err := os.MkdirAll(unrelated, 0o750); err != nil {
		t.Fatal(err)
	}

	// A stray file at base-level (not a visibility dir) must be tolerated.
	if err := os.WriteFile(filepath.Join(baseDir, ".DS_Store"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	repos := []github.Repo{
		{Name: "repo1", Language: "JavaScript", Visibility: "Public"},
		{Name: "repo2", Language: "Go", Visibility: "Public"},
	}
	normalizeLayoutDirCase(baseDir, repos)

	canonical := filepath.Join(baseDir, "Public", "JavaScript", "repo1")
	if _, err := os.Stat(canonical); err != nil {
		t.Errorf("expected %s to exist after case normalization: %v", canonical, err)
	}
	if _, err := os.Stat(filepath.Join(baseDir, "Public", "Go", "repo2")); err != nil {
		t.Errorf("expected idempotent dir %s to remain: %v", already, err)
	}
	canonicalUnrelated := filepath.Join(baseDir, "Public", "Configurations", "stuff")
	if _, err := os.Stat(canonicalUnrelated); err != nil {
		t.Errorf("expected unrelated dir %s to remain untouched: %v", canonicalUnrelated, err)
	}

	// Empty repos list short-circuits without error.
	normalizeLayoutDirCase(baseDir, nil)

	// Unreadable base dir is a no-op (just exercising the early-return).
	normalizeLayoutDirCase("/invalid_dir_that_does_not_exist", repos)
}

func TestRunWiresGitTokenProvider(t *testing.T) {
	oldFetch := fetchRepos
	defer func() { fetchRepos = oldFetch }()
	fetchRepos = func(ctx context.Context, owner string, opts github.FetchOptions) ([]github.Repo, error) {
		return nil, nil
	}
	oldIsTerminal := isTerminal
	defer func() { isTerminal = oldIsTerminal }()
	isTerminal = func(fd uintptr) bool { return false }

	oldTok, had := os.LookupEnv("GITHUB_TOKEN")
	defer func() {
		if had {
			_ = os.Setenv("GITHUB_TOKEN", oldTok)
		} else {
			_ = os.Unsetenv("GITHUB_TOKEN")
		}
	}()
	_ = os.Setenv("GITHUB_TOKEN", "tok-xyz")

	baseDir, _ := os.MkdirTemp("", "engine_tok")
	defer func() { _ = os.RemoveAll(baseDir) }()

	Run(context.Background(), defaultRunOptions(baseDir))
	if git.TokenProvider == nil {
		t.Fatal("expected git.TokenProvider to be wired after Run")
	}
	if got := git.TokenProvider(); got != "tok-xyz" {
		t.Errorf("git.TokenProvider() = %q, want tok-xyz", got)
	}
}

func TestProcessRepoRelocatesByCanonicalIdentity(t *testing.T) {
	base := t.TempDir()
	existing := filepath.Join(base, "Public", "Go", "old")
	target := filepath.Join(base, "acme", "Public", "Go", "repo")
	if err := os.MkdirAll(filepath.Join(existing, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, ".git", "config"), []byte("[remote \"origin\"]\nurl = git@github.com:acme/repo.git\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := processRepo(context.Background(), "acme", "https", false, false, git.CloneOptions{}, SyncOptions{}, Job{
		Repo:   github.Repo{Name: "repo", Owner: "acme", FullName: "acme/repo", Visibility: "Public", Language: "Go"},
		Target: target, Existing: existing,
	})
	if !result.Moved || result.Action != "SKIP" {
		t.Fatalf("unexpected relocation result: %+v", result)
	}
	if !git.IsRepository(target) {
		t.Fatalf("expected repository at relocated target %s", target)
	}
}

func TestProcessRepoRejectsIdentityCollision(t *testing.T) {
	base := t.TempDir()
	existing := filepath.Join(base, "old")
	target := filepath.Join(base, "target")
	for _, path := range []string{existing, target} {
		if err := os.MkdirAll(filepath.Join(path, ".git"), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	result := processRepo(context.Background(), "acme", "https", false, false, git.CloneOptions{}, SyncOptions{}, Job{
		Repo:   github.Repo{Name: "repo", Owner: "acme", FullName: "acme/repo"},
		Target: target, Existing: existing,
	})
	if result.Action != "ERROR" || !strings.Contains(result.Message, "target collision") {
		t.Fatalf("unexpected collision result: %+v", result)
	}
}

func TestProcessRepoAcceptsCaseInsensitivePathAlias(t *testing.T) {
	base := t.TempDir()
	existing := filepath.Join(base, "Public", "Go", "repo")
	target := filepath.Join(base, "public", "go", "repo")
	for _, path := range []string{existing, target} {
		if err := os.MkdirAll(filepath.Join(path, ".git"), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(existing, ".git", "config"), []byte("[remote \"origin\"]\nurl = https://github.com/acme/repo.git\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldSameFile := sameFile
	sameFile = func(os.FileInfo, os.FileInfo) bool { return true }
	t.Cleanup(func() { sameFile = oldSameFile })

	result := processRepo(context.Background(), "acme", "https", false, false, git.CloneOptions{}, SyncOptions{}, Job{
		Repo:   github.Repo{Name: "repo", Owner: "acme", FullName: "acme/repo"},
		Target: target, Existing: existing,
	})
	if result.Action != "SKIP" || result.Target != existing || result.Moved {
		t.Fatalf("case-insensitive alias result: %+v", result)
	}
}

func TestProcessRepoReportsCaseAliasStatFailure(t *testing.T) {
	base := t.TempDir()
	existing := filepath.Join(base, "Public", "Go", "repo")
	target := filepath.Join(base, "public", "go", "repo")
	if err := os.MkdirAll(target, 0o750); err != nil {
		t.Fatal(err)
	}

	oldStat := statPath
	statPath = func(path string) (os.FileInfo, error) {
		if path == existing {
			return nil, errors.New("existing stat")
		}
		return oldStat(path)
	}
	t.Cleanup(func() { statPath = oldStat })

	result := processRepo(context.Background(), "acme", "https", false, false, git.CloneOptions{}, SyncOptions{}, Job{
		Repo:   github.Repo{Name: "repo", Owner: "acme", FullName: "acme/repo"},
		Target: target, Existing: existing,
	})
	if result.Action != "ERROR" || !strings.Contains(result.Message, "failed checking existing clone") {
		t.Fatalf("case alias stat failure result: %+v", result)
	}
}

func TestSearchLayoutIncludesRepositoryOwner(t *testing.T) {
	repo := github.Repo{Name: "shared", Owner: "second", FullName: "second/shared", Visibility: "Public", Language: "Go"}
	opts := defaultRunOptions(t.TempDir())
	opts.Owner = "topic:shared"
	if got := effectiveLayout(opts, repo); got != searchLayout {
		t.Fatalf("effective layout = %q, want %q", got, searchLayout)
	}
	rel, err := evaluateLayout(effectiveLayout(opts, repo), repo, opts.Owner)
	if err != nil {
		t.Fatal(err)
	}
	if rel != "second/Public/Go/shared" {
		t.Fatalf("search path = %q", rel)
	}
}

// --- Phase 2: smart-sync via PushedAt ---------------------------------------

// withGitPullStub replaces gitPull, gitCurrentBranch, and gitIsEmpty
// with harmless stubs that count pull invocations, returning a restore
// function. gitIsEmpty defaults to false so the pre-v0.0.13 tests
// (which use fake .git directories) don't now trip the empty-repo
// SKIP path and see SYNC-turned-to-SKIP regressions.
func withGitPullStub(t *testing.T) (called *int, restore func()) {
	t.Helper()
	oldPull := gitPull
	oldBranch := gitCurrentBranch
	oldEmpty := gitIsEmpty
	n := 0
	gitPull = func(ctx context.Context, targetDir string, opts git.PullOptions) error {
		n++
		return nil
	}
	gitCurrentBranch = func(_ context.Context, targetDir string) (string, error) { return "main", nil }
	gitIsEmpty = func(_ context.Context, targetDir string) bool { return false }
	return &n, func() {
		gitPull = oldPull
		gitCurrentBranch = oldBranch
		gitIsEmpty = oldEmpty
	}
}

func mustExistingClone(t *testing.T, target string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(target, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
}

func TestProcessRepoSkipWhenPushedAtUnchanged(t *testing.T) {
	baseDir := t.TempDir()
	target := filepath.Join(baseDir, "Public", "Go", "repo1")
	mustExistingClone(t, target)

	pushed := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	// Pre-stamp the state with the same pushed_at the API will report.
	if err := writeCloneState(target, cloneState{
		LastSyncedPushedAt: pushed,
		LastSyncedAt:       pushed,
	}); err != nil {
		t.Fatal(err)
	}

	called, restore := withGitPullStub(t)
	defer restore()

	job := Job{
		Repo: github.Repo{
			Name: "repo1", Language: "Go", Visibility: "Public",
			DefaultBranch: "main", PushedAt: pushed,
		},
		Target: target,
	}
	msg := processRepo(context.Background(), "owner", "https", true, false, git.CloneOptions{}, SyncOptions{}, job)
	if msg.Action != "SKIP" {
		t.Fatalf("expected SKIP, got %s (%s)", msg.Action, msg.Message)
	}
	if !strings.Contains(msg.Message, "up-to-date") {
		t.Errorf("expected up-to-date message, got %q", msg.Message)
	}
	if *called != 0 {
		t.Errorf("gitPull must not be called when pushed_at unchanged, was called %d times", *called)
	}
}

func TestProcessRepoSyncsWhenPushedAtAdvances(t *testing.T) {
	baseDir := t.TempDir()
	target := filepath.Join(baseDir, "Public", "Go", "repo1")
	mustExistingClone(t, target)

	old := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	newer := old.Add(time.Hour)
	if err := writeCloneState(target, cloneState{LastSyncedPushedAt: old}); err != nil {
		t.Fatal(err)
	}

	called, restore := withGitPullStub(t)
	defer restore()

	job := Job{
		Repo: github.Repo{
			Name: "repo1", Language: "Go", Visibility: "Public",
			DefaultBranch: "main", PushedAt: newer,
		},
		Target: target,
	}
	msg := processRepo(context.Background(), "owner", "https", true, false, git.CloneOptions{}, SyncOptions{}, job)
	if msg.Action != "SYNC" {
		t.Fatalf("expected SYNC, got %s (%s)", msg.Action, msg.Message)
	}
	if *called != 1 {
		t.Errorf("gitPull must be called exactly once, was called %d times", *called)
	}

	got, err := readCloneState(target)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if !got.LastSyncedPushedAt.Equal(newer) {
		t.Errorf("state should be re-stamped with new pushed_at, got %v", got.LastSyncedPushedAt)
	}
	if got.LastSyncedAt.IsZero() {
		t.Errorf("LastSyncedAt should be set after a successful sync")
	}
}

func TestProcessRepoForceSyncOverridesState(t *testing.T) {
	baseDir := t.TempDir()
	target := filepath.Join(baseDir, "Public", "Go", "repo1")
	mustExistingClone(t, target)

	pushed := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	// State matches the API value: ordinarily this would SKIP, but Force=true
	// must pull anyway.
	if err := writeCloneState(target, cloneState{LastSyncedPushedAt: pushed}); err != nil {
		t.Fatal(err)
	}

	called, restore := withGitPullStub(t)
	defer restore()

	job := Job{
		Repo: github.Repo{
			Name: "repo1", Language: "Go", Visibility: "Public",
			DefaultBranch: "main", PushedAt: pushed,
		},
		Target: target,
	}
	msg := processRepo(context.Background(), "owner", "https", true, false, git.CloneOptions{}, SyncOptions{Force: true}, job)
	if msg.Action != "SYNC" {
		t.Fatalf("expected SYNC under Force, got %s (%s)", msg.Action, msg.Message)
	}
	if *called != 1 {
		t.Errorf("gitPull must be called under Force, was called %d times", *called)
	}
}

func TestProcessRepoStateMissingFallsThrough(t *testing.T) {
	baseDir := t.TempDir()
	target := filepath.Join(baseDir, "Public", "Go", "repo1")
	mustExistingClone(t, target)
	// No state file at all -> the engine must pull (cannot know if upstream
	// changed) and then stamp the state.

	pushed := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	called, restore := withGitPullStub(t)
	defer restore()

	job := Job{
		Repo: github.Repo{
			Name: "repo1", Language: "Go", Visibility: "Public",
			DefaultBranch: "main", PushedAt: pushed,
		},
		Target: target,
	}
	msg := processRepo(context.Background(), "owner", "https", true, false, git.CloneOptions{}, SyncOptions{}, job)
	if msg.Action != "SYNC" {
		t.Fatalf("expected SYNC when state missing, got %s (%s)", msg.Action, msg.Message)
	}
	if *called != 1 {
		t.Errorf("gitPull must be called when state missing, was called %d times", *called)
	}
	if _, err := os.Stat(filepath.Join(target, ".git", StateFileName)); err != nil {
		t.Errorf("expected state file to be written after sync, got %v", err)
	}
}

func TestProcessRepoZeroPushedAtFallsThrough(t *testing.T) {
	// When the GitHub API does not report a pushed_at (rare but possible for
	// brand-new empty repos), the engine must pull rather than mistakenly
	// treat zero-value as "everything up to date".
	baseDir := t.TempDir()
	target := filepath.Join(baseDir, "Public", "Go", "repo1")
	mustExistingClone(t, target)
	if err := writeCloneState(target, cloneState{LastSyncedPushedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	called, restore := withGitPullStub(t)
	defer restore()

	job := Job{
		Repo: github.Repo{
			Name: "repo1", Language: "Go", Visibility: "Public",
			DefaultBranch: "main", // PushedAt: zero
		},
		Target: target,
	}
	msg := processRepo(context.Background(), "owner", "https", true, false, git.CloneOptions{}, SyncOptions{}, job)
	if msg.Action != "SYNC" {
		t.Fatalf("expected SYNC when API pushed_at is zero, got %s", msg.Action)
	}
	if *called != 1 {
		t.Errorf("gitPull must be called when API pushed_at is zero")
	}
}

func TestProcessRepoCloneStampsState(t *testing.T) {
	baseDir := t.TempDir()
	target := filepath.Join(baseDir, "Public", "Go", "repo1")
	// No pre-existing .git/, so processRepo will take the clone path.

	oldClone := gitClone
	defer func() { gitClone = oldClone }()
	gitClone = func(ctx context.Context, url, targetDir string, opts git.CloneOptions) error {
		// Mimic a real clone by creating the .git dir so a follow-up
		// processRepo call wouldn't try to clone again.
		return os.MkdirAll(filepath.Join(targetDir, ".git"), 0o750)
	}

	pushed := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	job := Job{
		Repo: github.Repo{
			Name: "repo1", Language: "Go", Visibility: "Public",
			DefaultBranch: "main", PushedAt: pushed,
			CloneURL: "http://clone",
		},
		Target: target,
	}
	msg := processRepo(context.Background(), "owner", "https", true, false, git.CloneOptions{}, SyncOptions{}, job)
	if msg.Action != "CLONE" {
		t.Fatalf("expected CLONE, got %s (%s)", msg.Action, msg.Message)
	}
	got, err := readCloneState(target)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if !got.LastSyncedPushedAt.Equal(pushed) {
		t.Errorf("clone must stamp state with the API pushed_at, got %v", got.LastSyncedPushedAt)
	}
}

func TestEvaluateLayout(t *testing.T) {
	repo := github.Repo{
		Name:          "repo1",
		Language:      "Go",
		Visibility:    "Public",
		DefaultBranch: "main",
	}

	tests := []struct {
		name       string
		layout     string
		repo       github.Repo
		owner      string
		want       string
		wantErrSub string
	}{
		{
			name:   "default empty layout",
			layout: "",
			repo:   repo,
			owner:  "user1",
			want:   "Public/Go/repo1",
		},
		{
			name:   "custom flat layout",
			layout: "{{.Owner}}/{{.Name}}",
			repo:   repo,
			owner:  "user1",
			want:   "user1/repo1",
		},
		{
			name:   "custom language-only layout",
			layout: "{{.Language}}/{{.Name}}",
			repo:   repo,
			owner:  "user1",
			want:   "go/repo1",
		},
		{
			name:   "github pages uses html layout",
			layout: "",
			repo:   github.Repo{Name: "site.github.io", Language: "JavaScript", Visibility: "Public"},
			owner:  "user1",
			want:   "Public/Web/site.github.io",
		},
		{
			name:   "fork uses forks collection",
			layout: "",
			repo:   github.Repo{Name: "forked", Language: "Rust", Visibility: "Public", Fork: true},
			owner:  "user1",
			want:   "Forks/Rust/forked",
		},
		{
			name:       "escape folder path directory traversal",
			layout:     "../{{.Name}}",
			repo:       repo,
			owner:      "user1",
			wantErrSub: "escapes base directory",
		},
		{
			name:       "escape folder path absolute path",
			layout:     "/{{.Name}}",
			repo:       repo,
			owner:      "user1",
			wantErrSub: "escapes base directory",
		},
		{
			name:       "invalid template syntax",
			layout:     "{{.Name",
			repo:       repo,
			owner:      "user1",
			wantErrSub: "unclosed action",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := evaluateLayout(tt.layout, tt.repo, tt.owner)
			if tt.wantErrSub != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErrSub)
				}
				if !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("expected error containing %q, got: %v", tt.wantErrSub, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseLayoutTemplateRejectsOversizedInput(t *testing.T) {
	if _, err := parseLayoutTemplate(strings.Repeat("x", maxLayoutTemplateBytes+1)); err == nil {
		t.Fatal("expected oversized layout error")
	}
}
