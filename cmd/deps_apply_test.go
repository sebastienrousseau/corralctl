// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	gh "github.com/google/go-github/v90/github"
	"github.com/sebastienrousseau/corralctl/internal/github"
)

// applyWorld is the GitHub the apply tests stand up: repositories, their
// open Dependabot pull requests and release state, and a log of writes.
type applyWorld struct {
	t       *testing.T
	repos   []string
	prs     map[string][]github.PullRequest
	states  map[string]github.ReleaseState
	exists  map[string]bool // repo -> feat/v<next> exists
	release map[string]int  // repo -> open release pull request
	fail    map[string]error
	extra   map[string]github.ReleasePRs // repo -> other or merged release pull requests
	labeled []github.LabeledPullRequest  // release pull requests earlier runs opened
	commits map[string][]github.Commit   // "repo#n" -> its commits
	ci      map[string][]string          // repo -> successive check verdicts on its release pull request
	sleeps  int
	writes  []string
}

// stubApply installs w behind every deps seam and restores them after.
func stubApply(t *testing.T, w *applyWorld) {
	t.Helper()
	stubDeps(t)
	readState, branchExists, releasePRs := depsReadState, depsBranchExists, depsReleasePRs
	createBranch, merge, createPR, closePR, prCI, sleep := depsCreateBranch, depsMerge, depsCreatePR, depsClosePR, depsPRCI, depsSleep
	labeled, prCommits, addLabel, mergeRelease := depsLabeled, depsPRCommits, depsAddLabel, depsMergeRelease
	live, signoff, timeout := depsLive, depsSignoff, depsCITimeout
	t.Cleanup(func() {
		depsReadState, depsBranchExists, depsReleasePRs = readState, branchExists, releasePRs
		depsCreateBranch, depsMerge, depsCreatePR, depsClosePR, depsPRCI, depsSleep = createBranch, merge, createPR, closePR, prCI, sleep
		depsLabeled, depsPRCommits, depsAddLabel, depsMergeRelease = labeled, prCommits, addLabel, mergeRelease
		depsLive, depsSignoff, depsCITimeout = live, signoff, timeout
	})
	depsCITimeout = 2 * ciPoll
	depsFetchRepos = func(_ context.Context, owner string, _ github.FetchOptions) ([]github.Repo, error) {
		var out []github.Repo
		for _, r := range w.repos {
			out = append(out, github.Repo{Owner: owner, Name: r})
		}
		return out, nil
	}
	depsListPRs = func(_ context.Context, _ *gh.Client, owner, name string) ([]github.PullRequest, error) {
		return w.prs[owner+"/"+name], nil
	}
	depsReadState = func(_ context.Context, _ *gh.Client, owner, name string) (github.ReleaseState, error) {
		return w.states[owner+"/"+name], w.fail["state "+owner+"/"+name]
	}
	depsBranchExists = func(_ context.Context, _ *gh.Client, owner, name, branch string) (bool, error) {
		return w.exists[owner+"/"+name], w.fail["exists "+owner+"/"+name]
	}
	depsReleasePRs = func(_ context.Context, _ *gh.Client, owner, name, branch, base string) (github.ReleasePRs, error) {
		r := w.extra[owner+"/"+name]
		r.Open = w.release[owner+"/"+name]
		return r, w.fail["openpr "+owner+"/"+name]
	}
	depsPRCI = func(_ context.Context, _ *gh.Client, owner, name string, n int) (string, error) {
		repo := owner + "/" + name
		if err := w.fail["ci "+repo]; err != nil {
			return "", err
		}
		seq := w.ci[repo]
		if len(seq) == 0 {
			return "success", nil
		}
		w.ci[repo] = seq[1:]
		return seq[0], nil
	}
	depsSleep = func(time.Duration) { w.sleeps++ }
	depsLabeled = func(_ context.Context, _ *gh.Client, owner, label string) ([]github.LabeledPullRequest, error) {
		if label != "corral-deps" {
			t.Errorf("searched label %q", label)
		}
		return w.labeled, w.fail["search"]
	}
	depsPRCommits = func(_ context.Context, _ *gh.Client, owner, name string, n int) ([]github.Commit, error) {
		key := fmt.Sprintf("%s/%s#%d", owner, name, n)
		return w.commits[key], w.fail["commits "+key]
	}
	depsAddLabel = func(_ context.Context, _ *gh.Client, owner, name string, n int, label string) error {
		return w.write(fmt.Sprintf("label %s/%s#%d %s", owner, name, n, label), "label "+owner+"/"+name)
	}
	depsMergeRelease = func(_ context.Context, _ *gh.Client, owner, name string, n int, msg string) error {
		return w.write(fmt.Sprintf("release %s/%s#%d %q", owner, name, n, msg), "release "+owner+"/"+name)
	}
	depsCreateBranch = func(_ context.Context, _ *gh.Client, owner, name, branch, from string) error {
		return w.write(fmt.Sprintf("branch %s/%s %s<-%s", owner, name, branch, from), "branch "+owner+"/"+name)
	}
	depsMerge = func(_ context.Context, _ *gh.Client, owner, name, base, head, msg string) (string, error) {
		return "m", w.write(fmt.Sprintf("merge %s/%s %s<-%s %q", owner, name, base, head, msg), "merge "+owner+"/"+name)
	}
	depsCreatePR = func(_ context.Context, _ *gh.Client, owner, name, head, base, title, body string) (int, error) {
		n := 202
		if w.fail["nopr "+owner+"/"+name] != nil {
			n = 0
		}
		return n, w.write(fmt.Sprintf("pr %s/%s %s->%s %q", owner, name, head, base, title), "pr "+owner+"/"+name)
	}
	depsClosePR = func(_ context.Context, _ *gh.Client, owner, name string, n int, comment string) error {
		return w.write(fmt.Sprintf("close %s/%s#%d %q", owner, name, n, comment), "close "+owner+"/"+name)
	}
}

