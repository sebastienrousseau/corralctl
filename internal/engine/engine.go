// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package engine provides the core concurrency and execution logic for Corral.
package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/template"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
	"github.com/sebastienrousseau/corral/internal/diag"
	"github.com/sebastienrousseau/corral/internal/forge"
	"github.com/sebastienrousseau/corral/internal/git"
	"github.com/sebastienrousseau/corral/internal/github"
	"github.com/sebastienrousseau/corral/internal/tui"
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

const (
	defaultLayout          = "{{.Collection}}/{{.Bucket}}/{{.Name}}"
	searchLayout           = "{{.Owner}}/{{.Collection}}/{{.Bucket}}/{{.Name}}"
	maxLayoutTemplateBytes = 4096
)

var (
	fetchRepos       = fetchFromForge
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
	// Validated here rather than at the fetch, so `--forge gitub` fails
	// immediately instead of after a workspace scan.
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
	fetchForgeName, fetchForgeURL = f.Name(), opts.ForgeURL
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

	// Detect cancellation after workers + consumer drain. ctx.Err() at this
	// point is the authoritative signal: it's set if and only if a SIGINT/
	// SIGTERM (or a parent's cancel()) fired during the run. The result set
	// in that case is partial and we must signal that to downstream consumers
	// so they don't mistake an aborted run for a clean one.
	if err := ctx.Err(); err != nil {
		outcome.summary.Canceled = true
		emitCancellation(opts.Output, isTTY, encoder, err)
	}

	if usesClassicLayout(opts) && !opts.DryRun {
		cleanupEmptyFolders(opts.BaseDir, repos)
	}

	orphanPaths := emitOrphanReport(opts, repos, &outcome, encoder)

	if err := emitAggregate(opts, &outcome, orphanPaths); err != nil {
		return err
	}
	return runOutcomeError(&outcome)
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

// installTokenProvider lets git authenticate HTTPS clones and pulls of
// private repositories with the same credential resolved for the GitHub API.
// The token is resolved at most once, and only if git actually asks for it.
func installTokenProvider(ctx context.Context, opts RunOptions) {
	var tokenOnce sync.Once
	var token string
	git.TokenProvider = func() string {
		tokenOnce.Do(func() { token = github.Token(ctx, opts.Fetch.AuthMode) })
		return token
	}
}

// resolveRepos produces the repository set for this run, either from the
// interactive selector or from a direct API fetch. The bool reports whether
// the run should proceed: it is false when the user dismissed the selector
// or selected nothing, which is a clean exit rather than a failure.
func resolveRepos(ctx context.Context, opts RunOptions, isTTY bool) ([]github.Repo, bool, error) {
	if !opts.Interactive {
		announceFetch(opts, isTTY)
		repos, err := fetchRepos(ctx, opts.Owner, opts.Fetch)
		if err != nil {
			return nil, false, err
		}
		warnOnLimit(opts, len(repos))
		return repos, true, nil
	}

	tui.Version = opts.Version
	repos, ok, err := runSelector(ctx, opts.Owner, opts.Fetch, func() ([]github.Repo, error) {
		return fetchRepos(ctx, opts.Owner, opts.Fetch)
	})
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	if len(repos) == 0 {
		if opts.Output == OutputText {
			fmt.Println("No repositories selected.")
		}
		return nil, false, nil
	}
	if opts.Output == OutputText && isTTY {
		fmt.Print("\033[2J\033[H")
	}
	warnOnLimit(opts, len(repos))
	return repos, true, nil
}

// announceFetch prints the banner and progress line that precede a
// non-interactive fetch, honouring the selected output format and whether
// stdout is a terminal.
func announceFetch(opts RunOptions, isTTY bool) {
	if opts.Output != OutputText {
		return
	}
	if !isTTY {
		log.Println(fetchAnnouncement(opts))
		return
	}
	if os.Getenv("CORRAL_SHOW_LOGO") != "0" {
		fmt.Print(tui.GetStyledLogo())
		fmt.Print(lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render("   ⧇ Organising Repositories") + "\n")
		fmt.Print(lipgloss.NewStyle().Foreground(lipgloss.Color("238")).Render("   "+strings.Repeat("─", 58)) + "\n\n")
	}
	fmt.Println(fetchAnnouncement(opts))
}

// fetchAnnouncement names the host being listed from.
//
// It said "GitHub" unconditionally, which became a lie the moment
// cloning worked against five forges: somebody running `--forge codeberg`
// was told their repositories were coming from GitHub. A message that
// names the wrong service is worse than no message, because it is the
// only confirmation a user gets that the flag took effect.
func fetchAnnouncement(opts RunOptions) string {
	name := "GitHub"
	if f, err := forge.Resolve(opts.Forge, opts.ForgeURL); err == nil {
		name = f.Name()
	}
	if opts.ForgeURL != "" {
		// The instance matters more than the software for a self-hosted
		// deployment: two of them run the same forge.
		return fmt.Sprintf("Fetching repositories from %s (%s)...", name, opts.ForgeURL)
	}
	return fmt.Sprintf("Fetching repositories from %s...", name)
}

// warnOnLimit reports a fetch that returned exactly --limit repositories,
// which means the listing was probably truncated.
func warnOnLimit(opts RunOptions, fetched int) {
	if opts.Fetch.Limit > 0 && fetched == opts.Fetch.Limit && opts.Output == OutputText {
		fmt.Printf("WARNING: Fetched exactly %d repositories. There may be more.\n", opts.Fetch.Limit)
	}
}

// prepareWorkspace performs the one-off layout work a classic-layout run
// needs before any repository is processed: creating the collection folders,
// migrating clones left by older layouts, and repairing directory case.
func prepareWorkspace(opts RunOptions, repos []github.Repo) error {
	if !usesClassicLayout(opts) || opts.DryRun {
		return nil
	}
	if err := ensureAppleCollections(opts.BaseDir); err != nil {
		return fmt.Errorf("failed creating repository collections: %w", err)
	}
	migrateLegacy(opts.BaseDir, repos)
	normalizeLayoutDirCase(opts.BaseDir, repos)
	return nil
}

// runOutcome is everything the worker phase produced.
type runOutcome struct {
	results   []RepoResult
	summary   Summary
	outputErr error
}

// executeJobs runs the whole worker phase: it starts the pool, enqueues one
// job per repository, streams results to the selected output, and returns
// once every worker and the consumer have drained.
func executeJobs(
	ctx context.Context,
	opts RunOptions,
	repos []github.Repo,
	layoutTemplate *template.Template,
	isTTY bool,
	encoder *json.Encoder,
) runOutcome {
	existingByRemote := discoverExistingRepos(opts.BaseDir)

	jobs := make(chan Job, len(repos))
	results := make(chan RepoResult, len(repos))

	workers := startWorkers(ctx, opts, jobs, results)
	scheduled := enqueueJobs(ctx, opts, repos, layoutTemplate, existingByRemote, jobs, results)
	close(jobs)

	outcome := runOutcome{}
	outcome.summary.Total = scheduled

	var program *tea.Program
	if opts.Output == OutputText && isTTY {
		program = tea.NewProgram(tui.NewModel(scheduled))
	}

	var consumer sync.WaitGroup
	consumer.Add(1)
	go func() {
		defer consumer.Done()
		consumeResults(opts, results, program, encoder, &outcome)
	}()

	if program != nil {
		if _, err := runProgram(program); err != nil {
			fmt.Fprintf(os.Stderr, "Error running TUI: %v\n", err)
		}
		// Ensure Send unblocks if an injected runner or an early TUI failure
		// returns without shutting down Bubble Tea's context.
		program.Kill()
	}

	workers.Wait()
	close(results)
	consumer.Wait()
	return outcome
}

// startWorkers launches opts.Concurrency goroutines draining jobs into
// results, and returns the WaitGroup that reports when they have all
// finished. Every send and receive is guarded by ctx so a cancelled run
// unwinds instead of blocking on a full channel.
func startWorkers(ctx context.Context, opts RunOptions, jobs <-chan Job, results chan<- RepoResult) *sync.WaitGroup {
	var wg sync.WaitGroup
	for i := 0; i < opts.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job, ok := <-jobs:
					if !ok {
						return
					}
					msg := processRepo(ctx, opts.Owner, opts.Protocol, opts.DoSync, opts.DryRun, opts.Clone, opts.Sync, job)
					applyJobFinderTags(opts, job, msg)
					select {
					case <-ctx.Done():
						return
					case results <- msg:
					}
				}
			}
		}()
	}
	return &wg
}

