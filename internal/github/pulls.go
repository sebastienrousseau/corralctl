// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package github

import (
	"context"
	"fmt"

	gh "github.com/google/go-github/v90/github"
)

// DependabotLogin is the account Dependabot opens pull requests as.
const DependabotLogin = "dependabot[bot]"

// PullRequest is an open pull request, with what triaging it needs: its
// head branch, GitHub's mergeable state and the state of its checks.
type PullRequest struct {
	// Repo is "owner/name".
	Repo string
	// Number is the pull request number.
	Number int
	// Title is the pull request title.
	Title string
	// URL is the pull request's web address.
	URL string
	// Branch is the head branch.
	Branch string
	// HeadSHA is the head commit.
	HeadSHA string
	// Mergeable is GitHub's mergeable_state: "clean", "behind", "dirty",
	// "blocked", "unstable" or "unknown" (not computed yet).
	Mergeable string
	// CI is "success", "failure", "pending" or "none", combining check
	// runs and commit statuses on the head commit.
	CI string
}

// DependabotPullRequests lists the open pull requests Dependabot opened in
// owner/name, every page of them, with each one's mergeable state and CI.
func DependabotPullRequests(ctx context.Context, client *gh.Client, owner, name string) ([]PullRequest, error) {
	var out []PullRequest
	opts := &gh.PullRequestListOptions{State: "open", ListOptions: gh.ListOptions{PerPage: 100}}
	for {
		page, resp, err := client.PullRequests.List(ctx, owner, name, opts)
		if err != nil {
			return nil, fmt.Errorf("list pull requests in %s/%s: %w", owner, name, err)
		}
		for _, pr := range page {
			if pr.GetUser().GetLogin() != DependabotLogin {
				continue
			}
			full, err := describe(ctx, client, owner, name, pr.GetNumber())
			if err != nil {
				return nil, err
			}
			out = append(out, full)
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// describe reads one pull request in full: the list endpoint leaves
// mergeable_state out, and the checks live on the head commit.
func describe(ctx context.Context, client *gh.Client, owner, name string, number int) (PullRequest, error) {
	pr, _, err := client.PullRequests.Get(ctx, owner, name, number)
	if err != nil {
		return PullRequest{}, fmt.Errorf("read %s/%s#%d: %w", owner, name, number, err)
	}
	sha := pr.GetHead().GetSHA()
	ci, err := checkState(ctx, client, owner, name, sha)
	if err != nil {
		return PullRequest{}, fmt.Errorf("read checks for %s/%s#%d: %w", owner, name, number, err)
	}
	return PullRequest{
		Repo: owner + "/" + name, Number: number, Title: pr.GetTitle(), URL: pr.GetHTMLURL(),
		Branch: pr.GetHead().GetRef(), HeadSHA: sha, Mergeable: pr.GetMergeableState(), CI: ci,
	}, nil
}

// checkState combines the check runs and commit statuses on a commit into
// one verdict: any failure wins, then anything unfinished, then success; a
// commit with neither has no checks at all.
func checkState(ctx context.Context, client *gh.Client, owner, name, sha string) (string, error) {
	runs, _, err := client.Checks.ListCheckRunsForRef(ctx, owner, name, sha,
		&gh.ListCheckRunsOptions{ListOptions: gh.ListOptions{PerPage: 100}})
	if err != nil {
		return "", err
	}
	status, _, err := client.Repositories.GetCombinedStatus(ctx, owner, name, sha, nil)
	if err != nil {
		return "", err
	}
	v := verdict{}
	for _, run := range runs.CheckRuns {
		v.addRun(run.GetStatus(), run.GetConclusion())
	}
	for _, s := range status.Statuses {
		v.addStatus(s.GetState())
	}
	return v.String(), nil
}

// verdict accumulates check results.
type verdict struct{ seen, failed, pending bool }

// addRun records a check run: "completed" with a conclusion, or still
// queued or in progress.
func (v *verdict) addRun(status, conclusion string) {
	v.seen = true
	switch {
	case status != "completed":
		v.pending = true
	case conclusion == "failure", conclusion == "cancelled", conclusion == "timed_out",
		conclusion == "action_required", conclusion == "startup_failure":
		v.failed = true
	}
}

// addStatus records a commit status: "success", "pending", "failure" or
// "error".
func (v *verdict) addStatus(state string) {
	v.seen = true
	switch state {
	case "pending":
		v.pending = true
	case "failure", "error":
		v.failed = true
	}
}

// String is the combined verdict.
func (v verdict) String() string {
	switch {
	case v.failed:
		return "failure"
	case v.pending:
		return "pending"
	case v.seen:
		return "success"
	default:
		return "none"
	}
}