// write logs a write, or fails it when the world says so.
func (w *applyWorld) write(entry, key string) error {
	if err := w.fail[key]; err != nil {
		return err
	}
	w.writes = append(w.writes, entry)
	return nil
}

// dependabotPR is an open, green, clean Dependabot pull request.
func dependabotPR(repo string, n int, title string) github.PullRequest {
	return github.PullRequest{Repo: repo, Number: n, Title: title, URL: fmt.Sprintf("https://github.com/%s/pull/%d", repo, n),
		Branch: fmt.Sprintf("dependabot/npm_and_yarn/x-%d", n), CI: "success", Mergeable: "clean"}
}

// world is two repositories: o/a with nothing set up for v0.0.58, and o/b
// whose feat/v0.0.10 and release pull request #12 already exist, plus a
// superseded pull request to close.
func world(t *testing.T) *applyWorld {
	return &applyWorld{
		t:     t,
		repos: []string{"a", "b"},
		prs: map[string][]github.PullRequest{
			"o/a": {dependabotPR("o/a", 1, "bump x from 1.0.0 to 1.0.1")},
			"o/b": {dependabotPR("o/b", 5, "bump z from 1.0.0 to 1.0.1"), dependabotPR("o/b", 6, "bump z from 1.0.0 to 1.0.2")},
		},
		states: map[string]github.ReleaseState{
			"o/a": {DefaultBranch: "main", LatestTag: "v0.0.57"},
			"o/b": {DefaultBranch: "master", LatestTag: "v0.0.9"},
		},
		exists:  map[string]bool{"o/b": true},
		release: map[string]int{"o/b": 12},
		fail:    map[string]error{},
		extra:   map[string]github.ReleasePRs{},
		ci:      map[string][]string{},
		commits: map[string][]github.Commit{},
	}
}

