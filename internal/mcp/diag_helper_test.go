// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"strings"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/diag"
)

// captureDiag redirects diagnostic output into a buffer at the given level
// for the duration of one test, restoring the previous level and sink
// afterwards so verbosity cannot leak between tests.
func captureDiag(t *testing.T, level diag.Level) *strings.Builder {
	t.Helper()
	previous := diag.CurrentLevel()
	var buf strings.Builder
	diag.SetOutput(&buf)
	diag.SetLevel(level)
	t.Cleanup(func() {
		diag.SetLevel(previous)
		diag.SetOutput(nil)
	})
	return &buf
}
