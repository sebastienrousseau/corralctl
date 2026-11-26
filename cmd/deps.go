// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/sebastienrousseau/corralctl/internal/deps"
	"github.com/sebastienrousseau/corralctl/internal/github"
	"github.com/spf13/cobra"
)

var (
	depsOutput    string
	depsOverrides string
	depsAuth      string
	depsArchived  bool
	depsForks     bool

	// Seams, so a test can stand in for GitHub.
	depsFetchRepos = github.FetchReposWithOptions
	depsNewClient  = github.NewClient
	depsListPRs    = github.DependabotPullRequests
)

var depsCmd = &cobra.Command{
	Use:   "deps",
	Short: "Triage the open Dependabot pull requests across an account",
	Long: "Read every open Dependabot pull request across an owner's repositories and decide how each " +
		"one is handled. Read-only: nothing is merged, closed or pushed. Kept updates are folded into " +
		"the repository's feat/v<next> release branch, never merged into main directly.",
}

var depsInventoryCmd = &cobra.Command{
	Use:     "inventory <owner>",
	Short:   "List every open Dependabot pull request with its checks and merge state",
	Args:    cobra.ExactArgs(1),
	PreRunE: validateDepsOutput,
	RunE: func(cmd *cobra.Command, args []string) error {
		prs, err := depsInventory(cmdContext(cmd), args[0])
		if err != nil {
			return err
		}
		return writeInventory(cmd.OutOrStdout(), prs, depsOutput)
	},
}

var depsPlanCmd = &cobra.Command{
	Use:     "plan <owner>",
	Short:   "Tier every open Dependabot pull request and find the fleet batches",
	Args:    cobra.ExactArgs(1),
	PreRunE: validateDepsOutput,
	RunE: func(cmd *cobra.Command, args []string) error {
		overrides, err := readOverrides(depsOverrides)
		if err != nil {
			return err
		}
		prs, err := depsInventory(cmdContext(cmd), args[0])
		if err != nil {
			return err
		}
		return writePlan(cmd.OutOrStdout(), deps.Build(prs, overrides), depsOutput)
	},
}

// validateDepsOutput accepts the two output formats.
func validateDepsOutput(*cobra.Command, []string) error {
	if depsOutput != "text" && depsOutput != "json" {
		return errors.New("--output must be text or json")
	}
	return nil
}

// depsInventory reads the open Dependabot pull requests of every
// repository owner has, skipping forks and archived repositories unless
// asked: neither takes Dependabot updates that need handling.
func depsInventory(ctx context.Context, owner string) ([]deps.PR, error) {
	opts := github.FetchOptions{
		Limit: 0, AuthMode: github.AuthMode(depsAuth),
		IncludeForks: depsForks, IncludeArchived: depsArchived,
	}
	repos, err := depsFetchRepos(ctx, owner, opts)
	if err != nil {
		return nil, err
	}
	client, err := depsNewClient(ctx, opts)
	if err != nil {
		return nil, err
	}
	var out []deps.PR
	for _, r := range repos {
		prs, err := depsListPRs(ctx, client, r.Owner, r.Name)
		if err != nil {
			return nil, err
		}
		for _, p := range prs {
			out = append(out, deps.PR{
				Repo: p.Repo, Number: p.Number, Title: p.Title, URL: p.URL,
				Branch: p.Branch, CI: deps.CI(p.CI), Mergeable: p.Mergeable,
			})
		}
	}
	return out, nil
}

// readOverrides loads known-bad versions from a JSON file: a list of
// {"package", "skip", "use"} objects. No path means none.
func readOverrides(path string) ([]deps.Override, error) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read overrides: %w", err)
	}
	var out []deps.Override
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("parse overrides %s: %w", path, err)
	}
	return out, nil
}

// writeInventory prints the pull requests, one per line, or as JSON.
func writeInventory(w io.Writer, prs []deps.PR, format string) error {
	if format == "json" {
		return writeDepsJSON(w, prs)
	}
	for _, p := range prs {
		if _, err := fmt.Fprintf(w, "%s#%d\t%s\t%s\t%s\n", p.Repo, p.Number, p.CI, p.Mergeable, p.Title); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "%d open Dependabot pull request(s)\n", len(prs))
	return err
}

// writePlan prints the plan as a Markdown report, or as JSON.
func writePlan(w io.Writer, p deps.Plan, format string) error {
	if format == "json" {
		return writeDepsJSON(w, p)
	}
	_, err := io.WriteString(w, deps.Report(p))
	return err
}

// writeDepsJSON writes v as indented JSON.
func writeDepsJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func init() {
	for _, c := range []*cobra.Command{depsInventoryCmd, depsPlanCmd} {
		c.Flags().StringVarP(&depsOutput, "output", "o", "text", "output format: text or json")
		c.Flags().StringVar(&depsAuth, "auth", string(github.AuthModeAuto), "authentication mode: auto, token, gh")
		c.Flags().BoolVar(&depsForks, "include-forks", false, "include forked repositories")
		c.Flags().BoolVar(&depsArchived, "include-archived", false, "include archived repositories")
	}
	depsPlanCmd.Flags().StringVar(&depsOverrides, "overrides", "",
		`JSON file of known-bad versions: [{"package": "...", "skip": "0.0.9", "use": "0.0.10"}]`)
	depsCmd.AddCommand(depsInventoryCmd, depsPlanCmd)
	rootCmd.AddCommand(depsCmd)
}