func TestDepsApplyDryRunChangesNothing(t *testing.T) {
	w := world(t)
	stubApply(t, w)
	out, err := runDeps(t, "apply", "o")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"plan  o/a: create feat/v0.0.58 from main",
		"plan  o/a: merge #1 (dependabot/npm_and_yarn/x-1) into feat/v0.0.58",
		"plan  o/a: open the release pull request feat/v0.0.58 -> main (draft)",
		"plan  o/a: close #1",
		"plan  o/b: merge #6", "plan  o/b: close #6", "plan  o/b: close #5",
		"dry run: nothing was changed; pass --live to apply",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "create feat/v0.0.10") || strings.Contains(out, "open the release pull request feat/v0.0.10") {
		t.Errorf("o/b's branch and release pull request exist and must be reused:\n%s", out)
	}
	if len(w.writes) != 0 {
		t.Errorf("a dry run wrote %v", w.writes)
	}
}

func TestDepsApplyLiveNeedsASignoff(t *testing.T) {
	for _, signoff := range []string{"", "Someone", "<a@b>", "A B <not-an-email>"} {
		stubApply(t, world(t))
		_, err := runDeps(t, "apply", "o", "--live", "--signoff", signoff)
		if err == nil || !strings.Contains(err.Error(), "--live needs --signoff") {
			t.Errorf("signoff %q: err = %v", signoff, err)
		}
	}
}

func TestDepsApplyLiveFoldsInOrder(t *testing.T) {
	w := world(t)
	stubApply(t, w)
	out, err := runDeps(t, "apply", "o", "--live", "--signoff", "Ada Lovelace <ada@example.com>")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"branch o/a feat/v0.0.58<-main",
		`merge o/a feat/v0.0.58<-dependabot/npm_and_yarn/x-1 "Merge #1 into feat/v0.0.58\n\nbump x from 1.0.0 to 1.0.1\n\nSigned-off-by: Ada Lovelace <ada@example.com>"`,
		`pr o/a feat/v0.0.58->main "chore(release): v0.0.58"`,
		"label o/a#202 corral-deps",
		"close o/a#1 \"Folded into `feat/v0.0.58`; it ships with the release pull request #202 (~/Code/AGENTS.md section 3: updates reach main only through feat/v<next>).\"",
		`merge o/b feat/v0.0.10<-dependabot/npm_and_yarn/x-6 "Merge #6 into feat/v0.0.10\n\nbump z from 1.0.0 to 1.0.2\n\nSigned-off-by: Ada Lovelace <ada@example.com>"`,
		"close o/b#6 \"Folded into `feat/v0.0.10`; it ships with the release pull request #12 (~/Code/AGENTS.md section 3: updates reach master only through feat/v<next>).\"",
		`close o/b#5 "Closed by corralctl deps: superseded by #6."`,
	}
	if strings.Join(w.writes, "\n") != strings.Join(want, "\n") {
		t.Errorf("writes:\n%s\nwant:\n%s", strings.Join(w.writes, "\n"), strings.Join(want, "\n"))
	}
	if strings.Contains(out, "dry run") || !strings.Contains(out, "do    o/a: merge #1") {
		t.Errorf("a live run reports what it did:\n%s", out)
	}
}

func TestDepsApplySkipsARepositoryWithoutAReleaseTag(t *testing.T) {
	w := world(t)
	w.states["o/a"] = github.ReleaseState{DefaultBranch: "main", LatestTag: "nightly"}
	stubApply(t, w)
	out, err := runDeps(t, "apply", "o")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `skip  o/a: "nightly" is not a vX.Y.Z release tag`) || strings.Contains(out, "o/a: merge") {
		t.Errorf("o/a should be skipped:\n%s", out)
	}
}

func TestDepsApplyStopsWhenItCannotReadARepository(t *testing.T) {
	boom := errors.New("boom")
	for _, key := range []string{"state o/a", "exists o/a", "openpr o/a"} {
		w := world(t)
		w.fail[key] = boom
		stubApply(t, w)
		if _, err := runDeps(t, "apply", "o"); !errors.Is(err, boom) {
			t.Errorf("%s: err = %v, want boom", key, err)
		}
	}
}

