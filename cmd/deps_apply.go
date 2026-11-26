// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	gh "github.com/google/go-github/v90/github"
	"github.com/sebastienrousseau/corralctl/internal/deps"
	"github.com/sebastienrousseau/corralctl/internal/github"
	"github.com/spf13/cobra"
)

// breakerLimit is how many failures in a row stop `deps apply`: three in a
// row across repositories points at something shared (the token, the API,
// a common workflow), and retrying the rest would only repeat it.
const breakerLimit = 3

var (
	depsLive    bool
	depsSignoff string

	// Seams, so a test can stand in for GitHub.
	depsReadState    = github.ReadReleaseState
	depsBranchExists = github.BranchExists
	depsOpenPR       = github.OpenPullRequest
	depsCreateBranch = github.CreateBranch
	depsMerge        = github.MergeBranch
	depsCreatePR     = github.CreatePullRequest
	depsClosePR      = github.ClosePullRequest
)

// signoffPattern is a DCO identity: "Name <email>".
var signoffPattern = regexp.MustCompile(`^[^<>]+ <[^<>@\s]+@[^<>\s]+>$`)

var depsApplyCmd = &cobra.Command{
	Use:   "apply <owner>",
	Short: "Fold tier 1 updates into each feat/v<next> and close what is done (dry run unless --live)",
	Long: "Plan as `deps plan` does, then, per repository: create feat/v<next> from the default branch if " +
		"needed, merge each tier 1 pull request's head into it, open the release pull request if none is " +
		"open, and close each merged or tier 4 pull request with a comment saying why. Tier 2 and 3 pull " +
		"requests are left for a person. Without --live it prints what it would do and changes nothing.",
	Args:    cobra.ExactArgs(1),
	PreRunE: validateDepsApply,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmdContext(cmd)
		overrides, err := readOverrides(depsOverrides)
		if err != nil {
			return err
		}
		prs, err := depsInventory(ctx, args[0])
		if err != nil {
			return err
		}
		client, err := depsNewClient(ctx, github.FetchOptions{AuthMode: github.AuthMode(depsAuth)})
		if err != nil {
			return err
		}
		plan := deps.Build(prs, overrides)
		states, err := releaseStates(ctx, client, plan)
		if err != nil {
			return err
		}
		folds, skipped := deps.Folds(plan, states)
		return runFolds(ctx, &lines{w: cmd.OutOrStdout()}, client, folds, skipped)
	},
}

// validateDepsApply refuses a live run without a sign-off to put on the
// merge commits: they carry the maintainer's DCO, never a bot's.
func validateDepsApply(*cobra.Command, []string) error {
	if depsLive && !signoffPattern.MatchString(depsSignoff) {
		return errors.New(`--live needs --signoff "Name <email>": the merge commits carry the maintainer's Signed-off-by`)
	}
	return nil
}

// releaseStates reads, for every repository with tier 1 work, what Folds
// needs. A repository it cannot read is left out, so Folds skips it.
func releaseStates(ctx context.Context, client *gh.Client, p deps.Plan) (map[string]deps.RepoState, error) {
	states := map[string]deps.RepoState{}
	for _, d := range p.Decisions {
		if d.Tier != deps.TierFast {
			continue
		}
		if _, done := states[d.PR.Repo]; done {
			continue
		}
		s, err := releaseState(ctx, client, d.PR.Repo)
		if err != nil {
			return nil, err
		}
		states[d.PR.Repo] = s
	}
	return states, nil
}

// releaseState reads one repository's branch, tag and release pull request.
func releaseState(ctx context.Context, client *gh.Client, repo string) (deps.RepoState, error) {
	owner, name, _ := strings.Cut(repo, "/")
	rs, err := depsReadState(ctx, client, owner, name)
	if err != nil {
		return deps.RepoState{}, err
	}
	s := deps.RepoState{Repo: repo, DefaultBranch: rs.DefaultBranch, LatestTag: rs.LatestTag}
	branch, ok := releaseBranch(rs.LatestTag)
	if !ok {
		return s, nil // Folds names the tag that is not a release; nothing more to read.
	}
	if s.BranchExists, err = depsBranchExists(ctx, client, owner, name, branch); err != nil {
		return deps.RepoState{}, err
	}
	if s.ReleasePR, err = depsOpenPR(ctx, client, owner, name, branch, rs.DefaultBranch); err != nil {
		return deps.RepoState{}, err
	}
	return s, nil
}

// releaseBranch is feat/v<next> for a repository whose newest tag is tag;
// ok is false when tag is not a vX.Y.Z release tag.
func releaseBranch(tag string) (string, bool) {
	next, err := deps.NextVersion(tag)
	return "feat/" + next, err == nil
}

// lines writes progress lines and keeps the first write error, so a
// closed pipe ends the run with an error instead of being ignored.
type lines struct {
	w   io.Writer
	err error
}

// printf writes one formatted line unless an earlier write failed.
func (l *lines) printf(format string, args ...any) {
	if l.err == nil {
		_, l.err = fmt.Fprintf(l.w, format, args...)
	}
}

