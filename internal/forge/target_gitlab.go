// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package forge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// Whoami implements Target.
func (g GitLab) Whoami(ctx context.Context, opts TargetOptions) (string, error) {
	c, err := g.targetClient(opts)
	if err != nil {
		return "", err
	}
	var user struct {
		Username string `json:"username"`
	}
	if _, err := c.call(ctx, http.MethodGet, "/api/v4/user", nil, &user); err != nil {
		return "", err
	}
	if user.Username == "" {
		return "", errors.New("gitlab: /user returned no username")
	}
	return user.Username, nil
}

// EnsureRepo implements Target.
//
// GitLab calls a repository a project and files it under a namespace. A
// personal namespace is the default when namespace_id is omitted, and it
// is omitted on purpose: the id from /user is the *user's* id, which on
// gitlab.com is not the id of their namespace, and sending it produces
// "namespace: is not valid". A group is looked up by path instead, which
// returns the id the create endpoint actually wants.
func (g GitLab) EnsureRepo(ctx context.Context, owner string, spec RepoSpec, opts TargetOptions) (Remote, error) {
	c, err := g.targetClient(opts)
	if err != nil {
		return Remote{}, err
	}
	path := "/api/v4/projects/" + url.PathEscape(owner+"/"+spec.Name)
	var p gitlabProject
	if _, err := c.call(ctx, http.MethodGet, path, nil, &p); err == nil {
		return g.remote(p, false), nil
	} else if !statusIs(err, http.StatusNotFound) {
		return Remote{}, err
	}

	visibility := "public"
	if spec.Private {
		visibility = "private"
	}
	payload := map[string]any{
		"name":                   spec.Name,
		"path":                   spec.Name,
		"description":            spec.Description,
		"visibility":             visibility,
		"initialize_with_readme": false,
	}
	if owner != opts.Login {
		var ns struct {
			ID int64 `json:"id"`
		}
		if _, err := c.call(ctx, http.MethodGet, "/api/v4/namespaces/"+url.PathEscape(owner), nil, &ns); err != nil {
			return Remote{}, fmt.Errorf("resolving namespace %q: %w", owner, err)
		}
		if ns.ID == 0 {
			return Remote{}, fmt.Errorf("namespace %q has no id", owner)
		}
		payload["namespace_id"] = ns.ID
	}
	_, err = c.call(ctx, http.MethodPost, "/api/v4/projects", payload, &p)
	switch {
	case err == nil:
		return g.remote(p, true), nil
	case lostCreateRace(err):
		if _, err := c.call(ctx, http.MethodGet, path, nil, &p); err != nil {
			return Remote{}, err
		}
		return g.remote(p, false), nil
	default:
		return Remote{}, err
	}
}

// PushAuth implements Target. GitLab accepts a personal access token as
// the password for the fixed username oauth2.
func (GitLab) PushAuth(opts TargetOptions) (string, string) { return "oauth2", opts.Token }

func (GitLab) targetClient(opts TargetOptions) (*restClient, error) {
	base := opts.BaseURL
	if base == "" {
		base = DefaultGitLabURL
	}
	return targetClient(base, "PRIVATE-TOKEN", "", opts)
}

func (GitLab) remote(p gitlabProject, created bool) Remote {
	return Remote{
		HTTPSURL: p.HTTPURLToRepo,
		SSHURL:   p.SSHURLToRepo,
		Private:  p.Visibility != "public",
		Created:  created,
	}
}
