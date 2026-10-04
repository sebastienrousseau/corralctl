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
)

// AdoptOptions encapsulates options for discovering, relocating, and
// provisioning a remote repository for an untracked local repository.
type AdoptOptions struct {
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

// AdoptResult describes the outcome of adopting a repository.
type AdoptResult struct {
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

// ExecuteAdoption executes the repository adoption process: local relocation,
// remote creation on the forge, remote origin configuration, and initial push.
func ExecuteAdoption(ctx context.Context, opts AdoptOptions) (AdoptResult, error) {
	res := AdoptResult{
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

	finalPath := opts.LocalPath
	if opts.RelocateLocal && opts.NewTargetDir != "" && opts.NewTargetDir != opts.LocalPath {
		cleanTarget := filepath.Clean(opts.NewTargetDir)
		if opts.DryRun {
			res.FinalPath = cleanTarget
		} else {
			if err := adoptMkdirAll(filepath.Dir(cleanTarget), 0o750); err != nil {
				return res, fmt.Errorf("creating directory layout: %w", err)
			}
			if err := adoptRename(opts.LocalPath, cleanTarget); err != nil {
				return res, fmt.Errorf("relocating repository: %w", err)
			}
			finalPath = cleanTarget
			res.FinalPath = finalPath
		}
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

	if !adoptIsEmpty(ctx, finalPath) {
		branch, branchErr := adoptCurrentBranch(ctx, finalPath)
		if branchErr != nil || branch == "" {
			branch = "main"
		}
		var cred *git.PushCredential
		if opts.Protocol != "ssh" && opts.ForgeOptions.Token != "" {
			u, s := opts.Forge.PushAuth(opts.ForgeOptions)
			cred = &git.PushCredential{Username: u, Secret: s}
		}
		if err := adoptPushUpstream(ctx, finalPath, "origin", branch, chosenURL, cred); err != nil {
			return res, fmt.Errorf("initial push to origin/%s failed: %w", branch, err)
		}
	}

	res.Action = "ADOPTED"
	res.Message = fmt.Sprintf("remote repository linked to %s and pushed", chosenURL)
	return res, nil
}
