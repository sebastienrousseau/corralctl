// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// cleanup removes a temporary directory and reports a failure if removal
// errors, satisfying errcheck for deferred cleanup.
func cleanup(t *testing.T, dir string) {
	t.Helper()
	if err := os.RemoveAll(dir); err != nil {
		t.Errorf("failed to remove %s: %v", dir, err)
	}
}

func run(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...) // #nosec G204 -- test helper invokes caller-selected fixtures only
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v failed: %v (%s)", name, args, err, string(out))
	}
}

func setupTestRepo(t *testing.T) (bareDir string, workDir string) {
	t.Helper()

	bareDir, err := os.MkdirTemp("", "git_test_upstream_bare")
	if err != nil {
		t.Fatal(err)
	}
	run(t, "git", "-C", bareDir, "init", "--bare")

	workDir, err = os.MkdirTemp("", "git_test_upstream_work")
	if err != nil {
		t.Fatal(err)
	}
	run(t, "git", "-C", workDir, "init")
	run(t, "git", "-C", workDir, "config", "user.name", "Test")
	run(t, "git", "-C", workDir, "config", "user.email", "test@test.com")

	file := filepath.Join(workDir, "test.txt")
	if err := os.WriteFile(file, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, "git", "-C", workDir, "add", "test.txt")
	run(t, "git", "-C", workDir, "commit", "-m", "init")
	run(t, "git", "-C", workDir, "branch", "-M", "main")
	run(t, "git", "-C", workDir, "remote", "add", "origin", bareDir)
	run(t, "git", "-C", workDir, "push", "-u", "origin", "main")
	run(t, "git", "-C", bareDir, "symbolic-ref", "HEAD", "refs/heads/main")

	return bareDir, workDir
}

func TestGitCommands(t *testing.T) {
	upstream, workDir := setupTestRepo(t)
	defer cleanup(t, upstream)
	defer cleanup(t, workDir)

	targetDir, err := os.MkdirTemp("", "git_test_target")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(t, targetDir)
	if err := os.RemoveAll(targetDir); err != nil {
		t.Fatal(err)
	}

	err = Clone(context.Background(), upstream, targetDir, CloneOptions{RecurseSubmodules: true})
	if err != nil {
		t.Errorf("Failed to clone: %v", err)
	}

	branch, err := CurrentBranch(context.Background(), targetDir)
	if err != nil || branch == "" {
		t.Errorf("Expected non-empty branch, got %q (err: %v)", branch, err)
	}

	remote, err := RemoteOrigin(context.Background(), targetDir)
	if err != nil || remote != upstream {
		t.Errorf("Expected remote %s, got %s (err: %v)", upstream, remote, err)
	}
	run(t, "git", "-C", targetDir, "config", "merge.verifySignatures", "false")

	file := filepath.Join(workDir, "test2.txt")
	if err := os.WriteFile(file, []byte("test2"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, "git", "-C", workDir, "add", "test2.txt")
	run(t, "git", "-C", workDir, "commit", "-m", "add test2")
	run(t, "git", "-C", workDir, "push", "origin", "main")

	err = Pull(context.Background(), targetDir, PullOptions{RecurseSubmodules: true})
	if err != nil {
		t.Errorf("Failed to pull: %v", err)
	}

	err = Clone(context.Background(), "invalid_url_that_does_not_exist", "/invalid/target/dir", CloneOptions{})
	if err == nil {
		t.Errorf("Expected clone to fail")
	}

	err = Pull(context.Background(), "/invalid/target/dir", PullOptions{})
	if err == nil {
		t.Errorf("Expected pull to fail")
	}

	_, err = CurrentBranch(context.Background(), "/invalid/target/dir")
	if err == nil {
		t.Errorf("Expected current branch to fail")
	}

	_, err = RemoteOrigin(context.Background(), "/invalid/target/dir")
	if err == nil {
		t.Errorf("Expected remote origin to fail")
	}
}

// TestCloneOptions exercises each optional clone flag branch independently by
// cloning from a local upstream repository.
func TestCloneOptions(t *testing.T) {
	upstream, workDir := setupTestRepo(t)
	defer cleanup(t, upstream)
	defer cleanup(t, workDir)

	cases := []struct {
		name string
		opts CloneOptions
	}{
		{"SingleBranch", CloneOptions{SingleBranch: true}},
		{"Blobless", CloneOptions{Blobless: true}},
		{"Depth", CloneOptions{Depth: 1}},
		{"RecurseSubmodules", CloneOptions{RecurseSubmodules: true}},
		{"All", CloneOptions{RecurseSubmodules: true, SingleBranch: true, Blobless: true, Depth: 1}},
		{"None", CloneOptions{}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			targetDir, err := os.MkdirTemp("", "git_test_opts_target")
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup(t, targetDir)
			// git clone requires the target to not exist (or be empty).
			if err := os.RemoveAll(targetDir); err != nil {
				t.Fatal(err)
			}

			if err := Clone(context.Background(), upstream, targetDir, tc.opts); err != nil {
				t.Errorf("Clone with %s failed: %v", tc.name, err)
			}
		})
	}
}

