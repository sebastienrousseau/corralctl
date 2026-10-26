// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build unix

package mcp

import (
	"errors"
	"syscall"
	"testing"
)

// getrlimit and fallbackWatchBudget exist only where the open-file limit
// is read (watch_budget_unix.go), so this half of the budget test does too.
func TestWatchBudgetFallsBackWhenTheLimitIsUnreadable(t *testing.T) {
	old := getrlimit
	getrlimit = func(int, *syscall.Rlimit) error { return errors.New("no limit for you") }
	defer func() { getrlimit = old }()
	if got := defaultWatchBudget(); got != fallbackWatchBudget {
		t.Fatalf("defaultWatchBudget() with getrlimit failing = %d, want %d", got, fallbackWatchBudget)
	}
}
