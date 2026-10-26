// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"fmt"

	"github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/sebastienrousseau/corralctl/internal/github"
	"github.com/sebastienrousseau/corralctl/internal/sanitize"
	"github.com/sebastienrousseau/corralctl/internal/syncguard"
)

// The checks corral_sync_repo makes before a pull. They are syncguard's,
// the ones `corralctl clone` makes before every pull, reached through
// seams so a test can stub them; the MCP path carries no copy of them.
var (
	syncOriginMismatch  = syncguard.Default.OriginMismatch
	syncBranchRefusal   = syncguard.Default.BranchRefusal
	originDefaultBranch = git.OriginDefaultBranch
)

// maxSyncReason bounds a refusal reason, which carries branch names and
// remote identities chosen by whoever owns the repository.
const maxSyncReason = 512

// syncGuards is the refusal cascade for corral_sync_repo. The pull is
// `git pull --rebase --autostash`, which rewrites whatever branch is
// checked out, so it runs only when:
//
//   - origin is set, and is still the repository the index recorded when
//     the agent's query was resolved (syncguard OriginMismatch, the CLI's
//     origin-collision check, comparing git.CanonicalRemote identities);
//   - the clone has a commit and is on the branch origin/HEAD names
//     (syncguard BranchRefusal, the CLI's skip rule). The CLI takes the
//     default branch from the forge's listing; the server never contacts
//     a forge API, so it takes the one the clone recorded, and refuses
//     when there is none rather than trusting HEAD.
func syncGuards(ctx context.Context, repo RepoEntry, path string) (string, bool) {
	if git.CanonicalRemote(repo.RemoteURL) == "" {
		return "origin is not set or cannot be read; refusing to pull", false
	}
	if msg, mismatch := syncOriginMismatch(github.Repo{CloneURL: repo.RemoteURL}, path); mismatch {
		return sanitize.Untrusted(msg, maxSyncReason), false
	}
	branch, err := originDefaultBranch(ctx, path)
	if err != nil {
		return sanitize.Untrusted(fmt.Sprintf("cannot tell the default branch: %v", err), maxSyncReason), false
	}
	if reason, refuse := syncBranchRefusal(ctx, path, branch); refuse {
		return sanitize.Untrusted(reason+"; corral only pulls a clone on its default branch", maxSyncReason), false
	}
	return "", true
}
