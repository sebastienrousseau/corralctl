// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/engine"
)

func TestCloneRunsTheSameOperationAsTheRoot(t *testing.T) {
	oldRun, oldYes, oldInteractive, oldExit, oldTimeout := engineRun, assumeYes, interactive, osExit, apiTimeout
	t.Cleanup(func() {
		engineRun, assumeYes, interactive, osExit, apiTimeout = oldRun, oldYes, oldInteractive, oldExit, oldTimeout
	})
	assumeYes, interactive, apiTimeout = true, false, time.Second
	osExit = func(code int) { t.Fatalf("unexpected exit %d", code) }
	var got engine.RunOptions
	engineRun = func(_ context.Context, opts engine.RunOptions) { got = opts }
	base := t.TempDir()

	cloneCmd.Run(cloneCmd, []string{"owner", base, "7"})
	if got.Owner != "owner" || got.BaseDir != base || got.Fetch.Limit != 7 {
		t.Fatalf("parsed options = %+v", got)
	}
	if err := cloneCmd.PreRunE(cloneCmd, []string{"owner"}); err != nil {
		t.Fatalf("PreRunE: %v", err)
	}
	// The same flags as the root, on the same variables.
	for _, name := range []string{"limit", "concurrency", "protocol", "layout", "orphans", "output", "interactive", "yes", "forge"} {
		if cloneCmd.Flags().Lookup(name) == nil {
			t.Errorf("clone lacks --%s", name)
		}
	}
	if rootCmd.Run == nil || cloneCmd.Run == nil {
		t.Fatal("both spellings must run")
	}
}

func TestValidateCloneArgs(t *testing.T) {
	if err := validateCloneArgs(cloneCmd, nil); err == nil || !strings.Contains(err.Error(), "at least 1") {
		t.Fatalf("no args = %v", err)
	}
	if err := validateCloneArgs(cloneCmd, []string{"a", "b", "c", "d"}); err == nil || !strings.Contains(err.Error(), "at most 3") {
		t.Fatalf("four args = %v", err)
	}
	if err := validateCloneArgs(cloneCmd, []string{"owner", "forks"}); err == nil || !strings.Contains(err.Error(), "--type") {
		t.Fatalf("legacy keyword = %v", err)
	}
	// Unlike the root, a name near a subcommand is just an owner here.
	if err := validateCloneArgs(cloneCmd, []string{"statuss", "dir", "5"}); err != nil {
		t.Fatalf("owner near a subcommand name = %v", err)
	}
}
