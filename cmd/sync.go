// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/diag"
	"github.com/sebastienrousseau/corralctl/internal/engine"
	"github.com/sebastienrousseau/corralctl/internal/forge"
	"github.com/sebastienrousseau/corralctl/internal/github"
	"github.com/sebastienrousseau/corralctl/internal/mirror"
	"github.com/spf13/cobra"
)

var (
	syncTo          []string
	syncProtocol    string
	syncConcurrency int
	syncTimeout     time.Duration
	syncOutput      string
	syncWalk        = mirror.Walk
	syncRun         = mirror.Run
	syncToken       = engine.ForgeToken
	// asTarget is a seam: every registered forge is a Target today, so the
	// refusal below is unreachable without one, and an unreachable error
	// path is an untested error path.
	asTarget = forge.AsTarget
)

// syncCmd mirrors the organised tree out to other forges.
//
// It replaces the standalone corral-sync tool. That tool knew two
// destinations; this knows every forge corralctl can clone from, because
// the forge package now has a receiving side, and it reuses the same
// tokens, the same walk and the same non-interactive git as the rest of
// corralctl.
var syncCmd = &cobra.Command{
	Use:   "sync [base_dir]",
	Short: "Mirror the organised local tree to other forges",
	Long: `Pushes every repository under base_dir to each destination named with --to,
creating the destination repository when it does not exist and bringing its
branches and tags to parity with the local clone: everything local exists
there, and nothing else does. Branches are never forced.

A repository is never pushed to the forge its origin lives on, and a
destination that already holds a same-named repository with the wrong
visibility is refused rather than reused.

Destinations are named as <forge>[:<owner>][@<url>]:

  --to gitlab                          gitlab.com, under the token's own account
  --to gitlab:my-group                 gitlab.com, under a group
  --to codeberg --to bitbucket:work    two destinations
  --to gitea@https://git.example.com   a self-hosted instance

Credentials come from the environment: GITHUB_TOKEN or the gh CLI for
GitHub, GITLAB_TOKEN, BITBUCKET_TOKEN, and GITEA_TOKEN, FORGEJO_TOKEN or
CODEBERG_TOKEN for the Gitea family (each also accepts a CORRAL_-prefixed
name). Over HTTPS the same token authenticates the push; --protocol ssh
uses your keys instead.`,
	Args: cobra.MaximumNArgs(1),
	PreRunE: func(cmd *cobra.Command, _ []string) error {
		syncProtocol = strings.ToLower(strings.TrimSpace(syncProtocol))
		syncOutput = strings.ToLower(strings.TrimSpace(syncOutput))
		if len(syncTo) == 0 {
			return mirror.ErrNoDestinations
		}
		if syncProtocol != "https" && syncProtocol != "ssh" {
			return errors.New("--protocol must be either ssh or https")
		}
		if syncConcurrency < 1 {
			return errors.New("--concurrency must be >= 1")
		}
		if syncTimeout <= 0 {
			return errors.New("--timeout must be > 0")
		}
		if !validOperationalOutput(syncOutput) {
			return errors.New("--output must be text, json, or ndjson")
		}
		if _, err := parseDestinations(syncTo); err != nil {
			return err
		}
		return nil
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmdContext(cmd)
		specs, err := parseDestinations(syncTo)
		if err != nil {
			return err
		}
		dests, err := buildDestinations(ctx, specs)
		if err != nil {
			return err
		}
		root := resolvedBaseDir(args)
		repos, err := syncWalk(root)
		if err != nil {
			return fmt.Errorf("scanning %s: %w", root, err)
		}
		if len(repos) == 0 {
			diag.Warnf("corralctl sync: no repositories under %s", root)
			return nil
		}
		names := make([]string, 0, len(dests))
		for _, d := range dests {
			names = append(names, d.Name+":"+d.Owner)
		}
		diag.Infof("corralctl sync: %d repositories under %s to %s", len(repos), root, strings.Join(names, ", "))

		printer := newSyncPrinter(syncOutput)
		summary := syncRun(ctx, mirror.Options{
			Repos:        repos,
			Destinations: dests,
			Concurrency:  syncConcurrency,
			Timeout:      syncTimeout,
			DryRun:       dryRun,
			Report:       printer.report,
		})
		if err := printer.finish(summary); err != nil {
			return err
		}
		diag.Infof("corralctl sync: mirrored %d, skipped %d, errors %d", summary.Mirrored, summary.Skipped, summary.Errors)
		if summary.Errors > 0 {
			return fmt.Errorf("%d mirror operation(s) failed; see the ERROR lines above", summary.Errors)
		}
		return nil
	},
}

// destinationSpec is one parsed --to value.
type destinationSpec struct {
	forge forge.Forge
	// name is the forge's registered name, which is also the remote name.
	name  string
	owner string
	// baseURL is the instance, when one was named.
	baseURL string
	// host is what the origin guard compares against.
	host string
}

// parseDestinations reads every --to value, refusing a forge that does not
// exist, one that cannot receive a mirror, a self-hosted family member
// with no instance, and the same forge twice — two destinations would
// share one remote name and the second push would prune the first.
func parseDestinations(values []string) ([]destinationSpec, error) {
	seen := map[string]bool{}
	out := make([]destinationSpec, 0, len(values))
	for _, raw := range values {
		spec, err := parseDestination(raw)
		if err != nil {
			return nil, err
		}
		if seen[spec.name] {
			return nil, fmt.Errorf("--to %s is given twice; a forge can be a destination once per run", spec.name)
		}
		seen[spec.name] = true
		out = append(out, spec)
	}
	return out, nil
}

