// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package cmd provides the command-line interface for the Corral application.
package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/sebastienrousseau/corral/internal/diag"
	"github.com/sebastienrousseau/corral/internal/engine"
	"github.com/sebastienrousseau/corral/internal/github"
	"github.com/spf13/cobra"
)

// Version is the build version of Corral. It is overridden at release time via
// -ldflags "-X github.com/sebastienrousseau/corral/cmd.Version=<version>"
// (set by goreleaser) and by `make build` via `git describe`. The "dev"
// fallback makes an un-injected build obviously local rather than masquerading
// as a stale release tag.
var Version = "dev"

var (
	baseDir             string
	concurrency         int
	dryRun              bool
	orphans             bool
	protocol            string
	noSync              bool
	recurseSubmodules   bool
	limit               int
	output              string
	authMode            string
	forgeName           string
	forgeURL            string
	visibility          string
	includeForks        bool
	includeArchived     bool
	includeLanguagesCSV string
	excludeLanguagesCSV string
	cloneBlobless       bool
	cloneSingleBranch   bool
	cloneDepth          int
	forceSync           bool
	ignoreSubmoduleErrs bool
	layout              string
	interactive         bool
	finderTags          bool
	assumeYes           bool
	repoType            string
	repoSort            string
	retryMax            int
	retryMinBackoff     time.Duration
	retryMaxBackoff     time.Duration
	apiTimeout          time.Duration
	apiRequestTimeout   time.Duration
	apiTotalTimeout     time.Duration
	logLevel            string
	osExit              = os.Exit
	engineRun           = engine.Run
	preflightRunner     = runPreflight
)

var rootCmd = &cobra.Command{
	Use:   "corralctl <owner|topic:<topic>|language:<language>> [base_dir] [limit]",
	Short: "Automatically clone and organise repositories by owner, topic, or language.",
	Args:  validateRootArgs,
	PreRunE: func(cmd *cobra.Command, args []string) error {
		return validateCommonFlags(cmd)
	},
	// The bare form: `corralctl <owner>` does what `corralctl clone <owner>`
	// does. Kept so every existing invocation, cron line and script keeps
	// working; `clone` is the documented spelling.
	Run: runClone,
}

func parseCSV(csv string) []string {
	if strings.TrimSpace(csv) == "" {
		return nil
	}
	parts := strings.Split(csv, ",")
	values := make([]string, 0, len(parts))
	for _, p := range parts {
		v := strings.TrimSpace(p)
		if v == "" {
			continue
		}
		values = append(values, v)
	}
	return values
}

func cmdContext(cmd *cobra.Command) context.Context {
	if cmd == nil {
		return context.Background()
	}
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	ExecuteContext(context.Background())
}

// ExecuteContext executes the root command with the provided context.
func ExecuteContext(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	rootCmd.SetContext(ctx)
	// Cobra prints the error and then the full usage block; this function then
	// printed the error a second time, so every mistake produced a ~52-line
	// wall with the message duplicated at both ends. Silence both and render
	// once here, with the actionable line last — clig.dev puts the most
	// important information at the end of the output.
	rootCmd.SilenceErrors = true
	rootCmd.SilenceUsage = true
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "corralctl: %v\n", err)
		fmt.Fprintf(os.Stderr, "\nRun 'corralctl --help' for usage.\n")
		osExit(1)
	}
}

// userHomeDir resolves the current user's home directory. It is indirected
// through a variable so tests can exercise the fallback path.
var userHomeDir = os.UserHomeDir

// defaultBaseDir returns the default root directory for cloned repositories,
// falling back to the current directory when the home directory cannot be
// determined.
func defaultBaseDir() string {
	home, err := userHomeDir()
	if err != nil {
		home = "." // fallback
	}
	return filepath.Join(home, "Code")
}

