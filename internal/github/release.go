// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"

	gh "github.com/google/go-github/v90/github"
)

// releaseTag matches a release tag ref such as "refs/tags/v0.0.57".
var releaseTag = regexp.MustCompile(`^refs/tags/v(\d+)\.(\d+)\.(\d+)$`)

// ReleaseState is a repository's default branch and newest release tag.
type ReleaseState struct {
	// DefaultBranch is the branch a release pull request targets.
	DefaultBranch string
	// LatestTag is the highest vX.Y.Z tag, or "" when there is none.
	LatestTag string
}

// ReadReleaseState reads owner/name's default branch and its newest
// vX.Y.Z tag, compared as numbers so v0.0.10 is newer than v0.0.9.
func ReadReleaseState(ctx context.Context, client *gh.Client, owner, name string) (ReleaseState, error) {
	repo, _, err := client.Repositories.Get(ctx, owner, name)
	if err != nil {
		return ReleaseState{}, fmt.Errorf("read %s/%s: %w", owner, name, err)
	}
	refs, _, err := client.Git.ListMatchingRefs(ctx, owner, name, "tags/v")
	if err != nil {
		return ReleaseState{}, fmt.Errorf("list tags in %s/%s: %w", owner, name, err)
	}
	return ReleaseState{DefaultBranch: repo.GetDefaultBranch(), LatestTag: newestTag(refs)}, nil
}

// newestTag returns the highest vX.Y.Z tag among refs, without "refs/tags/".
func newestTag(refs []*gh.Reference) string {
	best, bestV := "", [3]int{-1, -1, -1}
	for _, r := range refs {
		m := releaseTag.FindStringSubmatch(r.GetRef())
		if m == nil {
			continue
		}
		var v [3]int
		for i := range v {
			v[i], _ = strconv.Atoi(m[i+1]) // the pattern admits only digits
		}
		if v[0] > bestV[0] || v[0] == bestV[0] && (v[1] > bestV[1] || v[1] == bestV[1] && v[2] > bestV[2]) {
			best, bestV = r.GetRef()[len("refs/tags/"):], v
		}
	}
	return best
}

// BranchExists reports whether owner/name has branch. A missing branch is
// an answer, not an error.
func BranchExists(ctx context.Context, client *gh.Client, owner, name, branch string) (bool, error) {
	_, _, err := client.Git.GetRef(ctx, owner, name, "heads/"+branch)
	switch {
	case err == nil:
		return true, nil
	case apiStatus(err) == http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("read branch %s in %s/%s: %w", branch, owner, name, err)
	}
}

// OpenPullRequest returns the number of the open pull request from head
// into base in owner/name, or 0 when there is none.
func OpenPullRequest(ctx context.Context, client *gh.Client, owner, name, head, base string) (int, error) {
	prs, _, err := client.PullRequests.List(ctx, owner, name, &gh.PullRequestListOptions{
		State: "open", Head: owner + ":" + head, Base: base,
	})
	if err != nil {
		return 0, fmt.Errorf("list pull requests from %s in %s/%s: %w", head, owner, name, err)
	}
	if len(prs) == 0 {
		return 0, nil
	}
	return prs[0].GetNumber(), nil
}

// CreateBranch creates branch in owner/name at the head of from.
func CreateBranch(ctx context.Context, client *gh.Client, owner, name, branch, from string) error {
	ref, _, err := client.Git.GetRef(ctx, owner, name, "heads/"+from)
	if err != nil {
		return fmt.Errorf("read branch %s in %s/%s: %w", from, owner, name, err)
	}
	_, _, err = client.Git.CreateRef(ctx, owner, name, gh.CreateRef{Ref: "refs/heads/" + branch, SHA: ref.GetObject().GetSHA()})
	if err != nil {
		return fmt.Errorf("create branch %s in %s/%s: %w", branch, owner, name, err)
	}
	return nil
}

// ErrMergeConflict reports that head cannot be merged into base cleanly.
var ErrMergeConflict = errors.New("merge conflict")

// MergeBranch merges head into base in owner/name with message, as a merge
// commit GitHub signs. It returns the merge commit, or "" when base
// already contains head; a conflict is ErrMergeConflict.
func MergeBranch(ctx context.Context, client *gh.Client, owner, name, base, head, message string) (string, error) {
	commit, resp, err := client.Repositories.Merge(ctx, owner, name, gh.RepositoryMergeRequest{
		Base: base, Head: head, CommitMessage: &message,
	})
	switch {
	case err == nil && resp.StatusCode == http.StatusNoContent:
		return "", nil
	case err == nil:
		return commit.GetSHA(), nil
	case apiStatus(err) == http.StatusConflict:
		return "", fmt.Errorf("merge %s into %s in %s/%s: %w", head, base, owner, name, ErrMergeConflict)
	default:
		return "", fmt.Errorf("merge %s into %s in %s/%s: %w", head, base, owner, name, err)
	}
}

// CreatePullRequest opens a pull request from head into base in owner/name
// and returns its number.
func CreatePullRequest(ctx context.Context, client *gh.Client, owner, name, head, base, title, body string) (int, error) {
	pr, _, err := client.PullRequests.Create(ctx, owner, name, gh.CreatePullRequest{
		Title: &title, Head: head, Base: base, Body: &body,
	})
	if err != nil {
		return 0, fmt.Errorf("open a pull request from %s in %s/%s: %w", head, owner, name, err)
	}
	return pr.GetNumber(), nil
}

// ClosePullRequest comments on, then closes, owner/name#number.
func ClosePullRequest(ctx context.Context, client *gh.Client, owner, name string, number int, comment string) error {
	if _, _, err := client.Issues.CreateComment(ctx, owner, name, number, &gh.IssueComment{Body: &comment}); err != nil {
		return fmt.Errorf("comment on %s/%s#%d: %w", owner, name, number, err)
	}
	closed := "closed"
	if _, _, err := client.PullRequests.Edit(ctx, owner, name, number, &gh.PullRequest{State: &closed}); err != nil {
		return fmt.Errorf("close %s/%s#%d: %w", owner, name, number, err)
	}
	return nil
}

// apiStatus is the HTTP status of a GitHub API error, or 0.
func apiStatus(err error) int {
	var er *gh.ErrorResponse
	if errors.As(err, &er) && er.Response != nil {
		return er.Response.StatusCode
	}
	return 0
}