func TestDepsApplyReportsAFailedRepositoryAndGoesOn(t *testing.T) {
	boom := errors.New("boom")
	for _, key := range []string{"branch o/a", "merge o/a", "pr o/a", "close o/a"} {
		w := world(t)
		w.fail[key] = boom
		stubApply(t, w)
		out, err := runDeps(t, "apply", "o", "--live", "--signoff", "A B <a@b.c>")
		if err != nil {
			t.Errorf("%s: one failed repository is not a stopped run: %v", key, err)
		}
		if !strings.Contains(out, "FAIL  o/a: boom") || !strings.Contains(out, "do    o/b: close #5") {
			t.Errorf("%s: o/a should fail and o/b still run:\n%s", key, out)
		}
	}
}

func TestDepsApplyClosesTierFourEvenWhereNothingMerges(t *testing.T) {
	w := world(t)
	w.fail["close o/b"] = errors.New("boom")
	w.prs["o/b"] = []github.PullRequest{dependabotPR("o/b", 5, "bump z from 1.0.0 to 1.0.1"), dependabotPR("o/b", 6, "bump z from 1.0.0 to 2.0.0")}
	stubApply(t, w)
	out, _ := runDeps(t, "apply", "o", "--live", "--signoff", "A B <a@b.c>")
	if !strings.Contains(out, "FAIL  o/b: boom") || strings.Contains(out, "o/b: merge") {
		t.Errorf("o/b has only a tier 4 close, which fails:\n%s", out)
	}
}

func TestDepsApplyStopsAfterThreeFailuresInARow(t *testing.T) {
	w := world(t)
	w.repos = []string{"a", "b", "c", "d"}
	for i, r := range []string{"o/a", "o/b", "o/c", "o/d"} {
		// A different package each: the same bump would be a fleet, held
		// behind its canary instead of tried four times.
		w.prs[r] = []github.PullRequest{dependabotPR(r, 1, fmt.Sprintf("bump p%d from 1.0.0 to 1.0.1", i))}
		w.states[r] = github.ReleaseState{DefaultBranch: "main", LatestTag: "v0.0.1"}
		w.fail["merge "+r] = errors.New("token revoked")
	}
	stubApply(t, w)
	out, err := runDeps(t, "apply", "o", "--live", "--signoff", "A B <a@b.c>")
	if err == nil || !strings.Contains(err.Error(), "stopped after 3 failures in a row") {
		t.Errorf("err = %v", err)
	}
	if strings.Contains(out, "o/d") {
		t.Errorf("the fourth repository should not be tried:\n%s", out)
	}
}

func TestDepsApplyPassesInventoryErrorsOn(t *testing.T) {
	stubApply(t, world(t))
	boom := errors.New("boom")
	depsFetchRepos = func(context.Context, string, github.FetchOptions) ([]github.Repo, error) { return nil, boom }
	if _, err := runDeps(t, "apply", "o"); !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
	stubApply(t, world(t))
	calls := 0
	depsNewClient = func(context.Context, github.FetchOptions) (*gh.Client, error) {
		calls++
		if calls == 2 { // the first is the inventory's; the second is apply's own
			return nil, boom
		}
		return nil, nil
	}
	if _, err := runDeps(t, "apply", "o"); !errors.Is(err, boom) {
		t.Errorf("client: err = %v", err)
	}
	stubApply(t, world(t))
	if _, err := runDeps(t, "apply", "o", "--overrides", "/does/not/exist.json"); err == nil {
		t.Error("an unreadable overrides file is an error")
	}
}

func TestDepsApplyReadsEachRepositoryOnce(t *testing.T) {
	w := world(t)
	w.prs["o/a"] = append(w.prs["o/a"], dependabotPR("o/a", 2, "bump y from 2.0.0 to 2.0.1"))
	stubApply(t, w)
	reads := 0
	read := depsReadState
	depsReadState = func(ctx context.Context, c *gh.Client, owner, name string) (github.ReleaseState, error) {
		reads++
		return read(ctx, c, owner, name)
	}
	out, err := runDeps(t, "apply", "o")
	if err != nil {
		t.Fatal(err)
	}
	if reads != 2 {
		t.Errorf("read release state %d times for 2 repositories", reads)
	}
	if !strings.Contains(out, "plan  o/a: merge #1") || !strings.Contains(out, "plan  o/a: merge #2") {
		t.Errorf("both of o/a's updates are folded:\n%s", out)
	}
}

