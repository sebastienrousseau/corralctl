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
	"strings"
	"sync"
	"time"

	gitutil "github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/spf13/cobra"
)

var (
	execConcurrency  int
	execLanguages    string
	execExcludeLangs string
	execVisibility   string
	execTimeout      time.Duration
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
the other repositories carry on.`,
	Args: cobra.MinimumNArgs(1),
	PreRunE: func(cmd *cobra.Command, args []string) error {
		if execConcurrency < 1 {
			return fmt.Errorf("--concurrency must be >= 1")
		}
		if execTimeout < 0 {
			return fmt.Errorf("--timeout must be >= 0")
		}
		execVisibility = strings.ToLower(strings.TrimSpace(execVisibility))
		if execVisibility != "all" && execVisibility != "public" && execVisibility != "private" {
			return fmt.Errorf("--visibility must be one of: all, public, private")
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
			fmt.Println("No matching repositories found.")
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
		summary := execBatch(cmdContext(cmd), filtered, execOptions{Root: bDir, Command: commandStr, Concurrency: execConcurrency, Timeout: execTimeout})
		if summary.Failed > 0 || summary.Canceled {
			osExit(1)
		}
	},
}

type localRepoInfo struct {
	Path       string
	Visibility string // "public", "private"
	Language   string
}

// ExecSummary is the aggregate outcome of a batch command.
type ExecSummary struct {
	Total    int
	Failed   int
	Canceled bool
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

func filterLocalRepos(repos []localRepoInfo) []string {
	var out []string
	includeLang := toLookupSet(parseCSV(execLanguages))
	excludeLang := toLookupSet(parseCSV(execExcludeLangs))

	for _, r := range repos {
		if execVisibility == "public" && r.Visibility != "public" {
			continue
		}
		if execVisibility == "private" && r.Visibility != "private" {
			continue
		}
		if len(includeLang) > 0 {
			if _, ok := includeLang[r.Language]; !ok {
				continue
			}
		}
		if len(excludeLang) > 0 {
			if _, ok := excludeLang[r.Language]; ok {
				continue
			}
		}
		out = append(out, r.Path)
	}
	return out
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
}

// execResult is the outcome of the command in one repository.
type execResult struct {
	// Repo labels the repository in the output.
	Repo string
	// Output is the command's combined stdout and stderr, capped.
	Output string
	// Err is why the command failed; nil on success.
	Err error
	// TimedOut reports that the per-repository deadline killed it.
	TimedOut bool
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
	report := func(r execResult) {
		mu.Lock()
		defer mu.Unlock()
		printExecText(r, opts.Timeout)
		if r.Err != nil {
			summary.Failed++
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

	out := &cappedBuffer{limit: maxExecOutputBytes}
	cmd.Stdout = out
	cmd.Stderr = out
	err := cmd.Run()
	timedOut := err != nil && ctx.Err() == nil && errors.Is(repoCtx.Err(), context.DeadlineExceeded)
	return execResult{Repo: execRepoLabel(opts.Root, path), Output: out.String(), Err: err, TimedOut: timedOut}
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
	if r.Output != "" {
		fmt.Print(r.Output)
	}
	switch {
	case r.TimedOut:
		fmt.Printf("Command timed out after %s\n", timeout)
	case r.Err != nil:
		fmt.Printf("Command failed: %v\n", r.Err)
	}
}

func init() {
	execCmd.Flags().IntVarP(&execConcurrency, "concurrency", "c", 4, "number of concurrent operations")
	execCmd.Flags().StringVar(&execLanguages, "languages", "", "comma-separated language filter")
	execCmd.Flags().StringVar(&execExcludeLangs, "exclude-languages", "", "comma-separated language exclude filter")
	execCmd.Flags().DurationVar(&execTimeout, "timeout", 0, "deadline per repository (0 to disable)")
	execCmd.Flags().StringVar(&execVisibility, "visibility", "all", "filter repositories by visibility: all, public, private")
	rootCmd.AddCommand(execCmd)
}