// enqueueJobs resolves each repository's target directory and queues it for
// a worker, returning the number of results the consumer should expect. A
// repository whose layout cannot be evaluated, or which is cloned in more
// than one place, is reported directly as an ERROR result and not queued.
func enqueueJobs(
	ctx context.Context,
	opts RunOptions,
	repos []github.Repo,
	layoutTemplate *template.Template,
	existingByRemote map[string][]string,
	jobs chan<- Job,
	results chan<- RepoResult,
) int {
	scheduled := 0
	for _, repo := range repos {
		existingPaths := existingByRemote[repoRemoteIdentity(repo)]
		relPath, err := executeLayout(layoutTemplate, repo, opts.Owner)
		if err != nil {
			scheduled++
			results <- RepoResult{
				RepoName: repo.Name, Action: "ERROR",
				Message:    fmt.Sprintf("failed to evaluate layout: %v", err),
				Visibility: repo.Visibility, Language: normalizeLanguage(repo.Language),
				DryRun: opts.DryRun, Protocol: opts.Protocol,
			}
			continue
		}
		targetDir := filepath.Join(opts.BaseDir, relPath)
		if len(existingPaths) > 1 {
			scheduled++
			results <- RepoResult{
				RepoName: repo.Name, Action: "ERROR", Target: targetDir,
				Message:    "duplicate clone locations: " + strings.Join(existingPaths, ", "),
				Visibility: repo.Visibility, Language: normalizeLanguage(repo.Language),
				DryRun: opts.DryRun, Protocol: opts.Protocol,
			}
			continue
		}
		legacyDir := filepath.Join(opts.BaseDir, normalizeLanguage(repo.Language), repo.Name)
		select {
		case <-ctx.Done():
			return scheduled
		case jobs <- Job{Repo: repo, Target: targetDir, Legacy: legacyDir, Existing: firstPath(existingPaths)}:
			scheduled++
		}
	}
	return scheduled
}

