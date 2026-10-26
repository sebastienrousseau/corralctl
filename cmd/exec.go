// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/diag"
	"github.com/sebastienrousseau/corralctl/internal/engine"
	gitutil "github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/spf13/cobra"
)

var (
	execConcurrency  int
	execLanguages    string
	execExcludeLangs string
	execVisibility   string
	execTimeout      time.Duration
	execOutput       string
	findRepos        = findLocalRepos
	execBatch        = runExecCommands
	walkLocalRepos   = filepath.WalkDir
	execGOOS         = runtime.GOOS
)

var execCmd = &cobra.Command{
	Use:   "exec <command>",
	Short: "Execute a command across all organised local repositories",
	Long: `Runs a shell command concurrently across all repositories found under the base directory.

The command line is interpreted by /bin/sh -c on Unix and by cmd.exe /C
(or %COMSPEC%) on Windows, whatever $SHELL names, so the same command
means the same thing for every user. To use another shell, name it in
the command: corralctl exec "fish -c 'string upper (basename $PWD)'".

--timeout sets a deadline for each repository on its own (0, the
default, means none). A command that overruns is killed together with
the processes it started, reported as timed out, and counted as failed;
the other repositories carry on.

--output json writes one {"results": [...], "summary": {...}} document
when the run ends, and --output ndjson one record per repository as it
finishes, the same shapes sync writes. Each record carries repo (the
path under the base directory), exit_code (-1 when killed or not
started), duration_ms, stdout, stderr (each capped at 1 MiB), timed_out,
and error when the command failed. Diagnostics go to stderr.`,
	Args: cobra.MinimumNArgs(1),
	PreRunE: func(cmd *cobra.Command, args []string) error {
		if execConcurrency < 1 {
			return fmt.Errorf("--concurrency must be >= 1")
		}
		if execTimeout < 0 {
			return fmt.Errorf("--timeout must be >= 0")
		}
		execOutput = strings.ToLower(strings.TrimSpace(execOutput))
		if !validOperationalOutput(execOutput) {
			return errors.New("--output must be text, json, or ndjson")
		}
		if dryRun && execOutput != string(engine.OutputText) {
			return errors.New("--dry-run lists repositories as text; drop --output or --dry-run")
		}
		execVisibility = strings.ToLower(strings.TrimSpace(execVisibility))
		if !slices.Contains(execVisibilities(), execVisibility) {
			return fmt.Errorf("--visibility must be one of: %s", strings.Join(execVisibilities(), ", "))
		}
		return nil
	},
	Run: func(cmd *cobra.Command, args []string) {
		commandStr := strings.Join(args, " ")
		bDir := baseDir
		if bDir == "" {
			bDir = defaultBaseDir()
		}

		// Find all Git repos under bDir
		repos, err := findRepos(bDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed to scan directory: %v\n", err)
			osExit(1)
			return
		}

		// Filter repos
		filtered := filterLocalRepos(repos)
		if len(filtered) == 0 {
			reportNoExecRepos()
			return
		}

		if dryRun {
			fmt.Printf("Dry-run: Would execute %q in:\n", commandStr)
			for _, r := range filtered {
				fmt.Printf("  - %s\n", r)
			}
			return
		}

		// Run commands concurrently
		summary := execBatch(cmdContext(cmd), filtered, execOptions{
			Root: bDir, Command: commandStr, Concurrency: execConcurrency,
			Timeout: execTimeout, Output: execOutput,
		})
		if summary.Failed > 0 || summary.Canceled || summary.OutputFailed {
			osExit(1)
		}
	},
}

// reportNoExecRepos says that no repository matched: on stdout in the
// text format, and on stderr when stdout carries JSON.
func reportNoExecRepos() {
	if execSeparateStreams(execOutput) {
		diag.Warnf("corralctl exec: no matching repositories found")
		return
	}
	fmt.Println("No matching repositories found.")
}

type localRepoInfo struct {
	Path       string
	Visibility string // lower-cased top-level collection: "public", "private", "work", "forks"
	Language   string
}

// ExecSummary is the aggregate outcome of a batch command.
type ExecSummary struct {
	Total    int  `json:"total"`
	Failed   int  `json:"failed"`
	Canceled bool `json:"canceled"`
	// OutputFailed reports that the JSON document could not be written.
	OutputFailed bool `json:"-"`
}

const maxExecOutputBytes = 1 << 20

type cappedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	originalLen := len(p)
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
			b.truncated = true
		}
		_, _ = b.buf.Write(p)
	} else if len(p) > 0 {
		b.truncated = true
	}
	return originalLen, nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.buf.String()
	if b.truncated {
		out += "\n[corralctl: output truncated at 1 MiB]\n"
	}
	return out
}

