// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

// TestOperationalCommandsAcceptTheFlagsTheyConsume is the regression test for
// the 28-of-31 flag gap.
//
// plan, prune and profile all build their RunOptions from the same
// package-level variables via operationalRunOptions(), but the flags that set
// those variables were registered with rootCmd.Flags() — root only. So the
// commands consumed the values while rejecting every attempt to set them:
//
//	$ corralctl plan acme --limit 5
//	unknown flag: --limit
//
// which meant plan always ran at limit=1000, concurrency=1, visibility=all.
func TestOperationalCommandsAcceptTheFlagsTheyConsume(t *testing.T) {
	// Every flag operationalRunOptions() reads must be settable on the
	// commands that call it.
	fetchOwned := []string{
		"limit", "visibility", "include-forks", "include-archived",
		"languages", "exclude-languages", "auth", "type", "sort",
		"retry-max", "retry-min-backoff", "retry-max-backoff", "api-timeout",
	}
	cloneOwned := []string{
		"concurrency", "protocol", "no-sync", "recurse-submodules",
		"clone-blobless", "blobless", "clone-single-branch", "clone-depth",
		"force-sync", "ignore-submodule-failures", "layout", "finder-tags",
	}

	cases := []struct {
		cmd   *cobra.Command
		fetch bool
		clone bool
	}{
		{rootCmd, true, true},
		{planCmd, true, true},
		{profileCmd, true, true},
		// prune removes clones and never creates one, so the clone group would
		// be inert noise on its help.
		{pruneCmd, true, false},
	}

	for _, tc := range cases {
		for _, name := range fetchOwned {
			got := tc.cmd.Flags().Lookup(name) != nil
			if got != tc.fetch {
				t.Errorf("%s: --%s present=%v, want %v", tc.cmd.Name(), name, got, tc.fetch)
			}
		}
		for _, name := range cloneOwned {
			got := tc.cmd.Flags().Lookup(name) != nil
			if got != tc.clone {
				t.Errorf("%s: --%s present=%v, want %v", tc.cmd.Name(), name, got, tc.clone)
			}
		}
	}
}

// TestSharedFlagSetsAreNotNil guards the failure mode that made the first
// attempt at this fix silently useless: Go runs a package's init() functions in
// filename order, so cmd/operations.go initialised before cmd/root.go. When the
// FlagSets were assigned in root.go's init(), they were still nil in
// operations.go and AddFlagSet(nil) is a no-op — the flags landed on the root
// command and nowhere else, with no error anywhere.
func TestSharedFlagSetsAreNotNil(t *testing.T) {
	if fetchFlags() == nil {
		t.Fatal("fetchFlags() is nil")
	}
	if cloneFlags() == nil {
		t.Fatal("cloneFlags() is nil")
	}
	if fetchFlags().Lookup("limit") == nil {
		t.Error("fetch group is missing --limit")
	}
	if cloneFlags().Lookup("concurrency") == nil {
		t.Error("clone group is missing --concurrency")
	}
}

// TestDefaultConcurrencyIsUsable pins the default above 1. corral shipped its
// documented concurrency feature switched off until v0.0.21, which left the
// README's "10x-50x faster" claim resting entirely on pushed_at caching.
func TestDefaultConcurrencyIsUsable(t *testing.T) {
	got := defaultConcurrency()
	if got < minDefaultConcurrency || got > maxDefaultConcurrency {
		t.Errorf("defaultConcurrency() = %d, want within [%d,%d]",
			got, minDefaultConcurrency, maxDefaultConcurrency)
	}
	if f := cloneFlags().Lookup("concurrency"); f == nil {
		t.Fatal("--concurrency not registered")
	} else if f.DefValue == "1" {
		t.Error("--concurrency still defaults to 1")
	}
}

// The operational commands must not accidentally acquire a second --output:
// they each define their own with different defaults and accepted values.
func TestOutputFlagStaysPerCommand(t *testing.T) {
	for _, c := range []*cobra.Command{statusCmd, planCmd, pruneCmd, profileCmd} {
		if c.Flags().Lookup("output") == nil {
			t.Errorf("%s lost its --output flag", c.Name())
		}
	}
	if fetchFlags().Lookup("output") != nil || cloneFlags().Lookup("output") != nil {
		t.Error("--output must not be in a shared group; commands differ in defaults and accepted values")
	}
}
