// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"text/template"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sebastienrousseau/corralctl/internal/github"
	"github.com/sebastienrousseau/corralctl/internal/tui"
)

// runOutcome is everything the worker phase produced.
type runOutcome struct {
	// results holds every result, but only for --output json, the one
	// format that needs the aggregate. The streaming formats have already
	// written each result by the time the run ends.
	results   []RepoResult
	summary   Summary
	outputErr error
	// aborted is set when the user quit the progress view mid-run.
	aborted error
}

// errProgressViewQuit is the cancellation cause recorded when the user
// quits the progress view (q or ctrl+c) before the run has finished.
var errProgressViewQuit = errors.New("progress view closed by the user")

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
	// Derived so that quitting the progress view can stop the workers and
	// any git process they started, not only the view.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
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

	var aborted error
	if program != nil && runProgressView(program) {
		aborted = errProgressViewQuit
		cancel(aborted)
	}

	workers.Wait()
	close(results)
	consumer.Wait()
	outcome.aborted = aborted
	return outcome
}

// runProgressView drives the progress TUI until it exits, and reports
// whether the user quit it before every result arrived.
func runProgressView(program *tea.Program) bool {
	final, err := runProgram(program)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error running TUI: %v\n", err)
	}
	// Ensure Send unblocks if an injected runner or an early TUI failure
	// returns without shutting down Bubble Tea's context.
	program.Kill()
	view, ok := final.(interface{ Aborted() bool })
	return ok && view.Aborted()
}

// runCancellation reports why the run stopped early: the caller's context
// (SIGINT, SIGTERM or a parent cancel) or the user quitting the progress
// view. It returns nil for a run that was allowed to finish.
func runCancellation(ctx context.Context, outcome *runOutcome) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return outcome.aborted
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
		outcome.record(opts.Output, msg)
		switch {
		case program != nil:
			program.Send(toLogMsg(msg))
		case opts.Output == OutputText:
			logger.Printf("%s [%s] %s: %s", resultIcon(msg), msg.Action, msg.RepoName, msg.Message)
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
			logger.Printf("%s", msg)
		}
	}
}

// record counts msg in the summary and keeps it only when the output
// format needs the aggregate, so a streamed run's memory does not grow
// with the number of repositories.
func (o *runOutcome) record(output OutputFormat, msg RepoResult) {
	o.summary.add(msg)
	if output == OutputJSON {
		o.results = append(o.results, msg)
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
