// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/sebastienrousseau/corralctl/internal/engine"
	"github.com/sebastienrousseau/corralctl/internal/forge"
	gitutil "github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/sebastienrousseau/corralctl/internal/github"
	corralmcp "github.com/sebastienrousseau/corralctl/internal/mcp"
	"github.com/spf13/cobra"
)

// configFile and profile now live in cmd/config.go.

type localStatus struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Remote      string `json:"remote,omitempty"`
	Visibility  string `json:"visibility,omitempty"`
	Language    string `json:"language,omitempty"`
	Dirty       bool   `json:"dirty"`
	DirtyDetail string `json:"dirty_detail,omitempty"`
}

type pruneResult struct {
	Path    string `json:"path"`
	Action  string `json:"action"`
	Message string `json:"message,omitempty"`
}

var (
	statusOutput    string
	planOutput      string
	pruneOutput     string
	opsFetchRepos   = fetchReposForOps
	removeAll       = os.RemoveAll
	localStateCheck = gitutil.HasUnpublishedWork
)

var statusCmd = &cobra.Command{
	Use:   "status [base_dir]",
	Short: "Report the state of organised local repositories",
	Args:  cobra.MaximumNArgs(1),
	PreRunE: func(cmd *cobra.Command, args []string) error {
		if statusOutput != "text" && statusOutput != "json" {
			return errors.New("--output must be text or json")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		root := resolvedBaseDir(args)
		idx, err := corralmcp.Scan(root)
		if err != nil {
			return err
		}
		rows := make([]localStatus, 0, len(idx.Repos))
		for _, repo := range idx.Repos {
			dirty, detail := gitutil.HasLocalChanges(cmdContext(cmd), repo.Path)
			rows = append(rows, localStatus{
				Name: repo.Name, Path: repo.Path, Remote: repo.RemoteURL,
				Visibility: repo.Visibility, Language: repo.Language,
				Dirty: dirty, DirtyDetail: detail,
			})
		}
		if statusOutput == string(engine.OutputJSON) {
			return writeJSON(os.Stdout, rows)
		}
		for _, row := range rows {
			state := "clean"
			if row.Dirty {
				state = "dirty: " + row.DirtyDetail
			}
			fmt.Printf("%-32s %-8s %s\n", row.Name, state, row.Path)
		}
		return nil
	},
}

var planCmd = &cobra.Command{
	Use:   "plan <owner|topic:<topic>|language:<language>>",
	Short: "Preview repository reconciliation without changing disk state",
	Args:  cobra.ExactArgs(1),
	PreRunE: func(cmd *cobra.Command, args []string) error {
		if !validOperationalOutput(planOutput) {
			return errors.New("--output must be text, json, or ndjson")
		}
		return validateCommonFlags(cmd)
	},
	Run: func(cmd *cobra.Command, args []string) {
		engineRun(cmdContext(cmd), operationalRunOptions(args[0], true, engine.OutputFormat(planOutput)))
	},
}

// fetchReposForOps lists an owner's repositories for the operational
// commands, through the selected forge.
//
// A named function rather than a closure so the seam test can still
// compare it against its production implementation by pointer — a seam
// that cannot be checked is how a stub leaks out of a test unnoticed.
//
// prune used to call the GitHub client directly regardless of --forge, so
// the flag was accepted and then ignored: it compared another host's
// clones against a GitHub listing.
func fetchReposForOps(ctx context.Context, owner string, opts github.FetchOptions) ([]github.Repo, error) {
	return engine.FetchRepos(ctx, owner, opts, forgeName, forgeURL)
}

var pruneCmd = &cobra.Command{
	Use:   "prune <owner>",
	Short: "Remove safe local clones that no longer exist upstream",
	Args:  cobra.ExactArgs(1),
	PreRunE: func(cmd *cobra.Command, args []string) error {
		if pruneOutput != "text" && pruneOutput != "json" {
			return errors.New("--output must be text or json")
		}
		return validateCommonFlags(cmd)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		if !dryRun && !assumeYes {
			return errors.New("prune requires --yes; use --dry-run to preview")
		}
		owner := strings.ToLower(strings.TrimSpace(args[0]))
		repos, err := opsFetchRepos(cmdContext(cmd), owner, github.FetchOptions{
			Limit: limit, AuthMode: github.AuthMode(authMode),
			RequestTimeout: apiRequestTimeout, TotalTimeout: apiTotalTimeout,
			RetryMax: retryMax, RetryMinBackoff: retryMinBackoff, RetryMaxBackoff: retryMaxBackoff,
			IncludeArchived: true, IncludeForks: true,
		})
		if err != nil {
			return err
		}
		// A truncated upstream listing makes every repository past the cap look
		// like an orphan, and prune's answer to an orphan is rm -rf. The engine
		// warns about exactly this truncation on the sync path; prune had no
		// equivalent guard, so `corralctl prune acme --yes` against an owner
		// with more than --limit repositories deleted live clones.
		if limit > 0 && len(repos) >= limit {
			return fmt.Errorf(
				"refusing to prune: the upstream listing hit --limit (%d repositories)\n"+
					"Anything beyond the limit is missing from the comparison and would look\n"+
					"like an orphan. Re-run with --limit 0 to fetch every repository",
				limit)
		}
		// Identities come from the clone URL each forge returned, so they
		// carry the real host. Building them as "github.com/" + FullName
		// meant a Codeberg listing produced identities no Codeberg clone
		// could ever match — which, combined with the hardcoded owner
		// prefix below, made prune a silent no-op off GitHub. Fixing
		// either one alone would have made it delete.
		upstream := make(map[string]struct{}, len(repos))
		cloneURLs := make([]string, 0, len(repos))
		for _, repo := range repos {
			if repo.CloneURL != "" {
				cloneURLs = append(cloneURLs, repo.CloneURL)
				if id := gitutil.CanonicalRemote(repo.CloneURL); id != "" {
					upstream[id] = struct{}{}
				}
				continue
			}
			// A forge that returned no clone URL still has an identity;
			// falling back keeps it comparable rather than orphaned.
			identity := repo.FullName
			if identity == "" && repo.Owner != "" {
				identity = repo.Owner + "/" + repo.Name
			}
			if identity != "" {
				upstream[strings.ToLower("github.com/"+identity)] = struct{}{}
			}
		}
		idx, err := corralmcp.Scan(resolvedBaseDir(nil))
		if err != nil {
			return err
		}
		results := make([]pruneResult, 0)
		selected, err := forge.Resolve(forgeName, forgeURL)
		if err != nil {
			return err
		}
		prefixes := forge.OwnerPrefixes(selected, owner, forgeURL, cloneURLs)
		if len(prefixes) == 0 {
			// No prefix means nothing can be identified as this owner's,
			// and prune's answer to an unidentified clone must never be
			// rm -rf. Refusing beats deleting on a guess.
			return fmt.Errorf(
				"refusing to prune: cannot tell which clones belong to %q on %s\n"+
					"The upstream listing returned no repositories with clone URLs, so there is\n"+
					"nothing to compare against. Check the owner name, and --forge-url for a\n"+
					"self-hosted instance",
				owner, selected.Name())
		}
		for _, local := range idx.Repos {
			identity := gitutil.CanonicalRemote(local.RemoteURL)
			if !forge.MatchesOwner(identity, prefixes) {
				continue
			}
			if _, exists := upstream[identity]; exists {
				continue
			}
			unsafe, detail := localStateCheck(cmdContext(cmd), local.Path)
			if unsafe {
				results = append(results, pruneResult{Path: local.Path, Action: "REFUSE", Message: detail})
				continue
			}
			if dryRun {
				results = append(results, pruneResult{Path: local.Path, Action: "DRY-RUN", Message: "remove orphan"})
				continue
			}
			if err := removeAll(local.Path); err != nil {
				results = append(results, pruneResult{Path: local.Path, Action: "ERROR", Message: err.Error()})
				continue
			}
			results = append(results, pruneResult{Path: local.Path, Action: "DELETE"})
		}
		failed := false
		if pruneOutput == string(engine.OutputJSON) {
			if err := writeJSON(os.Stdout, results); err != nil {
				return err
			}
		} else if len(results) == 0 {
			// Text mode printed per-result lines and nothing else, so "nothing
			// to prune" was indistinguishable from "the command did no work" —
			// on a subcommand whose job is deleting directories, silence is the
			// one answer a user cannot safely interpret. JSON mode was already
			// unambiguous (it emits []).
			fmt.Printf("No prunable repositories found for %s.\n", owner)
		} else {
			for _, result := range results {
				fmt.Printf("%-8s %s", result.Action, result.Path)
				if result.Message != "" {
					fmt.Printf(": %s", result.Message)
				}
				fmt.Println()
			}
		}
		for _, result := range results {
			failed = failed || result.Action == "ERROR" || result.Action == "REFUSE"
		}
		if failed {
			return errors.New("one or more repositories could not be pruned")
		}
		return nil
	},
}

var profileCmd = &cobra.Command{
	Use:   "profile <name>",
	Short: "Reconcile every owner in a configured profile",
	Args:  cobra.ExactArgs(1),
	PreRunE: func(cmd *cobra.Command, args []string) error {
		if !validOperationalOutput(output) {
			return errors.New("--output must be text, json, or ndjson")
		}
		return validateCommonFlags(cmd)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig(configPath)
		if err != nil {
			return err
		}
		selected, ok := cfg.Profiles[args[0]]
		if !ok {
			return fmt.Errorf("profile %q is not configured", args[0])
		}
		if err := validateProfile(args[0], selected); err != nil {
			return err
		}
		// Apply the profile's settings onto this command's flags before
		// building RunOptions, so a profile can set anything a flag can rather
		// than the five fields the previous hand-written mapping covered. An
		// explicitly-passed flag still wins.
		if _, err := applySettings(cmd, selected.effectiveSettings(),
			fmt.Sprintf("profile %q", args[0])); err != nil {
			return err
		}
		for _, owner := range selected.Owners {
			engineRun(cmdContext(cmd), operationalRunOptions(owner, dryRun, engine.OutputFormat(output)))
		}
		return nil
	},
}

func operationalRunOptions(owner string, preview bool, format engine.OutputFormat) engine.RunOptions {
	return engine.RunOptions{
		Owner: owner, BaseDir: resolvedBaseDir(nil), Concurrency: concurrency,
		DryRun: preview, Orphans: orphans, Protocol: protocol, DoSync: !noSync,
		Output: format, Interactive: false, Layout: layout, FinderTags: finderTags, Version: Version,
		Forge: forgeName, ForgeURL: forgeURL,
		Fetch: github.FetchOptions{
			Limit: limit, Visibility: visibility, IncludeForks: includeForks,
			IncludeArchived: includeArchived, IncludeLanguages: parseCSV(includeLanguagesCSV),
			ExcludeLanguages: parseCSV(excludeLanguagesCSV), AuthMode: github.AuthMode(authMode),
			RetryMax: retryMax, RetryMinBackoff: retryMinBackoff,
			RetryMaxBackoff: retryMaxBackoff,
			RequestTimeout:  apiRequestTimeout, TotalTimeout: apiTotalTimeout,
		},
		Clone: gitutil.CloneOptions{
			RecurseSubmodules: recurseSubmodules, SingleBranch: cloneSingleBranch,
			Blobless: cloneBlobless, Depth: cloneDepth,
		},
		Sync: engine.SyncOptions{Force: forceSync, IgnoreSubmoduleFailures: ignoreSubmoduleErrs},
	}
}

func resolvedBaseDir(args []string) string {
	if len(args) > 0 && args[0] != "" {
		return args[0]
	}
	if baseDir != "" {
		return baseDir
	}
	return defaultBaseDir()
}

func writeJSON(target *os.File, value any) error {
	encoder := json.NewEncoder(target)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func validOperationalOutput(value string) bool {
	return value == string(engine.OutputText) || value == string(engine.OutputJSON) || value == string(engine.OutputNDJSON)
}

func init() {
	statusCmd.Flags().StringVar(&statusOutput, "output", "text", "output format: text or json")
	planCmd.Flags().StringVar(&planOutput, "output", "json", "output format: text, json, or ndjson")
	pruneCmd.Flags().StringVar(&pruneOutput, "output", "text", "output format: text or json")
	pruneCmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "confirm removal of safe orphaned clones")
	profileCmd.Flags().StringVar(&output, "output", "text", "output format: text, json, or ndjson")
	// The operational commands read the same package-level flag variables via
	// operationalRunOptions(), so give them the flags that feed it. Before
	// v0.0.21 they consumed those values while rejecting every attempt to set
	// them ("unknown flag: --limit").
	//
	// prune deliberately gets only the fetch group: it removes clones and never
	// creates one, so clone-depth and friends would be inert noise on its help.
	for _, c := range []*cobra.Command{planCmd, profileCmd} {
		c.Flags().AddFlagSet(fetchFlags())
		c.Flags().AddFlagSet(cloneFlags())
	}
	pruneCmd.Flags().AddFlagSet(fetchFlags())

	rootCmd.AddCommand(statusCmd, planCmd, pruneCmd, profileCmd)
}
