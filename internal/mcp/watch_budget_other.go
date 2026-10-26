// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build !unix

package mcp

// defaultWatchBudget is the cap itself where there is no open-file limit to
// read: Windows watches with one handle per directory and has no per-process
// descriptor ceiling of the Unix kind.
func defaultWatchBudget() int { return maxWatchBudget }
