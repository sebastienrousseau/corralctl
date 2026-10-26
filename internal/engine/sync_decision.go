// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/diag"
	"github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/sebastienrousseau/corralctl/internal/github"
)

// processRepo brings one repository to its desired state: relocating an
// identity-matched clone that sits at an older layout path, then syncing an
// existing clone or making a new one. Every outcome is a RepoResult; no
// error escapes to the caller.
func processRepo(ctx context.Context, owner, protocol string, doSync, dryRun bool, cloneOpts git.CloneOptions, syncOpts SyncOptions, job Job) RepoResult {
	repo := job.Repo
	targetDir := job.Target
	result := RepoResult{
		RepoName:   repo.Name,
		Target:     targetDir,
		Visibility: repo.Visibility,
		Language:   normalizeLanguage(repo.Language),
		DryRun:     dryRun,
		Protocol:   protocol,
	}
	if err := ctx.Err(); err != nil {
		result.Action = "ERROR"
		result.Message = "operation canceled"
		return result
	}

	targetDir, terminal := relocateIdentityMatch(&result, job, targetDir, dryRun)
	if terminal != nil {
		return *terminal
	}

	if !dryRun {
		if err := os.MkdirAll(filepath.Dir(targetDir), 0o750); err != nil {
			result.Action = "ERROR"
			result.Message = fmt.Sprintf("failed creating target directory: %v", err)
			return result
		}
	}

	if git.IsRepository(targetDir) {
		return processExistingClone(ctx, result, repo, targetDir, doSync, dryRun, cloneOpts, syncOpts)
	}
	if info, err := os.Stat(targetDir); err == nil && info.IsDir() {
		result.Action = "SKIP"
		result.Message = "exists but is not a git repo"
		return result
	}
	return cloneRepository(ctx, result, repo, owner, protocol, targetDir, dryRun, cloneOpts)
}

// relocateIdentityMatch moves a clone found at a previous layout path to the
// path the current layout asks for. It returns the directory subsequent
// steps should operate on, plus a terminal RepoResult when the run for this
// repository is already decided (a dry-run preview, or a failure).
func relocateIdentityMatch(result *RepoResult, job Job, targetDir string, dryRun bool) (string, *RepoResult) {
	if job.Existing == "" || filepath.Clean(job.Existing) == filepath.Clean(targetDir) {
		return targetDir, nil
	}

	needsMove := true
	targetInfo, err := statPath(targetDir)
	switch {
	case err == nil:
		existingInfo, existingErr := statPath(job.Existing)
		if existingErr != nil {
			result.Action = "ERROR"
			result.Message = fmt.Sprintf("failed checking existing clone: %v", existingErr)
			return targetDir, result
		}
		if !sameFile(existingInfo, targetInfo) {
			result.Action = "ERROR"
			result.Message = fmt.Sprintf("target collision: %s already exists while matching clone is at %s", targetDir, job.Existing)
			return targetDir, result
		}
		// Case-insensitive filesystems can resolve Public and public to
		// the same directory even though their cleaned strings differ.
		targetDir = job.Existing
		result.Target = targetDir
		needsMove = false
	case !errors.Is(err, os.ErrNotExist):
		result.Action = "ERROR"
		result.Message = fmt.Sprintf("failed checking target collision: %v", err)
		return targetDir, result
	}

	if !needsMove {
		return targetDir, nil
	}
	if dryRun {
		result.Action = "DRY-RUN"
		result.Message = fmt.Sprintf("move %s to %s", job.Existing, targetDir)
		result.Moved = true
		return targetDir, result
	}
	if err := mkdirAll(filepath.Dir(targetDir), 0o750); err != nil {
		result.Action = "ERROR"
		result.Message = fmt.Sprintf("failed creating relocation target: %v", err)
		return targetDir, result
	}
	if err := renamePath(job.Existing, targetDir); err != nil {
		result.Action = "ERROR"
		result.Message = fmt.Sprintf("failed moving identity-matched clone from %s: %v", job.Existing, err)
		return targetDir, result
	}
	result.Moved = true
	return targetDir, nil
}