// findLocalRepos walks baseDir looking for directories containing a .git folder
func findLocalRepos(baseDir string) ([]localRepoInfo, error) {
	var repos []localRepoInfo
	err := walkLocalRepos(baseDir, func(path string, d os.DirEntry, err error) error {
		// An entry that cannot be read is skipped, not fatal: a single
		// unreadable directory must not stop the caller from finding the
		// repositories it can reach. The outer error still reports a base
		// directory that could not be walked at all.
		if err != nil {
			return nil //nolint:nilerr // deliberate: skip this entry, keep walking
		}
		if d.IsDir() && path != baseDir && gitutil.IsRepository(path) {
			repoDir := path
			rel, err := filepath.Rel(baseDir, repoDir)
			if err == nil {
				parts := strings.Split(filepath.ToSlash(rel), "/")
				var vis, lang string
				if len(parts) >= 3 {
					vis = strings.ToLower(parts[0])
					lang = strings.ToLower(parts[1])
				} else if len(parts) == 2 {
					lang = strings.ToLower(parts[0])
				}
				repos = append(repos, localRepoInfo{
					Path:       repoDir,
					Visibility: vis,
					Language:   lang,
				})
			}
			return filepath.SkipDir
		}
		return nil
	})
	return repos, err
}

// execVisibilities lists the values --visibility accepts: all, and each
// top-level collection of the layout, lower-cased as findLocalRepos
// records them.
func execVisibilities() []string {
	values := []string{"all"}
	for _, collection := range engine.Collections() {
		values = append(values, strings.ToLower(collection))
	}
	return values
}

func filterLocalRepos(repos []localRepoInfo) []string {
	var out []string
	includeLang := toLookupSet(parseCSV(execLanguages))
	excludeLang := toLookupSet(parseCSV(execExcludeLangs))

	for _, r := range repos {
		if execVisibility != "all" && r.Visibility != execVisibility {
			continue
		}
		if languageSelected(r.Language, includeLang, excludeLang) {
			out = append(out, r.Path)
		}
	}
	return out
}

// languageSelected reports whether a repository's language passes the
// include list (when one is given) and is not on the exclude list.
func languageSelected(language string, include, exclude map[string]struct{}) bool {
	if len(include) > 0 {
		if _, ok := include[language]; !ok {
			return false
		}
	}
	_, excluded := exclude[language]
	return !excluded
}

func toLookupSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(values))
	for _, v := range values {
		out[strings.ToLower(strings.TrimSpace(v))] = struct{}{}
	}
	return out
}

// execShell names the interpreter for the command line: /bin/sh on Unix,
// and cmd.exe (or %COMSPEC%) on Windows. $SHELL is deliberately not
// consulted, so a command means the same thing for every user; a
// fish or nushell user who wants their own shell writes it into the
// command, as in `corralctl exec "fish -c '...'"`.
func execShell() (shell, flag string) {
	if execGOOS != "windows" {
		return "/bin/sh", "-c"
	}
	if comspec := os.Getenv("COMSPEC"); comspec != "" {
		return comspec, "/C"
	}
	return "cmd.exe", "/C"
}

// execOptions describes one exec run.
type execOptions struct {
	// Root is the base directory; output names repositories relative
	// to it.
	Root string
	// Command is the command line handed to the platform shell.
	Command string
	// Concurrency is the number of repositories worked at once.
	Concurrency int
	// Timeout is the deadline for one repository; 0 means none.
	Timeout time.Duration
	// Output is the output format: text (or empty), json or ndjson.
	Output string
}

// execResult is the outcome of the command in one repository, and the
// record --output json and ndjson write for it.
type execResult struct {
	// Repo is the repository's slash-separated path under the base.
	Repo string `json:"repo"`
	// ExitCode is the command's exit status; -1 when it was killed or
	// could not start.
	ExitCode int `json:"exit_code"`
	// DurationMs is the command's wall-clock time in milliseconds.
	DurationMs int64 `json:"duration_ms"`
	// Stdout is the command's standard output, capped at 1 MiB. In the
	// text format it also carries standard error, interleaved.
	Stdout string `json:"stdout"`
	// Stderr is the command's standard error, capped at 1 MiB; empty in
	// the text format.
	Stderr string `json:"stderr"`
	// TimedOut reports that the per-repository deadline killed it.
	TimedOut bool `json:"timed_out"`
	// Error says why the command failed; empty on success.
	Error string `json:"error,omitempty"`
}

