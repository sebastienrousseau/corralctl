// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sebastienrousseau/corralctl/internal/diag"
	"github.com/sebastienrousseau/corralctl/internal/discover"
	"github.com/sebastienrousseau/corralctl/internal/engine"
	"github.com/sebastienrousseau/corralctl/internal/forge"
	"github.com/sebastienrousseau/corralctl/internal/github"
	"github.com/sebastienrousseau/corralctl/internal/tui"
	"github.com/spf13/cobra"
)

var (
	adoptForge         string
	adoptForgeURL      string
	adoptOwner         string
	adoptVisibility    string
	adoptRelocate      bool
	adoptTargetDir     string
	adoptProtocol      string
	adoptOutput        string
	adoptUntrackedOnly bool
	adoptInteractive   bool

	// Seams for testing
	adoptDiscoverRun  = discover.Discover
	adoptExecute      = engine.ExecuteAdoption
	adoptResolveForge = forge.Resolve
	adoptAsTarget     = forge.AsTarget
	adoptForgeToken   = engine.ForgeToken
	adoptFilepathAbs  = filepath.Abs
	adoptWizardRun    = tui.RunAdoptWizard
)

// adoptCmd scans for untracked local repositories, relocates them,
// and optionally provisions and links remote origins.
var adoptCmd = &cobra.Command{
	Use:   "adopt [path]",
	Short: "Discover untracked local repositories and link them to remotes",
	Long: `Scans the filesystem for untracked or unmanaged Git repositories, optionally
relocates them into the corralctl workspace layout, creates remote repositories on
the requested forge (GitHub, GitLab, Gitea, Forgejo, Codeberg, Bitbucket), and
links local repositories to their new remote origin.`,
	Args: cobra.MaximumNArgs(1),
	PreRunE: func(cmd *cobra.Command, args []string) error {
		adoptProtocol = strings.ToLower(strings.TrimSpace(adoptProtocol))
		if adoptProtocol != "https" && adoptProtocol != "ssh" {
			return errors.New("--protocol must be either https or ssh")
		}
		adoptVisibility = strings.ToLower(strings.TrimSpace(adoptVisibility))
		if adoptVisibility != "private" && adoptVisibility != "public" {
			return errors.New("--visibility must be private or public")
		}
		adoptOutput = strings.ToLower(strings.TrimSpace(adoptOutput))
		if !validOperationalOutput(adoptOutput) {
			return errors.New("--output must be text, json, or ndjson")
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmdContext(cmd)
		searchRoot := "."
		if len(args) > 0 {
			searchRoot = args[0]
		}

		absRoot, err := adoptFilepathAbs(searchRoot)
		if err != nil {
			return fmt.Errorf("resolving root directory: %w", err)
		}

		diag.Infof("Scanning for repositories under %s...", absRoot)
		candidates, err := adoptDiscoverRun(ctx, discover.Options{
			BaseDir:       absRoot,
			UntrackedOnly: adoptUntrackedOnly,
		})
		if err != nil {
			return fmt.Errorf("discovery failed: %w", err)
		}

		if len(candidates) == 0 {
			diag.Infof("No unmanaged repositories discovered under %s", absRoot)
			return nil
		}

		if adoptInteractive {
			selected, proceed, wizardErr := adoptWizardRun(ctx, candidates)
			if wizardErr != nil {
				return wizardErr
			}
			if !proceed || len(selected) == 0 {
				return nil
			}
			candidates = selected
		}

		return processAdoption(ctx, candidates)
	},
}

func processAdoption(ctx context.Context, candidates []discover.Candidate) error {
	var targetForge forge.Target
	var targetOpts forge.TargetOptions

	if adoptForge != "" {
		f, err := adoptResolveForge(adoptForge, adoptForgeURL)
		if err != nil {
			return err
		}
		tf, ok := adoptAsTarget(f)
		if !ok {
			return fmt.Errorf("forge %s does not support repository creation", adoptForge)
		}
		targetForge = tf

		tok := adoptForgeToken(ctx, f.Name(), github.AuthModeAuto)
		if tok == "" && !dryRun {
			return fmt.Errorf("no authentication token found for forge %s", f.Name())
		}
		targetOpts = forge.TargetOptions{
			Token:   tok,
			BaseURL: adoptForgeURL,
		}
	}

	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	var aggregateResults []engine.AdoptResult

	for _, cand := range candidates {
		newPath := cand.Path
		if adoptRelocate && adoptTargetDir != "" {
			newPath = filepath.Join(adoptTargetDir, cand.Name)
		}

		opts := engine.AdoptOptions{
			LocalPath:     cand.Path,
			NewTargetDir:  newPath,
			Forge:         targetForge,
			ForgeOptions:  targetOpts,
			Owner:         adoptOwner,
			RepoName:      cand.Name,
			Private:       adoptVisibility == "private",
			Protocol:      adoptProtocol,
			RelocateLocal: adoptRelocate,
			DryRun:        dryRun,
		}

		res, err := adoptExecute(ctx, opts)
		if err != nil {
			diag.Errorf("Failed to adopt %s: %v", cand.Name, err)
			continue
		}

		switch adoptOutput {
		case string(engine.OutputJSON):
			aggregateResults = append(aggregateResults, res)
		case string(engine.OutputNDJSON):
			_ = encoder.Encode(res)
		default:
			fmt.Printf("✓ [%s] %s -> %s (%s)\n", res.Action, res.RepoName, res.FinalPath, res.RemoteURL)
		}
	}

	if adoptOutput == string(engine.OutputJSON) {
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(aggregateResults)
	}

	return nil
}

func init() {
	adoptCmd.Flags().StringVar(&adoptForge, "forge", "", "Destination forge (github, gitlab, gitea, forgejo, codeberg, bitbucket)")
	adoptCmd.Flags().StringVar(&adoptForgeURL, "forge-url", "", "Custom instance URL for self-hosted forges")
	adoptCmd.Flags().StringVar(&adoptOwner, "owner", "", "Target user or organization on the remote forge")
	adoptCmd.Flags().StringVar(&adoptVisibility, "visibility", "private", "Repository visibility (private or public)")
	adoptCmd.Flags().BoolVar(&adoptRelocate, "relocate", false, "Relocate repository into target directory")
	adoptCmd.Flags().StringVar(&adoptTargetDir, "target-dir", "", "Target directory when relocating")
	adoptCmd.Flags().StringVar(&adoptProtocol, "protocol", "https", "Transport protocol (https or ssh)")
	adoptCmd.Flags().StringVar(&adoptOutput, "output", "text", "Output format (text, json, ndjson)")
	adoptCmd.Flags().BoolVar(&adoptUntrackedOnly, "untracked-only", true, "Limit discovery to repositories without an origin remote")
	adoptCmd.Flags().BoolVarP(&adoptInteractive, "interactive", "i", false, "display an interactive selector dashboard to pick repositories to adopt")
	adoptCmd.Flags().BoolVar(&dryRun, "dry-run", false, "Report intended actions without making changes")

	rootCmd.AddCommand(adoptCmd)
}