// consumeResults drains the results channel, accumulating the summary and
// emitting each result in the selected format. It runs until results is
// closed, which the caller does only after every worker has finished.
func consumeResults(
	opts RunOptions,
	results <-chan RepoResult,
	program *tea.Program,
	encoder *json.Encoder,
	outcome *runOutcome,
) {
	for msg := range results {
		outcome.results = append(outcome.results, msg)
		outcome.summary.add(msg)
		switch {
		case program != nil:
			program.Send(toLogMsg(msg))
		case opts.Output == OutputText:
			log.Printf("%s [%s] %s: %s", resultIcon(msg), msg.Action, msg.RepoName, msg.Message)
		case opts.Output == OutputNDJSON:
			if err := encoder.Encode(msg); err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: failed to encode ndjson result: %v\n", err)
				if outcome.outputErr == nil {
					outcome.outputErr = err
				}
			}
		}
	}
}

// resultIcon is the single-character status marker shown beside a result in
// non-TTY text output.
func resultIcon(msg RepoResult) string {
	switch {
	case msg.Action == "ERROR" || strings.HasPrefix(msg.Action, "FAIL"):
		return "✗"
	case msg.Action == "SKIP":
		return "-"
	default:
		return "✓"
	}
}

// emitOrphanReport finds and reports local clones that no longer exist
// upstream, returning the paths so the aggregate JSON document can carry
// them. Orphan detection is skipped on cancellation: the local tree may be
// mid-clone and the report would be misleading.
func emitOrphanReport(opts RunOptions, repos []github.Repo, outcome *runOutcome, encoder *json.Encoder) []string {
	if !opts.Orphans || outcome.summary.Canceled {
		return nil
	}
	orphanPaths := findOrphans(opts.Owner, opts.BaseDir, repos)
	switch opts.Output {
	case OutputText:
		emitOrphans(opts.Owner, orphanPaths)
	case OutputNDJSON:
		for _, orphan := range orphanPaths {
			if err := encoder.Encode(RepoResult{Action: "ORPHAN", Target: orphan, Message: "local repository is absent upstream"}); err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: failed to encode orphan result: %v\n", err)
				if outcome.outputErr == nil {
					outcome.outputErr = err
				}
			}
		}
	}
	return orphanPaths
}