func init() {
	rootCmd.Version = Version

	rootCmd.PersistentFlags().StringVar(&baseDir, "base-dir", defaultBaseDir(), "root directory for cloned repos")
	rootCmd.PersistentFlags().BoolVarP(&dryRun, "dry-run", "n", false, "preview actions without making changes")
	// Diagnostics go to stderr; stdout carries the selected output format.
	// This raises or lowers how much of stderr you get without changing what
	// lands on stdout, so `--log-level debug --output json` stays pipeable
	// while giving a bug report something to attach.
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", envLogLevel(),
		"diagnostic verbosity on stderr: error, warn, info or debug")

	// Shared groups, also registered on plan/prune/profile so those commands
	// can set what they already consume. See cmd/flags.go.
	// Assigned here rather than in the rootCmd literal: the hook reaches
	// knownFlagNames(), which walks rootCmd, and Go rejects that as an
	// initialization cycle when it appears in the composite literal.
	//
	// Config defaults are applied before each command's own PreRunE, so a value
	// from the file is validated by exactly the same checks as one typed on the
	// command line. PersistentPreRunE runs for every subcommand, which is what
	// makes the config apply to plan/prune/profile/status rather than only to
	// `profile` as it did before v0.0.21.
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		// `config --init` writes the file this hook would read.
		if configInit {
			return applyLogLevel()
		}
		if _, err := configuredDefaults(cmd); err != nil {
			return err
		}
		// After the config is applied, so `"log-level": "debug"` in the
		// defaults block behaves exactly like passing the flag.
		return applyLogLevel()
	}

	rootCmd.Flags().AddFlagSet(fetchFlags())
	rootCmd.Flags().AddFlagSet(cloneFlags())
	rootCmd.Flags().BoolVarP(&orphans, "orphans", "o", false, "detect and list local clones no longer present on the selected forge")
	rootCmd.Flags().StringVar(&output, "output", string(engine.OutputText), "output format: text, json, ndjson")
	rootCmd.Flags().BoolVarP(&interactive, "interactive", "i", false, "display an interactive selector dashboard to pick repositories to clone/sync")
	rootCmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "skip the preflight confirmation prompt when a new base directory would be created")

	// Offer "did you mean" for near-miss subcommands. Cobra only reaches its
	// own suggestion path when the root is not runnable with arguments, which
	// corralctl is, so validateRootArgs does the work; this keeps the distance
	// consistent for the paths cobra does handle.
	rootCmd.SuggestionsMinimumDistance = 2
}

// repoTypeValues and repoSortValues are the accepted --type and --sort values.
// Both were previously matched positionally, which is what made ordinary
// directory names dangerous to pass as base_dir.
var (
	repoTypeValues = []string{
		"all", "public", "private", "sources", "forks", "archived",
		"mirrors", "templates",
	}

	// unsupportedRepoTypes are --type values corral accepted and could
	// never satisfy, because the field they filter on is not carried by
	// the REST listing endpoints. They validated, ran, hit the API and
	// returned nothing — indistinguishable from a correct empty result.
	//
	// A filter that cannot be honoured is refused, never answered with
	// silence: a wrong answer the user cannot detect is worse than an
	// error that names the limitation.
	unsupportedRepoTypes = map[string]string{
		"can be sponsored": "sponsorship status is not returned by the GitHub repository listing API",
		"sponsored":        "sponsorship status is not returned by the GitHub repository listing API",
	}
	repoSortValues = []string{"last updated", "updated", "name", "stars"}
)

