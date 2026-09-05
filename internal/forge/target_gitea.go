// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package forge

import (
	"context"
	"errors"
	"net/http"
	"net/url"
)

// Whoami implements Target.
func (g gitea) Whoami(ctx context.Context, opts TargetOptions) (string, error) {
	c, err := g.targetClient(opts)
	if err != nil {
		return "", err
	}
	var user struct {
		Login string `json:"login"`
	}
	if _, err := c.call(ctx, http.MethodGet, "/api/v1/user", nil, &user); err != nil {
		return "", err
	}
	if user.Login == "" {
		return "", errors.New(g.name + ": /user returned no login")
	}
	return user.Login, nil
}

// EnsureRepo implements Target.
//
// The check comes before the create rather than relying on a 409: an
// instance with repository creation disabled, or a user at their quota,
// rejects the POST even when the mirror already exists and could simply
// be reused.
func (g gitea) EnsureRepo(ctx context.Context, owner string, spec RepoSpec, opts TargetOptions) (Remote, error) {
	c, err := g.targetClient(opts)
	if err != nil {
		return Remote{}, err
	}
	path := "/api/v1/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(spec.Name)
	var r giteaRepo
	if _, err := c.call(ctx, http.MethodGet, path, nil, &r); err == nil {
		return g.remote(r, false), nil
	} else if !statusIs(err, http.StatusNotFound) {
		return Remote{}, err
	}

	createPath := "/api/v1/user/repos"
	if owner != opts.Login {
		createPath = "/api/v1/orgs/" + url.PathEscape(owner) + "/repos"
	}
	payload := map[string]any{
		"name":        spec.Name,
		"description": spec.Description,
		"private":     spec.Private,
		"auto_init":   false,
	}
	_, err = c.call(ctx, http.MethodPost, createPath, payload, &r)
	switch {
	case err == nil:
		return g.remote(r, true), nil
	case lostCreateRace(err):
		if _, err := c.call(ctx, http.MethodGet, path, nil, &r); err != nil {
			return Remote{}, err
		}
		return g.remote(r, false), nil
	default:
		return Remote{}, err
	}
}

// PushAuth implements Target. Gitea, Forgejo and Codeberg take the token
// as the password for the account's own login.
func (gitea) PushAuth(opts TargetOptions) (string, string) { return opts.Login, opts.Token }

func (g gitea) targetClient(opts TargetOptions) (*restClient, error) {
	base := opts.BaseURL
	if base == "" {
		base = g.defaultURL
	}
	if base == "" {
		return nil, errors.New(g.name + " has no single public instance; name your instance's address in --to")
	}
	return targetClient(base, "Authorization", "token ", opts)
}

func (gitea) remote(r giteaRepo, created bool) Remote {
	return Remote{
		HTTPSURL: r.CloneURL,
		SSHURL:   r.SSHURL,
		Private:  r.Private || r.Internal,
		Created:  created,
	}
}