func TestDepsApplyReportsAClosedPipe(t *testing.T) {
	stubApply(t, world(t))
	err := newApplier(context.Background(), &lines{w: errorWriter{}}, nil).runAll(nil, nil, []string{"o/x: one", "o/y: two"})
	if err == nil {
		t.Error("a failed write should end the run with an error")
	}
}

// fleetWorld is the config-kit case: the same bump open in o/a, o/b and
// o/c. o/a is the canary; o/c also has its own unrelated update.
func fleetWorld(t *testing.T) *applyWorld {
	w := world(t)
	kit := "bump kit from 0.0.9 to 0.0.10"
	w.repos = []string{"a", "b", "c"}
	w.prs = map[string][]github.PullRequest{
		"o/a": {dependabotPR("o/a", 1, kit)},
		"o/b": {dependabotPR("o/b", 2, kit)},
		"o/c": {dependabotPR("o/c", 3, kit), dependabotPR("o/c", 4, "bump other from 1.0.0 to 1.0.1")},
	}
	w.states = map[string]github.ReleaseState{}
	for _, r := range []string{"o/a", "o/b", "o/c"} {
		w.states[r] = github.ReleaseState{DefaultBranch: "main", LatestTag: "v0.0.1"}
	}
	w.exists, w.release = map[string]bool{}, map[string]int{}
	return w
}

func TestDepsApplyDryRunNamesTheCanary(t *testing.T) {
	stubApply(t, fleetWorld(t))
	out, err := runDeps(t, "apply", "o")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"fleet kit to 0.0.10: canary o/a, then 2 sibling(s)",
		"plan  wait for o/a's release pull request checks to pass",
		"plan  o/b: merge #2", "plan  o/c: merge #3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry run lacks %q:\n%s", want, out)
		}
	}
	// o/c's own update runs before the canary's checks; its kit bump after.
	if strings.Index(out, "o/c: merge #4") > strings.Index(out, "wait for o/a") ||
		strings.Index(out, "o/c: merge #3") < strings.Index(out, "wait for o/a") {
		t.Errorf("o/c's free update goes first and its fleet bump after the canary:\n%s", out)
	}
	if strings.Count(out, "o/c: create feat/v0.0.2") != 1 || strings.Count(out, "o/c: open the release") != 1 {
		t.Errorf("o/c's branch and release pull request are made once, then reused:\n%s", out)
	}
}

func TestDepsApplyFoldsTheSiblingsOnceTheCanaryPasses(t *testing.T) {
	w := fleetWorld(t)
	w.ci["o/a"] = []string{"none", "pending", "success"}
	stubApply(t, w)
	out, err := runDeps(t, "apply", "o", "--live", "--signoff", "A B <a@b.c>")
	if err != nil {
		t.Fatal(err)
	}
	if w.sleeps != 2 {
		t.Errorf("polled the canary %d times between reads, want 2", w.sleeps)
	}
	joined := strings.Join(w.writes, "\n")
	for _, want := range []string{"merge o/b feat/v0.0.2<-dependabot/npm_and_yarn/x-2", "merge o/c feat/v0.0.2<-dependabot/npm_and_yarn/x-3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("writes lack %q:\n%s", want, joined)
		}
	}
	if strings.Count(joined, "branch o/c") != 1 || strings.Count(joined, "pr o/c") != 1 {
		t.Errorf("o/c's branch and release pull request are created once:\n%s", joined)
	}
	if strings.Contains(out, "hold") {
		t.Errorf("nothing is held when the canary passes:\n%s", out)
	}
}

