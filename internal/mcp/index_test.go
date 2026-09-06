// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/diag"
)

// makeFakeRepo creates a directory layout that looks like a corral-
// managed clone: <base>/<vis>/<lang>/<name>/.git with an optional
// .git/config to seed the remote URL and an optional sidecar.
func makeFakeRepo(t *testing.T, base, vis, lang, name, originURL, sidecar string) string {
	t.Helper()
	repo := filepath.Join(base, vis, lang, name)
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	if originURL != "" {
		cfg := "[remote \"origin\"]\n\turl = " + originURL + "\n"
		if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if sidecar != "" {
		// Since v0.0.20 the sidecar lives inside the git directory so it
		// stays out of `git status`.
		if err := os.WriteFile(filepath.Join(repo, ".git", stateFileName), []byte(sidecar), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

// makeLegacyRepo is makeFakeRepo with the sidecar at the pre-v0.0.20
// working-tree path, so the migration read path stays covered.
func makeLegacyRepo(t *testing.T, base, vis, lang, name, originURL, sidecar string) string {
	t.Helper()
	repo := makeFakeRepo(t, base, vis, lang, name, originURL, "")
	if sidecar != "" {
		if err := os.WriteFile(filepath.Join(repo, legacyStateFileName), []byte(sidecar), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return repo
}

// TestReadStateFallsBackToLegacySidecar covers clones written by a pre-v0.0.20
// corralctl: their smart-sync state must still be reported.
func TestReadStateFallsBackToLegacySidecar(t *testing.T) {
	base := t.TempDir()
	repo := makeLegacyRepo(t, base, "Public", "go", "legacy",
		"https://github.com/o/legacy.git", `{"last_synced_at":"2026-06-30T00:00:00Z"}`)
	state, ok := readState(repo)
	if !ok {
		t.Fatal("expected legacy sidecar to be read")
	}
	if state.LastSyncedAt != "2026-06-30T00:00:00Z" {
		t.Errorf("last_synced_at = %q", state.LastSyncedAt)
	}
}

// TestReadStatePrefersGitDirOverLegacy pins the precedence when both exist.
func TestReadStatePrefersGitDirOverLegacy(t *testing.T) {
	base := t.TempDir()
	repo := makeFakeRepo(t, base, "Public", "go", "both",
		"https://github.com/o/both.git", `{"last_synced_at":"2026-08-01T00:00:00Z"}`)
	if err := os.WriteFile(filepath.Join(repo, legacyStateFileName),
		[]byte(`{"last_synced_at":"2020-01-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	state, ok := readState(repo)
	if !ok {
		t.Fatal("expected state to be read")
	}
	if state.LastSyncedAt != "2026-08-01T00:00:00Z" {
		t.Errorf("expected git-dir sidecar to win, got %q", state.LastSyncedAt)
	}
}

func TestScanFindsExpectedLayout(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "https://github.com/o/alpha.git", `{"last_synced_at":"2026-06-30T00:00:00Z"}`)
	makeFakeRepo(t, base, "Public", "rust", "beta", "", "")
	makeFakeRepo(t, base, "Private", "python", "gamma", "", "")

	idx, err := Scan(base)
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(idx.Repos) != 3 {
		t.Fatalf("expected 3 repos, got %d: %+v", len(idx.Repos), idx.Repos)
	}
	// Sorted by RelPath: Private/python/gamma, Public/go/alpha, Public/rust/beta
	want := []string{"Private/python/gamma", "Public/go/alpha", "Public/rust/beta"}
	for i, r := range idx.Repos {
		if r.RelPath != want[i] {
			t.Errorf("repo[%d] RelPath = %q, want %q", i, r.RelPath, want[i])
		}
	}
	// First entry has no state, second has remote+state.
	alpha := idx.Repos[1]
	if alpha.RemoteURL != "https://github.com/o/alpha.git" {
		t.Errorf("alpha remote = %q", alpha.RemoteURL)
	}
	if alpha.State == nil || alpha.State.LastSyncedAt == "" {
		t.Errorf("alpha state not parsed: %+v", alpha.State)
	}
	if alpha.Visibility != "Public" || alpha.Language != "go" {
		t.Errorf("alpha vis/lang wrong: %q/%q", alpha.Visibility, alpha.Language)
	}
}

func TestScanRejectsNonDirectoryRoot(t *testing.T) {
	tmp, err := os.CreateTemp("", "mcp_root_*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_ = tmp.Close()

	_, err = Scan(tmp.Name())
	if err == nil {
		t.Fatal("expected error scanning a file as root")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("expected 'not a directory' message, got %v", err)
	}
}

func TestScanTolerates_UnreadableSubtree(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "ok", "", "")
	// Drop a regular file at the path the walker would treat as a
	// candidate parent — confirms a per-entry error doesn't abort the
	// whole scan.
	if err := os.WriteFile(filepath.Join(base, "garbage"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	idx, err := Scan(base)
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(idx.Repos) != 1 || idx.Repos[0].Name != "ok" {
		t.Errorf("expected one clone 'ok', got %+v", idx.Repos)
	}
}

func TestScanHonoursMaxDepth(t *testing.T) {
	base := t.TempDir()
	// Construct a path deeper than maxIndexDepth. The repo should NOT
	// be picked up because the walker stops descending.
	deep := filepath.Join(base, "a", "b", "c", "d", "e", "deep")
	if err := os.MkdirAll(filepath.Join(deep, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	idx, err := Scan(base)
	if err != nil {
		t.Fatalf("Scan failed: %v", err)
	}
	if len(idx.Repos) != 0 {
		t.Errorf("expected depth limit to hide deep repo, got %+v", idx.Repos)
	}
}

func TestIndexFindUniqueAndAmbiguous(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	makeFakeRepo(t, base, "Private", "go", "alpha", "", "") // intentional dupe
	makeFakeRepo(t, base, "Public", "rust", "beta", "", "")

	idx, err := Scan(base)
	if err != nil {
		t.Fatal(err)
	}

	// Unique by bare name when only one match.
	match, err := idx.Find("beta")
	if err != nil {
		t.Fatalf("Find(beta) err: %v", err)
	}
	if match.RelPath != "Public/rust/beta" {
		t.Errorf("Find(beta) returned %s", match.RelPath)
	}

	// Ambiguous bare name surfaces all candidates.
	_, err = idx.Find("alpha")
	if !errors.Is(err, ErrAmbiguous) {
		t.Errorf("Find(alpha) expected ErrAmbiguous, got %v", err)
	}

	// Path suffix disambiguates.
	match, err = idx.Find("Public/go/alpha")
	if err != nil {
		t.Fatalf("Find(Public/go/alpha) err: %v", err)
	}
	if match.Visibility != "Public" {
		t.Errorf("expected Public match, got %s", match.Visibility)
	}

	// Unknown returns ErrRepoNotFound.
	_, err = idx.Find("does-not-exist")
	if !errors.Is(err, ErrRepoNotFound) {
		t.Errorf("expected ErrRepoNotFound, got %v", err)
	}

	// Empty query is treated as not-found.
	_, err = idx.Find("")
	if !errors.Is(err, ErrRepoNotFound) {
		t.Errorf("expected ErrRepoNotFound on empty query, got %v", err)
	}
}

// TestReadStateLogsOnMalformedJSON asserts that a present-but-malformed
// .corral-state.json is logged to the log package (which the mcp
// subcommand routes to stderr, keeping stdout clean for JSON-RPC). A
// missing sidecar must NOT log — that's the expected state for any
// clone made before smart-sync existed and would flood the output.
func TestReadStateLogsOnMalformedJSON(t *testing.T) {
	// readState's parse failure is debug detail: a malformed sidecar is
	// recovered from silently at the default level, so the capture has to
	// raise the threshold to see it.
	buf := captureDiag(t, diag.LevelDebug)

	// Missing sidecar: silent.
	dirNoState := t.TempDir()
	if _, ok := readState(dirNoState); ok {
		t.Error("expected readState to report missing sidecar as absent")
	}
	if buf.Len() != 0 {
		t.Errorf("missing sidecar should not log, got: %s", buf.String())
	}

	// Malformed sidecar: logs with the path so operators can grep.
	buf.Reset()
	dirBad := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dirBad, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dirBad, ".git", stateFileName), []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readState(dirBad); ok {
		t.Error("expected readState to fail on malformed json")
	}
	if !strings.Contains(buf.String(), "parse state") {
		t.Errorf("expected 'parse state' log line, got: %q", buf.String())
	}
	if !strings.Contains(buf.String(), stateFileName) {
		t.Errorf("expected path in log line, got: %q", buf.String())
	}
}

func TestSafePathRejectsTraversal(t *testing.T) {
	base := t.TempDir()
	// Create a file inside and outside the root.
	if err := os.WriteFile(filepath.Join(base, "inside.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir() // entirely separate root
	if err := os.WriteFile(filepath.Join(outside, "outside.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	idx := &Index{Root: base}

	// Inside path is allowed.
	if _, err := idx.SafePath(filepath.Join(base, "inside.txt")); err != nil {
		t.Errorf("inside path should be allowed: %v", err)
	}
	// Relative inside path is allowed and joined to root.
	if _, err := idx.SafePath("inside.txt"); err != nil {
		t.Errorf("relative inside path should be allowed: %v", err)
	}
	// Outside absolute path is rejected.
	if _, err := idx.SafePath(filepath.Join(outside, "outside.txt")); err == nil {
		t.Error("absolute outside path should be rejected")
	}
	// .. traversal is rejected.
	if _, err := idx.SafePath(filepath.Join(base, "..", "escape")); err == nil {
		t.Error("traversal via .. should be rejected")
	}
}

func TestMarkStateSyncedPreservesPushedAt(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	wantPushed := "2026-07-01T12:00:00Z"
	body := `{"last_synced_pushed_at":"` + wantPushed + `","last_synced_at":"2026-07-01T13:00:00Z"}`
	if err := os.WriteFile(filepath.Join(repo, ".git", stateFileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := markStateSynced(repo); err != nil {
		t.Fatal(err)
	}
	state, ok := readState(repo)
	if !ok {
		t.Fatal("state was not readable after update")
	}
	if state.LastSyncedPushedAt != wantPushed {
		t.Errorf("pushed_at changed: got %q want %q", state.LastSyncedPushedAt, wantPushed)
	}
	if state.LastSyncedAt == "" || state.LastSyncedAt == "2026-07-01T13:00:00Z" {
		t.Errorf("last_synced_at was not refreshed: %+v", state)
	}
}

// TestScanExcludesWorkspaceRoot is the regression test for a workspace root
// that is itself a git repository — dotfiles, a monorepo, or simply
// `corralctl mcp --root .`.
//
// Scan matched the root on its first callback, appended it as an entry and then
// SkipDir aborted the whole walk, so the workspace collapsed to exactly one
// "repo" named after the root's basename and every real clone became invisible.
func TestScanExcludesWorkspaceRoot(t *testing.T) {
	base := t.TempDir()
	// The root is a repository too.
	if err := os.MkdirAll(filepath.Join(base, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	makeFakeRepo(t, base, "Public", "go", "alpha", "https://github.com/o/alpha.git", "")
	makeFakeRepo(t, base, "Public", "rust", "beta", "https://github.com/o/beta.git", "")

	idx, err := Scan(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Repos) != 2 {
		names := make([]string, 0, len(idx.Repos))
		for _, r := range idx.Repos {
			names = append(names, r.RelPath)
		}
		t.Fatalf("expected the 2 nested clones and not the root, got %d: %v", len(idx.Repos), names)
	}
	for _, r := range idx.Repos {
		if r.RelPath == "." || r.Path == base {
			t.Errorf("workspace root leaked into the index as %q", r.RelPath)
		}
	}
}

// TestSafeMutationPathRefusesRoot is the second half of that fix: even if the
// root did resolve as a target, a mutation must never accept it, because
// corral_delete_repo would then rm -rf the entire workspace.
func TestSafeMutationPathRefusesRoot(t *testing.T) {
	base := t.TempDir()
	idx := &Index{Root: base}

	// Reads of the root are fine.
	if _, err := idx.SafePath(base); err != nil {
		t.Errorf("SafePath must still allow the root: %v", err)
	}
	// Mutations of the root are not.
	for _, target := range []string{base, base + string(filepath.Separator), "."} {
		if _, err := idx.SafeMutationPath(target); err == nil {
			t.Errorf("SafeMutationPath(%q) must refuse the workspace root", target)
		}
	}
	// A real child is still a valid mutation target.
	child := filepath.Join(base, "Public", "go", "alpha")
	if err := os.MkdirAll(child, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := idx.SafeMutationPath(child); err != nil {
		t.Errorf("a child path must remain mutable: %v", err)
	}
}

// TestSafePathAllowsDotDotPrefixedNames pins the segment-wise boundary check: a
// repository legitimately named "..foo" is inside the root, and the old raw
// strings.HasPrefix(rel, "..") rejected it.
func TestSafePathAllowsDotDotPrefixedNames(t *testing.T) {
	base := t.TempDir()
	odd := filepath.Join(base, "..foo")
	if err := os.MkdirAll(odd, 0o750); err != nil {
		t.Fatal(err)
	}
	idx := &Index{Root: base}
	if _, err := idx.SafePath(odd); err != nil {
		t.Errorf("a repo named '..foo' is inside the root: %v", err)
	}
	// Real traversal is still refused.
	if _, err := idx.SafePath(filepath.Join(base, "..", "outside")); err == nil {
		t.Error("genuine ../ traversal must still be refused")
	}
}
