// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package syncguard holds the checks corral makes before it pulls into an
// existing clone: is the clone the repository we mean, and is pulling
// into it safe. `corralctl clone` (internal/engine) and the MCP
// corral_sync_repo tool (internal/mcp) both refuse on these functions, so
// the two paths cannot drift apart.
//
// It is its own package because internal/mcp must not import
// internal/engine, which links the terminal UI. It imports only git and
// github, so either side can depend on it.
package syncguard

import (
	"context"
	"fmt"
	"strings"

	"github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/sebastienrousseau/corralctl/internal/github"
)

// Reader is the git state the guards read. Each caller passes its own
// seams, so a test that stubs a caller's git access also drives the
// guards; Default reads the real repository.
type Reader struct {
	// RemoteOrigin returns the URL of the clone's origin remote.
	RemoteOrigin func(dir string) (string, error)
	// IsEmpty reports whether the clone has no commits.
	IsEmpty func(ctx context.Context, dir string) bool
	// CurrentBranch returns the branch the clone has checked out.
	CurrentBranch func(ctx context.Context, dir string) (string, error)
}

// Default reads the repository through the git package.
var Default = Reader{
	RemoteOrigin:  git.RemoteOriginFromConfig,
	IsEmpty:       git.IsEmpty,
	CurrentBranch: git.CurrentBranch,
}

// RemoteIdentity is the canonical identity of a listed repository, for
// comparison against a local clone's remote.
//
// Derived from the clone URL the forge returned, and through the same
// function the local side goes through, so the two are comparable by
// construction.
func RemoteIdentity(repo github.Repo) string {
	// The URL first, always: it is the only field that carries the host,
	// and every forge adapter populates it.
	if repo.CloneURL != "" {
		if id := git.CanonicalRemote(repo.CloneURL); id != "" {
			return id
		}
	}
	// Fallbacks for a repository with no clone URL, which no forge here
	// produces.
	if repo.FullName != "" {
		return strings.ToLower("github.com/" + repo.FullName)
	}
	if repo.Owner != "" && repo.Name != "" {
		return strings.ToLower("github.com/" + repo.Owner + "/" + repo.Name)
	}
	return ""
}

// OriginMismatch reports whether the clone at targetDir points somewhere
// other than the repository we intend to sync, which would otherwise mean
// pulling one project's history into another project's directory.
func (r Reader) OriginMismatch(repo github.Repo, targetDir string) (string, bool) {
	wantIdentity := RemoteIdentity(repo)
	if wantIdentity == "" {
		return "", false
	}
	remote, remoteErr := r.RemoteOrigin(targetDir)
	if remoteErr != nil {
		return fmt.Sprintf("cannot verify existing repository origin: %v", remoteErr), true
	}
	gotIdentity := git.CanonicalRemote(remote)
	if gotIdentity != wantIdentity {
		return fmt.Sprintf("origin collision: target has %s, expected %s", gotIdentity, wantIdentity), true
	}
	return "", false
}

// BranchRefusal reports why pulling into the clone at targetDir would be
// wrong before any network round trip: it has no commits, or it is on a
// branch other than defaultBranch. `git pull --rebase --autostash` on a
// feature branch rebases the user's own work, so a pull only ever updates
// the default branch.
//
// An empty upstream repo (created but never pushed to) results in an
// unborn HEAD locally, and `git pull` would fail with "no such ref was
// fetched"; that state is reported as a reason rather than surfaced as a
// git error.
func (r Reader) BranchRefusal(ctx context.Context, targetDir, defaultBranch string) (string, bool) {
	if r.IsEmpty(ctx, targetDir) {
		return "empty repository (no commits yet)", true
	}
	if branch, err := r.CurrentBranch(ctx, targetDir); err == nil && branch != defaultBranch {
		return fmt.Sprintf("on branch %s", branch), true
	}
	return "", false
}
