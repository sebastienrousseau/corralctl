// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package forge

import (
	"context"
	"errors"
	"net/http"
	"strings"

	gh "github.com/google/go-github/v90/github"
)

// newTargetGitHubClient is a seam for the construction-failure branch,
// for the same reason internal/github has one: gh.NewClient only fails on
// a malformed option, which this code cannot produce.
var newTargetGitHubClient = gh.NewClient

// Whoami implements Target.
func (g GitHub) Whoami(ctx context.Context, opts TargetOptions) (string, error) {
	client, err := g.targetClient(opts)
	if err != nil {
		return "", err
	}
	user, _, err := client.Users.Get(ctx, "")
	if err != nil {
		return "", err
	}
	if user.GetLogin() == "" {
		return "", errors.New("github: /user returned no login")
	}
	return user.GetLogin(), nil
}

// EnsureRepo implements Target.
func (g GitHub) EnsureRepo(ctx context.Context, owner string, spec RepoSpec, opts TargetOptions) (Remote, error) {
	client, err := g.targetClient(opts)
	if err != nil {
		return Remote{}, err
	}
	repo, _, err := client.Repositories.Get(ctx, owner, spec.Name)
	if err == nil {
		return g.remote(repo, false), nil
	}
	if !githubStatusIs(err, http.StatusNotFound) {
		return Remote{}, err
	}

	// An empty org means "under the authenticated user". Anything else
	// is an organisation the token must be allowed to create in.
	org := owner
	if strings.EqualFold(owner, opts.Login) {
		org = ""
	}
	created, _, err := client.Repositories.Create(ctx, org, &gh.Repository{
		Name:        gh.Ptr(spec.Name),
		Description: gh.Ptr(spec.Description),
		Private:     gh.Ptr(spec.Private),
		AutoInit:    gh.Ptr(false),
	})
	switch {
	case err == nil:
		return g.remote(created, true), nil
	case githubStatusIs(err, http.StatusUnprocessableEntity) && strings.Contains(strings.ToLower(err.Error()), "already exists"):
		repo, _, err := client.Repositories.Get(ctx, owner, spec.Name)
		if err != nil {
			return Remote{}, err
		}
		return g.remote(repo, false), nil
	default:
		return Remote{}, err
	}
}

// PushAuth implements Target. GitHub takes a token as the password for the
// fixed username x-access-token, which is how internal/git already
// authenticates clones.
func (GitHub) PushAuth(opts TargetOptions) (string, string) { return "x-access-token", opts.Token }

func (GitHub) targetClient(opts TargetOptions) (*gh.Client, error) {
	if strings.TrimSpace(opts.Token) == "" {
		return nil, ErrNoToken
	}
	timeout := opts.RequestTimeout
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	options := []gh.ClientOptionsFunc{
		gh.WithHTTPClient(&http.Client{Timeout: timeout}),
		gh.WithAuthToken(opts.Token),
	}
	if opts.BaseURL != "" {
		options = append(options, gh.WithEnterpriseURLs(opts.BaseURL, opts.BaseURL))
	}
	return newTargetGitHubClient(options...)
}

func (GitHub) remote(r *gh.Repository, created bool) Remote {
	return Remote{
		HTTPSURL: r.GetCloneURL(),
		SSHURL:   r.GetSSHURL(),
		Private:  r.GetPrivate(),
		Created:  created,
	}
}

// githubStatusIs reports whether err is a go-github error with the given
// HTTP status.
func githubStatusIs(err error, status int) bool {
	var er *gh.ErrorResponse
	return errors.As(err, &er) && er.Response != nil && er.Response.StatusCode == status
}