// runFolds applies (or, without --live, describes) each fold, stopping
// after breakerLimit failures in a row.
func runFolds(ctx context.Context, w *lines, client *gh.Client, folds []deps.RepoFold, skipped []string) error {
	for _, s := range skipped {
		w.printf("skip  %s\n", s)
	}
	failures := 0
	for _, f := range folds {
		if err := applyFold(ctx, w, client, f); err != nil {
			failures++
			w.printf("FAIL  %s: %v\n", f.Repo, err)
			if failures >= breakerLimit {
				return fmt.Errorf("stopped after %d failures in a row; the last: %w", failures, err)
			}
			continue
		}
		failures = 0
	}
	if !depsLive {
		w.printf("dry run: nothing was changed; pass --live to apply\n")
	}
	return w.err
}

// applyFold carries out one repository's fold, in the order the release
// branch rule needs: branch, merges, release pull request, closes.
func applyFold(ctx context.Context, w *lines, client *gh.Client, f deps.RepoFold) error {
	owner, name, _ := strings.Cut(f.Repo, "/")
	if len(f.Merge) > 0 {
		if err := ensureReleaseBranch(ctx, w, client, owner, name, f); err != nil {
			return err
		}
		release, err := mergeAndOpen(ctx, w, client, owner, name, f)
		if err != nil {
			return err
		}
		for _, d := range f.Merge {
			comment := fmt.Sprintf("Folded into `%s`; it ships with the release pull request #%d (~/Code/AGENTS.md section 3: updates reach %s only through feat/v<next>).", f.Branch, release, f.Base)
			if err := closePR(ctx, w, client, owner, name, d.PR.Number, comment); err != nil {
				return err
			}
		}
	}
	for _, d := range f.Close {
		if err := closePR(ctx, w, client, owner, name, d.PR.Number, "Closed by corralctl deps: "+d.Reason+"."); err != nil {
			return err
		}
	}
	return nil
}

// ensureReleaseBranch creates feat/v<next> when it does not exist yet.
func ensureReleaseBranch(ctx context.Context, w *lines, client *gh.Client, owner, name string, f deps.RepoFold) error {
	if !f.CreateBranch {
		return nil
	}
	w.printf("%s  %s: create %s from %s\n", verb(), f.Repo, f.Branch, f.Base)
	if !depsLive {
		return nil
	}
	return depsCreateBranch(ctx, client, owner, name, f.Branch, f.Base)
}

// mergeAndOpen merges each tier 1 head into the release branch, then opens
// the release pull request if there is none, and returns its number (0 in
// a dry run that would open it).
func mergeAndOpen(ctx context.Context, w *lines, client *gh.Client, owner, name string, f deps.RepoFold) (int, error) {
	for _, d := range f.Merge {
		w.printf("%s  %s: merge #%d (%s) into %s\n", verb(), f.Repo, d.PR.Number, d.PR.Branch, f.Branch)
		if !depsLive {
			continue
		}
		msg := fmt.Sprintf("Merge #%d into %s\n\n%s\n\nSigned-off-by: %s", d.PR.Number, f.Branch, d.PR.Title, depsSignoff)
		if _, err := depsMerge(ctx, client, owner, name, f.Branch, d.PR.Branch, msg); err != nil {
			return 0, err
		}
	}
	if !f.OpenReleasePR {
		return f.ReleasePR, nil
	}
	w.printf("%s  %s: open the release pull request %s -> %s\n", verb(), f.Repo, f.Branch, f.Base)
	if !depsLive {
		return 0, nil
	}
	title := "chore(release): " + strings.TrimPrefix(f.Branch, "feat/")
	return depsCreatePR(ctx, client, owner, name, f.Branch, f.Base, title, releaseBody(f))
}

// releaseBody is the release pull request's opening description: what was
// folded. The release preparation (version, changelog) is a person's.
func releaseBody(f deps.RepoFold) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This PR is the %s iteration; corralctl deps folded these Dependabot updates into it.\n\n", strings.TrimPrefix(f.Branch, "feat/"))
	b.WriteString("Prepare the version and changelog on this branch before merging.\n\n## What's Changed\n\n")
	for _, d := range f.Merge {
		fmt.Fprintf(&b, "* %s in %s\n", d.PR.Title, d.PR.URL)
	}
	return b.String()
}

// closePR closes one pull request with a comment.
func closePR(ctx context.Context, w *lines, client *gh.Client, owner, name string, number int, comment string) error {
	w.printf("%s  %s/%s: close #%d\n", verb(), owner, name, number)
	if !depsLive {
		return nil
	}
	return depsClosePR(ctx, client, owner, name, number, comment)
}

// verb labels a line as done or, in a dry run, only planned.
func verb() string {
	if depsLive {
		return "do  "
	}
	return "plan"
}

func init() {
	depsApplyCmd.Flags().StringVar(&depsAuth, "auth", string(github.AuthModeAuto), "authentication mode: auto, token, gh")
	depsApplyCmd.Flags().BoolVar(&depsForks, "include-forks", false, "include forked repositories")
	depsApplyCmd.Flags().BoolVar(&depsArchived, "include-archived", false, "include archived repositories")
	depsApplyCmd.Flags().StringVar(&depsOverrides, "overrides", "", "JSON file of known-bad versions, as for deps plan")
	depsApplyCmd.Flags().BoolVar(&depsLive, "live", false, "make the changes; without it, print them only")
	depsApplyCmd.Flags().StringVar(&depsSignoff, "signoff", "", `the maintainer's "Name <email>" for the merge commits' Signed-off-by (required with --live)`)
	depsCmd.AddCommand(depsApplyCmd)
}