// emitAggregate writes the single JSON document produced by --output json.
// The other formats have already streamed their output by this point.
func emitAggregate(opts RunOptions, outcome *runOutcome, orphanPaths []string) error {
	if opts.Output != OutputJSON {
		return nil
	}
	payload := struct {
		Summary Summary      `json:"summary"`
		Repos   []RepoResult `json:"repos"`
		Orphans []string     `json:"orphans,omitempty"`
	}{
		Summary: outcome.summary,
		Repos:   outcome.results,
		Orphans: orphanPaths,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(payload); err != nil {
		return fmt.Errorf("failed to encode json output: %w", err)
	}
	return nil
}

// runOutcomeError maps a completed run to the error the caller should see.
// Both failure modes are silent: cancellation is already announced by
// emitCancellation, and per-repository failures were emitted as results.
func runOutcomeError(outcome *runOutcome) error {
	if outcome.summary.Canceled {
		// POSIX 130 = killed by SIGINT. Lets scripted callers distinguish
		// cancellation from other failure modes (which exit 1).
		return &ExitError{Code: cancelExitCode, Silent: true}
	}
	if outcome.outputErr != nil {
		return &ExitError{Code: 1, Silent: true, Err: outcome.outputErr}
	}
	if outcome.summary.Failed > 0 {
		return &ExitError{Code: 1, Silent: true, Err: fmt.Errorf("%d repositor%s failed", outcome.summary.Failed, plural(outcome.summary.Failed))}
	}
	return nil
}

// plural returns the suffix that agrees with n for the stem "repositor".
func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

func ensureAppleCollections(baseDir string) error {
	for _, collection := range []string{"Public", "Private", "Work", "Forks"} {
		if err := mkdirAll(filepath.Join(baseDir, collection), 0o750); err != nil {
			return err
		}
	}
	return nil
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

// emitCancellation writes a final cancellation marker to the active output
// channel so non-interactive consumers learn the run was interrupted. The
// interactive TUI path (text + TTY) stays silent — the TUI already redraws
// on SIGINT and the user knows they pressed Ctrl-C.
func emitCancellation(output OutputFormat, isTTY bool, encoder *json.Encoder, cause error) {
	msg := fmt.Sprintf("operation canceled (%v)", cause)
	switch output {
	case OutputNDJSON:
		// Terminal record so a consumer piping into jq sees the cancel.
		// Action: "CANCELED" matches the verb scheme used for other actions.
		final := RepoResult{Action: "CANCELED", Message: msg}
		if err := encoder.Encode(final); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed to encode ndjson cancellation: %v\n", err)
		}
	case OutputJSON:
		// The aggregated json payload (written below in Run) already carries
		// summary.Canceled=true; nothing to emit here.
	case OutputText:
		if !isTTY {
			// Non-TTY text path: scripted log consumers get a single line.
			// TTY users get nothing — the TUI handles its own signal display.
			log.Printf("%s", msg)
		}
	}
}

func (s *Summary) add(msg RepoResult) {
	if msg.Moved {
		s.Moved++
	}
	switch msg.Action {
	case "CLONE":
		s.Cloned++
	case "SYNC":
		s.Synced++
	case "SKIP":
		s.Skipped++
	case "ERROR":
		s.Failed++
	}
}

func toLogMsg(msg RepoResult) tui.LogMsg {
	return tui.LogMsg{RepoName: msg.RepoName, Action: msg.Action, Message: msg.Message}
}

func usesClassicLayout(opts RunOptions) bool {
	return (opts.Layout == "" && !isSearchOwner(opts.Owner)) || opts.Layout == defaultLayout
}

func effectiveLayout(opts RunOptions, repo github.Repo) string {
	if opts.Layout != "" {
		return opts.Layout
	}
	if isSearchOwner(opts.Owner) {
		return searchLayout
	}
	return defaultLayout
}

func isSearchOwner(owner string) bool {
	return strings.HasPrefix(owner, "topic:") || strings.HasPrefix(owner, "language:")
}

// repoRemoteIdentity is the canonical identity of a listed repository, for
// comparison against a local clone's remote.
//
// Derived from the clone URL the forge returned, and through the same
// function the local side goes through, so the two are comparable by
// construction.
//
// It used to be "github.com/" + FullName, which only ever agreed with a
// real remote because GitHub's clone URLs happen to be
// github.com/<full name>.git. On any other forge it disagreed with every
// clone — and originMismatch treats a disagreement as an origin
// collision, so v0.0.30 refused to sync every existing Codeberg, GitLab,
// Gitea and Forgejo clone with "expected github.com/...". Cloning worked;
// the second run failed.
func repoRemoteIdentity(repo github.Repo) string {
	// The URL first, always: it is the only field that carries the host,
	// and every forge adapter populates it.
	if repo.CloneURL != "" {
		if id := git.CanonicalRemote(repo.CloneURL); id != "" {
			return id
		}
	}
	// Fallbacks for a repository with no clone URL, which no forge here
	// produces. They assume github.com because there is nothing else to
	// assume — which is precisely the assumption that caused the bug
	// above, so they are reached only when the URL is absent and never in
	// preference to it.
	if repo.FullName != "" {
		return strings.ToLower("github.com/" + repo.FullName)
	}
	if repo.Owner != "" && repo.Name != "" {
		return strings.ToLower("github.com/" + repo.Owner + "/" + repo.Name)
	}
	return ""
}

func firstPath(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}

func discoverExistingRepos(baseDir string) map[string][]string {
	found := make(map[string][]string)
	_ = walkDir(baseDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.IsDir() || path == baseDir {
			return nil
		}
		if git.IsRepository(path) {
			if remote, err := gitRemoteOrigin(path); err == nil {
				if identity := git.CanonicalRemote(remote); identity != "" {
					found[identity] = append(found[identity], path)
				}
			}
			return filepath.SkipDir
		}
		if skipDiscoveryDirectory(d.Name()) {
			return filepath.SkipDir
		}
		return nil
	})
	return found
}

func skipDiscoveryDirectory(name string) bool {
	switch name {
	case ".git", ".cache", ".next", ".venv", "DerivedData", "Pods", "build", "dist", "node_modules", "target", "vendor", "venv":
		return true
	default:
		return false
	}
}

