// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sebastienrousseau/corralctl/internal/git"
)

// hasDirtyWorkingTree reports tracked, staged or untracked modifications in the
// target repo. Indirected through a package var so tests can stub the dangerous
// "actually run git" path.
//
// This delegates to git.HasLocalChanges rather than shelling out itself. The
// local copy ran `git status --porcelain` a second time (HasUnpublishedWork
// already calls HasLocalChanges first) and, more importantly, skipped
// withGitEnv — so it lacked the non-interactive hardening that stops git
// prompting for credentials and hanging a stdio MCP session.
var hasDirtyWorkingTree = git.HasLocalChanges

// hasIgnoredContent reports gitignored files in the target repo. Indirected for
// the same reason as hasDirtyWorkingTree.
var hasIgnoredContent = git.HasIgnoredContent

// deleteGuards runs the refusal cascade against one path and reports the
// first reason it must not be deleted.
//
// Factored out because it runs twice: once against the live clone, and
// once against the staged copy after the rename that closes the window
// between the two. A second copy of the cascade would be a second place
// for a guard to be forgotten.
func deleteGuards(ctx context.Context, path string) (reason string, ok bool) {
	if !gitIsRepository(path) {
		return fmt.Sprintf("target %s is not a git repository", path), false
	}
	if dirty, out := hasDirtyWorkingTree(ctx, path); dirty {
		return fmt.Sprintf("uncommitted changes present: %s", out), false
	}
	if ahead, out := hasUnpushedCommits(ctx, path); ahead {
		return fmt.Sprintf("unpublished git state present: %s", out), false
	}
	// Gitignored content is the least recoverable thing in a clone — local
	// .env files, databases, caches — and `git status --porcelain` hides it
	// by design, so "clean" was never sufficient grounds to rm -rf.
	if ignored, out := hasIgnoredContent(ctx, path); ignored {
		return fmt.Sprintf("gitignored content present: %s", out), false
	}
	return "", true
}

// stagePrefix marks a directory as mid-deletion.
//
// Leading dot so it sorts out of the way and reads as machinery rather
// than as a repository somebody named oddly; the full word so that anyone
// who finds one after a crash can tell what it is and search for it.
const stagePrefix = ".corral-deleting-"

// stageForRemoval renames dir aside within its own parent directory.
//
// Within the parent, so the rename is a same-directory operation and
// therefore atomic and never a cross-device copy. A name that collides is
// treated as a failure rather than worked around: the only way one exists
// is a previous deletion that crashed midway, and silently reusing it
// would delete whatever it holds.
var stageForRemoval = func(dir string) (string, error) {
	parent := filepath.Dir(dir)
	suffix := make([]byte, 8)
	if _, err := randRead(suffix); err != nil {
		return "", fmt.Errorf("generating a staging name: %w", err)
	}
	staged := filepath.Join(parent, stagePrefix+filepath.Base(dir)+"-"+hex.EncodeToString(suffix))
	if _, err := os.Lstat(staged); err == nil {
		return "", fmt.Errorf("staging path %s already exists", staged)
	}
	if err := os.Rename(dir, staged); err != nil {
		return "", err
	}
	return staged, nil
}

// randRead is crypto/rand.Read, indirected so a test can make a staging
// name deterministic and drive the collision path, which is otherwise
// unreachable at one chance in 2^64.
var randRead = rand.Read

// unstageRemoval puts a staged clone back where it came from.
//
// Indirected alongside stageForRemoval so a test can drive the case where
// restoring fails, which is the one that leaves a user with a directory
// under a name they would never think to look for.
var unstageRemoval = func(staged, original string) error {
	return os.Rename(staged, original)
}

