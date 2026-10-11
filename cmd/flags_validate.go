// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/sebastienrousseau/corralctl/internal/engine"
	"github.com/sebastienrousseau/corralctl/internal/github"
	"github.com/spf13/cobra"
)

// deprecationWarning is indirected so tests can capture what a user would
// see on stderr without racing the real stream.
var deprecationWarning = func(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
}

// levenshtein is the standard edit distance, used only for "did you mean"
// suggestions over a handful of short subcommand names.
func levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, min(cur[j-1]+1, prev[j-1]+cost))
		}
		prev, cur = cur, prev
	}
	return prev[len(br)]
}

// resolveAPITimeouts validates the timeout flags and applies the deprecated
// --api-timeout when it is the only one given.
//
// --api-timeout was documented as "GitHub API request deadline" and applied
// as both the per-request deadline and the deadline for the entire
// paginated fetch. An organisation large enough to need 50 pages could not
// be listed at all, and the help text gave the user no reason to raise the
// value, because they believed it governed one request. It also made the
// rate-limit retry unreachable: a reset further out than the remaining
// budget always lost the race.
//
// The two are now separate. --api-timeout still works for at least one
// minor release, supplying both halves so behaviour is unchanged for anyone
// who set it deliberately.
// cmd is the command being validated, used only to ask which flags the
// user actually typed. It may be nil, which is treated as "nothing typed".
func resolveAPITimeouts(cmd *cobra.Command) error {
	changed := func(name string) bool {
		return cmd != nil && cmd.Flags().Changed(name)
	}
	requestSet := changed("api-request-timeout")
	totalSet := changed("api-total-timeout")

	if apiTimeout > 0 {
		deprecationWarning(
			"corralctl: --api-timeout is deprecated and will be removed in a future release.\n"+
				"  It was applied to every request AND to the whole paginated fetch, so a large\n"+
				"  organisation could not be listed at all. Use:\n"+
				"    --api-request-timeout %s   (one request)\n"+
				"    --api-total-timeout %s     (the whole fetch, including retries)",
			apiTimeout, apiTimeout)
		if !requestSet {
			apiRequestTimeout = apiTimeout
		}
		if !totalSet {
			apiTotalTimeout = apiTimeout
		}
	} else if apiTimeout < 0 {
		return fmt.Errorf("--api-timeout must be > 0")
	}

	if apiRequestTimeout <= 0 {
		return fmt.Errorf("--api-request-timeout must be > 0")
	}
	if apiTotalTimeout <= 0 {
		return fmt.Errorf("--api-total-timeout must be > 0")
	}
	if apiTotalTimeout < apiRequestTimeout {
		return fmt.Errorf(
			"--api-total-timeout (%s) must be >= --api-request-timeout (%s): "+
				"the whole fetch cannot be shorter than one request within it",
			apiTotalTimeout, apiRequestTimeout)
	}
	return nil
}

// validateCommonFlags checks the shared fetch/clone flag values.
//
// Extracted from the root command's PreRunE so plan, prune and profile run the
// same checks. Those commands consume the same variables, but validated none of
// them: a profile setting concurrency to -1 reached engine.Run, which printed
// an error and terminated the process rather than failing the command cleanly.
func validateCommonFlags(cmd *cobra.Command) error {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	output = strings.ToLower(strings.TrimSpace(output))
	authMode = strings.ToLower(strings.TrimSpace(authMode))
	visibility = strings.ToLower(strings.TrimSpace(visibility))

	if concurrency < 1 {
		return fmt.Errorf("--concurrency must be >= 1")
	}
	if limit < 0 {
		return fmt.Errorf("--limit must be >= 0")
	}
	if cloneDepth < 0 {
		return fmt.Errorf("--clone-depth must be >= 0")
	}
	if retryMax < 0 {
		return fmt.Errorf("--retry-max must be >= 0")
	}
	if retryMinBackoff <= 0 {
		return fmt.Errorf("--retry-min-backoff must be > 0")
	}
	if retryMaxBackoff <= 0 {
		return fmt.Errorf("--retry-max-backoff must be > 0")
	}
	if retryMaxBackoff < retryMinBackoff {
		return fmt.Errorf("--retry-max-backoff must be >= --retry-min-backoff")
	}
	if err := resolveAPITimeouts(cmd); err != nil {
		return err
	}
	if protocol != "https" && protocol != "ssh" {
		return fmt.Errorf("--protocol must be either ssh or https")
	}
	if output != string(engine.OutputText) && output != string(engine.OutputJSON) && output != string(engine.OutputNDJSON) {
		return fmt.Errorf("--output must be one of: text, json, ndjson")
	}
	if authMode != string(github.AuthModeAuto) && authMode != string(github.AuthModeToken) && authMode != string(github.AuthModeGH) {
		return fmt.Errorf("--auth must be one of: auto, token, gh")
	}
	if visibility != "all" && visibility != "public" && visibility != "private" {
		return fmt.Errorf("--visibility must be one of: all, public, private")
	}
	repoType = strings.ToLower(strings.TrimSpace(repoType))
	repoSort = strings.ToLower(strings.TrimSpace(repoSort))
	if reason, unsupported := unsupportedRepoTypes[repoType]; unsupported {
		return fmt.Errorf("--type %q is not supported: %s\n\nSupported values: %s",
			repoType, reason, strings.Join(repoTypeValues, ", "))
	}
	if repoType != "" && !slices.Contains(repoTypeValues, repoType) {
		return fmt.Errorf("--type must be one of: %s", strings.Join(repoTypeValues, ", "))
	}
	if repoSort != "" && !slices.Contains(repoSortValues, repoSort) {
		return fmt.Errorf("--sort must be one of: %s", strings.Join(repoSortValues, ", "))
	}
	// Validate the layout template before any network work so a typo
	// fails immediately instead of after a full paginated fetch.
	if layout != "" {
		if _, err := engine.ParseLayoutTemplate(layout); err != nil {
			return fmt.Errorf("--layout is not a valid template: %w", err)
		}
	}
	return nil
}
