// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/url"
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
	ForgeURL      string `json:"forge_url,omitempty" jsonschema:"Self-hosted forge instance URL. Only instances the server operator configured with corralctl mcp --forge-url are accepted; omit for the forge's public instance."`
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
	cleanLocal, cleanTarget, err := s.adoptPaths(input)
	if err != nil {
		return nil, MutationOutput{}, err
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
	if err := s.adoptForge(ctx, input, cleanTarget, &opts); err != nil {
		return nil, MutationOutput{}, err
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

// adoptPaths resolves local_path and, when relocating, new_target_dir
// inside the workspace root.
func (s *Server) adoptPaths(input adoptRepoInput) (local, target string, err error) {
	idx, err := s.scan()
	if err != nil {
		return "", "", fmt.Errorf("scan workspace: %v", err)
	}
	local, err = idx.SafeMutationPath(input.LocalPath)
	if err != nil {
		return "", "", fmt.Errorf("invalid local_path: %w", err)
	}
	target = local
	if input.RelocateLocal && input.NewTargetDir != "" {
		target, err = idx.SafeMutationPath(input.NewTargetDir)
		if err != nil {
			return "", "", fmt.Errorf("invalid new_target_dir: %w", err)
		}
	}
	return local, target, nil
}

// adoptForge fills in the destination forge, when one was named.
//
// forge_url is checked first, before the forge is resolved, a token is
// read or anything touches the network. The token presented to the forge
// is the operator's, read from their environment or gh, so letting the
// caller choose the host would let it collect that token: only a forge's
// public instance (no forge_url) or an instance the operator named with
// `corralctl mcp --forge-url` is accepted, and a refusal is audited.
func (s *Server) adoptForge(ctx context.Context, input adoptRepoInput, target string, opts *engine.AdoptOptions) error {
	if input.Forge == "" {
		return nil
	}
	if err := s.allowedForgeURL(input.ForgeURL); err != nil {
		rec := AuditRecord{
			Tool:   "corral_adopt_repo",
			Target: target,
			Args:   map[string]any{"repo_name": input.RepoName, "forge": input.Forge},
		}
		if auditErr := s.auditRefusal(rec, err.Error()); auditErr != nil {
			return fmt.Errorf("audit refusal failed: %v", auditErr)
		}
		return err
	}
	f, err := adoptForgeResolve(input.Forge, input.ForgeURL)
	if err != nil {
		return fmt.Errorf("resolving forge: %w", err)
	}
	tf, ok := adoptForgeAsTarget(f)
	if !ok {
		return fmt.Errorf("forge %s cannot receive repositories", input.Forge)
	}
	opts.Forge = tf
	opts.ForgeOptions = forge.TargetOptions{
		Token:   adoptForgeToken(ctx, f.Name(), github.AuthModeAuto),
		BaseURL: input.ForgeURL,
	}
	return nil
}

// allowedForgeURL refuses a forge_url the operator did not configure. An
// empty one means the forge's public instance and is always allowed.
func (s *Server) allowedForgeURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	want := instanceKey(raw)
	for _, allowed := range s.opts.ForgeURLs {
		if want != "" && instanceKey(allowed) == want {
			return nil
		}
	}
	return errors.New("forge_url is not an instance this server was started with: " +
		"omit forge_url for the forge's public instance, or have the operator restart " +
		"`corralctl mcp` with --forge-url naming it")
}

// instanceKey reduces an http(s) URL to scheme, lower-cased host and port,
// and path without a trailing slash, so equivalent spellings of one
// instance compare equal. Anything else yields "", which matches nothing.
func instanceKey(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	return u.Scheme + "://" + strings.ToLower(u.Host) + strings.TrimRight(u.Path, "/")
}
