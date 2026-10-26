// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package git

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realPath resolves symlinks so a macOS /var -> /private/var temp dir
// compares equal to the path git reports.
func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestChildEnvIgnoresRepositoryRedirects reproduces corralctl running
// inside a git hook or alias, where git exports GIT_DIR and friends. With
// those inherited, `git -C target` inspects whichever repository GIT_DIR
// names, so the unpublished-work guards would vouch for the wrong clone.
func TestChildEnvIgnoresRepositoryRedirects(t *testing.T) {
	ctx := context.Background()
	target, decoy := t.TempDir(), t.TempDir()
	initRepoWithCommit(t, target, "a.txt")
	initRepoWithCommit(t, decoy, "b.txt")
	runGit(t, target, "remote", "add", "origin", "https://example.invalid/target.git")
	runGit(t, decoy, "remote", "add", "origin", "https://example.invalid/decoy.git")

	decoyGit := filepath.Join(decoy, ".git")
	for name, value := range map[string]string{
		"GIT_DIR":                          decoyGit,
		"GIT_WORK_TREE":                    decoy,
		"GIT_INDEX_FILE":                   filepath.Join(decoyGit, "index"),
		"GIT_OBJECT_DIRECTORY":             filepath.Join(decoyGit, "objects"),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES": filepath.Join(decoyGit, "objects"),
		"GIT_COMMON_DIR":                   decoyGit,
		"GIT_NAMESPACE":                    "decoy",
	} {
		t.Setenv(name, value)
	}

	out, err := runGitOutput(ctx, target, "rev-parse", "--absolute-git-dir")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(out), realPath(t, filepath.Join(target, ".git")); got != want {
		t.Errorf("gitOutput inspected %s, want %s", got, want)
	}
	if origin, err := RemoteOrigin(ctx, target); err != nil || !strings.Contains(origin, "target.git") {
		t.Errorf("RemoteOrigin = %q, %v; want the target's origin", origin, err)
	}
	if err := runMirror(ctx, target, nil, "config", "corral.probe", "1"); err != nil {
		t.Fatal(err)
	}
	cfg, err := os.ReadFile(filepath.Join(target, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfg), "probe = 1") {
		t.Errorf("runMirror wrote to another repository's config")
	}
}

// TestCorralConfigAppendsToTheUsersEnvConfig reproduces S-10: a user who
// sets git config through GIT_CONFIG_COUNT/KEY_n/VALUE_n (a proxy, a CA
// bundle) lost it whenever corralctl injected its own credential, because
// corral wrote GIT_CONFIG_COUNT=1 and KEY_0 over theirs.
func TestCorralConfigAppendsToTheUsersEnvConfig(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	initRepoWithCommit(t, repo, "a.txt")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "corral.user")
	t.Setenv("GIT_CONFIG_VALUE_0", "kept")
	defer func() { TokenProvider = nil }()
	TokenProvider = func() string { return "secret" }

	out, err := runGitOutput(ctx, repo, "config", "--get", "corral.user")
	if err != nil || strings.TrimSpace(out) != "kept" {
		t.Errorf("user's env config lost under a token: %q, %v", out, err)
	}
	if _, err := runGitOutput(ctx, repo, "config", "--get", "http.https://github.com/.extraheader"); err != nil {
		t.Errorf("corral's own header is missing: %v", err)
	}

	cred, err := pushAuthEnv("https://forge.example/me/r.git", &PushCredential{Username: "u", Secret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if err := runMirror(ctx, repo, cred, "config", "--get", "corral.user"); err != nil {
		t.Errorf("user's env config lost under a push credential: %v", err)
	}
	if err := runMirror(ctx, repo, cred, "config", "--get", "http.https://forge.example/.extraheader"); err != nil {
		t.Errorf("push credential header is missing: %v", err)
	}
}

func TestInheritedConfigCount(t *testing.T) {
	for _, tc := range []struct {
		env  []string
		want int
	}{
		{nil, 0},
		{[]string{"GIT_CONFIG_COUNT=3"}, 3},
		{[]string{"GIT_CONFIG_COUNT=1", "GIT_CONFIG_COUNT=2"}, 2},
		{[]string{"GIT_CONFIG_COUNT="}, 0},
		{[]string{"GIT_CONFIG_COUNT=x"}, 0},
		{[]string{"GIT_CONFIG_COUNT=-4"}, 0},
	} {
		if got := inheritedConfigCount(tc.env); got != tc.want {
			t.Errorf("inheritedConfigCount(%q) = %d, want %d", tc.env, got, tc.want)
		}
	}
	if env := withConfig([]string{"A=b"}); len(env) != 1 {
		t.Errorf("no pairs must leave the environment alone, got %q", env)
	}
}
