// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package git

import (
	"context"
	"strings"
	"testing"
)

// #nosec G101 -- a fake credential the tests assert never reaches an error.
const leakyURL = "https://user:s3cret-token@127.0.0.1:1/me/repo.git"

// TestMirrorErrorsDoNotEchoTheURL reproduces D-04: a failing mirror
// command reported its whole argv, so `remote add -- <name> <url>` put the
// URL, and any credential in it, into terminal output and JSON results.
func TestMirrorErrorsDoNotEchoTheURL(t *testing.T) {
	err := EnsureRemote(context.Background(), t.TempDir(), "dest", leakyURL)
	if err == nil {
		t.Fatal("remote add outside a repository succeeded")
	}
	if strings.Contains(err.Error(), "s3cret-token") || strings.Contains(err.Error(), "127.0.0.1") {
		t.Errorf("error echoes the URL: %v", err)
	}
	if !strings.Contains(err.Error(), "remote add") || !strings.Contains(err.Error(), "dest") {
		t.Errorf("error lost the subcommand or remote name: %v", err)
	}
}

// TestMirrorErrorsRedactGitOutput covers the other route a credential
// takes into the message: git's own stderr, which can quote the URL.
func TestMirrorErrorsRedactGitOutput(t *testing.T) {
	repo := mirrorRepo(t)
	runGit(t, repo, "remote", "add", "dest", leakyURL)
	err := PushMirror(context.Background(), repo, "dest", leakyURL, nil)
	if err == nil {
		t.Fatal("push to a closed port succeeded")
	}
	if strings.Contains(err.Error(), "s3cret-token") {
		t.Errorf("error echoes the credential: %v", err)
	}
	if !strings.Contains(err.Error(), "push") || !strings.Contains(err.Error(), "dest") {
		t.Errorf("error lost the subcommand or remote name: %v", err)
	}
}
