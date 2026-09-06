// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/github"
)

// writeCloneFixture creates a directory that git.IsRepository accepts, with
// the given origin URL in its config. Passing an empty origin omits the
// remote block entirely, which is how a clone with no origin looks.
func writeCloneFixture(t *testing.T, dir, origin string) {
	t.Helper()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o750); err != nil {
		t.Fatal(err)
	}
	body := "[core]\n\trepositoryformatversion = 0\n"
	if origin != "" {
		body += "[remote \"origin\"]\n\turl = " + origin + "\n"
	}
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// migratableRepo is the repository the fixtures below claim to be a clone of.
func migratableRepo() github.Repo {
	return github.Repo{
		Name: "myrepo", Owner: "acme", FullName: "acme/myrepo",
		Language: "Go", Visibility: "Public",
	}
}

func TestMigratableCloneRejectsMissingOrigin(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "myrepo")
	// A repository whose config has no origin remote at all: reading the
	// origin fails, and a clone we cannot identify must not be moved.
	writeCloneFixture(t, dir, "")

	ok, why := migratableClone(dir, migratableRepo())
	if ok {
		t.Fatal("a clone with no readable origin was accepted for migration")
	}
	if !strings.Contains(why, "cannot read its origin remote") {
		t.Fatalf("reason does not name the cause: %q", why)
	}
}

func TestMigratableCloneRejectsUncomparableIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "myrepo")
	writeCloneFixture(t, dir, "https://github.com/acme/myrepo.git")

	// A repository with neither FullName nor Owner has no remote identity,
	// so there is nothing to compare the clone's origin against. Refusing
	// is the only safe answer: the alternative is moving a directory on the
	// strength of its name.
	anonymous := github.Repo{Name: "myrepo", Language: "Go", Visibility: "Public"}

	ok, why := migratableClone(dir, anonymous)
	if ok {
		t.Fatal("migration proceeded with no identity to compare against")
	}
	if !strings.Contains(why, "origin remote could not be compared") {
		t.Fatalf("reason does not name the cause: %q", why)
	}
}