// handleDeleteRepo deletes one clone from the workspace after rigorous safety checks.
func (s *Server) handleDeleteRepo(ctx context.Context, req *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, MutationOutput, error) {
	query := in.Query
	idx, err := s.scan()
	if err != nil {
		return nil, MutationOutput{}, fmt.Errorf("scan workspace: %v", err)
	}
	repo, err := idx.Find(query)
	if err != nil {
		return nil, MutationOutput{}, fmt.Errorf("%v", err)
	}
	safe, err := idx.SafeMutationPath(repo.Path)
	if err != nil {
		return nil, MutationOutput{}, fmt.Errorf("%v", err)
	}

	// One deletion at a time per clone. Over stdio there was one session
	// and one request; over HTTP there are neither, and the window between
	// these checks and the removal is wide enough for a second session to
	// change what is being checked.
	lock := s.repoLocks.lock(safe)
	lock.Lock()
	defer lock.Unlock()

	rec := AuditRecord{
		Tool:   "corral_delete_repo",
		Target: safe,
		Args:   map[string]any{"query": query},
	}

	// Refusal cascade: any single check failing aborts, audits,
	// and returns to the agent with a specific reason.
	if reason, ok := deleteGuards(ctx, safe); !ok {
		rec.Message = reason
		if auditErr := s.auditRefusal(rec, reason); auditErr != nil {
			return nil, MutationOutput{}, fmt.Errorf("%s; audit failed: %v", reason, auditErr)
		}
		return nil, MutationOutput{}, fmt.Errorf("%s", reason)
	}

	// Per-call authority. The cascade above stops mistakes; this is what
	// stops a persuaded agent choosing the one clone that passes every
	// check. Asked only once the deletion would otherwise proceed, so a
	// refusal the server can decide itself never reaches a person.
	if s.confirmDeletes {
		summary := fmt.Sprintf("Delete the local clone %s?", repo.Redacted().RelPath)
		detail := fmt.Sprintf(
			"Path: %s\nOrigin: %s\n\nCorral found no uncommitted, unpushed, stashed, "+
				"gitignored or submodule work in it. This removes the directory from disk.",
			safe, repo.Redacted().RemoteURL)

		switch decision, ask := s.confirmer.Confirm(req, summary, detail); decision {
		case confirmAsk:
			return ask, MutationOutput{}, nil
		case confirmUnavailable:
			rec.Message = "could not obtain confirmation: " + noElicitationMessage
			if auditErr := s.auditRefusal(rec, rec.Message); auditErr != nil {
				return nil, MutationOutput{}, fmt.Errorf("%s; audit failed: %v", rec.Message, auditErr)
			}
			return nil, MutationOutput{}, fmt.Errorf("%s; start the server with --no-confirm-deletes to delete "+
				"without asking, which is only appropriate for an unattended workspace you are willing to lose", rec.Message)
		case confirmDenied:
			rec.Message = "declined by the user"
			if auditErr := s.auditRefusal(rec, rec.Message); auditErr != nil {
				return nil, MutationOutput{}, fmt.Errorf("%s; audit failed: %v", rec.Message, auditErr)
			}
			return nil, MutationOutput{}, fmt.Errorf("deletion of %s was declined", repo.Redacted().RelPath)
		case confirmApproved:
			// Fall through to the deletion.
		}
	}

	rec, err = s.beginMutation(rec)
	if err != nil {
		return nil, MutationOutput{}, fmt.Errorf("audit intent failed: %v", err)
	}

	staged, stageErr := stageForRemoval(safe)
	if stageErr != nil {
		if auditErr := s.completeMutation(rec, "error", stageErr.Error()); auditErr != nil {
			return nil, MutationOutput{}, fmt.Errorf("staging for removal failed: %v; audit completion failed: %v", stageErr, auditErr)
		}
		return nil, MutationOutput{}, fmt.Errorf("staging for removal failed: %v", stageErr)
	}
	if reason, ok := deleteGuards(ctx, staged); !ok {
		restored := "restored"
		if unstageErr := unstageRemoval(staged, safe); unstageErr != nil {
			restored = "could NOT be restored, and is now at " + staged +
				"; move it back by hand (" + unstageErr.Error() + ")"
		}
		msg := reason + " (detected after staging, so the clone was not deleted and was " + restored + ")"
		if auditErr := s.completeMutation(rec, "refused", msg); auditErr != nil {
			return nil, MutationOutput{}, fmt.Errorf("%s; audit completion failed: %v", msg, auditErr)
		}
		return nil, MutationOutput{}, fmt.Errorf("%s", msg)
	}
	if err := removeMutation(staged); err != nil {
		if unstageErr := unstageRemoval(staged, safe); unstageErr != nil {
			err = fmt.Errorf("%w; and the staged copy at %s could not be restored: %v", err, staged, unstageErr)
		}
		if auditErr := s.completeMutation(rec, "error", err.Error()); auditErr != nil {
			return nil, MutationOutput{}, fmt.Errorf("remove failed: %v; audit completion failed: %v", err, auditErr)
		}
		return nil, MutationOutput{}, fmt.Errorf("remove failed: %v", err)
	}
	if err := s.completeMutation(rec, "ok", ""); err != nil {
		return nil, MutationOutput{}, fmt.Errorf("audit completion failed: %v", err)
	}
	s.invalidateScanCache()
	out := MutationOutput{Tool: "corral_delete_repo", Target: safe, Result: "deleted"}
	return jsonResult(out), out, nil
}
