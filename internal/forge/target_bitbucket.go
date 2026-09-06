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
//
// Bitbucket's owner is a workspace, and a personal workspace's slug is the
// account's username, so the username is the default owner.
func (bb Bitbucket) Whoami(ctx context.Context, opts TargetOptions) (string, error) {
	c, err := bb.targetClient(opts)
	if err != nil {
		return "", err
	}
	var user struct {
		Username string `json:"username"`
		Nickname string `json:"nickname"`
	}
	if _, err := c.call(ctx, http.MethodGet, "/2.0/user", nil, &user); err != nil {
		return "", err
	}
	if user.Username == "" {
		user.Username = user.Nickname
	}
	if user.Username == "" {
		return "", errors.New("bitbucket: /user returned no username")
	}
	return user.Username, nil
}

// EnsureRepo implements Target.
//
// Bitbucket creates with a PUT-shaped POST to the repository's own path,
// so the check and the create address the same URL.
func (bb Bitbucket) EnsureRepo(ctx context.Context, owner string, spec RepoSpec, opts TargetOptions) (Remote, error) {
	c, err := bb.targetClient(opts)
	if err != nil {
		return Remote{}, err
	}
	path := "/2.0/repositories/" + url.PathEscape(owner) + "/" + url.PathEscape(spec.Name)
	var r bitbucketRepo
	if _, err := c.call(ctx, http.MethodGet, path, nil, &r); err == nil {
		return bb.remote(r, false), nil
	} else if !statusIs(err, http.StatusNotFound) {
		return Remote{}, err
	}
	payload := map[string]any{
		"scm":         "git",
		"name":        spec.Name,
		"description": spec.Description,
		"is_private":  spec.Private,
	}
	_, err = c.call(ctx, http.MethodPost, path, payload, &r)
	switch {
	case err == nil:
		return bb.remote(r, true), nil
	case lostCreateRace(err):
		if _, err := c.call(ctx, http.MethodGet, path, nil, &r); err != nil {
			return Remote{}, err
		}
		return bb.remote(r, false), nil
	default:
		return Remote{}, err
	}
}

// PushAuth implements Target. Bitbucket takes an access token as the
// password for the fixed username x-token-auth.
func (Bitbucket) PushAuth(opts TargetOptions) (string, string) { return "x-token-auth", opts.Token }

func (Bitbucket) targetClient(opts TargetOptions) (*restClient, error) {
	return targetClient(bitbucketAPIBase(opts.BaseURL), "Authorization", "Bearer ", opts)
}

func (bb Bitbucket) remote(r bitbucketRepo, created bool) Remote {
	mapped := bb.toRepo(r)
	return Remote{
		HTTPSURL: mapped.CloneURL,
		SSHURL:   mapped.SSHURL,
		Private:  r.IsPrivate,
		Created:  created,
	}
}