func TestPullIgnoresSignatureVerification(t *testing.T) {
	upstream, workDir := setupTestRepo(t)
	defer cleanup(t, upstream)
	defer cleanup(t, workDir)

	targetDir, err := os.MkdirTemp("", "git_test_verify")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(t, targetDir)
	if err := os.RemoveAll(targetDir); err != nil {
		t.Fatal(err)
	}
	if err := Clone(context.Background(), upstream, targetDir, CloneOptions{}); err != nil {
		t.Fatalf("clone failed: %v", err)
	}

	// Enable signature verification locally. The commits are unsigned, so a
	// plain "git pull --rebase" would abort with a fatal error.
	run(t, "git", "-C", targetDir, "config", "rebase.verifySignatures", "true")
	run(t, "git", "-C", targetDir, "config", "merge.verifySignatures", "true")

	// Create a new unsigned upstream commit to pull.
	file := filepath.Join(workDir, "more.txt")
	if err := os.WriteFile(file, []byte("more"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, "git", "-C", workDir, "add", "more.txt")
	run(t, "git", "-C", workDir, "commit", "-m", "more")
	run(t, "git", "-C", workDir, "push", "origin", "main")

	// Pull must succeed despite verifySignatures=true, because it overrides it.
	if err := Pull(context.Background(), targetDir, PullOptions{}); err != nil {
		t.Fatalf("Pull should ignore signature verification, got: %v", err)
	}
}

func TestAuthEnv(t *testing.T) {
	defer func() { TokenProvider = nil }()

	TokenProvider = nil
	if env := authEnv(); env != nil {
		t.Errorf("expected nil env when TokenProvider is unset, got %v", env)
	}

	TokenProvider = func() string { return "" }
	if env := authEnv(); env != nil {
		t.Errorf("expected nil env for an empty token, got %v", env)
	}

	TokenProvider = func() string { return "secret" }
	env := authEnv()
	if len(env) != 3 || env[0] != "GIT_CONFIG_COUNT=1" {
		t.Fatalf("expected three auth env vars, got %v", env)
	}
}

func envContains(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}

func TestWithGitEnvAlwaysSetsNonInteractive(t *testing.T) {
	defer func() { TokenProvider = nil }()

	// No token: still must set the non-interactive guards so cron clones of
	// public repos don't hang on an SSH passphrase or askpass helper.
	TokenProvider = nil
	cmd := exec.Command("git", "version")
	withGitEnv(cmd)
	for _, want := range []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/true",
		"SSH_ASKPASS=/bin/true",
		"GCM_INTERACTIVE=Never",
	} {
		if !envContains(cmd.Env, want) {
			t.Errorf("expected withGitEnv to set %q, got %v", want, cmd.Env)
		}
	}
	// Inherited environment is preserved (PATH must always be present so
	// children can find their dependencies).
	if !envContains(cmd.Env, "PATH="+os.Getenv("PATH")) {
		t.Errorf("expected PATH to be inherited into cmd.Env")
	}
}

func TestWithGitEnvIncludesAuthHeader(t *testing.T) {
	defer func() { TokenProvider = nil }()

	TokenProvider = func() string { return "secret" }
	cmd := exec.Command("git", "version")
	withGitEnv(cmd)
	if !envContains(cmd.Env, "GIT_CONFIG_COUNT=1") {
		t.Errorf("expected GIT_CONFIG_COUNT=1 when a token is available, got %v", cmd.Env)
	}
	// Non-interactive guards still present alongside the auth header.
	if !envContains(cmd.Env, "GIT_TERMINAL_PROMPT=0") {
		t.Errorf("expected non-interactive guards to coexist with auth env")
	}
}

