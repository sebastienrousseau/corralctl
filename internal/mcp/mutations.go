// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Mutation tools for the corral MCP server. Only registered when the
// caller opts in via ServerOptions.EnableMutations (and, for
// corral_delete_repo, ServerOptions.EnableDestructiveMutations).
//
// Every handler:
//   1. Validates all inputs against the configured Root sandbox.
//   2. Writes an AuditRecord to the JSONL log BEFORE returning any
//      structured result, so a crash mid-tool still leaves a durable
//      trail of what the agent tried.
//   3. Uses the shared gitClone/gitPull vars from internal/git so the
//      same non-interactive env + auth pipeline that classic corralctl
//      relies on covers the MCP path too.
//
// Refusal is preferred over silent no-op or partial success: the tool
// response's IsError field is set with a concrete reason so the agent
// can adapt its next action rather than assuming the mutation went
// through.

package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sebastienrousseau/corralctl/internal/git"
)

// gitPull / gitClone are indirected through package vars so tests can
// stub the "actually shell out and hit the network" branch. Production
// callers get the real git package functions.
var (
	gitPull            = git.Pull
	gitClone           = git.Clone
	hasUnpushedCommits = git.HasUnpublishedWork
	gitIsRepository    = git.IsRepository
	statMutation       = os.Stat
	mkdirMutation      = os.MkdirAll
	removeMutation     = os.RemoveAll
	markSynced         = markStateSynced
)

var mutationSequence atomic.Uint64

func mutationID() string {
	return fmt.Sprintf("%d-%d", time.Now().UTC().UnixNano(), mutationSequence.Add(1))
}

func redactCloneURL(raw string) string {
	if parsed, err := url.Parse(raw); err == nil && parsed.Scheme != "" {
		if parsed.User != nil {
			parsed.User = url.User("REDACTED")
		}
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return parsed.String()
	}
	if at := strings.LastIndex(raw, "@"); at >= 0 {
		return "REDACTED@" + raw[at+1:]
	}
	return raw
}

// registerMutationTools attaches the non-destructive write tools
// (corral_sync_repo, corral_clone_repo) to the underlying MCP server.
// corral_delete_repo lives in registerDestructiveTools so callers can
// grant "may pull and clone" without also granting "may delete."
func (s *Server) registerMutationTools() {
	f := false
	t := true
	addTool(s, &mcp.Tool{
		Name:  "corral_sync_repo",
		Title: "Sync one repository",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			DestructiveHint: &f,
			IdempotentHint:  true,
			OpenWorldHint:   &t,
		},
		Description: "Run `git pull --rebase --autostash` against one clone in the Corral workspace. Requires --enable-mutations. Reuses corral's non-interactive git environment (no credential prompts, no signing pinentry) and honours smart-sync via the sync sidecar. Refuses when the repo isn't in the index or resolves outside the configured sandbox root.",
	}, s.handleSyncRepo)

	addTool(s, &mcp.Tool{
		Name:  "corral_clone_repo",
		Title: "Clone a repository into the workspace",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			DestructiveHint: &f,
			IdempotentHint:  false,
			OpenWorldHint:   &t,
		},
		Description: "Clone a repository into the Corral workspace. Requires --enable-mutations. Refuses when the target already exists (never overwrites) or would escape the sandbox root.",
	}, s.handleCloneRepo)
}

// registerDestructiveTools attaches corral_delete_repo. Kept in its
// own function so a future grep for "destructive tool registration"
// finds one and only one call site.
func (s *Server) registerDestructiveTools() {
	f := false
	t := true
	addTool(s, &mcp.Tool{
		Name:  "corral_delete_repo",
		Title: "Delete a clone from the workspace",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			DestructiveHint: &t,
			IdempotentHint:  false,
			OpenWorldHint:   &f,
		},
		Description: "Remove one clone from the Corral workspace. Requires --enable-mutations AND --enable-destructive-mutations. Refuses when the clone holds uncommitted, unpushed, stashed, gitignored or submodule work, or when it resolves to the workspace root itself. Every attempt is written to the audit log.",
	}, s.handleDeleteRepo)
}

