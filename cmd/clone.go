// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/sebastienrousseau/corral/internal/engine"
	"github.com/sebastienrousseau/corral/internal/git"
	"github.com/sebastienrousseau/corral/internal/github"
	"github.com/spf13/cobra"
)

// cloneCmd is the explicit spelling of what the bare root command does.
//
// corralctl grew from one verb into several — clone, sync, status, plan,
// prune, exec, mcp — and the one it started with was the only one without
// a name: `corralctl <owner>`. A CLI with subcommands where the first
// operation is the unnamed one reads as two tools sharing a binary. This
// gives it a name, and the root keeps accepting the bare form so nothing
// that already works stops working.
var cloneCmd = &cobra.Command{
	Use:   "clone <owner|topic:<topic>|language:<language>> [base_dir] [limit]",
	Short: "Clone and organise an owner's repositories, and keep existing clones in step",
	Long: `Lists the owner's repositories on the selected forge, clones the ones that
are missing into the organised layout under base_dir, and pulls the ones
that are already there. The bare form, "corralctl <owner>", does the same.`,
	Args: validateCloneArgs,
	PreRunE: func(cmd *cobra.Command, args []string) error {
		return validateCommonFlags(cmd)
	},
	Run: runClone,
}

// validateCloneArgs is validateRootArgs without the subcommand-typo check,
// which is meaningless here: an argument to `clone` cannot be a mistyped
// subcommand, because the subcommand has already been named.
func validateCloneArgs(_ *cobra.Command, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("requires at least 1 arg (the owner, topic:<topic>, or language:<language>)")
	}
	if len(args) > 3 {
		return fmt.Errorf("accepts at most 3 args (<owner> [base_dir] [limit]), received %d", len(args))
	}
	if len(args) > 1 {
		if kind, ok := legacyPositionalKeyword(args[1]); ok {
			return fmt.Errorf("%q is a --%s value, not a directory\n\n"+
				"Repository type and sort are flags, so base_dir is never guessed:\n"+
				"\tcorralctl clone %s --%s %q\n\n"+
				"To use a directory of that name, qualify it:\n\tcorralctl clone %s ./%s",
				args[1], kind, args[0], kind, args[1], args[0], args[1])
		}
	}
	return nil
}

// runClone is the clone operation, shared by the root command and `clone`.
func runClone(cmd *cobra.Command, args []string) {
	owner := args[0]
	filterType := strings.ToLower(strings.TrimSpace(repoType))
	filterSort := strings.ToLower(strings.TrimSpace(repoSort))
	bDir := baseDir
	lim := limit

	// The positional grammar is exactly what `Use` and the README
	// document: <owner> [base_dir] [limit]. Repository type and sort are
	// --type and --sort flags.
	//
	// Until v0.0.20 this parser also silently consumed args[1] as a
	// <type> and args[2] as a <sort> when they matched a keyword list,
	// which meant ten ordinary directory names — forks, stars, name,
	// public, private, templates and friends — were quietly swallowed and
	// the run fell back to $HOME/Code instead of the directory the user
	// named. validateRootArgs now rejects those instead of guessing.
	argIdx := 1
	if len(args) > argIdx {
		bDir = args[argIdx]
		argIdx++
	}
	if len(args) > argIdx {
		if _, err := fmt.Sscanf(args[argIdx], "%d", &lim); err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: limit must be a valid integer\n")
			osExit(1)
			return
		}
		if lim < 0 {
			fmt.Fprintf(os.Stderr, "ERROR: limit must be >= 0\n")
			osExit(1)
			return
		}
	}

	// Preflight banner + confirm. Prints the parsed owner + resolved
	// base_dir so a `corral i sebastienrousseau`-style arg typo is
	// obvious BEFORE the network fetch. When the base_dir doesn't
	// already exist and stdin is a TTY, also prompts for a
	// confirmation; --yes bypasses it, --dry-run implies bypass.
	// Interactive TUI mode has its own confirmation via /exit and
	// doesn't need the extra prompt.
	if !interactive {
		proceed, err := preflightRunner(owner, bDir)
		if err != nil {
			// Refused (e.g. no TTY to confirm a brand-new target
			// directory). This is a failure, not a choice, so exit
			// non-zero: a script must be able to tell it did nothing.
			fmt.Fprintf(os.Stderr, "corralctl: %v\n", err)
			osExit(1)
			return
		}
		if !proceed {
			fmt.Fprintln(os.Stderr, "Aborted.")
			osExit(0)
			return
		}
	}

	engineRun(cmdContext(cmd), engine.RunOptions{
		Owner:       owner,
		BaseDir:     bDir,
		Concurrency: concurrency,
		DryRun:      dryRun,
		Orphans:     orphans,
		Protocol:    protocol,
		DoSync:      !noSync,
		Output:      engine.OutputFormat(output),
		Interactive: interactive,
		Forge:       forgeName,
		ForgeURL:    forgeURL,
		Fetch: github.FetchOptions{
			Limit:            lim,
			Visibility:       visibility,
			IncludeForks:     includeForks,
			IncludeArchived:  includeArchived,
			IncludeLanguages: parseCSV(includeLanguagesCSV),
			ExcludeLanguages: parseCSV(excludeLanguagesCSV),
			AuthMode:         github.AuthMode(authMode),
			RetryMax:         retryMax,
			RetryMinBackoff:  retryMinBackoff,
			RetryMaxBackoff:  retryMaxBackoff,
			RequestTimeout:   apiRequestTimeout,
			TotalTimeout:     apiTotalTimeout,
			Type:             filterType,
			Sort:             filterSort,
		},
		Clone: git.CloneOptions{
			RecurseSubmodules: recurseSubmodules,
			SingleBranch:      cloneSingleBranch,
			Blobless:          cloneBlobless,
			Depth:             cloneDepth,
		},
		Sync: engine.SyncOptions{
			Force:                   forceSync,
			IgnoreSubmoduleFailures: ignoreSubmoduleErrs,
		},
		Layout:     layout,
		FinderTags: finderTags,
		Version:    Version,
	})
}

func init() {
	cloneCmd.Flags().AddFlagSet(fetchFlags())
	cloneCmd.Flags().AddFlagSet(cloneFlags())
	cloneCmd.Flags().BoolVarP(&orphans, "orphans", "o", false, "detect and list local clones no longer present on the selected forge")
	cloneCmd.Flags().StringVar(&output, "output", string(engine.OutputText), "output format: text, json, ndjson")
	cloneCmd.Flags().BoolVarP(&interactive, "interactive", "i", false, "display an interactive selector dashboard to pick repositories to clone/sync")
	cloneCmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "skip the preflight confirmation prompt when a new base directory would be created")
	rootCmd.AddCommand(cloneCmd)
}