// validateRootArgs replaces cobra.MinimumNArgs(1) on the root command.
//
// The root takes a bare <owner>, which means any typo'd subcommand is a
// syntactically valid invocation: `corralctl statuss` used to be read as
// "owner=statuss" and started a live GitHub fetch that cloned into
// $HOME/Code. This rejects an argument that is within edit distance 2 of a
// real subcommand and tells the user how to force it, which is clig.dev's
// "don't have a catch-all subcommand" applied to the one shape corralctl
// cannot avoid having.
func validateRootArgs(cmd *cobra.Command, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("requires at least 1 arg (the GitHub owner, topic:<topic>, or language:<language>)")
	}
	if len(args) > 3 {
		return fmt.Errorf("accepts at most 3 args (<owner> [base_dir] [limit]), received %d", len(args))
	}

	// `corralctl -- statuss` forces the owner reading.
	forced := cmd.ArgsLenAtDash() == 0
	if !forced {
		if suggestion := nearestSubcommand(cmd, args[0]); suggestion != "" {
			return fmt.Errorf("unknown command %q for %q\n\nDid you mean this?\n\t%s\n\n"+
				"If %q really is a GitHub owner, put any flags first and force it with:\n"+
				"\tcorralctl [flags] -- %s",
				args[0], cmd.Name(), suggestion, args[0], args[0])
		}
	}

	// A second positional is base_dir. Reject the old type/sort keywords
	// outright rather than guessing: before v0.0.20 they were swallowed as
	// filters and base_dir silently fell back to $HOME/Code.
	if len(args) > 1 {
		if kind, ok := legacyPositionalKeyword(args[1]); ok {
			return fmt.Errorf("%q is a --%s value, not a directory\n\n"+
				"Repository type and sort are now flags, so base_dir is never guessed:\n"+
				"\tcorralctl %s --%s %q\n\n"+
				"To use a directory of that name, qualify it:\n\tcorralctl %s ./%s",
				args[1], kind, args[0], kind, args[1], args[0], args[1])
		}
	}
	return nil
}

// envLogLevel is the default for --log-level, taken from CORRAL_LOG_LEVEL so
// the setting can be exported for a whole shell session rather than repeated
// on every invocation. An unset or unrecognised value leaves the default at
// "info"; a bad value passed explicitly is reported when the flag is applied.
func envLogLevel() string {
	if v := strings.TrimSpace(os.Getenv("CORRAL_LOG_LEVEL")); v != "" {
		if _, err := diag.ParseLevel(v); err == nil {
			return strings.ToLower(v)
		}
	}
	return diag.LevelInfo.String()
}

// applyLogLevel installs the requested verbosity, rejecting an unknown name
// rather than silently falling back — a typo'd level that quietly does
// nothing is how someone ends up filing a bug report with no debug output in
// it.
func applyLogLevel() error {
	level, err := diag.ParseLevel(logLevel)
	if err != nil {
		return err
	}
	diag.SetLevel(level)
	return nil
}

// nearestSubcommand returns the name of the subcommand closest to arg when arg
// looks like a misspelling of one, and "" otherwise. An exact match is not a
// typo — cobra dispatches those before Args ever runs.
func nearestSubcommand(cmd *cobra.Command, arg string) string {
	const maxDistance = 2
	arg = strings.ToLower(arg)
	best, bestDist := "", maxDistance+1
	for _, sub := range cmd.Commands() {
		if sub.Hidden {
			continue
		}
		for _, name := range append([]string{sub.Name()}, sub.Aliases...) {
			name = strings.ToLower(name)
			if name == arg {
				return "" // exact match; cobra would have dispatched it
			}
			if d := levenshtein(arg, name); d < bestDist {
				best, bestDist = name, d
			}
		}
	}
	if bestDist <= maxDistance {
		return best
	}
	return ""
}

// legacyPositionalKeyword reports whether s is one of the repository type or
// sort keywords the pre-v0.0.20 positional parser consumed.
func legacyPositionalKeyword(s string) (string, bool) {
	v := strings.ToLower(strings.TrimSpace(s))
	for _, t := range repoTypeValues {
		if v == t {
			return "type", true
		}
	}
	for _, t := range repoSortValues {
		if v == t {
			return "sort", true
		}
	}
	return "", false
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

// RootCommand returns the fully configured root command.
//
// Exposed so build-time tooling can walk the command tree — scripts/gen_docs.go
// renders manpages and shell completions from it, which is what keeps both in
// step with `--help` instead of drifting as hand-written files do. Callers must
// treat the result as read-only; mutating it changes what the CLI does.
func RootCommand() *cobra.Command { return rootCmd }

// deprecationWarning is indirected so tests can capture what a user would
// see on stderr without racing the real stream.
var deprecationWarning = func(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
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
