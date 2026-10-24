// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package forge

import (
	"context"
	"errors"
	"net/url"
	"time"
)

func init() {
	// One client, three names. Forgejo is a hard fork of Gitea that kept
	// the API, and Codeberg is a Forgejo instance — so these are aliases
	// with different defaults, not different implementations. Three copies
	// of the same client would be three things that could drift apart.
	Register(gitea{name: "gitea", hosts: nil, defaultURL: ""})
	Register(gitea{name: "forgejo", hosts: nil, defaultURL: ""})
	Register(gitea{name: "codeberg", hosts: []string{"codeberg.org"}, defaultURL: DefaultCodebergURL})
}

// DefaultCodebergURL is Codeberg's public instance.
const DefaultCodebergURL = "https://codeberg.org"

// gitea lists repositories from any instance of Gitea, Forgejo or
// Codeberg.
//
// Gitea and Forgejo have no single public instance, so "gitea" and
// "forgejo" require --forge-url. Codeberg has one, and defaults to it —
// which is the difference between the three registrations.
type gitea struct {
	name string
	// hosts are the domains that resolve to this forge. Empty for the
	// self-hosted names, which have no fixed domain.
	hosts []string
	// defaultURL is used when Options.BaseURL is empty. Empty means the
	// caller must supply one.
	defaultURL string
}

// Name implements Forge.
func (g gitea) Name() string { return g.name }

// Hosts implements Forge.
func (g gitea) Hosts() []string { return g.hosts }

// giteaRepo is the subset of Gitea's repository representation corral
// uses. Forgejo and Codeberg return the same shape.
type giteaRepo struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Owner    struct {
		Login string `json:"login"`
	} `json:"owner"`
	Language      string     `json:"language"`
	Private       bool       `json:"private"`
	Internal      bool       `json:"internal"`
	DefaultBranch string     `json:"default_branch"`
	CloneURL      string     `json:"clone_url"`
	SSHURL        string     `json:"ssh_url"`
	Fork          bool       `json:"fork"`
	Archived      bool       `json:"archived"`
	Updated       *time.Time `json:"updated_at"`
	StarsCount    int        `json:"stars_count"`
	Template      bool       `json:"template"`
	Mirror        bool       `json:"mirror"`
	Empty         bool       `json:"empty"`
}

// List implements Forge.
func (g gitea) List(ctx context.Context, owner string, opts Options) ([]Repo, error) {
	base := opts.BaseURL
	if base == "" {
		base = g.defaultURL
	}
	if base == "" {
		return nil, errors.New(g.name + " has no single public instance; pass --forge-url with your instance's address")
	}
	// Gitea's own scheme. "Authorization: token <t>" rather than Bearer,
	// which it does not accept for a personal access token.
	c, err := newRESTClient(base, opts.Token, "Authorization", "token ", opts)
	if err != nil {
		return nil, err
	}

	// A name is either a user or an organisation, and the endpoints
	// differ. Users first, for the same reason as GitLab.
	var lastErr error
	for _, path := range []string{"/api/v1/users/", "/api/v1/orgs/"} {
		repos, err := g.listPath(ctx, c, path+url.PathEscape(owner)+"/repos", opts)
		if err != nil {
			var nf *NotFoundError
			if errors.As(err, &nf) {
				lastErr = err
				continue
			}
			return nil, err
		}
		return Filter(repos, opts), nil
	}
	return nil, lastErr
}

// listPath pages one endpoint to exhaustion.
func (g gitea) listPath(ctx context.Context, c *restClient, path string, opts Options) ([]Repo, error) {
	var out []Repo
	for page := 1; page <= maxPages; page++ {
		var batch []giteaRepo
		if err := c.getPage(ctx, path, pageQuery(page, nil), &batch); err != nil {
			return nil, err
		}
		for _, r := range batch {
			out = append(out, g.toRepo(r))
		}
		if len(batch) < perPage {
			break
		}
		// Twice the limit, so filtering out forks and archived
		// repositories afterwards does not leave the answer short.
		if opts.Limit > 0 && len(out) >= opts.Limit*2 {
			break
		}
	}
	return out, nil
}

// toRepo maps a Gitea repository onto the shared shape.
func (g gitea) toRepo(r giteaRepo) Repo {
	var pushed time.Time
	if r.Updated != nil {
		// Gitea's updated_at moves on metadata changes as well as pushes,
		// so like GitLab's it is conservative: a pull that was not needed,
		// never a skip that was wrong.
		pushed = *r.Updated
	}
	visibility := "Public"
	if r.Private || r.Internal {
		visibility = "Private"
	}
	return Repo{
		ID:            r.ID,
		Owner:         r.Owner.Login,
		Name:          r.Name,
		FullName:      r.FullName,
		Language:      NormalizeLanguage(r.Language),
		Visibility:    visibility,
		DefaultBranch: r.DefaultBranch,
		CloneURL:      r.CloneURL,
		SSHURL:        r.SSHURL,
		Fork:          r.Fork,
		Archived:      r.Archived,
		PushedAt:      pushed,
		Stars:         r.StarsCount,
		IsTemplate:    r.Template,
		IsMirror:      r.Mirror,
	}
}
