// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package engine provides the core concurrency and execution logic for Corral.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-isatty"
	"github.com/sebastienrousseau/corralctl/internal/forge"
	"github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/sebastienrousseau/corralctl/internal/github"
	"github.com/sebastienrousseau/corralctl/internal/tui"
)

// OutputFormat controls how operation results are emitted.
type OutputFormat string

const (
	// OutputText emits human-readable, line-oriented progress output.
	OutputText OutputFormat = "text"
	// OutputJSON emits a single aggregated JSON document at the end of the run.
	OutputJSON OutputFormat = "json"
	// OutputNDJSON emits one JSON object per result as a stream of newline-delimited records.
	OutputNDJSON OutputFormat = "ndjson"
)

// RunOptions contains all execution controls for a run.
type RunOptions struct {
	// Owner is the GitHub user or organization whose repositories are processed.
	Owner string
	// BaseDir is the root directory under which repositories are laid out.
	BaseDir string
	// Concurrency is the number of worker goroutines processing repositories; must be >= 1.
	Concurrency int
	// DryRun, when true, reports intended actions without performing clone or pull operations.
	DryRun bool
	// Orphans, when true, enables detection of local repositories no longer present upstream.
	Orphans bool
	// Protocol selects the clone transport and must be either "https" or "ssh".
	Protocol string
	// DoSync, when true, pulls updates into existing repositories.
	DoSync bool
	// Output selects the result emission format (text, json, or ndjson).
	Output OutputFormat
	// Interactive, when true, displays an interactive selector before processing.
	Interactive bool

	// Fetch holds the options passed to the repository listing call.
	Fetch github.FetchOptions
	// Forge names the hosting service to list from: github, gitlab, gitea,
	// forgejo or codeberg. Empty means github, which is what corral did
	// before other forges were supported — a default that changes under
	// people is worse than a narrow one.
	Forge string
	// ForgeURL is a self-hosted instance's base address. Required for
	// gitea and forgejo, which have no single public instance; optional
	// for the others, which do.
	ForgeURL string
	// Clone holds the options passed to each Git clone operation.
	Clone git.CloneOptions
	// Sync controls when an already-cloned repository is actually pulled.
	Sync SyncOptions
	// Layout specifies the templated path structure for repositories. Custom
	// layouts may use Collection and Bucket in addition to the legacy fields.
	Layout string
	// FinderTags enables managed macOS Finder metadata on repository folders.
	FinderTags bool
	// Version is the build version of Corral.
	Version string
}

// SyncOptions configures the engine's per-repo sync decision. Kept separate
// from git.CloneOptions because forcing a sync is a corral-level policy
// choice, not a clone-time git flag.
type SyncOptions struct {
	// Force, when true, runs `git pull` even when the cached state shows
	// the upstream pushed_at is unchanged.
	Force bool
	// IgnoreSubmoduleFailures, when true with Clone.RecurseSubmodules, allows
	// the parent repository to update even when a submodule sync fails (e.g.
	// the submodule repo was deleted upstream or its access revoked). The
	// failure is logged as a WARN but not propagated.
	IgnoreSubmoduleFailures bool
}

// Job encapsulates a repository to be processed along with its target directories.
type Job struct {
	// Repo is the GitHub repository to be processed.
	Repo github.Repo
	// Target is the destination directory for the repository under the new layout.
	Target string
	// Legacy is the directory where the repository may exist under the old layout.
	Legacy string
	// Existing is an identity-matched clone at a previous layout path.
	Existing string
}

// RepoResult represents the final status of processing a repository.
type RepoResult struct {
	// RepoName is the name of the processed repository.
	RepoName string `json:"repo"`
	// Action is the outcome verb, such as CLONE, SYNC, SKIP, ERROR, or DRY-RUN.
	Action string `json:"action"`
	// Message is a human-readable description of the outcome.
	Message string `json:"message"`
	// Target is the destination directory for the repository.
	Target string `json:"target"`
	// Visibility is the repository visibility (e.g. Public or Private).
	Visibility string `json:"visibility"`
	// Language is the normalized primary language directory name.
	Language string `json:"language"`
	// DryRun indicates whether the run was performed in dry-run mode.
	DryRun bool `json:"dry_run"`
	// Protocol is the clone transport used (https or ssh).
	Protocol string `json:"protocol"`
	// ClonedURL is the URL used for cloning, if a clone was attempted.
	ClonedURL string `json:"clone_url,omitempty"`
	// SyncAttempt indicates whether a sync (pull) was attempted.
	SyncAttempt bool `json:"sync_attempt"`
	// Moved indicates that an identity-matched clone was relocated.
	Moved bool `json:"moved,omitempty"`
}