// audit writes a mutation record. Failure to audit is a fatal error
// for the surrounding tool call: an unlogged mutation defeats the
// mechanism, so callers should propagate this back to the agent as
// an IsError result.
func (s *Server) audit(rec AuditRecord) error {
	if s.auditor == nil {
		// Should be unreachable — the mutation tools are only registered
		// when the auditor is configured — but returning a clear error
		// beats a silent nil-deref if the wiring ever regresses.
		return fmt.Errorf("mutation attempted with no auditor configured")
	}
	return s.auditor.Write(rec)
}

func (s *Server) beginMutation(rec AuditRecord) (AuditRecord, error) {
	rec.OperationID = mutationID()
	rec.Phase = "intent"
	rec.Result = "pending"
	if err := s.audit(rec); err != nil {
		return rec, err
	}
	return rec, nil
}

func (s *Server) completeMutation(rec AuditRecord, result, message string) error {
	rec.Phase = "completion"
	rec.Result = result
	rec.Message = message
	rec.Timestamp = ""
	return s.audit(rec)
}

func (s *Server) auditRefusal(rec AuditRecord, message string) error {
	rec.OperationID = mutationID()
	return s.completeMutation(rec, "refused", message)
}

// syncRepoTool returns corral_sync_repo. Wraps git.Pull for a resolved
// repo. Preserves the same smart-sync sidecar semantics as the classic
// `corralctl <owner>` sync path — no separate write-through cache to
// maintain. Refuses when the repo is not in the workspace index or
// when the operation cannot be sandboxed to the configured Root.
func (s *Server) handleSyncRepo(ctx context.Context, _ *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, any, error) {
	query := in.Query
	idx, err := s.scan()
	if err != nil {
		return toolError("scan workspace: %v", err), nil, nil
	}
	repo, err := idx.Find(query)
	if err != nil {
		return toolError("%v", err), nil, nil
	}
	// Belt-and-braces sandbox check — Index.Find already returns
	// only Root-relative repos, but a future refactor might not.
	safe, err := idx.SafeMutationPath(repo.Path)
	if err != nil {
		return toolError("%v", err), nil, nil
	}

	rec := AuditRecord{
		Tool:   "corral_sync_repo",
		Target: safe,
		Args:   map[string]any{"query": query},
	}
	rec, err = s.beginMutation(rec)
	if err != nil {
		return toolError("audit intent failed: %v", err), nil, nil
	}
	pullErr := gitPull(ctx, safe, git.PullOptions{})
	if pullErr != nil {
		if auditErr := s.completeMutation(rec, "error", pullErr.Error()); auditErr != nil {
			return toolError("%v; audit completion failed: %v", pullErr, auditErr), nil, nil
		}
		return toolError("%v", pullErr), nil, nil
	}
	if err := markSynced(safe); err != nil {
		if auditErr := s.completeMutation(rec, "error", err.Error()); auditErr != nil {
			return toolError("state update failed: %v; audit completion failed: %v", err, auditErr), nil, nil
		}
		return toolError("state update failed: %v", err), nil, nil
	}
	if err := s.completeMutation(rec, "ok", ""); err != nil {
		return toolError("audit completion failed: %v", err), nil, nil
	}
	s.invalidateScanCache()
	return jsonResult(map[string]any{
		"tool":   "corral_sync_repo",
		"repo":   repo.RelPath,
		"result": "synced",
	}), nil, nil
}