func TestResolveGitBinarySuccess(t *testing.T) {
	old := lookPath
	oldBinary := gitBinary
	defer func() {
		lookPath = old
		gitBinary = oldBinary
	}()
	lookPath = func(name string) (string, error) {
		if name != "git" {
			t.Errorf("expected lookup for %q, got %q", "git", name)
		}
		return "/usr/local/bin/git", nil
	}
	if err := ResolveGitBinary(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gitBinary != "/usr/local/bin/git" {
		t.Errorf("expected gitBinary to be cached, got %q", gitBinary)
	}
}

func TestResolveGitBinaryMissing(t *testing.T) {
	old := lookPath
	oldBinary := gitBinary
	defer func() {
		lookPath = old
		gitBinary = oldBinary
	}()
	lookPath = func(name string) (string, error) {
		return "", exec.ErrNotFound
	}
	err := ResolveGitBinary()
	if err == nil {
		t.Fatal("expected error when git is missing from PATH")
	}
	if !strings.Contains(err.Error(), "git not found on PATH") {
		t.Errorf("error should explain missing git, got %v", err)
	}
}

func TestRemoteOriginFromConfigHTTPS(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `[core]
	repositoryformatversion = 0
[remote "origin"]
	url = https://github.com/seb/repo.git
	fetch = +refs/heads/*:refs/remotes/origin/*
`)
	got, err := RemoteOriginFromConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "https://github.com/seb/repo.git" {
		t.Errorf("got %q", got)
	}
}

func TestRemoteOriginFromConfigSSH(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "[remote \"origin\"]\r\n\turl = git@github.com:seb/repo.git\r\n")
	got, err := RemoteOriginFromConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "git@github.com:seb/repo.git" {
		t.Errorf("expected ssh url, got %q", got)
	}
}

func TestRemoteOriginFromConfigIgnoresOtherSections(t *testing.T) {
	dir := t.TempDir()
	// A different remote section appearing before origin must not leak its
	// url into the result.
	writeConfig(t, dir, `[remote "fork"]
	url = git@github.com:other/fork.git
[remote "origin"]
	url = https://github.com/seb/repo.git
[remote "upstream"]
	url = https://github.com/up/repo.git
`)
	got, err := RemoteOriginFromConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "https://github.com/seb/repo.git" {
		t.Errorf("got wrong url: %q", got)
	}
}

func TestRemoteOriginFromConfigSkipsCommentsAndBlanks(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, `
# top comment
; semicolon comment

[remote "origin"]
	# nested comment
	url = https://github.com/seb/repo.git
`)
	got, err := RemoteOriginFromConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "https://github.com/seb/repo.git" {
		t.Errorf("got %q", got)
	}
}

func TestRemoteOriginFromConfigMissingFile(t *testing.T) {
	dir := t.TempDir() // no .git/config inside
	_, err := RemoteOriginFromConfig(dir)
	if err == nil {
		t.Fatal("expected error when config is absent")
	}
	if !os.IsNotExist(err) {
		t.Errorf("expected wrapped os.ErrNotExist, got %v", err)
	}
}