// parseDestination parses <forge>[:<owner>][@<url>].
func parseDestination(raw string) (destinationSpec, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return destinationSpec{}, errors.New("--to must name a forge")
	}
	var baseURL string
	if at := strings.IndexByte(value, '@'); at >= 0 {
		value, baseURL = value[:at], strings.TrimSpace(value[at+1:])
	}
	name, owner, _ := strings.Cut(value, ":")
	name = strings.ToLower(strings.TrimSpace(name))
	owner = strings.TrimSpace(owner)

	f, err := forge.Get(name)
	if err != nil {
		return destinationSpec{}, fmt.Errorf("--to %q: %w", raw, err)
	}
	if _, ok := asTarget(f); !ok {
		return destinationSpec{}, fmt.Errorf("--to %q: %s can be listed from but not mirrored to", raw, f.Name())
	}
	var host string
	if baseURL != "" {
		u, err := url.Parse(baseURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return destinationSpec{}, fmt.Errorf("--to %q: the instance must be an http(s) URL with a host", raw)
		}
		host = strings.ToLower(u.Hostname())
	} else if hosts := f.Hosts(); len(hosts) > 0 {
		host = hosts[0]
	} else {
		return destinationSpec{}, fmt.Errorf("--to %q: %s has no single public instance; name yours as %s@https://your.instance", raw, f.Name(), f.Name())
	}
	return destinationSpec{forge: f, name: f.Name(), owner: owner, baseURL: baseURL, host: host}, nil
}

// buildDestinations resolves a credential for each spec. A destination
// with no token is an error before anything runs: creating a repository
// is never anonymous, and the run should not discover that on the first
// repository after scanning a thousand.
func buildDestinations(ctx context.Context, specs []destinationSpec) ([]mirror.Destination, error) {
	out := make([]mirror.Destination, 0, len(specs))
	for _, s := range specs {
		token := syncToken(ctx, s.name, github.AuthMode(authMode))
		if token == "" {
			return nil, fmt.Errorf("no credential for %s: set %s", s.name, tokenHint(s.name))
		}
		target, _ := asTarget(s.forge)
		out = append(out, mirror.Destination{
			Name:     s.name,
			Host:     s.host,
			Owner:    s.owner,
			Target:   target,
			Options:  forge.TargetOptions{Token: token, BaseURL: s.baseURL, RequestTimeout: apiRequestTimeout},
			Protocol: syncProtocol,
		})
	}
	return out, nil
}

// tokenHint names the environment variables a forge's credential is read
// from, in the order they are consulted.
func tokenHint(name string) string {
	switch name {
	case "github":
		return "GITHUB_TOKEN or GH_TOKEN, or sign in with `gh auth login`"
	case "gitlab":
		return "CORRAL_GITLAB_TOKEN or GITLAB_TOKEN"
	case "bitbucket":
		return "CORRAL_BITBUCKET_TOKEN or BITBUCKET_TOKEN"
	default:
		return "CORRAL_FORGE_TOKEN, FORGEJO_TOKEN, GITEA_TOKEN or CODEBERG_TOKEN"
	}
}

// syncPrinter renders results in the selected output format. Results
// arrive from worker goroutines, so writes are serialised.
type syncPrinter struct {
	format  string
	mu      sync.Mutex
	results []mirror.Result
}

func newSyncPrinter(format string) *syncPrinter {
	return &syncPrinter{format: format}
}

func (p *syncPrinter) report(r mirror.Result) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch p.format {
	case string(engine.OutputNDJSON):
		_ = writeNDJSON(os.Stdout, r)
	case string(engine.OutputJSON):
		p.results = append(p.results, r)
	default:
		repo := r.Repo
		if repo == "" {
			repo = "-"
		}
		fmt.Printf("%-8s %-10s %-32s %s\n", r.Action, r.Destination, repo, r.Message)
	}
}

// finish writes the JSON document, which needs every result and the
// summary, and is a no-op for the streaming formats.
func (p *syncPrinter) finish(summary mirror.Summary) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.format != string(engine.OutputJSON) {
		return nil
	}
	if p.results == nil {
		p.results = []mirror.Result{}
	}
	return writeJSON(os.Stdout, struct {
		Results []mirror.Result `json:"results"`
		Summary mirror.Summary  `json:"summary"`
	}{p.results, summary})
}

// writeNDJSON writes one value as a single JSON line.
func writeNDJSON(target *os.File, value any) error {
	encoder := json.NewEncoder(target)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func init() {
	syncCmd.Flags().StringArrayVar(&syncTo, "to", nil, "destination forge as <forge>[:<owner>][@<url>]; repeatable")
	syncCmd.Flags().StringVarP(&syncProtocol, "protocol", "p", "https", "push transport: https (the token authenticates) or ssh (your keys do)")
	syncCmd.Flags().IntVarP(&syncConcurrency, "concurrency", "c", defaultConcurrency(), "repositories mirrored at once")
	syncCmd.Flags().DurationVar(&syncTimeout, "timeout", 5*time.Minute, "deadline for one repository on one destination")
	syncCmd.Flags().StringVar(&syncOutput, "output", string(engine.OutputText), "output format: text, json, ndjson")
	syncCmd.Flags().StringVar(&authMode, "auth", string(github.AuthModeAuto), "GitHub authentication mode: auto, token, gh")
	syncCmd.Flags().DurationVar(&apiRequestTimeout, "api-request-timeout", 30*time.Second, "deadline for a single forge API request")
	rootCmd.AddCommand(syncCmd)
}
