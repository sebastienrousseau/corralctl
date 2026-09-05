// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/github"
)

// newStateRepo creates a real git repository with one commit. The sidecar now
// lives inside the git directory, so these tests need an actual repository
// rather than a bare temp dir.
func newStateRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...) // #nosec G204 -- args are literals from this test
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "--quiet")
	run("commit", "--allow-empty", "--quiet", "-m", "init")
	return dir
}

// gitStatus returns `git status --porcelain` output for dir, including files
// that would only be hidden by a user's global ignore file. Without
// --no-optional-locks and an empty global excludes file this test would pass on
// a machine whose ~/.config/git/ignore happens to list the sidecar — which is
// exactly how the bug this guards against went unnoticed.
func gitStatus(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "--no-optional-locks", "status", "--porcelain")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v\n%s", err, out)
	}
	return string(out)
}

// TestWriteCloneStateLeavesWorkingTreeClean is the regression test for the
// sidecar bug: before v0.0.20 the state file was written into the working tree,
// so every corral-managed clone reported as dirty. That broke `corralctl
// status`, and made both `corralctl prune` and the MCP delete tool refuse to
// act on any repository at all.
func TestWriteCloneStateLeavesWorkingTreeClean(t *testing.T) {
	dir := newStateRepo(t)
	if before := gitStatus(t, dir); before != "" {
		t.Fatalf("fixture repo is not clean to begin with:\n%s", before)
	}

	if err := writeCloneState(dir, cloneState{LastSyncedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("write: %v", err)
	}

	if after := gitStatus(t, dir); after != "" {
		t.Errorf("writing clone state dirtied the working tree:\n%s", after)
	}
	// And prove the state really was persisted, so a no-op write cannot pass
	// this test by accident.
	if _, err := os.Stat(filepath.Join(dir, ".git", StateFileName)); err != nil {
		t.Errorf("expected sidecar inside .git: %v", err)
	}
}

// TestWriteCloneStateMigratesLegacySidecar covers the upgrade path: a clone
// written by an older corralctl keeps its smart-sync state, and the stale
// working-tree file is cleaned up.
func TestWriteCloneStateMigratesLegacySidecar(t *testing.T) {
	dir := newStateRepo(t)
	legacy := filepath.Join(dir, LegacyStateFileName)
	pushed := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	if err := os.WriteFile(legacy,
		[]byte(`{"last_synced_pushed_at":"2026-03-01T12:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Legacy state is readable before any write happens.
	got, err := readCloneState(dir)
	if err != nil {
		t.Fatalf("read legacy: %v", err)
	}
	if !got.LastSyncedPushedAt.Equal(pushed) {
		t.Errorf("legacy pushed_at: got %v, want %v", got.LastSyncedPushedAt, pushed)
	}

	// The first write migrates it and removes the working-tree copy.
	if err := writeCloneState(dir, got); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("expected legacy sidecar to be removed, stat err = %v", err)
	}
	if status := gitStatus(t, dir); status != "" {
		t.Errorf("working tree dirty after migration:\n%s", status)
	}
	got, err = readCloneState(dir)
	if err != nil {
		t.Fatalf("read after migration: %v", err)
	}
	if !got.LastSyncedPushedAt.Equal(pushed) {
		t.Errorf("pushed_at lost in migration: got %v, want %v", got.LastSyncedPushedAt, pushed)
	}
}

// TestStatePathResolvesWorktreeGitdir covers the case that made the working-tree
// location tempting in the first place: in a linked worktree .git is a file, not
// a directory, so the sidecar has to follow the gitdir pointer.
func TestStatePathResolvesWorktreeGitdir(t *testing.T) {
	main := newStateRepo(t)
	wt := filepath.Join(t.TempDir(), "wt")
	cmd := exec.Command("git", "worktree", "add", "--quiet", wt) // #nosec G204 -- wt is a t.TempDir() path
	cmd.Dir = main
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git worktree add unavailable: %v\n%s", err, out)
	}

	info, err := os.Stat(filepath.Join(wt, ".git"))
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() {
		t.Skip("worktree .git is a directory on this git version")
	}

	if err := writeCloneState(wt, cloneState{LastSyncedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("write in worktree: %v", err)
	}
	if status := gitStatus(t, wt); status != "" {
		t.Errorf("worktree dirty after write:\n%s", status)
	}
	if _, err := readCloneState(wt); err != nil {
		t.Errorf("read back in worktree: %v", err)
	}
}

func TestCloneStateRoundTrip(t *testing.T) {
	dir := newStateRepo(t)
	now := time.Date(2026, 6, 29, 13, 0, 0, 0, time.UTC)
	want := cloneState{
		LastSyncedPushedAt: now,
		LastSyncedAt:       now.Add(time.Minute),
	}
	if err := writeCloneState(dir, want); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := readCloneState(dir)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !got.LastSyncedPushedAt.Equal(want.LastSyncedPushedAt) {
		t.Errorf("pushed_at: got %v, want %v", got.LastSyncedPushedAt, want.LastSyncedPushedAt)
	}
	if !got.LastSyncedAt.Equal(want.LastSyncedAt) {
		t.Errorf("synced_at: got %v, want %v", got.LastSyncedAt, want.LastSyncedAt)
	}
}

func TestReadCloneStateMissingReturnsZero(t *testing.T) {
	dir := newStateRepo(t) // repository, but no state file inside
	s, err := readCloneState(dir)
	if err != nil {
		t.Fatalf("expected nil error for missing state, got %v", err)
	}
	if !s.LastSyncedPushedAt.IsZero() || !s.LastSyncedAt.IsZero() {
		t.Errorf("expected zero state, got %+v", s)
	}
}

// TestReadCloneStateNonRepoReturnsZero keeps a directory that is not a
// repository from surfacing as an error: callers treat it as "never synced" and
// fall through to a full sync.
func TestReadCloneStateNonRepoReturnsZero(t *testing.T) {
	s, err := readCloneState(t.TempDir())
	if err != nil {
		t.Fatalf("expected nil error for non-repo, got %v", err)
	}
	if !s.LastSyncedAt.IsZero() {
		t.Errorf("expected zero state, got %+v", s)
	}
}

func TestReadCloneStateMalformedSurfacesError(t *testing.T) {
	dir := newStateRepo(t)
	path, err := statePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCloneState(dir); err == nil {
		t.Fatal("expected error for malformed state")
	}
}

// TestReadCloneStateMalformedLegacySurfacesError covers the fallback branch:
// a corrupt legacy sidecar must not be silently swallowed either.
func TestReadCloneStateMalformedLegacySurfacesError(t *testing.T) {
	dir := newStateRepo(t)
	if err := os.WriteFile(filepath.Join(dir, LegacyStateFileName), []byte("{{{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCloneState(dir); err == nil {
		t.Fatal("expected error for malformed legacy state")
	}
}

func TestWriteCloneStateAtomicReplacement(t *testing.T) {
	dir := newStateRepo(t)
	first := cloneState{LastSyncedPushedAt: time.Unix(1, 0).UTC()}
	if err := writeCloneState(dir, first); err != nil {
		t.Fatalf("write first: %v", err)
	}
	second := cloneState{LastSyncedPushedAt: time.Unix(2, 0).UTC()}
	if err := writeCloneState(dir, second); err != nil {
		t.Fatalf("write second: %v", err)
	}
	got, err := readCloneState(dir)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !got.LastSyncedPushedAt.Equal(second.LastSyncedPushedAt) {
		t.Errorf("expected second write to replace first, got %v", got.LastSyncedPushedAt)
	}
	// No leftover tmp files beside the sidecar.
	gitDir := filepath.Join(dir, ".git")
	entries, err := os.ReadDir(gitDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file in git dir: %s", e.Name())
		}
	}
}

func TestWriteCloneStateMissingDirError(t *testing.T) {
	err := writeCloneState("/no/such/path/anywhere", cloneState{})
	if err == nil {
		t.Fatal("expected error writing to nonexistent dir")
	}
}

// TestMigrateLegacyRefusesNonClones is the regression test for the silent
// relocation bug: migrateLegacy used a name match alone as grounds for
// os.Rename, so an unrelated directory that happened to share a name with one
// of the owner's repositories was moved — with no confirmation, and invisible in
// --dry-run because the preview path requires git.IsRepository.
func TestMigrateLegacyRefusesNonClones(t *testing.T) {
	base := t.TempDir()
	repo := github.Repo{
		Name: "tools", Owner: "acme", FullName: "acme/tools",
		Language: "Go", Visibility: "Public",
	}

	// A plain directory with the colliding name and a file the user cares about.
	legacy := filepath.Join(base, normalizeLanguage(repo.Language), repo.Name)
	if err := os.MkdirAll(legacy, 0o750); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(legacy, "important.txt")
	if err := os.WriteFile(keep, []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}

	migrateLegacy(base, []github.Repo{repo})

	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("a non-clone directory was relocated: %v", err)
	}
	target := filepath.Join(base, repositoryCollection(repo), repositoryBucket(repo), repo.Name)
	if _, err := os.Stat(target); err == nil {
		t.Error("migrateLegacy created the target from a directory that is not a clone")
	}
}

// TestMigrateLegacyRefusesForeignClone covers the second piece of evidence: a
// real git repository whose origin points somewhere else must also be left
// alone.
func TestMigrateLegacyRefusesForeignClone(t *testing.T) {
	base := t.TempDir()
	repo := github.Repo{
		Name: "tools", Owner: "acme", FullName: "acme/tools",
		Language: "Go", Visibility: "Public",
	}
	legacy := filepath.Join(base, normalizeLanguage(repo.Language), repo.Name)
	if err := os.MkdirAll(filepath.Join(legacy, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	// Same name, entirely different project.
	cfg := "[remote \"origin\"]\n\turl = https://github.com/someone-else/tools.git\n"
	if err := os.WriteFile(filepath.Join(legacy, ".git", "config"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	migrateLegacy(base, []github.Repo{repo})

	if _, err := os.Stat(filepath.Join(legacy, ".git")); err != nil {
		t.Errorf("a clone with a foreign origin was relocated: %v", err)
	}
}

// And the case migration exists for: a genuine clone of the repository does move.
func TestMigrateLegacyMovesMatchingClone(t *testing.T) {
	base := t.TempDir()
	repo := github.Repo{
		Name: "tools", Owner: "acme", FullName: "acme/tools",
		Language: "Go", Visibility: "Public",
	}
	legacy := filepath.Join(base, normalizeLanguage(repo.Language), repo.Name)
	if err := os.MkdirAll(filepath.Join(legacy, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := "[remote \"origin\"]\n\turl = https://github.com/acme/tools.git\n"
	if err := os.WriteFile(filepath.Join(legacy, ".git", "config"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	migrateLegacy(base, []github.Repo{repo})

	target := filepath.Join(base, repositoryCollection(repo), repositoryBucket(repo), repo.Name)
	if _, err := os.Stat(filepath.Join(target, ".git")); err != nil {
		t.Errorf("a genuine clone should have been migrated: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy path should be gone after migration, stat err = %v", err)
	}
}
