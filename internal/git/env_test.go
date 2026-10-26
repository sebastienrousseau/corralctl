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