func TestRemoteOriginFromConfigMissingOriginSection(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "[core]\n\trepositoryformatversion = 0\n")
	_, err := RemoteOriginFromConfig(dir)
	if err == nil {
		t.Fatal("expected error when origin section is missing")
	}
	if !strings.Contains(err.Error(), "origin url not found") {
		t.Errorf("error should mention missing origin url, got %v", err)
	}
}

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestPullArgsContainCommitSigningOverride(t *testing.T) {
	// Pull doesn't expose its args directly, so this verifies the override at
	// runtime against a real clone: configure global-style commit signing on a
	// local repo with no signing key available, then assert Pull still
	// succeeds (it would fail with "gpg failed to sign the data" otherwise).
	upstream, workDir := setupTestRepo(t)
	defer cleanup(t, upstream)
	defer cleanup(t, workDir)

	targetDir, err := os.MkdirTemp("", "git_test_sign")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(t, targetDir)
	if err := os.RemoveAll(targetDir); err != nil {
		t.Fatal(err)
	}
	if err := Clone(context.Background(), upstream, targetDir, CloneOptions{}); err != nil {
		t.Fatalf("clone failed: %v", err)
	}

	// Force a signing configuration that would normally hang or fail: ssh
	// signing with a nonexistent key. The -c overrides in Pull must disable
	// this so rebase succeeds.
	run(t, "git", "-C", targetDir, "config", "commit.gpgsign", "true")
	run(t, "git", "-C", targetDir, "config", "gpg.format", "ssh")
	run(t, "git", "-C", targetDir, "config", "user.signingkey", "/nonexistent/key")
	run(t, "git", "-C", targetDir, "config", "user.name", "Test")
	run(t, "git", "-C", targetDir, "config", "user.email", "test@test.com")

	// Make an upstream change so rebase actually replays a local commit on
	// pull. First make a local divergent commit so rebase has something to
	// re-apply through the signing path.
	if err := os.WriteFile(filepath.Join(targetDir, "local.txt"), []byte("l"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, "git", "-C", targetDir, "add", "local.txt")
	// The local commit must NOT be signed (we'd hit the bad key); use git
	// directly with explicit overrides to make the test setup deterministic.
	setup := exec.Command("git", "-C", targetDir, // #nosec G204 -- fixed executable and test-owned path
		"-c", "commit.gpgsign=false",
		"-c", "user.name=Test",
		"-c", "user.email=test@test.com",
		"commit", "-m", "local")
	if out, err := setup.CombinedOutput(); err != nil {
		t.Fatalf("setup commit failed: %v (%s)", err, out)
	}

	upstreamFile := filepath.Join(workDir, "upstream.txt")
	if err := os.WriteFile(upstreamFile, []byte("u"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, "git", "-C", workDir, "add", "upstream.txt")
	run(t, "git", "-C", workDir, "commit", "-m", "upstream")
	run(t, "git", "-C", workDir, "push", "origin", "main")

	if err := Pull(context.Background(), targetDir, PullOptions{}); err != nil {
		t.Fatalf("Pull must disable commit.gpgsign during rebase replay, got: %v", err)
	}
}

// TestPullIgnoreSubmoduleFailures verifies that when the parent pull
// succeeds but the post-pull submodule update step fails, the failure is
// swallowed (logged WARN) iff IgnoreSubmoduleFailures is set.
//
// The setup: a parent repo with a .gitmodules pointing at a path that does
// not exist (a "private" submodule URL we cannot resolve). A plain
// `git submodule update --init --recursive` will fail; we assert Pull only
// returns that failure when the flag is unset.
func TestPullIgnoreSubmoduleFailures(t *testing.T) {
	upstream, workDir := setupTestRepo(t)
	defer cleanup(t, upstream)
	defer cleanup(t, workDir)

	target, err := os.MkdirTemp("", "git_test_submod")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(t, target)
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	if err := Clone(context.Background(), upstream, target, CloneOptions{}); err != nil {
		t.Fatalf("clone failed: %v", err)
	}

	// Add a .gitmodules file pointing at an unreachable URL. We can't actually
	// run `git submodule add` against a non-existent URL, but we can hand-roll
	// a .gitmodules entry that submodule update will choke on.
	gm := filepath.Join(target, ".gitmodules")
	body := "[submodule \"missing\"]\n\tpath = missing\n\turl = file:///nonexistent/path/to/submodule.git\n"
	if err := os.WriteFile(gm, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// We need a divergent upstream commit so `pull --rebase` has work to do.
	// Otherwise the pull is a no-op and submodule update isn't exercised.
	if err := os.WriteFile(filepath.Join(workDir, "u.txt"), []byte("u"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, "git", "-C", workDir, "add", "u.txt")
	run(t, "git", "-C", workDir, "commit", "-m", "upstream")
	run(t, "git", "-C", workDir, "push", "origin", "main")

	// With the flag off, the failure should propagate. We do NOT call Pull
	// with RecurseSubmodules without the ignore flag here because the
	// invocation uses `git pull --recurse-submodules` which calls submodule
	// update inline — and our fake .gitmodules doesn't go through `git
	// submodule init` so the inline path may pass. The flag-on path is what
	// we actually need to exercise here: confirm the explicit submodule
	// update step runs AND its failure is swallowed.
	err = Pull(context.Background(), target, PullOptions{
		RecurseSubmodules:       true,
		IgnoreSubmoduleFailures: true,
	})
	if err != nil {
		t.Fatalf("Pull should swallow submodule failures with the flag on, got: %v", err)
	}
}

func TestCanonicalRemoteVariants(t *testing.T) {
	// #nosec G101 -- the fake credential verifies that canonicalization strips userinfo.
	tests := map[string]string{
		"":                                      "",
		"https://GitHub.com/Owner/Repo.git/":    "github.com/owner/repo",
		"ssh://git@github.com/Owner/Repo.git":   "github.com/owner/repo",
		"git@GitHub.com:Owner/Repo.git":         "github.com/owner/repo",
		"relative/Owner/Repo.git":               "relative/owner/repo",
		"https://user:pass@example.com/a/b.git": "example.com/a/b",
	}
	for input, want := range tests {
		if got := CanonicalRemote(input); got != want {
			t.Errorf("CanonicalRemote(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestHasLocalChanges(t *testing.T) {
	upstream, work := setupTestRepo(t)
	defer cleanup(t, upstream)
	defer cleanup(t, work)
	if dirty, detail := HasLocalChanges(context.Background(), work); dirty || detail != "" {
		t.Fatalf("clean repository reported unsafe: %v %q", dirty, detail)
	}
	if err := os.WriteFile(filepath.Join(work, "untracked"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if dirty, detail := HasLocalChanges(context.Background(), work); !dirty || !strings.Contains(detail, "local changes") {
		t.Fatalf("dirty repository not detected: %v %q", dirty, detail)
	}
	if dirty, detail := HasLocalChanges(context.Background(), filepath.Join(work, "missing")); !dirty || !strings.Contains(detail, "cannot inspect") {
		t.Fatalf("inspection failure must be unsafe: %v %q", dirty, detail)
	}
}

func TestHasUnpublishedWorkFailsClosedOnInvalidRepository(t *testing.T) {
	if unsafe, detail := HasUnpublishedWork(context.Background(), t.TempDir()); !unsafe || !strings.Contains(detail, "cannot inspect") {
		t.Fatalf("invalid repository must fail closed: %v %q", unsafe, detail)
	}
}

func TestResolveGitDirErrorsAndRelativeWorktree(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := resolveGitDir(missing); err == nil {
		t.Fatal("expected missing .git error")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveGitDir(dir); err == nil || !strings.Contains(err.Error(), "invalid gitdir") {
		t.Fatalf("expected invalid worktree error, got %v", err)
	}
	gitDir := filepath.Join(dir, "meta")
	if err := os.Mkdir(gitDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: meta\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveGitDir(dir)
	if err != nil || got != gitDir {
		t.Fatalf("relative worktree git dir = %q, %v", got, err)
	}
}

func TestHasUnpublishedWorkVerificationFailures(t *testing.T) {
	oldRun := runGitOutput
	t.Cleanup(func() { runGitOutput = oldRun })
	exitOne := exec.Command("sh", "-c", "exit 1").Run()
	tests := []struct {
		name string
		stub func(context.Context, string, ...string) (string, error)
		want string
	}{
		{"branches", func(ctx context.Context, dir string, args ...string) (string, error) {
			// The stash probe now runs before the commit count, so let it
			// report "no stash" (exit 1) and fail on rev-list.
			switch args[0] {
			case "status":
				return "", nil
			case "rev-parse":
				return "", exitOne
			}
			return "", errors.New("branch check")
		}, "unable to verify local branches"},
		{"stash", func(ctx context.Context, dir string, args ...string) (string, error) {
			if args[0] == "status" {
				return "", nil
			}
			return "", errors.New("stash check")
		}, "unable to verify stash"},
		{"local tags", func(ctx context.Context, dir string, args ...string) (string, error) {
			switch args[0] {
			case "status":
				return "", nil
			case "rev-list":
				return "0", nil
			case "rev-parse":
				return "", exitOne
			}
			return "", errors.New("tags check")
		}, "unable to inspect local tags"},
		{"remote tags", func(ctx context.Context, dir string, args ...string) (string, error) {
			switch args[0] {
			case "status":
				return "", nil
			case "rev-list":
				return "0", nil
			case "rev-parse":
				return "", exitOne
			case "show-ref":
				return "abc refs/tags/v1\n", nil
			}
			return "", errors.New("remote check")
		}, "unable to verify remote tags"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			runGitOutput = tc.stub
			unsafe, detail := HasUnpublishedWork(context.Background(), "/repo")
			if !unsafe || !strings.Contains(detail, tc.want) {
				t.Fatalf("result = %v %q, want %q", unsafe, detail, tc.want)
			}
		})
	}
}

func TestPullSwallowsInjectedSubmoduleFailure(t *testing.T) {
	upstream, work := setupTestRepo(t)
	defer cleanup(t, upstream)
	defer cleanup(t, work)
	oldUpdate := updateSubmodulesFn
	t.Cleanup(func() { updateSubmodulesFn = oldUpdate })
	updateSubmodulesFn = func(context.Context, string) error { return errors.New("submodule failed") }
	if err := Pull(context.Background(), work, PullOptions{RecurseSubmodules: true, IgnoreSubmoduleFailures: true}); err != nil {
		t.Fatalf("injected submodule failure must be swallowed: %v", err)
	}
	if err := updateSubmodules(context.Background(), filepath.Join(work, "missing")); err == nil {
		t.Fatal("expected direct submodule update failure")
	}
}

func TestRemoteOriginConfigAdditionalErrors(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoteOriginFromConfig(dir); err == nil {
		t.Fatal("expected missing config error")
	}
	config := "[remote \"origin\"]\nthis line has no equals\n" + strings.Repeat("x", 70_000)
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoteOriginFromConfig(dir); err == nil {
		t.Fatal("expected scanner error")
	}
}

func TestResolveGitDirReadFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: meta"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldRead := readFile
	t.Cleanup(func() { readFile = oldRead })
	readFile = func(string) ([]byte, error) { return nil, errors.New("read failed") }
	if _, err := resolveGitDir(dir); err == nil || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("expected read failure, got %v", err)
	}
}

// TestIsEmptyOnFreshRepo covers the class of clone corral surfaced as
// "sync failed: no such ref was fetched" — a repository that was
// initialised but has never had a commit. IsEmpty must return true so
// engine.processRepo can SKIP instead of erroring.
func TestIsEmptyOnFreshRepo(t *testing.T) {
	dir, err := os.MkdirTemp("", "git_test_empty")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(t, dir)
	run(t, "git", "-C", dir, "init")

	if !IsEmpty(context.Background(), dir) {
		t.Error("expected fresh git init to report empty")
	}
}

// TestIsEmptyOnCommittedRepo confirms IsEmpty returns false as soon as
// the repo has any commit, guarding against a naive check that would
// flag every repo as empty and skip everyone's syncs.
func TestIsEmptyOnCommittedRepo(t *testing.T) {
	dir, err := os.MkdirTemp("", "git_test_committed")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(t, dir)
	run(t, "git", "-C", dir, "init")
	run(t, "git", "-C", dir, "config", "user.name", "Test")
	run(t, "git", "-C", dir, "config", "user.email", "test@test.com")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, "git", "-C", dir, "add", "file.txt")
	run(t, "git", "-C", dir, "commit", "-m", "init")

	if IsEmpty(context.Background(), dir) {
		t.Error("committed repo should not report empty")
	}
}

// TestIsEmptyOnNonRepo verifies IsEmpty stays defensive: a directory
// that isn't a git repository at all returns true, matching the
// "cannot safely pull here" contract. Callers should have their own
// `.git` check before dispatching, but IsEmpty must not lie.
func TestIsEmptyOnNonRepo(t *testing.T) {
	dir, err := os.MkdirTemp("", "git_test_nonrepo")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup(t, dir)

	if !IsEmpty(context.Background(), dir) {
		t.Error("non-repo directory should report empty (defence-in-depth)")
	}
}

func TestWorktreeRepositoryAndOriginDetection(t *testing.T) {
	upstream, workDir := setupTestRepo(t)
	defer cleanup(t, upstream)
	defer cleanup(t, workDir)
	linked := filepath.Join(t.TempDir(), "linked")
	run(t, "git", "-C", workDir, "worktree", "add", "-b", "linked", linked)
	if !IsRepository(linked) {
		t.Fatal("linked worktree should be recognized as a repository")
	}
	got, err := RemoteOriginFromConfig(linked)
	if err != nil {
		t.Fatal(err)
	}
	if got != upstream {
		t.Errorf("origin = %q, want %q", got, upstream)
	}
}

func TestHasUnpublishedWork(t *testing.T) {
	newClone := func(t *testing.T) (string, string, string) {
		t.Helper()
		upstream, workDir := setupTestRepo(t)
		t.Cleanup(func() { cleanup(t, upstream) })
		t.Cleanup(func() { cleanup(t, workDir) })
		target := filepath.Join(t.TempDir(), "clone")
		if err := Clone(context.Background(), upstream, target, CloneOptions{}); err != nil {
			t.Fatal(err)
		}
		run(t, "git", "-C", target, "config", "user.name", "Test")
		run(t, "git", "-C", target, "config", "user.email", "test@test.com")
		return upstream, workDir, target
	}

	// TestHasUnpublishedWork/detached_HEAD is the regression test for the
	// widened rev-list. The old check used --branches, which covers
	// refs/heads/** only: a commit made in detached HEAD is reachable from HEAD
	// and from no branch, so the count came back 0, the guard said "safe", and
	// the delete destroyed work that existed nowhere else.
	t.Run("detached HEAD commit", func(t *testing.T) {
		_, _, target := newClone(t)
		run(t, "git", "-C", target, "switch", "--detach", "HEAD")
		if err := os.WriteFile(filepath.Join(target, "detached.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		run(t, "git", "-C", target, "add", "detached.txt")
		run(t, "git", "-C", target, "commit", "-m", "detached work")
		unpublished, detail := HasUnpublishedWork(context.Background(), target)
		if !unpublished {
			t.Fatal("a commit reachable only from a detached HEAD must block deletion")
		}
		if detail == "" {
			t.Error("refusal must explain itself")
		}
	})

	t.Run("clean clone", func(t *testing.T) {
		_, _, target := newClone(t)
		if unpublished, detail := HasUnpublishedWork(context.Background(), target); unpublished {
			t.Fatalf("clean clone reported unpublished: %s", detail)
		}
	})

	t.Run("commit on another branch", func(t *testing.T) {
		_, _, target := newClone(t)
		run(t, "git", "-C", target, "switch", "-c", "local-only")
		if err := os.WriteFile(filepath.Join(target, "local.txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		run(t, "git", "-C", target, "add", "local.txt")
		run(t, "git", "-C", target, "commit", "-m", "local")
		run(t, "git", "-C", target, "switch", "main")
		if unpublished, detail := HasUnpublishedWork(context.Background(), target); !unpublished || !strings.Contains(detail, "local refs or HEAD") {
			t.Fatalf("expected unpublished branch commit, got %t %q", unpublished, detail)
		}
	})

	t.Run("stash", func(t *testing.T) {
		_, _, target := newClone(t)
		if err := os.WriteFile(filepath.Join(target, "test.txt"), []byte("changed"), 0o600); err != nil {
			t.Fatal(err)
		}
		run(t, "git", "-C", target, "stash", "push", "-m", "local")
		if unpublished, detail := HasUnpublishedWork(context.Background(), target); !unpublished || !strings.Contains(detail, "stash") {
			t.Fatalf("expected stash detection, got %t %q", unpublished, detail)
		}
	})

	t.Run("local tag", func(t *testing.T) {
		_, _, target := newClone(t)
		run(t, "git", "-C", target, "tag", "local-only")
		if unpublished, detail := HasUnpublishedWork(context.Background(), target); !unpublished || !strings.Contains(detail, "tag local-only") {
			t.Fatalf("expected local tag detection, got %t %q", unpublished, detail)
		}
	})

	t.Run("published tag", func(t *testing.T) {
		_, workDir, target := newClone(t)
		run(t, "git", "-C", workDir, "tag", "published")
		run(t, "git", "-C", workDir, "push", "origin", "refs/tags/published")
		run(t, "git", "-C", target, "fetch", "--tags")
		if unpublished, detail := HasUnpublishedWork(context.Background(), target); unpublished {
			t.Fatalf("published tag reported unpublished: %s", detail)
		}
	})
}

// TestHasIgnoredContent covers the third delete-guard hole: `git status
// --porcelain` excludes ignored files by design, so a clone holding a local
// .env or a SQLite database reported clean — and those are exactly the files no
// remote has a copy of.
func TestHasIgnoredContent(t *testing.T) {
	upstream, workDir := setupTestRepo(t)
	t.Cleanup(func() { cleanup(t, upstream) })
	t.Cleanup(func() { cleanup(t, workDir) })
	target := filepath.Join(t.TempDir(), "clone")
	if err := Clone(context.Background(), upstream, target, CloneOptions{}); err != nil {
		t.Fatal(err)
	}
	run(t, "git", "-C", target, "config", "user.name", "Test")
	run(t, "git", "-C", target, "config", "user.email", "test@test.com")

	// A clean clone has nothing ignored.
	if ignored, detail := HasIgnoredContent(context.Background(), target); ignored {
		t.Fatalf("clean clone reported ignored content: %s", detail)
	}

	// Commit a .gitignore, then create the ignored file.
	if err := os.WriteFile(filepath.Join(target, ".gitignore"), []byte(".env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, "git", "-C", target, "add", ".gitignore")
	run(t, "git", "-C", target, "commit", "-m", "ignore env")
	if err := os.WriteFile(filepath.Join(target, ".env"), []byte("SECRET=1"), 0o600); err != nil {
		t.Fatal(err)
	}

	// git status --porcelain still says clean — that is the trap.
	if dirty, _ := HasLocalChanges(context.Background(), target); dirty {
		t.Fatal("fixture invalid: porcelain should not see an ignored file")
	}
	// HasIgnoredContent must see it.
	ignored, detail := HasIgnoredContent(context.Background(), target)
	if !ignored {
		t.Fatal("an ignored .env must block deletion")
	}
	if !strings.Contains(detail, ".env") {
		t.Errorf("detail should name the path, got %q", detail)
	}
}

func TestBranchOperations(t *testing.T) {
	upstream, workDir := setupTestRepo(t)
	t.Cleanup(func() { cleanup(t, upstream) })
	t.Cleanup(func() { cleanup(t, workDir) })

	ctx := context.Background()

	// List branches in workDir (should have main)
	branches, err := ListBranches(ctx, workDir, false)
	if err != nil {
		t.Fatalf("ListBranches failed: %v", err)
	}
	if len(branches) == 0 {
		t.Fatal("expected at least one branch")
	}
	var foundMain bool
	for _, b := range branches {
		if b.Name == "main" && b.Current {
			foundMain = true
		}
	}
	if !foundMain {
		t.Errorf("expected current main branch, got %+v", branches)
	}

	// List with all=true
	allBranches, err := ListBranches(ctx, workDir, true)
	if err != nil {
		t.Fatalf("ListBranches with all=true failed: %v", err)
	}
	if len(allBranches) < len(branches) {
		t.Errorf("expected at least %d branches, got %d", len(branches), len(allBranches))
	}

	// Invalid branch names
	if err := CreateBranch(ctx, workDir, "", ""); err == nil {
		t.Error("expected error for empty branch name")
	}
	if err := CreateBranch(ctx, workDir, "-bad", ""); err == nil {
		t.Error("expected error for branch name starting with dash")
	}
	if err := SwitchBranch(ctx, workDir, ""); err == nil {
		t.Error("expected error for empty switch branch name")
	}
	if err := SwitchBranch(ctx, workDir, "-bad"); err == nil {
		t.Error("expected error for switch branch name starting with dash")
	}

	// Create new branch
	if err := CreateBranch(ctx, workDir, "feature-x", ""); err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}

	// Create with startPoint
	if err := CreateBranch(ctx, workDir, "feature-y", "main"); err != nil {
		t.Fatalf("CreateBranch with startPoint failed: %v", err)
	}

	// Switch to feature-x
	if err := SwitchBranch(ctx, workDir, "feature-x"); err != nil {
		t.Fatalf("SwitchBranch failed: %v", err)
	}

	// Check current branch is now feature-x
	branches, err = ListBranches(ctx, workDir, false)
	if err != nil {
		t.Fatalf("ListBranches after switch failed: %v", err)
	}
	var foundFeatureX bool
	for _, b := range branches {
		if b.Name == "feature-x" && b.Current {
			foundFeatureX = true
		}
	}
	if !foundFeatureX {
		t.Errorf("expected feature-x to be current branch, got %+v", branches)
	}

	// Test error from runGitOutput
	nonExistent := filepath.Join(t.TempDir(), "nonexistent")
	if _, err := ListBranches(ctx, nonExistent, false); err == nil {
		t.Error("expected error listing branches in non-existent directory")
	}
	if err := CreateBranch(ctx, nonExistent, "feat", ""); err == nil {
		t.Error("expected error creating branch in non-existent directory")
	}
	if err := SwitchBranch(ctx, nonExistent, "feat"); err == nil {
		t.Error("expected error switching branch in non-existent directory")
	}
}

func TestParseBranchListEdgeCases(t *testing.T) {
	cases := []struct {
		input    string
		expected []BranchInfo
	}{
		{
			input:    "",
			expected: nil,
		},
		{
			input:    "\n   \n",
			expected: nil,
		},
		{
			input:    "*\n",
			expected: nil,
		},
		{
			input: "* main abc1234 origin/main\n  feat def5678\n  justname\n",
			expected: []BranchInfo{
				{Name: "main", Current: true, Commit: "abc1234", Remote: "origin/main"},
				{Name: "feat", Current: false, Commit: "def5678"},
				{Name: "justname", Current: false},
			},
		},
	}

	for _, tc := range cases {
		got := parseBranchList(tc.input)
		if len(got) != len(tc.expected) {
			t.Fatalf("parseBranchList(%q) length %d, expected %d", tc.input, len(got), len(tc.expected))
		}
		for i := range got {
			if got[i] != tc.expected[i] {
				t.Errorf("item %d got %+v, expected %+v", i, got[i], tc.expected[i])
			}
		}
	}
}

