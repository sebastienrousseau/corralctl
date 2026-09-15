// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !race

package tui

import (
	"context"
	"runtime"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/github"
)

// assertCancelledRunnerErrors exercises the real runSelectorProgram — the thin
// adapter over tea.NewProgram(...).Run() — against an already-cancelled
// context.
//
// It is excluded under -race on purpose. Starting a real Bubble Tea program
// trips a data race inside the dependencies, not in corral:
// Program.shutdown() calls cancelreader's Close(), which closes the underlying
// os.File while that reader's own goroutine is still using it. Both
// cancelreader backends are affected — kqueue when stdin is a terminal, select
// when it is not — so redirecting stdin does not avoid it; it only changes
// which backend races. corral's whole contribution to that stack is the one
// line at tui.go:187 that calls Run().
//
// Every path with corral logic in it goes through the runSelectorProgram seam,
// which the other tests stub, and those run under -race normally.
func assertCancelledRunnerErrors(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runSelectorProgram(ctx, NewSelectorModel(func() ([]github.Repo, error) { return nil, nil })); err == nil {
		t.Fatal("cancelled default selector runner must return an error")
	}
}