// Summary tracks aggregate run outcomes.
type Summary struct {
	// Total is the number of repositories scheduled for processing.
	Total int `json:"total"`
	// Cloned is the number of repositories successfully cloned.
	Cloned int `json:"cloned"`
	// Synced is the number of repositories successfully synced.
	Synced int `json:"synced"`
	// Moved is the number of repositories relocated to their desired layout.
	Moved int `json:"moved"`
	// Skipped is the number of repositories skipped.
	Skipped int `json:"skipped"`
	// Failed is the number of repositories that failed to process.
	Failed int `json:"failed"`
	// Canceled is true when the run was interrupted by ctx cancellation
	// (typically SIGINT/SIGTERM). The result set in that case is partial:
	// a scripted consumer reading json/ndjson output should treat it as
	// "not all repositories were processed" rather than a clean run.
	Canceled bool `json:"canceled,omitempty"`
}

// cancelExitCode is the POSIX convention for "killed by SIGINT" (128 + 2).
// Used when a non-interactive corralctl run is interrupted, so scripted
// callers can distinguish cancellation from other failures.
const cancelExitCode = 130

var (
	osExit           = os.Exit
	gitPull          = git.Pull
	gitClone         = git.Clone
	gitCurrentBranch = git.CurrentBranch
	gitIsEmpty       = git.IsEmpty
	gitRemoteOrigin  = git.RemoteOriginFromConfig
	isTerminal       = isatty.IsTerminal
	runProgram       = func(p *tea.Program) (tea.Model, error) { return p.Run() }
	runSelector      = tui.RunSelector
	walkDir          = filepath.WalkDir
	readDir          = os.ReadDir
	statPath         = os.Stat
	sameFile         = os.SameFile
	mkdirAll         = os.MkdirAll
	renamePath       = os.Rename
	applyTags        = applyFinderTags
)

// ExitError carries the process exit status a failed run should produce.
//
// The engine is a library: it reports failure by returning an error rather
// than by calling os.Exit, so an embedder can decide what a failure means.
// Run is the thin wrapper that turns this back into a process exit for the
// CLI. Silent is set when the condition was already reported to the user —
// per-repository failures are printed as results, and cancellation is
// announced by emitCancellation — so the caller must not print it a second
// time.
type ExitError struct {
	// Code is the process exit status the CLI should terminate with.
	Code int
	// Silent is true when the failure has already been reported to the user
	// and printing the error again would duplicate it.
	Silent bool
	// Err is the underlying cause, or nil when Silent.
	Err error
}

// Error implements the error interface.
func (e *ExitError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return fmt.Sprintf("run failed with exit status %d", e.Code)
}

// Unwrap returns the underlying cause so errors.Is and errors.As reach it.
func (e *ExitError) Unwrap() error { return e.Err }

// exitStatus reports the process exit status an error from RunE maps to.
// Anything that is not an ExitError is an ordinary failure and exits 1.
func exitStatus(err error) int {
	var exit *ExitError
	if errors.As(err, &exit) {
		return exit.Code
	}
	return 1
}

// silentFailure reports whether err was already communicated to the user.
func silentFailure(err error) bool {
	var exit *ExitError
	return errors.As(err, &exit) && exit.Silent
}

// Run executes the core Corral workflow and terminates the process when the
// run fails. It is the entry point for the CLI; embedders should call RunE,
// which returns the error instead of exiting.
func Run(ctx context.Context, opts RunOptions) {
	err := RunE(ctx, opts)
	if err == nil {
		return
	}
	if !silentFailure(err) {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
	}
	osExit(exitStatus(err))
}