func normalizeLanguage(lang string) string {
	if lang == "" {
		return "other"
	}
	l := lang
	switch l {
	case "C#":
		l = "CSharp"
	case "C++":
		l = "Cpp"
	}
	l = strings.ReplaceAll(l, " ", "_")
	l = strings.ReplaceAll(l, "/", "_")
	return strings.ToLower(l)
}

func repositoryBucket(repo github.Repo) string {
	if strings.HasSuffix(strings.ToLower(repo.Name), ".github.io") {
		return "Web"
	}
	return canonicalLanguage(repo.Language)
}

func canonicalLanguage(lang string) string {
	normalized := normalizeLanguage(lang)
	canonical := map[string]string{
		"c": "C", "cpp": "Cpp", "csharp": "CSharp", "css": "CSS",
		"dockerfile": "Docker", "go": "Go", "html": "HTML",
		"javascript": "JavaScript", "jupyter_notebook": "Python", "lua": "Lua",
		"objective-c": "Objective-C", "objective-c++": "Objective-Cpp", "other": "Other",
		"php": "PHP", "python": "Python", "ruby": "Ruby", "rust": "Rust",
		"scss": "SCSS", "shell": "Shell", "solidity": "Solidity", "stylus": "Stylus",
		"swift": "Swift", "tex": "TeX", "typescript": "TypeScript",
	}
	if bucket, ok := canonical[normalized]; ok {
		return bucket
	}
	parts := strings.FieldsFunc(normalized, func(r rune) bool { return r == '_' || r == '-' })
	for i, part := range parts {
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, "")
}

func repositoryCollection(repo github.Repo) string {
	if repo.Fork {
		return "Forks"
	}
	if strings.EqualFold(repo.Visibility, "private") {
		return "Private"
	}
	return "Public"
}

func migrateLegacy(baseDir string, repos []github.Repo) {
	for _, repo := range repos {
		legacyDir := filepath.Join(baseDir, normalizeLanguage(repo.Language), repo.Name)
		targetDir := filepath.Join(baseDir, repositoryCollection(repo), repositoryBucket(repo), repo.Name)

		if info, err := os.Stat(legacyDir); err == nil && info.IsDir() {
			// Only relocate something that is demonstrably this repository's
			// clone. The name match alone is not evidence: a plain directory at
			// ~/Code/go/tools that merely shares a name with a repo called
			// "tools" was moved with no prompt and — because the dry-run
			// preview path requires git.IsRepository — no preview either.
			if ok, why := migratableClone(legacyDir, repo); !ok {
				diag.Warnf("not migrating %s: %s", legacyDir, why)
				continue
			}
			if err := os.MkdirAll(filepath.Dir(targetDir), 0o750); err != nil {
				diag.Warnf("failed creating target parent for migration %s: %v", targetDir, err)
				continue
			}
			if err := os.Rename(legacyDir, targetDir); err != nil {
				diag.Warnf("failed migrating %s to %s: %v", legacyDir, targetDir, err)
			}
		}
	}
}

// migratableClone reports whether dir may be relocated as repo's clone, and if
// not, why. It requires two independent pieces of evidence: dir is a git
// repository, and its origin remote matches the repository being migrated.
//
// The name-only check this replaces made a directory's *name* sufficient
// grounds for os.Rename, so an unrelated folder that happened to share a name
// with one of the owner's repositories was silently moved.
func migratableClone(dir string, repo github.Repo) (bool, string) {
	if !git.IsRepository(dir) {
		return false, "not a git repository (name matches " + repo.Name + ", but nothing else does)"
	}
	origin, err := gitRemoteOrigin(dir)
	if err != nil {
		return false, "cannot read its origin remote: " + err.Error()
	}
	want := repoRemoteIdentity(repo)
	got := git.CanonicalRemote(origin)
	if want == "" || got == "" {
		return false, "origin remote could not be compared"
	}
	if got != want {
		return false, "origin is " + got + ", not " + want
	}
	return true, ""
}