// cloneRepoTool returns corral_clone_repo. Wraps git.Clone into the
// layout-templated target directory. Refuses if the target already
// exists (never silently overwrites) or if the destination would
// escape the sandbox root.
func (s *Server) handleCloneRepo(ctx context.Context, _ *mcp.CallToolRequest, in cloneInput) (*mcp.CallToolResult, any, error) {
	url := in.URL
	target := in.Target
	depth := in.Depth
	blobless := in.Blobless

	if err := validateCloneURL(url); err != nil {
		return toolError("%v", err), nil, nil
	}

	idx := &Index{Root: s.opts.Root}
	safeTarget, err := idx.SafeMutationPath(target)
	if err != nil {
		return toolError("%v", err), nil, nil
	}
	if _, err := statMutation(safeTarget); err == nil {
		return toolError("target %s already exists", safeTarget), nil, nil
	}

	rec := AuditRecord{
		Tool:   "corral_clone_repo",
		Target: safeTarget,
		Args:   map[string]any{"url": redactCloneURL(url), "target": target, "depth": depth, "blobless": blobless},
	}
	rec, err = s.beginMutation(rec)
	if err != nil {
		return toolError("audit intent failed: %v", err), nil, nil
	}
	if err := mkdirMutation(filepath.Dir(safeTarget), 0o750); err != nil {
		if auditErr := s.completeMutation(rec, "error", err.Error()); auditErr != nil {
			return toolError("create target parent: %v; audit completion failed: %v", err, auditErr), nil, nil
		}
		return toolError("create target parent: %v", err), nil, nil
	}
	cloneErr := gitClone(ctx, url, safeTarget, git.CloneOptions{
		Depth:    depth,
		Blobless: blobless,
	})
	if cloneErr != nil {
		if auditErr := s.completeMutation(rec, "error", cloneErr.Error()); auditErr != nil {
			return toolError("%v; audit completion failed: %v", cloneErr, auditErr), nil, nil
		}
		return toolError("%v", cloneErr), nil, nil
	}
	if err := s.completeMutation(rec, "ok", ""); err != nil {
		return toolError("audit completion failed: %v", err), nil, nil
	}
	s.invalidateScanCache()
	return jsonResult(map[string]any{
		"tool":   "corral_clone_repo",
		"target": safeTarget,
		"result": "cloned",
	}), nil, nil
}