func TestDepsApplyHoldsTheSiblingsWhenTheCanaryDoesNotPass(t *testing.T) {
	cases := map[string]func(w *applyWorld){
		"checks on #202 failed":                  func(w *applyWorld) { w.ci["o/a"] = []string{"pending", "failure"} },
		"checks on #202 were pending after 1m0s": func(w *applyWorld) { w.ci["o/a"] = []string{"pending", "pending", "pending", "pending"} },
		"its checks could not be read: boom":     func(w *applyWorld) { w.fail["ci o/a"] = errors.New("boom") },
		"the canary's own fold failed":           func(w *applyWorld) { w.fail["merge o/a"] = errors.New("boom") },
		"the canary has no release pull request": func(w *applyWorld) { w.fail["nopr o/a"] = errors.New("GitHub returned no number") },
	}
	for why, setUp := range cases {
		w := fleetWorld(t)
		setUp(w)
		stubApply(t, w)
		out, err := runDeps(t, "apply", "o", "--live", "--signoff", "A B <a@b.c>")
		if err != nil {
			t.Errorf("%s: a held fleet is not a failed run: %v", why, err)
		}
		for _, want := range []string{"hold  o/b: waits on canary o/a (" + why + ")", "hold  o/c: waits on canary o/a (" + why + ")"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: output lacks %q:\n%s", why, want, out)
			}
		}
		if strings.Contains(strings.Join(w.writes, "\n"), "merge o/b") {
			t.Errorf("%s: a held sibling must not be merged: %v", why, w.writes)
		}
	}
}