// normalizeLayoutDirCase applies Finder-facing capitalization to collection
// and ecosystem directories. APFS/HFS+ need an intermediate name for a
// case-only rename.
func normalizeLayoutDirCase(baseDir string, repos []github.Repo) {
	buckets := make(map[string]string, len(repos))
	for _, r := range repos {
		bucket := repositoryBucket(r)
		buckets[strings.ToLower(bucket)] = bucket
	}
	if len(buckets) == 0 {
		return
	}
	visEntries, err := readDir(baseDir)
	if err != nil {
		return
	}
	for _, ve := range visEntries {
		if !ve.IsDir() {
			continue
		}
		collection := canonicalCollectionName(ve.Name())
		if collection == "" {
			continue
		}
		visDir := filepath.Join(baseDir, ve.Name())
		if ve.Name() != collection && strings.EqualFold(ve.Name(), collection) {
			canonicalPath := filepath.Join(baseDir, collection)
			if renameCaseOnly(visDir, canonicalPath) {
				visDir = canonicalPath
			}
		}
		langEntries, err := readDir(visDir)
		if err != nil {
			continue
		}
		for _, le := range langEntries {
			if !le.IsDir() {
				continue
			}
			name := le.Name()
			canonical, ok := buckets[strings.ToLower(name)]
			if !ok || name == canonical {
				continue
			}
			src := filepath.Join(visDir, name)
			dst := filepath.Join(visDir, canonical)
			renameCaseOnly(src, dst)
		}
	}
}

func canonicalCollectionName(name string) string {
	for _, collection := range []string{"Public", "Private", "Forks", "Work"} {
		if strings.EqualFold(name, collection) {
			return collection
		}
	}
	return ""
}

func renameCaseOnly(src, dst string) bool {
	tmp := dst + ".corral-rename-tmp"
	if err := renamePath(src, tmp); err != nil {
		diag.Warnf("failed normalizing case for %s: %v", src, err)
		return false
	}
	if err := renamePath(tmp, dst); err != nil {
		diag.Warnf("failed normalizing case for %s -> %s: %v", tmp, dst, err)
		_ = renamePath(tmp, src)
		return false
	}
	return true
}

// cleanupEmptyFolders removes the now-empty legacy top-level language
// directories left behind by migrateLegacy. It only targets directories whose
// names match a repository language, and os.Remove deletes a directory only
// when it is empty, so unrelated entries under baseDir (e.g. .claude, other
// projects) are never touched.
func cleanupEmptyFolders(baseDir string, repos []github.Repo) {
	seenRoots := make(map[string]struct{})
	seenBuckets := make(map[string]struct{})
	for _, repo := range repos {
		lang := normalizeLanguage(repo.Language)
		if _, ok := seenRoots[lang]; !ok {
			seenRoots[lang] = struct{}{}
			_ = os.Remove(filepath.Join(baseDir, lang))
		}
		legacyBucket := filepath.Join(repositoryCollection(repo), lang)
		if _, ok := seenBuckets[legacyBucket]; !ok {
			seenBuckets[legacyBucket] = struct{}{}
			_ = os.Remove(filepath.Join(baseDir, legacyBucket))
		}
	}
}

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
	if msg, mismatched := originMismatch(repo, targetDir); mismatched {
		result.Action = "ERROR"
		result.Message = msg
		return result
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

// originMismatch reports whether the clone at targetDir points somewhere
// other than the repository we intend to sync, which would otherwise mean
// pulling one project's history into another project's directory.
func originMismatch(repo github.Repo, targetDir string) (string, bool) {
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
	// An empty upstream repo (created but never pushed to) results in an
	// unborn HEAD locally, and `git pull` would fail with "no such ref was
	// fetched". Detect that state cheaply and SKIP with a specific reason
	// instead of surfacing the git error as a sync failure.
	if gitIsEmpty(ctx, targetDir) {
		return "empty repository (no commits yet)", true
	}
	if branch, err := gitCurrentBranch(ctx, targetDir); err == nil && branch != repo.DefaultBranch {
		return fmt.Sprintf("on branch %s", branch), true
	}
	// Skip the network round-trip when the upstream pushed_at is unchanged
	// since the last successful sync. The cached value lives in
	// <targetDir>/.corral-state.json. A read error or a zero state falls
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
// sidecar so the next run can skip a no-op git pull. Best-effort: a write
// failure is logged but does not fail the operation, since the sidecar is
// purely an optimization (a missing or stale file falls through to the
// pre-sidecar behaviour of always pulling).
func stampCloneState(targetDir string, repo github.Repo) {
	if err := writeCloneState(targetDir, cloneState{
		LastSyncedPushedAt: repo.PushedAt,
		LastSyncedAt:       time.Now().UTC(),
	}); err != nil {
		diag.Warnf("failed writing %s in %s: %v", StateFileName, targetDir, err)
	}
}

// repoNameFromURL extracts the repository name from a git remote URL, stripping
// any trailing ".git" suffix. It returns an empty string when no segment exists.
func repoNameFromURL(url string) string {
	url = strings.TrimSuffix(strings.TrimSpace(url), ".git")
	if i := strings.LastIndexAny(url, "/:"); i >= 0 {
		return url[i+1:]
	}
	return url
}

func findOrphans(owner, baseDir string, repos []github.Repo) []string {
	return findOrphansOn(owner, baseDir, repos, fetchForgeName, fetchForgeURL)
}

// findOrphansOn is findOrphans with the forge named explicitly.
//
// The host is derived rather than assumed. It used to be the literal
// "github.com", which silently made orphan detection a no-op for every
// other forge: a Codeberg clone never matched the prefix, so it was
// skipped rather than compared. That failed safe — nothing was deleted
// wrongly — but `--orphans` reported nothing and looked like it had run.
func findOrphansOn(owner, baseDir string, repos []github.Repo, forgeName, forgeURL string) []string {
	repoMap := make(map[string]bool)
	identities := make(map[string]bool, len(repos))
	cloneURLs := make([]string, 0, len(repos))
	for _, r := range repos {
		repoMap[r.Name] = true
		if id := repoRemoteIdentity(r); id != "" {
			identities[id] = true
		}
		if r.CloneURL != "" {
			cloneURLs = append(cloneURLs, r.CloneURL)
		}
	}
	f, err := forge.Resolve(forgeName, forgeURL)
	if err != nil {
		// An unresolvable forge yields no prefixes, and no prefixes match
		// nothing. RunE rejects this long before here; the safe direction
		// is the right one regardless.
		f = nil
	}
	prefixes := forge.OwnerPrefixes(f, owner, forgeURL, cloneURLs)
	if len(prefixes) == 0 {
		return nil
	}

	var orphans []string
	// Per-entry walk errors are deliberately ignored inside the callback; the
	// outer error only signals an unreadable base directory, which is non-fatal
	// for best-effort orphan detection.
	_ = filepath.WalkDir(baseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // deliberate: see the comment above
		}
		if d.IsDir() && path != baseDir && git.IsRepository(path) {
			repoDir := path
			url, err := gitRemoteOrigin(repoDir)
			if err != nil {
				return filepath.SkipDir
			}
			// Ownership by canonical identity, not by substring. The old
			// strings.Contains test matched any host — a gitlab.com clone
			// under the same owner name counted as a GitHub orphan — and
			// matched unrelated path segments. git.CanonicalRemote is the
			// same identity migrateLegacy, originMismatch and prune use.
			identity := git.CanonicalRemote(url)
			if !forge.MatchesOwner(identity, prefixes) {
				return filepath.SkipDir
			}
			// Identity first; fall back to names so a locally-renamed
			// directory whose remote still points at a known repository
			// is not flagged.
			if !identities[identity] && !repoMap[filepath.Base(repoDir)] && !repoMap[repoNameFromURL(url)] {
				orphans = append(orphans, repoDir)
			}
			return filepath.SkipDir
		}
		return nil
	})

	return orphans
}

