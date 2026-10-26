// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package adopt brings an untracked local repository under management:
// it optionally relocates the clone into the layout, provisions a remote
// on a forge, points origin at it and pushes the current branch.
//
// It is shared by `corralctl adopt` and the MCP `corral_adopt_repo` tool,
// and lives outside internal/engine so the MCP server does not link the
// terminal UI that the engine drives.
package adopt

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/sebastienrousseau/corralctl/internal/forge"
	"github.com/sebastienrousseau/corralctl/internal/git"
)

// Seams for filesystem and Git operations during adoption.
var (
	adoptAddRemote     = git.EnsureRemote
	adoptPushUpstream  = git.PushBranch
	adoptRename        = os.Rename
	adoptMkdirAll      = os.MkdirAll
	adoptIsEmpty       = git.IsEmpty
	adoptCurrentBranch = git.CurrentBranch
	adoptCopyDir       = copyDirRecursive
	adoptRemoveAll     = os.RemoveAll
	adoptReadFile      = os.ReadFile
)

// Options encapsulates options for discovering, relocating, and
// provisioning a remote repository for an untracked local repository.
type Options struct {
	// LocalPath is the current filesystem path of the repository.
	LocalPath string
	// NewTargetDir is the destination directory if the repository is relocated.
	NewTargetDir string
	// Forge is the target forge interface for remote provisioning.
	Forge forge.Target
	// ForgeOptions holds authentication and client configuration for the forge.
	ForgeOptions forge.TargetOptions
	// Owner is the user or organization under which the remote repository is created.
	Owner string
	// RepoName is the repository name on the remote forge.
	RepoName string
	// Description is the repository description set on creation.
	Description string
	// Private specifies whether the remote repository is private or public.
	Private bool
	// Protocol specifies the transport protocol ("https" or "ssh").
	Protocol string
	// RelocateLocal indicates whether to move the repository into NewTargetDir.
	RelocateLocal bool
	// DryRun reports intended actions without executing mutations.
	DryRun bool
}

// Result describes the outcome of adopting a repository.
type Result struct {
	// RepoName is the name of the adopted repository.
	RepoName string `json:"repo_name"`
	// OriginalPath is the initial filesystem path before adoption.
	OriginalPath string `json:"original_path"`
	// FinalPath is the filesystem path where the repository resides after adoption.
	FinalPath string `json:"final_path"`
	// RemoteURL is the remote repository clone URL configured for origin.
	RemoteURL string `json:"remote_url,omitempty"`
	// Action describes the action performed (e.g. ADOPTED, RELOCATED, DRY-RUN).
	Action string `json:"action"`
	// Message describes the outcome or any error encountered.
	Message string `json:"message"`
}

// Execute runs the repository adoption process: local relocation,
// remote creation on the forge, remote origin configuration, and initial push.
func Execute(ctx context.Context, opts Options) (Result, error) {
	res := Result{
		RepoName:     opts.RepoName,
		OriginalPath: opts.LocalPath,
		FinalPath:    opts.LocalPath,
	}

	if strings.TrimSpace(opts.RepoName) == "" {
		return res, errors.New("repository name must not be empty")
	}
	if strings.TrimSpace(opts.LocalPath) == "" {
		return res, errors.New("local path must not be empty")
	}

	finalPath, err := relocate(opts, &res)
	if err != nil {
		return res, err
	}

	if opts.Forge == nil {
		res.Action = "RELOCATED"
		res.Message = "repository processed locally; no remote provisioned"
		return res, nil
	}

	if opts.DryRun {
		res.Action = "DRY-RUN"
		res.RemoteURL = fmt.Sprintf("https://%s/%s/%s.git", opts.ForgeOptions.BaseURL, opts.Owner, opts.RepoName)
		res.Message = "would create remote repository and link origin"
		return res, nil
	}

	return provision(ctx, opts, finalPath, res)
}

// relocate moves the clone to NewTargetDir when asked to, records the new
// location in res, and returns the path the clone now lives at. A dry run
// records where it would go and leaves it where it is.
func relocate(opts Options, res *Result) (string, error) {
	if !opts.RelocateLocal || opts.NewTargetDir == "" || opts.NewTargetDir == opts.LocalPath {
		return opts.LocalPath, nil
	}
	cleanTarget := filepath.Clean(opts.NewTargetDir)
	if opts.DryRun {
		res.FinalPath = cleanTarget
		return opts.LocalPath, nil
	}
	if err := adoptMkdirAll(filepath.Dir(cleanTarget), 0o750); err != nil {
		return "", fmt.Errorf("creating directory layout: %w", err)
	}
	if err := moveDir(opts.LocalPath, cleanTarget); err != nil {
		return "", err
	}
	res.FinalPath = cleanTarget
	return cleanTarget, nil
}

// moveDir renames src to dst, falling back to copy-then-remove when the
// two are on different devices.
func moveDir(src, dst string) error {
	err := adoptRename(src, dst)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.EXDEV) {
		return fmt.Errorf("relocating repository: %w", err)
	}
	if copyErr := adoptCopyDir(src, dst); copyErr != nil {
		return fmt.Errorf("relocating repository across devices: %w", copyErr)
	}
	_ = adoptRemoveAll(src)
	return nil
}

// provision creates the remote repository, points origin at it and pushes
// the current branch.
func provision(ctx context.Context, opts Options, finalPath string, res Result) (Result, error) {
	spec := forge.RepoSpec{
		Name:        opts.RepoName,
		Description: opts.Description,
		Private:     opts.Private,
	}

	remote, err := opts.Forge.EnsureRepo(ctx, opts.Owner, spec, opts.ForgeOptions)
	if err != nil {
		return res, fmt.Errorf("remote repository provisioning failed: %w", err)
	}

	chosenURL := remote.HTTPSURL
	if opts.Protocol == "ssh" && remote.SSHURL != "" {
		chosenURL = remote.SSHURL
	}
	res.RemoteURL = chosenURL

	if err := adoptAddRemote(ctx, finalPath, "origin", chosenURL); err != nil {
		return res, fmt.Errorf("setting git origin remote failed: %w", err)
	}
	if err := pushInitial(ctx, opts, finalPath, chosenURL); err != nil {
		return res, err
	}

	res.Action = "ADOPTED"
	res.Message = fmt.Sprintf("remote repository linked to %s and pushed", chosenURL)
	return res, nil
}

// pushInitial pushes the current branch (main when it cannot be read) to
// origin, with the forge's credential over HTTPS. An empty repository has
// nothing to push.
func pushInitial(ctx context.Context, opts Options, path, pushURL string) error {
	if adoptIsEmpty(ctx, path) {
		return nil
	}
	branch, branchErr := adoptCurrentBranch(ctx, path)
	if branchErr != nil || branch == "" {
		branch = "main"
	}
	var cred *git.PushCredential
	if opts.Protocol != "ssh" && opts.ForgeOptions.Token != "" {
		u, s := opts.Forge.PushAuth(opts.ForgeOptions)
		cred = &git.PushCredential{Username: u, Secret: s}
	}
	if err := adoptPushUpstream(ctx, path, "origin", branch, pushURL, cred); err != nil {
		return fmt.Errorf("initial push to origin/%s failed: %w", branch, err)
	}
	return nil
}

func copyDirRecursive(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		data, err := adoptReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
}