func TestDepsApplyReadsOtherAndMergedReleasePullRequests(t *testing.T) {
	w := world(t)
	w.extra["o/a"] = github.ReleasePRs{Other: 7, OtherBranch: "feat/v0.0.60"}
	w.extra["o/b"] = github.ReleasePRs{Merged: 8}
	stubApply(t, w)
	out, err := runDeps(t, "apply", "o")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"skip  o/a: release pull request #7 from feat/v0.0.60 is already open",
		"skip  o/b: feat/v0.0.10 was merged in #8 but v0.0.10 is not tagged",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestDepsApplyBreakerStopsAFailingFleet(t *testing.T) {
	w := fleetWorld(t)
	kit := "bump kit from 0.0.9 to 0.0.10"
	w.repos = append(w.repos, "d")
	w.prs["o/c"] = []github.PullRequest{dependabotPR("o/c", 3, kit)}
	w.prs["o/d"] = []github.PullRequest{dependabotPR("o/d", 5, kit)}
	w.states["o/d"] = github.ReleaseState{DefaultBranch: "main", LatestTag: "v0.0.1"}
	for _, r := range []string{"o/b", "o/c", "o/d"} {
		w.fail["merge "+r] = errors.New("token revoked")
	}
	stubApply(t, w)
	_, err := runDeps(t, "apply", "o", "--live", "--signoff", "A B <a@b.c>")
	if err == nil || !strings.Contains(err.Error(), "stopped after 3 failures in a row") {
		t.Errorf("three failing siblings after a passing canary stop the run, got %v", err)
	}
}

// ready, unprepared and foreign are the commits of three release pull
// requests an earlier run opened.
var (
	prepCommit = github.Commit{SHA: "r1r1r1r1", Subject: "chore(release): v0.0.2", Author: "sebastienrousseau", Parents: 1}
	foldCommit = github.Commit{SHA: "f1f1f1f1", Subject: "Merge #5 into feat/v0.0.2", Author: "web-flow", Parents: 2}
	handCommit = github.Commit{SHA: "h1h1h1h1", Subject: "feat: a person's change", Author: "sebastienrousseau", Parents: 1}
)

// settleWorld has one release pull request ready to merge (o/r#30), one
// not yet prepared (o/s#31), one carrying a person's commit (o/t#32), and
// one whose checks are pending (o/u#33). It has no Dependabot pull
// requests, so only the settle pass acts.
func settleWorld(t *testing.T) *applyWorld {
	w := world(t)
	w.repos, w.prs = nil, map[string][]github.PullRequest{}
	w.labeled = []github.LabeledPullRequest{
		{Repo: "o/r", Number: 30, Title: "chore(release): v0.0.2"},
		{Repo: "o/s", Number: 31, Title: "chore(release): v0.0.2"},
		{Repo: "o/t", Number: 32, Title: "chore(release): v0.0.2"},
		{Repo: "o/u", Number: 33, Title: "chore(release): v0.0.2"},
	}
	w.commits = map[string][]github.Commit{
		"o/r#30": {foldCommit, prepCommit},
		"o/s#31": {foldCommit},
		"o/t#32": {foldCommit, handCommit, prepCommit},
		"o/u#33": {foldCommit, prepCommit},
	}
	w.ci["o/u"] = []string{"pending"}
	return w
}

func TestDepsApplySettleSaysWhatItWouldMerge(t *testing.T) {
	w := settleWorld(t)
	stubApply(t, w)
	out, err := runDeps(t, "apply", "o")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"plan  o/r#30: merge the release pull request (hygiene and release preparation only, checks green)",
		"wait  o/s#31: no release preparation yet",
		"wait  o/t#32: commit h1h1h1h (feat: a person's change) is not a Dependabot change",
		"wait  o/u#33: checks are pending",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if len(w.writes) != 0 {
		t.Errorf("a dry run merged %v", w.writes)
	}
}

func TestDepsApplySettleMergesOnlyTheReadyOne(t *testing.T) {
	w := settleWorld(t)
	stubApply(t, w)
	if _, err := runDeps(t, "apply", "o", "--live", "--signoff", "Ada Lovelace <ada@example.com>"); err != nil {
		t.Fatal(err)
	}
	want := `release o/r#30 "chore(release): v0.0.2 (#30)\n\nSigned-off-by: Ada Lovelace <ada@example.com>"`
	if strings.Join(w.writes, "\n") != want {
		t.Errorf("writes = %v, want only %s", w.writes, want)
	}
}

func TestDepsApplySettleReportsFailures(t *testing.T) {
	w := settleWorld(t)
	w.fail["commits o/s#31"] = errors.New("boom")
	w.fail["release o/r"] = errors.New("not mergeable")
	stubApply(t, w)
	out, err := runDeps(t, "apply", "o", "--live", "--signoff", "A B <a@b.c>")
	if err != nil {
		t.Fatalf("two failures are under the breaker: %v", err)
	}
	for _, want := range []string{"FAIL  o/r: not mergeable", "FAIL  o/s: boom", "wait  o/u#33: checks are pending"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestDepsApplySettleReportsAChecksReadFailure(t *testing.T) {
	w := settleWorld(t)
	w.fail["ci o/u"] = errors.New("ci down")
	stubApply(t, w)
	out, err := runDeps(t, "apply", "o")
	if err != nil || !strings.Contains(out, "FAIL  o/u: ci down") {
		t.Errorf("err = %v:\n%s", err, out)
	}
}

func TestDepsApplySettleBreakerAndSearchErrors(t *testing.T) {
	w := settleWorld(t)
	for _, r := range []string{"o/r", "o/s", "o/t"} {
		w.commits[r+map[string]string{"o/r": "#30", "o/s": "#31", "o/t": "#32"}[r]] = []github.Commit{foldCommit, prepCommit}
		w.fail["release "+r] = errors.New("token revoked")
	}
	stubApply(t, w)
	if _, err := runDeps(t, "apply", "o", "--live", "--signoff", "A B <a@b.c>"); err == nil || !strings.Contains(err.Error(), "stopped after 3 failures in a row") {
		t.Errorf("three failed merges in a row stop the run, got %v", err)
	}
	w = settleWorld(t)
	w.fail["search"] = errors.New("search down")
	stubApply(t, w)
	if _, err := runDeps(t, "apply", "o"); err == nil || err.Error() != "search down" {
		t.Errorf("a failed search stops the run, got %v", err)
	}
}

func TestDepsApplyLabelFailureFailsTheRepository(t *testing.T) {
	w := world(t)
	w.fail["label o/a"] = errors.New("no label")
	stubApply(t, w)
	out, _ := runDeps(t, "apply", "o", "--live", "--signoff", "A B <a@b.c>")
	if !strings.Contains(out, "FAIL  o/a: no label") {
		t.Errorf("a release pull request that cannot be labelled fails its repository:\n%s", out)
	}
}