func emitOrphans(owner string, orphans []string) {
	fmt.Println("\n--- Orphan Detection ---")
	for _, orphan := range orphans {
		fmt.Printf("Orphan found: %s\n", orphan)
	}
	if len(orphans) == 0 {
		fmt.Printf("No orphaned repositories found for %s.\n", owner)
	}
}

func detectOrphans(owner, baseDir string, repos []github.Repo) {
	emitOrphans(owner, findOrphans(owner, baseDir, repos))
}

func evaluateLayout(layoutTpl string, repo github.Repo, owner string) (string, error) {
	if layoutTpl == "" {
		layoutTpl = defaultLayout
	}
	tmpl, err := parseLayoutTemplate(layoutTpl)
	if err != nil {
		return "", err
	}
	return executeLayout(tmpl, repo, owner)
}

// ParseLayoutTemplate validates a --layout template without rendering it, so
// the CLI can reject a malformed template during flag validation instead of
// after a full paginated GitHub fetch has already happened.
func ParseLayoutTemplate(layout string) (*template.Template, error) {
	return parseLayoutTemplate(layout)
}

func parseLayoutTemplate(layout string) (*template.Template, error) {
	if len(layout) > maxLayoutTemplateBytes {
		return nil, fmt.Errorf("layout template exceeds %d bytes", maxLayoutTemplateBytes)
	}
	return template.New("layout").Parse(layout)
}

