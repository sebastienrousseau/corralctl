// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sebastienrousseau/corralctl/internal/engine"
	"github.com/sebastienrousseau/corralctl/internal/forge"
	"github.com/sebastienrousseau/corralctl/internal/github"
)

// adoptRepoInput is the argument set for corral_adopt_repo.
type adoptRepoInput struct {
	LocalPath     string `json:"local_path" jsonschema:"Filesystem path of the repository to adopt."`
	NewTargetDir  string `json:"new_target_dir,omitempty" jsonschema:"Optional destination directory to relocate the repository into."`
	Forge         string `json:"forge,omitempty" jsonschema:"Optional destination forge (e.g. github, gitlab)."`
	ForgeURL      string `json:"forge_url,omitempty" jsonschema:"Custom instance URL for self-hosted forges."`
	Owner         string `json:"owner,omitempty" jsonschema:"Owner or namespace under which the remote repository is created."`
	RepoName      string `json:"repo_name" jsonschema:"Name of the repository."`
	Private       bool   `json:"private,omitempty" jsonschema:"Repository visibility: true for private, false for public."`
	Protocol      string `json:"protocol,omitempty" jsonschema:"Transport protocol: 'https' (default) or 'ssh'."`
	RelocateLocal bool   `json:"relocate_local,omitempty" jsonschema:"When true, move repository into new_target_dir."`
	DryRun        bool   `json:"dry_run,omitempty" jsonschema:"When true, simulate without disk or network mutations."`
}

func (s *Server) handleAdoptRepo(ctx context.Context, _ *mcp.CallToolRequest, input adoptRepoInput) (*mcp.CallToolResult, MutationOutput, error) {
	if strings.TrimSpace(input.RepoName) == "" {
		return nil, MutationOutput{}, fmt.Errorf("repo_name must not be empty")
	}
	idx, err := s.scan()
	if err != nil {
		return nil, MutationOutput{}, fmt.Errorf("scan workspace: %v", err)
	}
	cleanLocal, err := idx.SafeMutationPath(input.LocalPath)
	if err != nil {
		return nil, MutationOutput{}, fmt.Errorf("invalid local_path: %w", err)
	}
	cleanTarget := cleanLocal
	if input.RelocateLocal && input.NewTargetDir != "" {
		targetResolved, err := idx.SafeMutationPath(input.NewTargetDir)
		if err != nil {
			return nil, MutationOutput{}, fmt.Errorf("invalid new_target_dir: %w", err)
		}
		cleanTarget = targetResolved
	}

	opts := engine.AdoptOptions{
		LocalPath:     cleanLocal,
		NewTargetDir:  cleanTarget,
		Owner:         input.Owner,
		RepoName:      input.RepoName,
		Private:       input.Private,
		Protocol:      input.Protocol,
		RelocateLocal: input.RelocateLocal,
		DryRun:        input.DryRun,
	}

	if input.Forge != "" {
		f, err := adoptForgeResolve(input.Forge, input.ForgeURL)
		if err != nil {
			return nil, MutationOutput{}, fmt.Errorf("resolving forge: %w", err)
		}
		tf, ok := adoptForgeAsTarget(f)
		if !ok {
			return nil, MutationOutput{}, fmt.Errorf("forge %s cannot receive repositories", input.Forge)
		}
		opts.Forge = tf
		tok := adoptForgeToken(ctx, f.Name(), github.AuthModeAuto)
		opts.ForgeOptions = forge.TargetOptions{
			Token:   tok,
			BaseURL: input.ForgeURL,
		}
	}

	rec := AuditRecord{
		Tool:   "corral_adopt_repo",
		Target: cleanTarget,
		Args: map[string]any{
			"repo_name":  input.RepoName,
			"local_path": cleanLocal,
		},
	}
	rec, err = s.beginMutation(rec)
	if err != nil {
		return nil, MutationOutput{}, fmt.Errorf("audit intent failed: %v", err)
	}

	res, err := adoptExecFunc(ctx, opts)
	if err != nil {
		_ = s.completeMutation(rec, "error", err.Error())
		return nil, MutationOutput{}, fmt.Errorf("adoption failed: %w", err)
	}

	if err := s.completeMutation(rec, "ok", ""); err != nil {
		return nil, MutationOutput{}, fmt.Errorf("audit completion failed: %v", err)
	}
	s.invalidateScanCache()
	out := MutationOutput{
		Tool:   "corral_adopt_repo",
		Repo:   res.RepoName,
		Result: res.Action,
		Target: res.FinalPath,
	}
	return jsonResult(out), out, nil
}
