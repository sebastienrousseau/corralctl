// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Mutation tools for the corral MCP server. Only registered when the
// caller opts in via ServerOptions.EnableMutations (and, for
// corral_delete_repo, ServerOptions.EnableDestructiveMutations).
//
// Every handler:
//  1. Validates all inputs against the configured Root sandbox.
//  2. Writes an AuditRecord to the JSONL log BEFORE returning any
//     structured result, so a crash mid-tool still leaves a durable
//     trail of what the agent tried.
//  3. Uses the shared gitClone/gitPull vars from internal/git so the
//     same non-interactive env + auth pipeline that classic corralctl
//     relies on covers the MCP path too.
//
// Refusal is preferred over silent no-op or partial success: the tool
// response's IsError field is set with a concrete reason so the agent
// can adapt its next action rather than assuming the mutation went
// through.

package mcp

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sebastienrousseau/corralctl/internal/adopt"
	"github.com/sebastienrousseau/corralctl/internal/forge"
	"github.com/sebastienrousseau/corralctl/internal/git"
)

// gitPull / gitClone are indirected through package vars so tests can
// stub the "actually shell out and hit the network" branch. Production
// callers get the real git package functions.
var (
	gitPull            = git.Pull
	gitClone           = git.Clone
	gitCreateWorktree  = git.CreateWorktree
	gitRemoveWorktree  = git.RemoveWorktree
	gitPruneWorktrees  = git.PruneWorktrees
	gitListWorktrees   = git.ListWorktrees
	hasLocalChanges    = git.HasLocalChanges
	hasUnpushedCommits = git.HasUnpublishedWork
	gitIsRepository    = git.IsRepository
	statMutation       = os.Stat
	statPath           = os.Stat
	mkdirMutation      = os.MkdirAll
	removeMutation     = os.RemoveAll
	markSynced         = markStateSynced
	adoptExecFunc      = adopt.Execute
	adoptForgeResolve  = forge.Resolve
	adoptForgeAsTarget = forge.AsTarget
	adoptForgeToken    = forge.Token
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
// (sync, clone, adopt and the three worktree tools) to the MCP server.
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

	addTool(s, &mcp.Tool{
		Name:  "corral_create_worktree",
		Title: "Create an isolated agent worktree",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			DestructiveHint: &f,
			IdempotentHint:  false,
			OpenWorldHint:   &f,
		},
		Description: "Create an isolated Git worktree for an agent under .git/corral-worktrees/<branch> linked to a repository clone. Requires --enable-mutations. Protects the developer's working directory by keeping agent branching and edits isolated. Refuses when the target worktree already exists or would escape the repository.",
	}, s.handleCreateWorktree)

	addTool(s, &mcp.Tool{
		Name:  "corral_release_worktree",
		Title: "Release an agent worktree",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			DestructiveHint: &f,
			IdempotentHint:  false,
			OpenWorldHint:   &f,
		},
		Description: "Remove a linked agent worktree created under .git/corral-worktrees. Requires --enable-mutations. Refuses when uncommitted or untracked changes exist in the worktree unless force=true is passed. Prunes stale worktree administrative metadata.",
	}, s.handleReleaseWorktree)

	addTool(s, &mcp.Tool{
		Name:  "corral_prune_worktrees",
		Title: "Prune stale or temporary agent worktrees",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			DestructiveHint: &f,
			IdempotentHint:  false,
			OpenWorldHint:   &f,
		},
		Description: "Prune stale, detached, or temporary agent worktrees across workspace repositories. Requires --enable-mutations. Skips main repository worktrees and refuses removal of modified worktrees unless force=true is passed.",
	}, s.handlePruneWorktrees)

	addTool(s, &mcp.Tool{
		Name:  "corral_adopt_repo",
		Title: "Adopt an untracked repository into the workspace",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:    false,
			DestructiveHint: &f,
			IdempotentHint:  false,
			OpenWorldHint:   &t,
		},
		Description: "Adopt a local repository into the Corral workspace, optionally relocating it into layout taxonomy, provisioning a matching remote repository on a forge (GitHub, GitLab, Gitea, Bitbucket), and linking the remote origin. Requires --enable-mutations.",
	}, s.handleAdoptRepo)
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
func (s *Server) handleSyncRepo(ctx context.Context, _ *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, MutationOutput, error) {
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

	rec := AuditRecord{
		Tool:   "corral_sync_repo",
		Target: safe,
		Args:   map[string]any{"query": query},
	}
	rec, err = s.beginMutation(rec)
	if err != nil {
		return nil, MutationOutput{}, fmt.Errorf("audit intent failed: %v", err)
	}
	pullErr := gitPull(ctx, safe, git.PullOptions{})
	if pullErr != nil {
		if auditErr := s.completeMutation(rec, "error", pullErr.Error()); auditErr != nil {
			return nil, MutationOutput{}, fmt.Errorf("%v; audit completion failed: %v", pullErr, auditErr)
		}
		return nil, MutationOutput{}, fmt.Errorf("%v", pullErr)
	}
	if err := markSynced(safe); err != nil {
		if auditErr := s.completeMutation(rec, "error", err.Error()); auditErr != nil {
			return nil, MutationOutput{}, fmt.Errorf("state update failed: %v; audit completion failed: %v", err, auditErr)
		}
		return nil, MutationOutput{}, fmt.Errorf("state update failed: %v", err)
	}
	if err := s.completeMutation(rec, "ok", ""); err != nil {
		return nil, MutationOutput{}, fmt.Errorf("audit completion failed: %v", err)
	}
	s.invalidateScanCache()
	out := MutationOutput{Tool: "corral_sync_repo", Repo: repo.RelPath, Result: "synced"}
	return jsonResult(out), out, nil
}

