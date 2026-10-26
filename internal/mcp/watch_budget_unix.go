// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build unix

package mcp

import "syscall"

// getrlimit is the seam a test replaces to drive the failure branch.
var getrlimit = syscall.Getrlimit

// fallbackWatchBudget is used when the limit cannot be read: the smallest
// soft limit in common use (macOS's 256), halved.
const fallbackWatchBudget = 128

// defaultWatchBudget derives the budget from the current soft limit on open
// files. The Go runtime raises the soft limit to the hard limit at start-up,
// so this is the most the process can have.
func defaultWatchBudget() int {
	var lim syscall.Rlimit
	if err := getrlimit(syscall.RLIMIT_NOFILE, &lim); err != nil {
		return fallbackWatchBudget
	}
	return budgetFromLimit(lim.Cur)
}
