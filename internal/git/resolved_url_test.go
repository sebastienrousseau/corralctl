// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package git

import (
	"context"
	"errors"
	"os/exec"
	"testing"
)

// ResolvedRemoteURL applies git's insteadOf rewrites, which a raw read of
// .git/config does not.
func TestResolvedRemoteURLAppliesInsteadOf(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"remote", "add", "origin", "work:me/repo.git"},
		{"config", "url.git@github.com:.insteadOf", "work:"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil { // #nosec G204 -- fixed fixture arguments
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	got, err := ResolvedRemoteURL(context.Background(), dir, "origin")
	if err != nil {
		t.Fatal(err)
	}
	if got != "git@github.com:me/repo.git" {
		t.Fatalf("resolved = %q", got)
	}
}

// git ls-remote --get-url echoes the remote's name when it cannot resolve
// it, so a failure is only reachable through the seam.
func TestResolvedRemoteURLReportsFailure(t *testing.T) {
	oldRun := runGitOutput
	t.Cleanup(func() { runGitOutput = oldRun })
	runGitOutput = func(context.Context, string, ...string) (string, error) {
		return "", errors.New("git failed")
	}
	if _, err := ResolvedRemoteURL(context.Background(), t.TempDir(), "origin"); err == nil {
		t.Fatal("a git failure must be reported")
	}
}
