// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package forge

import (
	"context"

	"github.com/sebastienrousseau/corralctl/internal/github"
)

func init() { Register(GitHub{}) }

// GitHub lists repositories through the existing internal/github client.
//
// An adapter rather than a rewrite. That client carries the parts of
// talking to GitHub that are genuinely intricate — secondary rate limits,
// retry budgets, the auth ladder from an explicit token through the
// environment to the gh CLI, search-vs-list pagination — and none of that
// is knowledge worth re-deriving against a smaller REST client just to
// make the package uniform.
//
// The hand-written clients exist for the forges corral had no client for
// at all. Where one already works, the abstraction wraps it.
type GitHub struct{}

// Name implements Forge.
func (GitHub) Name() string { return "github" }

// Hosts implements Forge.
func (GitHub) Hosts() []string { return []string{"github.com"} }

// List implements Forge.
func (GitHub) List(ctx context.Context, owner string, opts Options) ([]Repo, error) {
	repos, err := githubFetch(ctx, owner, github.FetchOptions{
		Limit:           opts.Limit,
		Visibility:      opts.Visibility,
		IncludeForks:    opts.IncludeForks,
		IncludeArchived: opts.IncludeArchived,
		RequestTimeout:  opts.RequestTimeout,
		TotalTimeout:    opts.TotalTimeout,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Repo, 0, len(repos))
	for _, r := range repos {
		out = append(out, Repo{
			ID:            r.ID,
			Owner:         r.Owner,
			Name:          r.Name,
			FullName:      r.FullName,
			Language:      NormalizeLanguage(r.Language),
			Visibility:    NormalizeVisibility(false, r.Visibility),
			DefaultBranch: r.DefaultBranch,
			CloneURL:      r.CloneURL,
			SSHURL:        r.SSHURL,
			Fork:          r.Fork,
			Archived:      r.Archived,
			PushedAt:      r.PushedAt,
			Stars:         r.Stars,
			IsTemplate:    r.IsTemplate,
			IsMirror:      r.IsMirror,
		})
	}
	// The GitHub client already applies these, so this is a no-op in
	// practice. Running it anyway means one place decides what a filter
	// means, and a change to Filter cannot leave GitHub behaving
	// differently from every other forge.
	return Filter(out, opts), nil
}

// githubFetch is indirected so tests exercise the mapping without the
// network.
var githubFetch = github.FetchReposWithOptions