func TestMigrateLegacyReportsUnusableTargetParent(t *testing.T) {
	base := t.TempDir()
	repo := migratableRepo()

	legacy := filepath.Join(base, normalizeLanguage(repo.Language), repo.Name)
	if err := os.MkdirAll(legacy, 0o750); err != nil {
		t.Fatal(err)
	}
	writeCloneFixture(t, legacy, "https://github.com/acme/myrepo.git")

	// Occupy the target's parent path with a regular file, so creating the
	// destination directory fails with ENOTDIR.
	target := filepath.Join(base, repositoryCollection(repo), repositoryBucket(repo), repo.Name)
	parent := filepath.Dir(target)
	if err := os.MkdirAll(filepath.Dir(parent), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(parent, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	migrateLegacy(base, []github.Repo{repo})

	// The clone must still be where it was: a failed migration leaves the
	// working copy alone rather than half-moving it.
	if _, err := os.Stat(filepath.Join(legacy, ".git")); err != nil {
		t.Fatalf("legacy clone disappeared after a failed migration: %v", err)
	}
}

func TestMigrateLegacyReportsRenameFailure(t *testing.T) {
	base := t.TempDir()
	repo := migratableRepo()

	legacy := filepath.Join(base, normalizeLanguage(repo.Language), repo.Name)
	if err := os.MkdirAll(legacy, 0o750); err != nil {
		t.Fatal(err)
	}
	writeCloneFixture(t, legacy, "https://github.com/acme/myrepo.git")

	// A non-empty directory already sitting at the destination: rename
	// refuses to replace it, on every supported platform.
	target := filepath.Join(base, repositoryCollection(repo), repositoryBucket(repo), repo.Name)
	if err := os.MkdirAll(target, 0o750); err != nil {
		t.Fatal(err)
	}
	occupant := filepath.Join(target, "occupied.txt")
	if err := os.WriteFile(occupant, []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}

	migrateLegacy(base, []github.Repo{repo})

	if _, err := os.Stat(filepath.Join(legacy, ".git")); err != nil {
		t.Fatalf("legacy clone disappeared after a failed migration: %v", err)
	}
	if _, err := os.Stat(occupant); err != nil {
		t.Fatalf("failed migration clobbered the existing destination: %v", err)
	}
}

func TestWriteCloneStateRejectsNonRepository(t *testing.T) {
	// Not a git repository, so there is no .git directory to put the
	// sidecar in. The error must surface rather than the state being
	// written somewhere arbitrary.
	if err := writeCloneState(t.TempDir(), cloneState{}); err == nil {
		t.Fatal("writing clone state outside a repository should fail")
	}
}

func TestParseLayoutTemplateExportedWrapper(t *testing.T) {
	tmpl, err := ParseLayoutTemplate("{{.Owner}}/{{.Name}}")
	if err != nil {
		t.Fatalf("valid layout rejected: %v", err)
	}
	if tmpl == nil {
		t.Fatal("valid layout returned a nil template")
	}
	if _, err := ParseLayoutTemplate("{{"); err == nil {
		t.Fatal("malformed layout accepted")
	}
}

func TestExitErrorReporting(t *testing.T) {
	cause := errors.New("underlying cause")

	withCause := &ExitError{Code: 1, Err: cause}
	if withCause.Error() != "underlying cause" {
		t.Fatalf("Error() = %q, want the cause's message", withCause.Error())
	}
	if !errors.Is(withCause, cause) {
		t.Fatal("errors.Is did not reach the wrapped cause")
	}

	silent := &ExitError{Code: cancelExitCode, Silent: true}
	if !strings.Contains(silent.Error(), "130") {
		t.Fatalf("Error() = %q, want it to name the exit status", silent.Error())
	}
	if silent.Unwrap() != nil {
		t.Fatal("a silent ExitError should have no cause to unwrap")
	}
	if !silentFailure(silent) {
		t.Fatal("silentFailure did not recognise a silent ExitError")
	}
	if silentFailure(cause) {
		t.Fatal("a plain error was reported as already-communicated")
	}
	if got := exitStatus(silent); got != cancelExitCode {
		t.Fatalf("exitStatus = %d, want %d", got, cancelExitCode)
	}
	if got := exitStatus(cause); got != 1 {
		t.Fatalf("exitStatus of a plain error = %d, want 1", got)
	}
}

func TestRunOutcomeErrorPluralisesFailures(t *testing.T) {
	one := runOutcomeError(&runOutcome{summary: Summary{Failed: 1}})
	if one == nil || !strings.Contains(one.Error(), "1 repository failed") {
		t.Fatalf("single failure message = %v", one)
	}

	many := runOutcomeError(&runOutcome{summary: Summary{Failed: 3}})
	if many == nil || !strings.Contains(many.Error(), "3 repositories failed") {
		t.Fatalf("multiple failure message = %v", many)
	}

	if err := runOutcomeError(&runOutcome{}); err != nil {
		t.Fatalf("a clean run produced an error: %v", err)
	}
}

func TestWriteCloneStateReportsTempFileFailure(t *testing.T) {
	original := createStateTemp
	t.Cleanup(func() { createStateTemp = original })
	createStateTemp = func(string, string) (stateTempFile, error) {
		return nil, errors.New("no space left on device")
	}

	repo := newStateRepo(t)
	err := writeCloneState(repo, cloneState{})
	if err == nil {
		t.Fatal("expected the temp-file failure to surface")
	}
	if !strings.Contains(err.Error(), "no space left on device") {
		t.Fatalf("error lost its cause: %v", err)
	}

	// The sidecar must not exist: a failed write leaves no partial state
	// behind for the next run to trust.
	path, pathErr := statePath(repo)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a failed write left a sidecar behind: %v", statErr)
	}
}
