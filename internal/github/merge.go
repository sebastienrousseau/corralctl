// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package github

import (
	"context"
	"errors"
	"fmt"
	"strings"

	gh "github.com/google/go-github/v90/github"
)

// LabeledPullRequest is an open pull request found by its label.
type LabeledPullRequest struct {
	// Repo is "owner/name".
	Repo string
	// Number is the pull request number.
	Number int
	// Title is the pull request title.
	Title string
}

// LabeledPullRequests finds the open pull requests labelled label across
// owner's repositories, every page of the search.
func LabeledPullRequests(ctx context.Context, client *gh.Client, owner, label string) ([]LabeledPullRequest, error) {
	query := fmt.Sprintf("is:pr is:open label:%s user:%s", label, owner)
	opts := &gh.SearchOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	var out []LabeledPullRequest
	for {
		res, resp, err := client.Search.Issues(ctx, query, opts)
		if err != nil {
			return nil, fmt.Errorf("search %q: %w", query, err)
		}
		for _, is := range res.Issues {
			out = append(out, LabeledPullRequest{Repo: repoOf(is.GetRepositoryURL()), Number: is.GetNumber(), Title: is.GetTitle()})
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// repoOf is "owner/name" from an API repository URL such as
// "https://api.github.com/repos/owner/name".
func repoOf(url string) string {
	parts := strings.Split(strings.TrimRight(url, "/"), "/")
	if len(parts) < 2 {
		return url
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}

// Commit is one commit on a pull request.
type Commit struct {
	// SHA is the commit.
	SHA string
	// Subject is the first line of its message.
	Subject string
	// Author is the GitHub login of its author, or "".
	Author string
	// Parents is how many parents it has.
	Parents int
}

// PullRequestCommits lists the commits on owner/name#number, every page.
func PullRequestCommits(ctx context.Context, client *gh.Client, owner, name string, number int) ([]Commit, error) {
	opts := &gh.ListOptions{PerPage: 100}
	var out []Commit
	for {
		page, resp, err := client.PullRequests.ListCommits(ctx, owner, name, number, opts)
		if err != nil {
			return nil, fmt.Errorf("list commits on %s/%s#%d: %w", owner, name, number, err)
		}
		for _, c := range page {
			subject, _, _ := strings.Cut(c.GetCommit().GetMessage(), "\n")
			out = append(out, Commit{SHA: c.GetSHA(), Subject: subject, Author: c.GetAuthor().GetLogin(), Parents: len(c.Parents)})
		}
		if resp.NextPage == 0 {
			return out, nil
		}
		opts.Page = resp.NextPage
	}
}

// AddLabel labels owner/name#number; GitHub creates the label if the
// repository does not have it yet.
func AddLabel(ctx context.Context, client *gh.Client, owner, name string, number int, label string) error {
	if _, _, err := client.Issues.AddLabelsToIssue(ctx, owner, name, number, []string{label}); err != nil {
		return fmt.Errorf("label %s/%s#%d: %w", owner, name, number, err)
	}
	return nil
}

// MergeReleasePullRequest merges owner/name#number with message: a draft
// is marked ready for review first, and the merge uses the first method
// the repository allows of merge commit, squash and rebase, so the fold
// merges' signatures survive wherever merge commits are allowed.
func MergeReleasePullRequest(ctx context.Context, client *gh.Client, owner, name string, number int, message string) error {
	pr, _, err := client.PullRequests.Get(ctx, owner, name, number)
	if err != nil {
		return fmt.Errorf("read %s/%s#%d: %w", owner, name, number, err)
	}
	if pr.GetDraft() {
		if err := markReady(ctx, client, pr.GetNodeID()); err != nil {
			return fmt.Errorf("mark %s/%s#%d ready: %w", owner, name, number, err)
		}
	}
	repo, _, err := client.Repositories.Get(ctx, owner, name)
	if err != nil {
		return fmt.Errorf("read %s/%s: %w", owner, name, err)
	}
	method, err := mergeMethod(repo)
	if err != nil {
		return fmt.Errorf("merge %s/%s#%d: %w", owner, name, number, err)
	}
	if _, _, err := client.PullRequests.Merge(ctx, owner, name, number, message, &gh.PullRequestOptions{MergeMethod: method}); err != nil {
		return fmt.Errorf("merge %s/%s#%d: %w", owner, name, number, err)
	}
	return nil
}

// mergeMethod is the first merge method repo allows, in the order merge
// commit, squash, rebase.
func mergeMethod(repo *gh.Repository) (string, error) {
	switch {
	case repo.GetAllowMergeCommit():
		return "merge", nil
	case repo.GetAllowSquashMerge():
		return "squash", nil
	case repo.GetAllowRebaseMerge():
		return "rebase", nil
	default:
		return "", errors.New("the repository allows no merge method")
	}
}

// markReady takes a pull request out of draft. The REST API cannot; the
// GraphQL markPullRequestReadyForReview mutation can.
func markReady(ctx context.Context, client *gh.Client, nodeID string) error {
	return markReadyAt(ctx, client, graphqlURL(client.BaseURL()), nodeID)
}

// markReadyAt sends the mutation to the GraphQL endpoint at url.
func markReadyAt(ctx context.Context, client *gh.Client, url, nodeID string) error {
	body := map[string]any{
		"query":     `mutation($id: ID!) { markPullRequestReadyForReview(input: {pullRequestId: $id}) { pullRequest { isDraft } } }`,
		"variables": map[string]string{"id": nodeID},
	}
	req, err := client.NewRequest(ctx, "POST", url, body)
	if err != nil {
		return err
	}
	var out struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if _, err := client.Do(req, &out); err != nil {
		return err
	}
	if len(out.Errors) > 0 {
		return errors.New(out.Errors[0].Message)
	}
	return nil
}

// graphqlURL is the GraphQL endpoint beside a REST base URL: /graphql on
// github.com, /api/graphql on an Enterprise Server (whose REST base ends
// in /api/v3/).
func graphqlURL(restBase string) string {
	if strings.HasSuffix(restBase, "/api/v3/") {
		return strings.TrimSuffix(restBase, "v3/") + "graphql"
	}
	return restBase + "graphql"
}
