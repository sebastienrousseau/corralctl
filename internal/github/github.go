// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package github provides functionality to interact with the GitHub API.
package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	gh "github.com/google/go-github/v90/github"
)

// FetchRepos retrieves repositories for a given owner up to the specified limit.
func FetchRepos(ctx context.Context, owner string, limit int) ([]Repo, error) {
	return FetchReposWithOptions(ctx, owner, FetchOptions{Limit: limit})
}

// FetchReposWithOptions retrieves repositories with explicit fetch options.
func FetchReposWithOptions(ctx context.Context, owner string, opts FetchOptions) ([]Repo, error) {
	if strings.TrimSpace(owner) == "" {
		return nil, errors.New("owner must not be empty")
	}

	opts = normalizeFetchOptions(opts)
	base, host, err := enterpriseBase(opts.BaseURL)
	if err != nil {
		return nil, err
	}
	token, err := resolveTokenFor(ctx, opts.AuthMode, host)
	if err != nil {
		return nil, err
	}

	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, opts.TotalTimeout)
	defer cancel()

	httpClient := &http.Client{
		Timeout: opts.RequestTimeout,
		Transport: &retryTransport{
			base:       http.DefaultTransport,
			maxRetries: opts.RetryMax,
			minBackoff: opts.RetryMinBackoff,
			maxBackoff: opts.RetryMaxBackoff,
		},
	}

	clientOpts := []gh.ClientOptionsFunc{gh.WithHTTPClient(httpClient), gh.WithAuthToken(token)}
	if base != "" {
		// WithEnterpriseURLs appends api/v3/ (and api/uploads/), which is
		// where an Enterprise Server serves its REST API.
		clientOpts = append(clientOpts, gh.WithEnterpriseURLs(base, base))
	}
	client, err := newGitHubClient(clientOpts...)
	if err != nil {
		return nil, fmt.Errorf("construct GitHub client: %w", err)
	}
	return FetchReposWithClientOptions(ctx, client, owner, opts)
}

// newGitHubClient constructs the go-github client. A seam rather than a
// direct call so the construction-failure branch is reachable from a test.
var newGitHubClient = gh.NewClient

// FetchReposWithClient allows injecting a GitHub client for retrieving repositories.
// This is primarily exposed for testing purposes.
func FetchReposWithClient(ctx context.Context, client *gh.Client, owner string, limit int) ([]Repo, error) {
	return FetchReposWithClientOptions(ctx, client, owner, FetchOptions{Limit: limit})
}

// pageFetcher retrieves one page of repositories from whichever GitHub
// listing endpoint suits the requested owner.
type pageFetcher func(page int) ([]*gh.Repository, *gh.Response, error)

// listingKind records which listing endpoint an owner resolves to.
type listingKind struct {
	search            bool
	org               bool
	authenticatedUser bool
}

// FetchReposWithClientOptions allows injecting a GitHub client and advanced filtering.
func FetchReposWithClientOptions(ctx context.Context, client *gh.Client, owner string, opts FetchOptions) ([]Repo, error) {
	opts = normalizeFetchOptions(opts)

	if client == nil {
		return nil, errors.New("github client must not be nil")
	}
	if strings.TrimSpace(owner) == "" {
		return nil, errors.New("owner must not be empty")
	}

	kind, err := resolveListingKind(ctx, client, owner)
	if err != nil {
		return nil, err
	}

	collector := &repoCollector{
		limit:       opts.Limit,
		includeLang: toLookupSet(opts.IncludeLanguages),
		excludeLang: toLookupSet(opts.ExcludeLanguages),
		opts:        opts,
	}
	fetchPage := newPageFetcher(ctx, client, owner, opts, kind)

	repos, resp, err := fetchPage(1)
	if err != nil {
		return nil, fmt.Errorf("failed listing repositories for '%s': %w", owner, err)
	}
	collector.absorb(repos)

	switch {
	case resp.LastPage > 1 && collector.wants():
		if err := collectConcurrently(ctx, owner, fetchPage, resp.LastPage, collector); err != nil {
			return nil, err
		}
	case resp.NextPage > 0 && collector.wants():
		if err := collectSequentially(owner, fetchPage, resp.NextPage, collector); err != nil {
			return nil, err
		}
	}

	sortRepos(collector.repos, opts.Sort)
	return collector.repos, nil
}

// resolveListingKind determines which listing endpoint owner should be read
// from, performing the owner lookups that decision needs.
func resolveListingKind(ctx context.Context, client *gh.Client, owner string) (listingKind, error) {
	kind := listingKind{
		search: strings.HasPrefix(owner, "topic:") || strings.HasPrefix(owner, "language:"),
	}
	if kind.search {
		return kind, nil
	}

	u, _, err := client.Users.Get(ctx, owner)
	if err != nil {
		return kind, describeOwnerLookupError(ctx, client, owner, err)
	}
	kind.org = u.GetType() == "Organization"
	if kind.org {
		return kind, nil
	}

	if authedUser, _, authErr := client.Users.Get(ctx, ""); authErr == nil {
		login := authedUser.GetLogin()
		kind.authenticatedUser = login != "" && login == u.GetLogin()
	}
	return kind, nil
}

