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
	"time"

	gh "github.com/google/go-github/v90/github"
	"github.com/sebastienrousseau/corralctl/internal/deps"
	"github.com/sebastienrousseau/corralctl/internal/github"
	"github.com/spf13/cobra"
)

// breakerLimit is how many failures in a row stop `deps apply`: three in a
// row across repositories points at something shared (the token, the API,
// a common workflow), and retrying the rest would only repeat it.
const breakerLimit = 3

// ciPoll is how often a canary's release pull request checks are read.
const ciPoll = 30 * time.Second

var (
	depsLive      bool
	depsSignoff   string
	depsCITimeout time.Duration

	// Seams, so a test can stand in for GitHub and the clock.
	depsReadState    = github.ReadReleaseState
	depsBranchExists = github.BranchExists
	depsReleasePRs   = github.ReleasePullRequests
	depsCreateBranch = github.CreateBranch
	depsMerge        = github.MergeBranch
	depsCreatePR     = github.CreateDraftPullRequest
	depsClosePR      = github.ClosePullRequest
	depsPRCI         = github.PullRequestCI
	depsSleep        = time.Sleep
	depsLabeled      = github.LabeledPullRequests
	depsPRCommits    = github.PullRequestCommits
	depsAddLabel     = github.AddLabel
	depsMergeRelease = github.MergeReleasePullRequest
	depsFileAt       = github.FileAt
)

// signoffPattern is a DCO identity: "Name <email>".
var signoffPattern = regexp.MustCompile(`^[^<>]+ <[^<>@\s]+@[^<>\s]+>$`)