// cloneRepoTool returns corral_clone_repo. Wraps git.Clone into the
// layout-templated target directory. Refuses if the target already
// exists (never silently overwrites) or if the destination would
// escape the sandbox root.
func (s *Server) handleCloneRepo(ctx context.Context, _ *mcp.CallToolRequest, in cloneInput) (*mcp.CallToolResult, MutationOutput, error) {
	url := in.URL
	target := in.Target
	depth := in.Depth
	blobless := in.Blobless

	if err := validateCloneURL(url); err != nil {
		return nil, MutationOutput{}, fmt.Errorf("%v", err)
	}

	idx := &Index{Root: s.opts.Root}
	safeTarget, err := idx.SafeMutationPath(target)
	if err != nil {
		return nil, MutationOutput{}, fmt.Errorf("%v", err)
	}
	if _, err := statMutation(safeTarget); err == nil {
		return nil, MutationOutput{}, fmt.Errorf("target %s already exists", safeTarget)
	}

	rec := AuditRecord{
		Tool:   "corral_clone_repo",
		Target: safeTarget,
		Args:   map[string]any{"url": redactCloneURL(url), "target": target, "depth": depth, "blobless": blobless},
	}
	rec, err = s.beginMutation(rec)
	if err != nil {
		return nil, MutationOutput{}, fmt.Errorf("audit intent failed: %v", err)
	}
	if err := mkdirMutation(filepath.Dir(safeTarget), 0o750); err != nil {
		if auditErr := s.completeMutation(rec, "error", err.Error()); auditErr != nil {
			return nil, MutationOutput{}, fmt.Errorf("create target parent: %v; audit completion failed: %v", err, auditErr)
		}
		return nil, MutationOutput{}, fmt.Errorf("create target parent: %v", err)
	}
	cloneErr := gitClone(ctx, url, safeTarget, git.CloneOptions{
		Depth:    depth,
		Blobless: blobless,
	})
	if cloneErr != nil {
		if auditErr := s.completeMutation(rec, "error", cloneErr.Error()); auditErr != nil {
			return nil, MutationOutput{}, fmt.Errorf("%v; audit completion failed: %v", cloneErr, auditErr)
		}
		return nil, MutationOutput{}, fmt.Errorf("%v", cloneErr)
	}
	if err := s.completeMutation(rec, "ok", ""); err != nil {
		return nil, MutationOutput{}, fmt.Errorf("audit completion failed: %v", err)
	}
	s.invalidateScanCache()
	out := MutationOutput{Tool: "corral_clone_repo", Target: safeTarget, Result: "cloned"}
	return jsonResult(out), out, nil
}