// newPageFetcher returns the page-fetching closure for the resolved
// listing kind. Every endpoint is paged 100 at a time.
func newPageFetcher(ctx context.Context, client *gh.Client, owner string, opts FetchOptions, kind listingKind) pageFetcher {
	return func(p int) ([]*gh.Repository, *gh.Response, error) {
		listOpts := gh.ListOptions{Page: p, PerPage: perPage}
		switch {
		case kind.search:
			result, resp, err := client.Search.Repositories(ctx, owner, &gh.SearchOptions{
				Sort:        "stars",
				ListOptions: listOpts,
			})
			if err != nil {
				return nil, nil, err
			}
			return result.Repositories, resp, nil
		case kind.org:
			return client.Repositories.ListByOrg(ctx, owner, &gh.RepositoryListByOrgOptions{
				Type:        orgTypeForVisibility(opts.Visibility),
				Sort:        "updated",
				ListOptions: listOpts,
			})
		case kind.authenticatedUser:
			return client.Repositories.ListByAuthenticatedUser(ctx, &gh.RepositoryListByAuthenticatedUserOptions{
				Visibility:  opts.Visibility,
				Affiliation: "owner",
				Sort:        "updated",
				ListOptions: listOpts,
			})
		default:
			return client.Repositories.ListByUser(ctx, owner, &gh.RepositoryListByUserOptions{
				Type:        "owner",
				Sort:        "updated",
				ListOptions: listOpts,
			})
		}
	}
}

func normalizeFetchOptions(opts FetchOptions) FetchOptions {
	if opts.Limit < 0 {
		opts.Limit = 0
	}
	if opts.Visibility == "" {
		opts.Visibility = "all"
	}
	opts.Visibility = strings.ToLower(strings.TrimSpace(opts.Visibility))
	if opts.Visibility != "all" && opts.Visibility != "public" && opts.Visibility != "private" {
		opts.Visibility = "all"
	}
	opts.AuthMode = normalizeAuthMode(opts.AuthMode)
	if opts.RetryMax < 0 {
		opts.RetryMax = 0
	} else if opts.RetryMax == 0 {
		opts.RetryMax = defaultRetryMax
	}
	if opts.RetryMinBackoff <= 0 {
		opts.RetryMinBackoff = defaultRetryMinBackoff
	}
	if opts.RetryMaxBackoff <= 0 {
		opts.RetryMaxBackoff = defaultRetryMaxBackoff
	}
	if opts.RetryMaxBackoff < opts.RetryMinBackoff {
		opts.RetryMaxBackoff = opts.RetryMinBackoff
	}
	if opts.Timeout > 0 {
		if opts.RequestTimeout <= 0 {
			opts.RequestTimeout = opts.Timeout
		}
		if opts.TotalTimeout <= 0 {
			opts.TotalTimeout = opts.Timeout
		}
	}
	if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = defaultRequestTimeout
	}
	if opts.TotalTimeout <= 0 {
		opts.TotalTimeout = defaultTotalTimeout
	}
	if opts.TotalTimeout < opts.RequestTimeout {
		opts.TotalTimeout = opts.RequestTimeout
	}
	return opts
}

func orgTypeForVisibility(v string) string {
	switch v {
	case "public":
		return "public"
	case "private":
		return "private"
	default:
		return "all"
	}
}

// describeOwnerLookupError turns a failed owner lookup into a message a person
// can act on.
func describeOwnerLookupError(ctx context.Context, client *gh.Client, owner string, err error) error {
	var apiErr *gh.ErrorResponse
	if !errors.As(err, &apiErr) || apiErr.Response == nil ||
		apiErr.Response.StatusCode != http.StatusNotFound {
		return fmt.Errorf("cannot look up %q on GitHub: %w", owner, err)
	}

	msg := fmt.Sprintf("no GitHub user or organisation named %q", owner)

	if login := authenticatedLogin(ctx, client); login != "" {
		if login != owner && levenshtein(strings.ToLower(owner), strings.ToLower(login)) <= 2 {
			return fmt.Errorf("%s\n\nDid you mean your own account?\n\tcorralctl %s", msg, login)
		}
	}
	return fmt.Errorf("%s\n\nCheck the spelling. If it is a private organisation, "+
		"confirm your credentials can see it with `gh auth status`", msg)
}

func authenticatedLogin(ctx context.Context, client *gh.Client) string {
	u, _, err := client.Users.Get(ctx, "")
	if err != nil {
		return ""
	}
	return u.GetLogin()
}

// levenshtein is the standard edit distance, used only to decide whether a
// mistyped owner is close enough to the authenticated user's login to suggest.
//
// Deliberately duplicated rather than shared: internal/github has no internal
// imports, and keeping that leaf property is worth more than de-duplicating
// twenty lines of arithmetic.
func levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, min(cur[j-1]+1, prev[j-1]+cost))
		}
		prev, cur = cur, prev
	}
	return prev[len(br)]
}
