// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/sebastienrousseau/corralctl/internal/github"
	"github.com/sebastienrousseau/corralctl/internal/tui"
)

// quitProgressView stands in for a person pressing the given key in the
// progress view before any result has arrived.
// It waits for started so the quit lands while a clone is in flight.
func quitProgressView(t *testing.T, key tea.KeyMsg, started <-chan struct{}) func(*tea.Program) (tea.Model, error) {
	t.Helper()
	return func(*tea.Program) (tea.Model, error) {
		<-started
		final, _ := tui.NewModel(1).Update(key)
		return final, nil
	}
}

// TestQuitInProgressViewCancelsRun pins A-06: q or ctrl+c in the progress
// view used to close the view and leave the workers cloning with no
// output, then exit 0. Quitting the view must cancel the run's context,
// so an in-flight clone is interrupted, and report cancellation (130).
func TestQuitInProgressViewCancelsRun(t *testing.T) {
	for name, key := range map[string]tea.KeyMsg{
		"q":      {Type: tea.KeyRunes, Runes: []rune{'q'}},
		"ctrl+c": {Type: tea.KeyCtrlC},
	} {
		t.Run(name, func(t *testing.T) {
			oldFetch, oldClone, oldTTY, oldProgram := fetchRepos, gitClone, isTerminal, runProgram
			t.Cleanup(func() { fetchRepos, gitClone, isTerminal, runProgram = oldFetch, oldClone, oldTTY, oldProgram })
			fetchRepos = func(context.Context, string, github.FetchOptions) ([]github.Repo, error) {
				return []github.Repo{{Name: "slow", Language: "Go", Visibility: "Public", DefaultBranch: "main"}}, nil
			}
			started := make(chan struct{})
			cloneSawCancel := make(chan bool, 1)
			gitClone = func(ctx context.Context, _, _ string, _ git.CloneOptions) error {
				close(started)
				select {
				case <-ctx.Done():
					cloneSawCancel <- true
					return ctx.Err()
				case <-time.After(2 * time.Second):
					cloneSawCancel <- false
					return nil
				}
			}
			isTerminal = func(uintptr) bool { return true }
			runProgram = quitProgressView(t, key, started)

			opts := defaultRunOptions(t.TempDir())
			opts.DoSync = false
			err := RunE(context.Background(), opts)

			if got := exitStatus(err); got != cancelExitCode {
				t.Fatalf("exit status = %d (err %v), want %d", got, err, cancelExitCode)
			}
			if !silentFailure(err) {
				t.Errorf("cancellation should be silent, got %v", err)
			}
			if !<-cloneSawCancel {
				t.Error("in-flight clone was not cancelled when the progress view quit")
			}
		})
	}
}

// TestProgressViewCompletionDoesNotCancel guards the other side: a view
// that closes because every result arrived is a clean run, not an abort.
func TestProgressViewCompletionDoesNotCancel(t *testing.T) {
	oldFetch, oldTTY, oldProgram := fetchRepos, isTerminal, runProgram
	t.Cleanup(func() { fetchRepos, isTerminal, runProgram = oldFetch, oldTTY, oldProgram })
	fetchRepos = func(context.Context, string, github.FetchOptions) ([]github.Repo, error) {
		return []github.Repo{{Name: "r", Language: "Go", Visibility: "Public", DefaultBranch: "main"}}, nil
	}
	isTerminal = func(uintptr) bool { return true }
	runProgram = func(*tea.Program) (tea.Model, error) {
		final, _ := tui.NewModel(1).Update(tui.LogMsg{RepoName: "r", Action: "DRY-RUN", Message: "git clone"})
		return final, nil
	}
	opts := defaultRunOptions(t.TempDir())
	opts.DryRun = true
	if err := RunE(context.Background(), opts); err != nil {
		t.Fatalf("RunE = %v, want nil", err)
	}
}

// TestRunCancellation covers both sources of an early stop.
func TestRunCancellation(t *testing.T) {
	if err := runCancellation(context.Background(), &runOutcome{}); err != nil {
		t.Errorf("clean run: got %v, want nil", err)
	}
	if err := runCancellation(context.Background(), &runOutcome{aborted: errProgressViewQuit}); !errors.Is(err, errProgressViewQuit) {
		t.Errorf("view quit: got %v, want %v", err, errProgressViewQuit)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runCancellation(ctx, &runOutcome{}); !errors.Is(err, context.Canceled) {
		t.Errorf("signal: got %v, want context.Canceled", err)
	}
}