// deleteRepoTool returns corral_delete_repo. This is the highest-risk
// operation the MCP server exposes: it removes a local clone from
// disk. The safeguards are deliberately paranoid:
//
//  1. Requires BOTH EnableMutations and EnableDestructiveMutations
//     to be registered at all.
//  2. Resolves the target via SafeMutationPath so path traversal cannot
//     escape the sandbox.
//  3. Refuses if the working tree has uncommitted changes.
//  4. Refuses if there are unpushed commits on any branch.
//  5. Refuses if the target isn't a git repository at all
//     (defence against typos deleting an unrelated directory).
//  6. Always writes an audit record before removing anything, so a
//     race between the check and the removal is still logged.
//  7. When ServerOptions.ConfirmDeletes is set, requires a person to
//     approve this particular deletion. The cascade above stops
//     mistakes; nothing else here stops a persuaded agent choosing the
//     one clone that passes every check.
//
// Step 7 makes this a two-pass handler: the first call returns an input
// request and no content, the client puts the question to a person, and
// the same call arrives again carrying the answer. Every check runs on
// both passes, and nothing is written until the approved one.
func (s *Server) handleDeleteRepo(ctx context.Context, req *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, any, error) {
	query := in.Query
	idx, err := s.scan()
	if err != nil {
		return toolError("scan workspace: %v", err), nil, nil
	}
	repo, err := idx.Find(query)
	if err != nil {
		return toolError("%v", err), nil, nil
	}
	safe, err := idx.SafeMutationPath(repo.Path)
	if err != nil {
		return toolError("%v", err), nil, nil
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
			return toolError("%s; audit failed: %v", reason, auditErr), nil, nil
		}
		return toolError("%s", reason), nil, nil
	}

	// Per-call authority. The cascade above stops mistakes; this is what
	// stops a persuaded agent choosing the one clone that passes every
	// check. Asked only once the deletion would otherwise proceed, so a
	// refusal the server can decide itself never reaches a person.
	//
	// Everything above this point is a read, so serving the call twice —
	// once to ask, once with the answer — costs a scan and re-checks the
	// clone against whatever changed while the person was deciding.
	if s.confirmDeletes {
		summary := fmt.Sprintf("Delete the local clone %s?", repo.Redacted().RelPath)
		detail := fmt.Sprintf(
			"Path: %s\nOrigin: %s\n\nCorral found no uncommitted, unpushed, stashed, "+
				"gitignored or submodule work in it. This removes the directory from disk.",
			safe, repo.Redacted().RemoteURL)

		switch decision, ask := s.confirmer.Confirm(req, summary, detail); decision {
		case confirmAsk:
			// Not an outcome: the client now puts the question to a person
			// and calls the tool again with the answer. Nothing is audited
			// yet because nothing has been decided.
			return ask, nil, nil
		case confirmUnavailable:
			// Fail closed. If nobody can be asked, nobody has approved.
			rec.Message = "could not obtain confirmation: " + noElicitationMessage
			if auditErr := s.auditRefusal(rec, rec.Message); auditErr != nil {
				return toolError("%s; audit failed: %v", rec.Message, auditErr), nil, nil
			}
			return toolError("%s\n\nStart the server with --no-confirm-deletes to delete without asking, "+
				"which is only appropriate for an unattended workspace you are willing to lose.", rec.Message), nil, nil
		case confirmDenied:
			rec.Message = "declined by the user"
			if auditErr := s.auditRefusal(rec, rec.Message); auditErr != nil {
				return toolError("%s; audit failed: %v", rec.Message, auditErr), nil, nil
			}
			return toolError("deletion of %s was declined", repo.Redacted().RelPath), nil, nil
		case confirmApproved:
			// Fall through to the deletion.
		}
	}

	rec, err = s.beginMutation(rec)
	if err != nil {
		return toolError("audit intent failed: %v", err), nil, nil
	}
	// Stage the clone aside before removing it (SEC-5).
	//
	// Every check above ran against a path any other process on this
	// machine can still write to. The rename closes that: it is atomic
	// within a directory, and afterwards nothing can reach the clone under
	// the name it knew — a concurrent `git commit` either fails or writes
	// into a directory it has just recreated, which is not the one being
	// deleted. The checks then run once more against the staged copy, so
	// work that landed between the first check and the rename is seen
	// before anything is destroyed rather than after.
	staged, stageErr := stageForRemoval(safe)
	if stageErr != nil {
		if auditErr := s.completeMutation(rec, "error", stageErr.Error()); auditErr != nil {
			return toolError("staging for removal failed: %v; audit completion failed: %v", stageErr, auditErr), nil, nil
		}
		return toolError("staging for removal failed: %v", stageErr), nil, nil
	}
	if reason, ok := deleteGuards(ctx, staged); !ok {
		// Something changed under us. Put it back exactly where it was and
		// refuse; a deletion that lost a race must lose it safely.
		restored := "restored"
		if unstageErr := unstageRemoval(staged, safe); unstageErr != nil {
			restored = "could NOT be restored, and is now at " + staged +
				"; move it back by hand (" + unstageErr.Error() + ")"
		}
		msg := reason + " (detected after staging, so the clone was not deleted and was " + restored + ")"
		if auditErr := s.completeMutation(rec, "refused", msg); auditErr != nil {
			return toolError("%s; audit completion failed: %v", msg, auditErr), nil, nil
		}
		return toolError("%s", msg), nil, nil
	}
	if err := removeMutation(staged); err != nil {
		// The clone is staged but not removed. Try to put it back rather
		// than leaving it under a name nobody will look for.
		if unstageErr := unstageRemoval(staged, safe); unstageErr != nil {
			err = fmt.Errorf("%w; and the staged copy at %s could not be restored: %v", err, staged, unstageErr)
		}
		if auditErr := s.completeMutation(rec, "error", err.Error()); auditErr != nil {
			return toolError("remove failed: %v; audit completion failed: %v", err, auditErr), nil, nil
		}
		return toolError("remove failed: %v", err), nil, nil
	}
	if err := s.completeMutation(rec, "ok", ""); err != nil {
		return toolError("audit completion failed: %v", err), nil, nil
	}
	s.invalidateScanCache()
	return jsonResult(map[string]any{
		"tool":   "corral_delete_repo",
		"target": safe,
		"result": "deleted",
	}), nil, nil
}

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