// RunE executes the core Corral workflow, orchestrating GitHub API fetches,
// legacy layout migrations, concurrent Git operations, and orphaned
// repository detection. It returns nil on a clean run, and otherwise an
// error — an *ExitError when the failure maps to a specific exit status.
func RunE(ctx context.Context, opts RunOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := opts.normalize(); err != nil {
		return err
	}
	if err := checkRunForge(opts); err != nil {
		return err
	}
	installTokenProvider(ctx, opts)

	isTTY := isTerminal(os.Stdout.Fd())
	// stdout is reserved for the selected output format. Diagnostics always go
	// to stderr so JSON and NDJSON remain parseable.
	log.SetOutput(os.Stderr)

	repos, proceed, err := resolveRepos(ctx, opts, isTTY)
	if err != nil {
		return err
	}
	if !proceed {
		return nil
	}

	layoutTemplate, err := parseLayoutTemplate(effectiveLayout(opts, github.Repo{}))
	if err != nil {
		return fmt.Errorf("invalid layout template: %w", err)
	}
	if err := prepareWorkspace(opts, repos); err != nil {
		return err
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)

	outcome := executeJobs(ctx, opts, repos, layoutTemplate, isTTY, encoder)
	return finishRun(ctx, opts, repos, isTTY, encoder, &outcome)
}

// checkRunForge validates the forge selection. The selection itself
// travels with opts to every reader (the listing and orphan detection),
// never through package state, so concurrent runs cannot see each
// other's forge. Validated here rather than at the fetch, so `--forge gitub` fails
// immediately instead of after a workspace scan.
func checkRunForge(opts RunOptions) error {
	f, err := forge.Resolve(opts.Forge, opts.ForgeURL)
	if err != nil {
		return err
	}
	// Search selectors are a GitHub API feature. Passed to any other forge
	// they are read as an owner name, and the 404 that follows blames the
	// owner — sending somebody to look for a typo that is not there.
	if isSearchOwner(opts.Owner) && f.Name() != "github" {
		return fmt.Errorf(
			"%s does not support %q: topic: and language: are GitHub search queries, "+
				"and %s lists repositories by owner\n"+
				"Name an owner instead, or drop --forge to search GitHub",
			f.Name(), opts.Owner, f.Name())
	}
	return nil
}

// finishRun reports a completed worker phase: it marks and announces a
// cancelled run, tidies the classic layout, reports orphans, writes the
// json aggregate, and maps the outcome to the error RunE returns.
func finishRun(
	ctx context.Context,
	opts RunOptions,
	repos []github.Repo,
	isTTY bool,
	encoder *json.Encoder,
	outcome *runOutcome,
) error {
	// Detect cancellation after workers + consumer drain. runCancellation is
	// the authoritative signal: it is set if and only if a SIGINT/SIGTERM, a
	// parent's cancel() or quitting the progress view stopped the run. The
	// result set in that case is partial and we must signal that to
	// downstream consumers so they don't mistake it for a clean run.
	if err := runCancellation(ctx, outcome); err != nil {
		outcome.summary.Canceled = true
		emitCancellation(opts.Output, isTTY, encoder, err)
	}

	if usesClassicLayout(opts) && !opts.DryRun {
		cleanupEmptyFolders(opts.BaseDir, repos)
	}

	orphanPaths := emitOrphanReport(opts, repos, outcome, encoder)

	if err := emitAggregate(opts, outcome, orphanPaths); err != nil {
		return err
	}
	return runOutcomeError(outcome)
}

// normalize validates the option set and fills in documented defaults. It is
// the single place a bad option becomes an error, so every caller — CLI,
// MCP server or embedder — rejects the same inputs for the same reasons.
func (opts *RunOptions) normalize() error {
	if opts.Concurrency < 1 {
		return errors.New("concurrency must be >= 1")
	}
	if opts.Fetch.Limit < 0 {
		return errors.New("limit must be >= 0")
	}
	if opts.Owner == "" {
		return errors.New("owner must not be empty")
	}
	if opts.BaseDir == "" {
		return errors.New("base directory must not be empty")
	}
	if opts.Protocol != "https" && opts.Protocol != "ssh" {
		return errors.New("protocol must be either ssh or https")
	}
	if opts.Output == "" {
		opts.Output = OutputText
	}
	return nil
}