func runExecCommands(ctx context.Context, repoPaths []string, opts execOptions) ExecSummary {
	jobs := make(chan string, len(repoPaths))
	for _, p := range repoPaths {
		jobs <- p
	}
	close(jobs)

	var wg sync.WaitGroup
	var mu sync.Mutex
	summary := ExecSummary{Total: len(repoPaths)}
	printer := newResultPrinter[execResult, ExecSummary](opts.Output, func(r execResult) {
		printExecText(r, opts.Timeout)
	})
	report := func(r execResult) {
		printer.report(r)
		if r.Error != "" {
			mu.Lock()
			summary.Failed++
			mu.Unlock()
		}
	}
	for i := 0; i < opts.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			execWorker(ctx, jobs, opts, report)
		}()
	}
	wg.Wait()
	summary.Canceled = ctx.Err() != nil
	if err := printer.finish(summary); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: writing output: %v\n", err)
		summary.OutputFailed = true
	}
	return summary
}

// execWorker runs the command in each repository it takes from jobs
// until jobs is drained or ctx is done.
func execWorker(ctx context.Context, jobs <-chan string, opts execOptions, report func(execResult)) {
	for {
		select {
		case <-ctx.Done():
			return
		case path, ok := <-jobs:
			if !ok {
				return
			}
			report(runExecOne(ctx, path, opts))
		}
	}
}

// execWaitDelay bounds how long a timed-out command's output may stay
// open after it is killed, for a child that escaped the process group.
const execWaitDelay = 2 * time.Second

// runExecOne runs the command in one repository, under opts.Timeout when
// one is set.
func runExecOne(ctx context.Context, path string, opts execOptions) execResult {
	repoCtx, cancel := ctx, context.CancelFunc(func() {})
	if opts.Timeout > 0 {
		repoCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
	}
	defer cancel()
	shell, shellFlag := execShell()

	// #nosec G204,G702 -- exec intentionally runs the user-supplied command through the platform shell.
	cmd := exec.CommandContext(repoCtx, shell, shellFlag, opts.Command)
	cmd.Dir = path
	cmd.Env = os.Environ()
	if opts.Timeout > 0 {
		killExecProcessGroup(cmd)
		cmd.WaitDelay = execWaitDelay
	}

	stdout := &cappedBuffer{limit: maxExecOutputBytes}
	stderr := stdout
	if execSeparateStreams(opts.Output) {
		stderr = &cappedBuffer{limit: maxExecOutputBytes}
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	start := time.Now()
	err := cmd.Run()
	r := execResult{
		Repo:       execRepoLabel(opts.Root, path),
		DurationMs: time.Since(start).Milliseconds(),
		Stdout:     stdout.String(),
		TimedOut:   err != nil && ctx.Err() == nil && errors.Is(repoCtx.Err(), context.DeadlineExceeded),
	}
	if stderr != stdout {
		r.Stderr = stderr.String()
	}
	r.ExitCode, r.Error = execExitStatus(err)
	return r
}

// execSeparateStreams reports whether the format keeps stdout and stderr
// apart. The text format interleaves them, as a terminal would.
func execSeparateStreams(format string) bool {
	return format == string(engine.OutputJSON) || format == string(engine.OutputNDJSON)
}

// execExitStatus turns the error from running a command into its exit
// code and a failure message: 0 and "" on success, the process's status
// when it exited non-zero, and -1 when it was killed or never started.
func execExitStatus(err error) (int, string) {
	if err == nil {
		return 0, ""
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), err.Error()
	}
	return -1, err.Error()
}

// execRepoLabel names a repository in the output by its slash-separated
// path relative to root, so Public/Go/x and Private/Go/x stay distinct.
// A path that is not under root keeps its full form.
func execRepoLabel(root, path string) string {
	if root == "" {
		return path
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return filepath.ToSlash(rel)
}

// printExecText writes one result in the text format: a header naming
// the repository, the captured output, and the failure if there was one.
func printExecText(r execResult, timeout time.Duration) {
	fmt.Printf("\n--- [%s] ---\n", r.Repo)
	if r.Stdout != "" {
		fmt.Print(r.Stdout)
	}
	switch {
	case r.TimedOut:
		fmt.Printf("Command timed out after %s\n", timeout)
	case r.Error != "":
		fmt.Printf("Command failed: %s\n", r.Error)
	}
}

func init() {
	execCmd.Flags().IntVarP(&execConcurrency, "concurrency", "c", 4, "number of concurrent operations")
	execCmd.Flags().StringVar(&execLanguages, "languages", "", "comma-separated language filter")
	execCmd.Flags().StringVar(&execExcludeLangs, "exclude-languages", "", "comma-separated language exclude filter")
	execCmd.Flags().StringVar(&execOutput, "output", string(engine.OutputText), "output format: text, json, ndjson")
	execCmd.Flags().DurationVar(&execTimeout, "timeout", 0, "deadline per repository (0 to disable)")
	execCmd.Flags().StringVar(&execVisibility, "visibility", "all", "filter by top-level collection: "+strings.Join(execVisibilities(), ", "))
	rootCmd.AddCommand(execCmd)
}
