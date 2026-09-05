// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/github"
)

// makeClone writes a bare-minimum clone with the given origin remote.
func makeClone(t *testing.T, root, rel, origin string) string {
	t.Helper()
	dir := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := "[remote \"origin\"]\n\turl = " + origin + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestFindOrphansUsesCanonicalIdentity covers the replacement of a raw
// substring test with git.CanonicalRemote.
//
// The old check was strings.Contains(url, "/"+owner+"/"), which matched any
// host — so a gitlab.com clone under a same-named owner counted as a GitHub
// orphan — and matched unrelated path segments.
func TestFindOrphansUsesCanonicalIdentity(t *testing.T) {
	root := t.TempDir()

	// Known upstream: must never be reported.
	makeClone(t, root, "Public/Go/api", "https://github.com/acme/api.git")
	// Genuinely gone from upstream, same owner: must be reported.
	gone := makeClone(t, root, "Public/Go/retired", "https://github.com/acme/retired.git")
	// Same owner name on a different forge: not a GitHub orphan.
	makeClone(t, root, "Public/Go/elsewhere", "https://gitlab.com/acme/elsewhere.git")
	// A different owner on GitHub: not this owner's orphan.
	makeClone(t, root, "Public/Go/foreign", "https://github.com/other/foreign.git")
	// An owner name appearing mid-path but not as the namespace.
	makeClone(t, root, "Public/Go/decoy", "https://github.com/vendor/acme/decoy.git")
	// Unreadable remote: skipped rather than reported.
	noRemote := filepath.Join(root, "Public/Go/broken")
	if err := os.MkdirAll(filepath.Join(noRemote, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}

	repos := []github.Repo{
		{Name: "api", Owner: "acme", FullName: "acme/api"},
	}

	got := findOrphans("acme", root, repos)

	if len(got) != 1 || got[0] != gone {
		t.Fatalf("findOrphans = %v, want exactly [%s]", got, gone)
	}
}

// TestFindOrphansMatchesRenamedDirectory keeps the pre-existing behaviour
// that a locally-renamed directory whose remote still points at a known
// repository is not an orphan.
func TestFindOrphansMatchesRenamedDirectory(t *testing.T) {
	root := t.TempDir()
	makeClone(t, root, "Public/Go/renamed-locally", "https://github.com/acme/api.git")

	repos := []github.Repo{{Name: "api", Owner: "acme", FullName: "acme/api"}}
	if got := findOrphans("acme", root, repos); len(got) != 0 {
		t.Fatalf("findOrphans = %v, want none: the remote still identifies a known repo", got)
	}
}
