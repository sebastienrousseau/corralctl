// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build race

package tui

import "testing"

// Under -race the real-runner check is a no-op; see the !race build of this
// helper for why (a data race inside bubbletea/cancelreader's shutdown, not in
// corral).
//
// Deliberately not t.Skip: skipping would abort the calling test, and the
// assertions after this point cover corral's own selector logic, which must
// keep running under the race detector.
func assertCancelledRunnerErrors(t *testing.T) {
	t.Helper()
	t.Log("skipping the real Bubble Tea runner under -race: bubbletea/cancelreader race their own shutdown")
}

func assertDefaultGraphRunner(t *testing.T) {
	t.Helper()
	t.Log("skipping the real Bubble Tea graph runner under -race: bubbletea/cancelreader race their own shutdown")
}

func assertDefaultAdoptRunner(t *testing.T) {
	t.Helper()
	t.Log("skipping the real Bubble Tea adopt runner under -race: bubbletea/cancelreader race their own shutdown")
}