func executeLayout(tmpl *template.Template, repo github.Repo, owner string) (string, error) {
	var buf bytes.Buffer
	data := struct {
		Visibility    string
		Language      string
		Collection    string
		Bucket        string
		Name          string
		Owner         string
		FullName      string
		ID            int64
		DefaultBranch string
	}{
		Visibility:    strings.ToLower(repo.Visibility),
		Language:      normalizeLanguage(repo.Language),
		Collection:    repositoryCollection(repo),
		Bucket:        repositoryBucket(repo),
		Name:          repo.Name,
		Owner:         firstNonEmpty(repo.Owner, owner),
		FullName:      repo.FullName,
		ID:            repo.ID,
		DefaultBranch: repo.DefaultBranch,
	}
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	cleanPath := filepath.Clean(buf.String())
	if strings.HasPrefix(cleanPath, "..") || cleanPath == "." || filepath.IsAbs(cleanPath) || strings.HasPrefix(buf.String(), "/") || strings.HasPrefix(buf.String(), "\\") {
		return "", fmt.Errorf("layout escapes base directory: %s", cleanPath)
	}
	return filepath.ToSlash(cleanPath), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// fetchFromForge lists an owner's repositories from the selected hosting
// service.
//
// The seam the engine calls, and the one place a forge.Repo becomes the
// engine's own repository shape.
//
// That shape still lives in internal/github, which is now a misnomer: it
// is the engine's type, and GitHub is one of five things that can produce
// it. Renaming it across the engine, the TUI and the command layer is
// churn with no behaviour attached, so it is left for a change that has a
// reason of its own. The conversion below is where the seam actually is.
func fetchFromForge(ctx context.Context, owner string, opts github.FetchOptions) ([]github.Repo, error) {
	return FetchRepos(ctx, owner, opts, fetchForgeName, fetchForgeURL)
}

// FetchRepos lists an owner's repositories from the named forge.
//
// Exported because prune needs it. prune used to call the GitHub client
// directly, so `--forge codeberg` was accepted and then ignored: it
// compared Codeberg clones against a GitHub listing. Everything that
// decides what is missing upstream has to come through one path, or the
// flag lies.
func FetchRepos(ctx context.Context, owner string, opts github.FetchOptions, forgeName, forgeURL string) ([]github.Repo, error) {
	f, err := forge.Resolve(forgeName, forgeURL)
	if err != nil {
		return nil, err
	}
	// GitHub keeps its existing path entirely: the adapter delegates to
	// the same client, so its auth ladder, rate-limit handling and search
	// pagination are untouched by this indirection.
	repos, err := f.List(ctx, owner, forge.Options{
		Limit:           opts.Limit,
		Visibility:      opts.Visibility,
		IncludeForks:    opts.IncludeForks,
		IncludeArchived: opts.IncludeArchived,
		Token:           forgeToken(ctx, f.Name(), opts.AuthMode),
		BaseURL:         forgeURL,
		RequestTimeout:  opts.RequestTimeout,
		TotalTimeout:    opts.TotalTimeout,
	})
	if err != nil {
		return nil, err
	}

	out := make([]github.Repo, 0, len(repos))
	for _, r := range repos {
		out = append(out, github.Repo{
			ID:            r.ID,
			Owner:         r.Owner,
			FullName:      r.FullName,
			Name:          r.Name,
			Language:      r.Language,
			Visibility:    r.Visibility,
			DefaultBranch: r.DefaultBranch,
			CloneURL:      r.CloneURL,
			SSHURL:        r.SSHURL,
			Fork:          r.Fork,
			Archived:      r.Archived,
			PushedAt:      r.PushedAt,
			Stars:         r.Stars,
			IsTemplate:    r.IsTemplate,
			IsMirror:      r.IsMirror,
		})
	}
	return out, nil
}

// fetchForgeName and fetchForgeURL carry the run's forge selection into
// the seam.
//
// Package-level rather than parameters because fetchRepos is a variable
// the tests replace, and widening its signature would rewrite every one
// of those. Set once by Run before anything reads them.
var (
	fetchForgeName string
	fetchForgeURL  string
)

// forgeToken resolves the credential for a forge.
//
// GitHub's is resolved by its own client, which knows the ladder from an
// explicit token through the environment to the gh CLI — passing a token
// in would bypass that. The others read one environment variable each,
// named the way each forge's own tooling names it, so a developer who
// already has it exported does not have to learn a corral-specific name.
var forgeToken = func(ctx context.Context, name string, mode github.AuthMode) string {
	switch name {
	case "github":
		return "" // resolved inside the GitHub client
	case "gitlab":
		return firstEnv("CORRAL_GITLAB_TOKEN", "GITLAB_TOKEN", "CI_JOB_TOKEN")
	case "bitbucket":
		return firstEnv("CORRAL_BITBUCKET_TOKEN", "BITBUCKET_TOKEN")
	default:
		return firstEnv("CORRAL_FORGE_TOKEN", "FORGEJO_TOKEN", "GITEA_TOKEN", "CODEBERG_TOKEN")
	}
}

// ForgeToken resolves the credential `corralctl sync` presents to a
// destination forge, through the same ladder `corralctl clone` uses to
// list from one: GitHub through its own client, every other forge from
// the environment variable its own tooling names.
func ForgeToken(ctx context.Context, name string, mode github.AuthMode) string {
	if name == "github" {
		return github.Token(ctx, mode)
	}
	return forgeToken(ctx, name, mode)
}

// firstEnv returns the first of these variables that is set and non-empty.
func firstEnv(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}