// processExistingClone handles a target that is already a git repository:
// it confirms the clone is the repository we think it is, then either syncs
// it or reports why it was left alone.
func processExistingClone(
	ctx context.Context,
	result RepoResult,
	repo github.Repo,
	targetDir string,
	doSync, dryRun bool,
	cloneOpts git.CloneOptions,
	syncOpts SyncOptions,
) RepoResult {
	if msg, mismatched := OriginMismatch(repo, targetDir); mismatched {
		result.Action = "ERROR"
		result.Message = msg
		return result
	}
	if !dryRun {
		recordVisibility(targetDir, repo)
	}
	if doSync {
		return syncExistingClone(ctx, result, repo, targetDir, dryRun, cloneOpts, syncOpts)
	}
	result.Action = "SKIP"
	if result.Moved {
		result.Message = "moved to desired layout; sync disabled"
	} else {
		result.Message = "already exists"
	}
	return result
}

// OriginMismatch reports whether the clone at targetDir points somewhere
// other than the repository we intend to sync, which would otherwise mean
// pulling one project's history into another project's directory.
//
// Exported so the MCP sync tool refuses on the same comparison as
// `corralctl clone`, rather than on a copy of it.
func OriginMismatch(repo github.Repo, targetDir string) (string, bool) {
	wantIdentity := repoRemoteIdentity(repo)
	if wantIdentity == "" {
		return "", false
	}
	remote, remoteErr := gitRemoteOrigin(targetDir)
	if remoteErr != nil {
		return fmt.Sprintf("cannot verify existing repository origin: %v", remoteErr), true
	}
	gotIdentity := git.CanonicalRemote(remote)
	if gotIdentity != wantIdentity {
		return fmt.Sprintf("origin collision: target has %s, expected %s", gotIdentity, wantIdentity), true
	}
	return "", false
}

// syncExistingClone pulls an existing clone, skipping the network round-trip
// whenever the local state already proves the pull would be a no-op.
func syncExistingClone(
	ctx context.Context,
	result RepoResult,
	repo github.Repo,
	targetDir string,
	dryRun bool,
	cloneOpts git.CloneOptions,
	syncOpts SyncOptions,
) RepoResult {
	result.SyncAttempt = true
	if dryRun {
		result.Action = "DRY-RUN"
		result.Message = "git pull"
		return result
	}
	if reason, skip := syncSkipReason(ctx, repo, targetDir, syncOpts); skip {
		result.Action = "SKIP"
		result.Message = reason
		return result
	}
	err := gitPull(ctx, targetDir, git.PullOptions{
		RecurseSubmodules:       cloneOpts.RecurseSubmodules,
		IgnoreSubmoduleFailures: syncOpts.IgnoreSubmoduleFailures,
	})
	if err != nil {
		result.Action = "ERROR"
		result.Message = fmt.Sprintf("sync failed: %v", err)
		return result
	}
	stampCloneState(targetDir, repo)
	result.Action = "SYNC"
	result.Message = "synced successfully"
	return result
}

