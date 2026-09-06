// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/forge"
	"github.com/sebastienrousseau/corralctl/internal/github"
	"github.com/spf13/pflag"
)

// Shared flag groups.
//
// Until v0.0.21 every one of these was registered with rootCmd.Flags(), which
// binds a flag to the root command *only*. But plan, prune and profile all call
// operationalRunOptions(), which reads the same package-level variables — so
// those commands consumed the values while offering no way to set them:
//
//	$ corralctl plan acme --limit 5
//	unknown flag: --limit
//
// `corralctl plan` therefore always ran at limit=1000, concurrency=1,
// visibility=all, includeForks=true, unconfigurable and unstated. 28 of 31
// flags behaved this way.
//
// These are shared FlagSets rather than rootCmd.PersistentFlags() so the group
// lands on exactly the commands that act on it. Persistent flags would also
// attach to `mcp`, `completion` and `config`, where a clone-depth flag is
// noise. Every flag still binds to the same package-level variable, so a value
// parsed on any command is visible to operationalRunOptions().
// Built through sync.Once accessors rather than in an init(), because Go runs a
// package's init() functions in filename order: cmd/operations.go initialises
// before cmd/root.go. Assigning these in root.go's init() left them nil when
// operations.go ran, and AddFlagSet(nil) is a silent no-op — the flags appeared
// on the root command and nowhere else, which is the exact bug being fixed.
// Lazy construction removes the ordering dependency entirely.
var (
	fetchFlagsOnce sync.Once
	fetchFlagsSet  *pflag.FlagSet
	cloneFlagsOnce sync.Once
	cloneFlagsSet  *pflag.FlagSet
)

// fetchFlags returns the shared fetch/filter flag group.
func fetchFlags() *pflag.FlagSet {
	fetchFlagsOnce.Do(func() { fetchFlagsSet = newFetchFlags() })
	return fetchFlagsSet
}

// cloneFlags returns the shared clone/sync flag group.
func cloneFlags() *pflag.FlagSet {
	cloneFlagsOnce.Do(func() { cloneFlagsSet = newCloneFlags() })
	return cloneFlagsSet
}

// newFetchFlags returns the flags that decide *which* repositories a command
// operates on. Relevant to any command that lists from a hosting service.
func newFetchFlags() *pflag.FlagSet {
	fs := pflag.NewFlagSet("fetch", pflag.ContinueOnError)
	fs.StringVar(&forgeName, "forge", "",
		"hosting service to list from: "+strings.Join(forge.Names(), ", ")+" (default github)")
	fs.StringVar(&forgeURL, "forge-url", "",
		"base URL of a self-hosted instance (required for gitea and forgejo, which have no single public instance)")
	fs.IntVarP(&limit, "limit", "l", 1000, "max repos to list (0 for no limit)")
	fs.StringVar(&visibility, "visibility", "all", "repository visibility filter: all, public, private")
	fs.BoolVar(&includeForks, "include-forks", true, "include forked repositories under the Forks collection")
	fs.BoolVar(&includeArchived, "include-archived", true, "include archived repositories and tag them On Hold")
	fs.StringVar(&includeLanguagesCSV, "languages", "", "comma-separated language allow list")
	fs.StringVar(&excludeLanguagesCSV, "exclude-languages", "", "comma-separated language deny list")
	fs.StringVar(&authMode, "auth", string(github.AuthModeAuto), "authentication mode: auto, token, gh")
	fs.StringVar(&repoType, "type", "", "repository type filter: "+strings.Join(repoTypeValues, ", "))
	fs.StringVar(&repoSort, "sort", "", "repository sort order: "+strings.Join(repoSortValues, ", "))
	fs.IntVar(&retryMax, "retry-max", 4, "max retries for transient forge API failures")
	fs.DurationVar(&retryMinBackoff, "retry-min-backoff", 500*time.Millisecond, "minimum retry backoff")
	fs.DurationVar(&retryMaxBackoff, "retry-max-backoff", 8*time.Second, "maximum retry backoff")
	fs.DurationVar(&apiRequestTimeout, "api-request-timeout", 30*time.Second,
		"deadline for a single forge API request")
	fs.DurationVar(&apiTotalTimeout, "api-total-timeout", 10*time.Minute,
		"deadline for the whole paginated fetch, including retries and backoff")
	// Deprecated in v0.0.29. It was documented as a per-request deadline and
	// applied as both, which capped a whole paginated listing at 30s. Kept
	// working for at least one minor release per the stability guarantees in
	// the README, warning on stderr — never on stdout, which carries the
	// selected output format.
	fs.DurationVar(&apiTimeout, "api-timeout", 0,
		"deprecated: use --api-request-timeout and --api-total-timeout")
	return fs
}

// newCloneFlags returns the flags that decide *how* repositories are cloned and
// synced. Not attached to prune, which only removes clones.
func newCloneFlags() *pflag.FlagSet {
	fs := pflag.NewFlagSet("clone", pflag.ContinueOnError)
	// Default concurrency is deliberately above 1. It shipped at 1 until
	// v0.0.21, which meant the documented concurrency feature was off unless
	// asked for, and the README's "10x-50x faster" claim rested entirely on
	// pushed_at caching. Sized from the machine rather than a fixed number, and
	// capped: the work is network- and git-bound, so more workers past a point
	// buys nothing and risks GitHub's secondary rate limits.
	fs.IntVarP(&concurrency, "concurrency", "c", defaultConcurrency(), "number of concurrent operations")
	fs.StringVarP(&protocol, "protocol", "p", "https", "clone protocol (ssh or https)")
	fs.BoolVar(&noSync, "no-sync", false, "skip pulling latest changes for existing repos")
	fs.BoolVar(&recurseSubmodules, "recurse-submodules", false, "initialize submodules on clone and sync")
	fs.BoolVar(&cloneBlobless, "clone-blobless", false, "use partial clone filter=blob:none")
	fs.BoolVar(&cloneSingleBranch, "clone-single-branch", false, "clone only the default branch")
	fs.IntVar(&cloneDepth, "clone-depth", 0, "perform shallow clone with the given depth (0 disables)")
	fs.BoolVar(&forceSync, "force-sync", false, "always run git pull, ignoring the cached pushed_at state")
	fs.BoolVar(&ignoreSubmoduleErrs, "ignore-submodule-failures", false,
		"with --recurse-submodules, swallow submodule update failures so the parent repo still syncs")
	fs.StringVar(&layout, "layout", "", "templated path structure for repositories (e.g. {{.Visibility}}/{{.Language}}/{{.Name}})")
	fs.BoolVar(&finderTags, "finder-tags", runtime.GOOS == "darwin", "apply managed macOS Finder Tags to repository folders")
	return fs
}

// numCPU reports the host's usable core count. A seam rather than a direct
// call so both clamp branches below are reachable from a test: the bounds
// only bite on hosts with fewer than four or more than eight cores, which
// makes the clamps untestable on whatever machine happens to run CI.
var numCPU = runtime.NumCPU

// defaultConcurrency sizes the worker pool from the host, bounded to a range
// that is useful without being hostile to the GitHub API.
func defaultConcurrency() int {
	n := numCPU()
	if n < minDefaultConcurrency {
		return minDefaultConcurrency
	}
	if n > maxDefaultConcurrency {
		return maxDefaultConcurrency
	}
	return n
}

const (
	minDefaultConcurrency = 4
	maxDefaultConcurrency = 8
)

// --output is deliberately NOT in a shared group: plan defaults to json while
// status and prune default to text, and they accept different value sets, so
// each command registers its own.