var depsApplyCmd = &cobra.Command{
	Use:   "apply <owner>",
	Short: "Fold tier 1 updates into each feat/v<next> and close what is done (dry run unless --live)",
	Long: "Plan as `deps plan` does, then, per repository: create feat/v<next> from the default branch if " +
		"needed, merge each tier 1 pull request's head into it, open the release pull request (as a draft) " +
		"if none is open, and close each merged or tier 4 pull request with a comment saying why. A bump " +
		"shared by several repositories is folded into one canary repository first; the others follow only " +
		"once the canary's release pull request checks pass. Tier 2 and 3 pull requests are left for a " +
		"person. Without --live it prints what it would do and changes nothing.",
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
		a := newApplier(ctx, &lines{w: cmd.OutOrStdout()}, client)
		if err := a.settle(args[0]); err != nil {
			return err
		}
		return a.runAll(folds, plan.Fleets, skipped)
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
// needs.
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

// releaseState reads one repository's branch, tag and release pull requests.
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
	prs, err := depsReleasePRs(ctx, client, owner, name, branch, rs.DefaultBranch)
	if err != nil {
		return deps.RepoState{}, err
	}
	s.ReleasePR, s.OtherReleasePR, s.OtherReleaseBranch, s.MergedReleasePR = prs.Open, prs.Other, prs.OtherBranch, prs.Merged
	if prs.Merged != 0 {
		// Was the merge a prepared release waiting for its tag, or an
		// iteration merged before its version and changelog were written?
		changelog, _, err := depsFileAt(ctx, client, owner, name, "CHANGELOG.md", prs.MergedSHA)
		if err != nil {
			return deps.RepoState{}, err
		}
		s.MergedPrepared = deps.ChangelogHasVersion(changelog, strings.TrimPrefix(branch, "feat/"))
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

// applier runs folds. It counts failures in a row for the breaker and
// remembers, per repository, the release pull request a fold used and
// whether its release branch now exists, so a sibling folded later in the
// run reuses them.
type applier struct {
	ctx      context.Context
	w        *lines
	client   *gh.Client
	failures int
	release  map[string]int
	branched map[string]bool
	failed   map[string]bool
}

// newApplier starts a run.
func newApplier(ctx context.Context, w *lines, client *gh.Client) *applier {
	return &applier{ctx: ctx, w: w, client: client,
		release: map[string]int{}, branched: map[string]bool{}, failed: map[string]bool{}}
}

// runAll folds everything that does not wait on a canary, then each
// fleet's siblings once its canary has passed, and stops after
// breakerLimit failures in a row.
func (a *applier) runAll(folds []deps.RepoFold, fleets []deps.Fleet, skipped []string) error {
	for _, s := range skipped {
		a.w.printf("skip  %s\n", s)
	}
	first, gates := deps.Stage(folds, fleets)
	for _, f := range first {
		if err := a.run(f); err != nil {
			return err
		}
	}
	for _, g := range gates {
		if err := a.gate(g); err != nil {
			return err
		}
	}
	if !depsLive {
		a.w.printf("dry run: nothing was changed; pass --live to apply\n")
	}
	return a.w.err
}

// run applies one fold with what earlier folds in the run left behind, and
// counts it for the breaker.
func (a *applier) run(f deps.RepoFold) error {
	if a.branched[f.Repo] {
		f.CreateBranch = false
	}
	if n, done := a.release[f.Repo]; done {
		f.OpenReleasePR, f.ReleasePR = false, n
	}
	release, err := applyFold(a.ctx, a.w, a.client, f)
	if err != nil {
		return a.fail(f.Repo, err)
	}
	a.failures = 0
	if len(f.Merge) > 0 {
		a.branched[f.Repo], a.release[f.Repo] = true, release
	}
	return nil
}

// gate folds a fleet's siblings once its canary has passed, or says why
// they are held.
func (a *applier) gate(g deps.Gate) error {
	a.w.printf("fleet %s to %s: canary %s, then %d sibling(s)\n", g.Fleet.Package, g.Fleet.To, g.Canary, len(g.Siblings))
	if why := a.canaryVerdict(g.Canary); why != "" {
		for _, s := range g.Siblings {
			a.w.printf("hold  %s: waits on canary %s (%s)\n", s.Repo, g.Canary, why)
		}
		return nil
	}
	for _, s := range g.Siblings {
		if err := a.run(s); err != nil {
			return err
		}
	}
	return nil
}

// canaryVerdict is "" when the canary's release pull request checks pass,
// and otherwise why its siblings must wait. A dry run cannot wait, so it
// says it would and lists the siblings' folds.
func (a *applier) canaryVerdict(canary string) string {
	if !depsLive {
		a.w.printf("plan  wait for %s's release pull request checks to pass\n", canary)
		return ""
	}
	if a.failed[canary] {
		return "the canary's own fold failed"
	}
	n := a.release[canary]
	if n == 0 {
		return "the canary has no release pull request"
	}
	owner, name, _ := strings.Cut(canary, "/")
	return a.waitForChecks(owner, name, n)
}

// waitForChecks reads owner/name#n's checks every ciPoll until they pass
// (""), fail, or are still unfinished at depsCITimeout.
func (a *applier) waitForChecks(owner, name string, n int) string {
	for waited := time.Duration(0); ; waited += ciPoll {
		ci, err := depsPRCI(a.ctx, a.client, owner, name, n)
		switch {
		case err != nil:
			return "its checks could not be read: " + err.Error()
		case ci == "success":
			return ""
		case ci == "failure":
			return fmt.Sprintf("checks on #%d failed", n)
		case waited >= depsCITimeout:
			return fmt.Sprintf("checks on #%d were %s after %s", n, ci, depsCITimeout)
		}
		depsSleep(ciPoll)
	}
}

// settle merges the release pull requests earlier runs opened that are
// ready: hygiene and the release preparation only (deps.MergeReady), with
// passing checks. Any other is left for a person, with the reason.
func (a *applier) settle(owner string) error {
	prs, err := depsLabeled(a.ctx, a.client, owner, deps.ReleaseLabel)
	if err != nil {
		return err
	}
	for _, pr := range prs {
		if err := a.settleOne(pr); err != nil {
			return err
		}
	}
	return nil
}

// settleOne merges one labelled release pull request if it is ready.
func (a *applier) settleOne(pr github.LabeledPullRequest) error {
	owner, name, _ := strings.Cut(pr.Repo, "/")
	why, err := a.mergeBlocker(owner, name, pr.Number)
	if err != nil {
		return a.fail(pr.Repo, err)
	}
	if why != "" {
		a.w.printf("wait  %s#%d: %s\n", pr.Repo, pr.Number, why)
		return nil
	}
	a.w.printf("%s  %s#%d: merge the release pull request (hygiene and release preparation only, checks green)\n", verb(), pr.Repo, pr.Number)
	if !depsLive {
		return nil
	}
	msg := fmt.Sprintf("%s (#%d)\n\nSigned-off-by: %s", pr.Title, pr.Number, depsSignoff)
	if err := depsMergeRelease(a.ctx, a.client, owner, name, pr.Number, msg); err != nil {
		return a.fail(pr.Repo, err)
	}
	a.failures = 0
	return nil
}

// mergeBlocker is "" when owner/name#n may be merged, and otherwise why not.
func (a *applier) mergeBlocker(owner, name string, n int) (string, error) {
	commits, err := depsPRCommits(a.ctx, a.client, owner, name, n)
	if err != nil {
		return "", err
	}
	cs := make([]deps.Commit, 0, len(commits))
	for _, c := range commits {
		cs = append(cs, deps.Commit{SHA: c.SHA, Subject: c.Subject, Author: c.Author, Parents: c.Parents})
	}
	if ok, why := deps.MergeReady(cs); !ok {
		return why, nil
	}
	ci, err := depsPRCI(a.ctx, a.client, owner, name, n)
	if err != nil {
		return "", err
	}
	if ci != "success" {
		return "checks are " + ci, nil
	}
	return "", nil
}

// fail reports one failure and counts it for the breaker.
func (a *applier) fail(repo string, err error) error {
	a.failed[repo] = true
	a.failures++
	a.w.printf("FAIL  %s: %v\n", repo, err)
	if a.failures >= breakerLimit {
		return fmt.Errorf("stopped after %d failures in a row; the last: %w", a.failures, err)
	}
	return nil
}

// applyFold carries out one repository's fold, in the order the release
// branch rule needs: branch, merges, release pull request, closes. It
// returns the release pull request (0 in a dry run that would open one).
func applyFold(ctx context.Context, w *lines, client *gh.Client, f deps.RepoFold) (int, error) {
	owner, name, _ := strings.Cut(f.Repo, "/")
	release := f.ReleasePR
	if len(f.Merge) > 0 {
		if err := ensureReleaseBranch(ctx, w, client, owner, name, f); err != nil {
			return 0, err
		}
		var err error
		if release, err = mergeAndOpen(ctx, w, client, owner, name, f); err != nil {
			return 0, err
		}
		for _, d := range f.Merge {
			comment := fmt.Sprintf("Folded into `%s`; it ships with the release pull request #%d (~/Code/AGENTS.md section 3: updates reach %s only through feat/v<next>).", f.Branch, release, f.Base)
			if err := closePR(ctx, w, client, owner, name, d.PR.Number, comment); err != nil {
				return 0, err
			}
		}
	}
	for _, d := range f.Close {
		if err := closePR(ctx, w, client, owner, name, d.PR.Number, "Closed by corralctl deps: "+d.Reason+"."); err != nil {
			return 0, err
		}
	}
	return release, nil
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
	w.printf("%s  %s: open the release pull request %s -> %s (draft)\n", verb(), f.Repo, f.Branch, f.Base)
	if !depsLive {
		return 0, nil
	}
	title := "chore(release): " + strings.TrimPrefix(f.Branch, "feat/")
	n, err := depsCreatePR(ctx, client, owner, name, f.Branch, f.Base, title, releaseBody(f))
	if err != nil {
		return 0, err
	}
	// The label is how a later run finds the release pull requests it may merge.
	return n, depsAddLabel(ctx, client, owner, name, n, deps.ReleaseLabel)
}

// releaseBody is the release pull request's opening description: what was
// folded. The release preparation (version, changelog) is a person's.
func releaseBody(f deps.RepoFold) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This PR is the %s iteration; corralctl deps folded these Dependabot updates into it.\n\n", strings.TrimPrefix(f.Branch, "feat/"))
	b.WriteString("Prepare the version and changelog on this branch, then mark it ready for review.\n\n## What's Changed\n\n")
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
	depsApplyCmd.Flags().DurationVar(&depsCITimeout, "ci-timeout", 15*time.Minute, "how long to wait for a canary's release pull request checks")
	depsCmd.AddCommand(depsApplyCmd)
}