// syncSkipReason reports why a pull should be skipped, if it should. Each
// case is a state where pulling is either impossible or provably pointless.
func syncSkipReason(ctx context.Context, repo github.Repo, targetDir string, syncOpts SyncOptions) (string, bool) {
	if reason, refuse := BranchRefusal(ctx, targetDir, repo.DefaultBranch); refuse {
		return reason, true
	}
	// Skip the network round-trip when the upstream pushed_at is unchanged
	// since the last successful sync. The cached value lives in
	// <targetDir>/.git/corral-state.json. A read error or a zero state falls
	// through to the original pull-always behaviour, so a missing or corrupt
	// sidecar can never cause a stale working tree.
	if !syncOpts.Force && !repo.PushedAt.IsZero() {
		if st, err := readCloneState(targetDir); err == nil &&
			!st.LastSyncedPushedAt.IsZero() &&
			!repo.PushedAt.After(st.LastSyncedPushedAt) {
			return "up-to-date (pushed_at unchanged)", true
		}
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
// git error. Exported so the MCP sync tool refuses on the same checks as
// `corralctl clone`.
func BranchRefusal(ctx context.Context, targetDir, defaultBranch string) (string, bool) {
	if gitIsEmpty(ctx, targetDir) {
		return "empty repository (no commits yet)", true
	}
	if branch, err := gitCurrentBranch(ctx, targetDir); err == nil && branch != defaultBranch {
		return fmt.Sprintf("on branch %s", branch), true
	}
	return "", false
}

// cloneRepository makes a new clone at targetDir over the selected protocol.
func cloneRepository(
	ctx context.Context,
	result RepoResult,
	repo github.Repo,
	owner, protocol, targetDir string,
	dryRun bool,
	cloneOpts git.CloneOptions,
) RepoResult {
	result.ClonedURL = cloneURL(repo, owner, protocol)
	if dryRun {
		result.Action = "DRY-RUN"
		result.Message = "git clone"
		return result
	}
	if err := gitClone(ctx, result.ClonedURL, targetDir, cloneOpts); err != nil {
		result.Action = "ERROR"
		result.Message = fmt.Sprintf("clone failed: %v", err)
		return result
	}
	stampCloneState(targetDir, repo)
	result.Action = "CLONE"
	result.Message = "cloned successfully"
	return result
}

// cloneURL selects the URL to clone from. For ssh it prefers the forge's
// own ssh_url, and derives one from the HTTPS URL when the forge did not
// supply one.
//
// It used to fall back to "git@github.com:owner/name.git". That is
// reachable — a Gitea or Forgejo instance with SSH disabled returns an
// empty ssh_url — and the consequence is not a failure but something
// worse: cloning a *different* repository, from a host the user never
// named, that happens to share an owner and name.
//
// Deriving the host from the HTTPS URL keeps the fallback on the instance
// the repository actually came from. It can still be wrong about the SSH
// port or user for an unusual deployment, but it cannot be wrong about
// which server to talk to.
func cloneURL(repo github.Repo, owner, protocol string) string {
	if protocol != "ssh" {
		return repo.CloneURL
	}
	if repo.SSHURL != "" {
		return repo.SSHURL
	}
	if identity := git.CanonicalRemote(repo.CloneURL); identity != "" {
		if slash := strings.IndexByte(identity, '/'); slash > 0 {
			return fmt.Sprintf("git@%s:%s.git", identity[:slash], identity[slash+1:])
		}
	}
	// Nothing to derive from. The conventional GitHub form is the only
	// remaining guess, and it is at least the one that was correct before
	// other forges existed.
	return fmt.Sprintf("git@github.com:%s/%s.git", owner, repo.Name)
}

// stampCloneState records the upstream pushed_at in the per-clone state
// sidecar so the next run can skip a no-op git pull, along with the
// visibility the forge listed, which `corralctl sync` reads. Best-effort: a write
// failure is logged but does not fail the operation, since the sidecar is
// purely an optimization (a missing or stale file falls through to the
// pre-sidecar behaviour of always pulling).
func stampCloneState(targetDir string, repo github.Repo) {
	if err := writeCloneState(targetDir, cloneState{
		LastSyncedPushedAt: repo.PushedAt,
		LastSyncedAt:       time.Now().UTC(),
		Visibility:         visibilityRecord(repo.Visibility),
	}); err != nil {
		diag.Warnf("failed writing %s in %s: %v", StateFileName, targetDir, err)
	}
}

func applyJobFinderTags(opts RunOptions, job Job, result RepoResult) {
	if !opts.FinderTags || opts.DryRun {
		return
	}
	tagPath := result.Target
	if result.Action == "ERROR" || strings.HasPrefix(result.Action, "FAIL") {
		tagPath = job.Existing
	}
	if tagPath == "" {
		return
	}
	if err := applyTags(tagPath, job.Repo, result); err != nil {
		diag.Warnf("failed applying Finder tags to %s: %v", tagPath, err)
	}
}
