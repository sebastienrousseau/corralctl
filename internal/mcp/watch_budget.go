// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

// maxWatchBudget caps the descriptors the workspace watcher may hold however
// high the process limit is. The layout directories and .git directories of
// a few thousand clones fit well inside it.
const maxWatchBudget = 16384

// watchBudget is the number of descriptors the workspace watcher may use. A
// variable so a test can make the budget bind without building a workspace
// large enough to reach it.
var watchBudget = defaultWatchBudget

// budgetFromLimit is half the process's open-file limit, capped at
// maxWatchBudget. The other half is for the work the watcher exists to
// serve: the workspace scan, content search, mapped index files and git.
func budgetFromLimit(limit uint64) int {
	half := limit / 2
	if half > maxWatchBudget {
		return maxWatchBudget
	}
	return int(half)
}
