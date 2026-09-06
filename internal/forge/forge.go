// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package forge abstracts the hosting service a repository comes from.
//
// Corral's *reading* has never been GitHub-specific: the index, the MCP
// server, symbol lookup and content search all work against clones on
// disk, whatever produced them. Only the *cloning* side knew one host.
// That asymmetry is what this package removes.
//
// # What a forge has to do
//
// Two things. Given an owner, list their repositories — that is [Forge],
// and everything after it (the layout, the clone, the sync, the index) is
// already host-agnostic, because it operates on a URL and a directory.
// And given a local repository, make sure a place for it exists and say
// where to push — that is [Target], the receiving side `corralctl sync`
// needs, and every forge here implements both.
//
// That is why each interface is small. A design that mirrored each
// forge's full API would be a large surface with one caller, and every
// forge would drag in a client library an order of magnitude larger than
// the part of it corral uses.
//
// # Why the clients are hand-written
//
// Corral holds a small number of direct dependencies and a hand-maintained
// SBOM that CI checks against go.mod. An official SDK per forge would
// multiply that for a few list endpoints. GitHub keeps go-github, because
// it was already there and its rate-limit and pagination handling is
// genuinely intricate; GitLab and the Gitea family get a few hundred lines
// of net/http each, which is smaller than the code that would configure an
// SDK to do the same thing.
//
// # Codeberg, Forgejo and Gitea
//
// One implementation. Forgejo is a hard fork of Gitea and kept its API;
// Codeberg is a Forgejo instance. Treating them as three would be three
// copies of the same client that could drift apart, so they are three
// names for one.
package forge

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/git"
)

// Repo is one repository, as any forge describes it.
//
// Deliberately the intersection rather than the union. A field only
// belongs here if corral uses it — for the layout, for the sync decision,
// or for a filter — because a field that only one forge populates makes
// every other forge look broken.
type Repo struct {
	// ID is the forge's own identifier. Unique within a forge, not across
	// them.
	ID int64
	// Owner is the user or organisation the repository belongs to.
	Owner string
	// Name is the repository name, without the owner prefix.
	Name string
	// FullName is the canonical owner/name identity.
	FullName string
	// Language is the primary language, or "Other" when the forge does not
	// say.
	Language string
	// Visibility is normalised to "Public" or "Private". Forges spell this
	// differently — GitLab has "internal", Gitea has a boolean — and the
	// layout only has two directories, so it is collapsed here rather than
	// in four places downstream.
	Visibility string
	// DefaultBranch is the default branch name.
	DefaultBranch string
	// CloneURL is the HTTPS clone URL.
	CloneURL string
	// SSHURL is the SSH clone URL.
	SSHURL string
	// Fork reports whether this is a fork of another repository.
	Fork bool
	// Archived reports whether the repository is archived or read-only.
	Archived bool
	// PushedAt is the last push to any branch, used to skip a pull when
	// nothing changed upstream. Zero when the forge does not report it,
	// which costs a pull rather than correctness.
	PushedAt time.Time
	// Stars is the star count, where the forge has one.
	Stars int
	// IsTemplate reports a template repository.
	IsTemplate bool
	// IsMirror reports a mirror of a repository hosted elsewhere.
	IsMirror bool
}

// Options are the filters and limits a listing honours.
//
// A subset of what any one forge's API accepts, because the ones missing
// here are applied by corral after the fact anyway — and doing it that way
// means a filter behaves identically on every forge rather than however
// each host happens to implement it.
type Options struct {
	// Limit caps the number of repositories returned; 0 means no limit.
	Limit int
	// Visibility filters by visibility: "all", "public" or "private".
	Visibility string
	// IncludeForks keeps forks.
	IncludeForks bool
	// IncludeArchived keeps archived repositories.
	IncludeArchived bool
	// Token authenticates the request. Empty means an anonymous listing,
	// which every forge here allows for public repositories.
	Token string
	// BaseURL overrides the host, for a self-hosted instance. Empty means
	// the forge's public instance.
	BaseURL string
	// RequestTimeout bounds one HTTP request.
	RequestTimeout time.Duration
	// TotalTimeout bounds the whole paginated listing, including retries
	// and the waiting between them.
	TotalTimeout time.Duration
}

// Forge lists the repositories an owner has on one hosting service.
type Forge interface {
	// Name is the identifier a user types: "github", "gitlab", "gitea",
	// "forgejo", "codeberg".
	Name() string
	// Hosts are the domains this forge serves, lowercased, used to infer a
	// forge from a URL.
	Hosts() []string
	// List returns owner's repositories.
	//
	// An owner that does not exist is an error, not an empty list: the two
	// are indistinguishable to a caller, and "this user has no
	// repositories" is the wrong conclusion to act on.
	List(ctx context.Context, owner string, opts Options) ([]Repo, error)
}

// registry maps a forge name to its implementation.
var registry = map[string]Forge{}

// hostIndex maps a hostname to the forge that serves it.
var hostIndex = map[string]Forge{}

// Register adds a forge. Called from package init functions; it panics on
// a duplicate name, which can only be a programming error.
func Register(f Forge) {
	name := strings.ToLower(f.Name())
	if _, taken := registry[name]; taken {
		panic(fmt.Sprintf("forge: %q is already registered", name))
	}
	registry[name] = f
	for _, h := range f.Hosts() {
		hostIndex[strings.ToLower(h)] = f
	}
}

// Get returns the forge with the given name.
//
// An unknown name is an error naming the ones that exist, rather than a
// silent fallback to GitHub: a user who typed "gitub" should be told, not
// quietly given something else.
func Get(name string) (Forge, error) {
	f, ok := registry[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return nil, fmt.Errorf("unknown forge %q; known forges are %s",
			name, strings.Join(Names(), ", "))
	}
	return f, nil
}

// Names lists the registered forges, sorted.
func Names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ForHost returns the forge serving a hostname.
//
// Matching is on the exact host and then on its parent domains, so
// "gitlab.example.com" does not resolve to GitLab but "www.gitlab.com"
// does. A self-hosted instance on an unrecognised domain is not an error
// here — the caller names the forge explicitly with --forge — so this
// reports "unknown" rather than guessing.
func ForHost(host string) (Forge, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return nil, false
	}
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i] // strip a port
	}
	if f, ok := hostIndex[host]; ok {
		return f, true
	}
	// Try parent domains, so a subdomain of a known host resolves.
	for {
		i := strings.IndexByte(host, '.')
		if i < 0 {
			return nil, false
		}
		host = host[i+1:]
		if f, ok := hostIndex[host]; ok {
			return f, true
		}
	}
}

// ForRemoteURL infers the forge from a git remote URL.
//
// Both spellings a remote takes: the URL form
// (https://host/owner/repo.git) and the scp-like form
// (git@host:owner/repo.git), which is not a URL and which url.Parse reads
// as a path rather than failing.
func ForRemoteURL(remote string) (Forge, bool) {
	host := hostFromRemote(remote)
	if host == "" {
		return nil, false
	}
	return ForHost(host)
}

// hostFromRemote extracts the hostname from either remote spelling.
func hostFromRemote(remote string) string {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return ""
	}
	// scp-like: [user@]host:path. Detected before parsing, because
	// url.Parse accepts it and returns something misleading.
	if !strings.Contains(remote, "://") {
		at := strings.IndexByte(remote, '@')
		colon := strings.IndexByte(remote, ':')
		if colon > at {
			return remote[at+1 : colon]
		}
		return ""
	}
	u, err := url.Parse(remote)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// NormalizeVisibility collapses a forge's visibility vocabulary to the two
// values corral's layout has directories for.
//
// GitLab's "internal" — visible to any signed-in user of the instance —
// is Private. It is not public, and a directory called Internal that only
// one forge ever populates would be worse than the small loss of nuance.
func NormalizeVisibility(private bool, raw string) string {
	if private {
		return "Private"
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "private", "internal":
		return "Private"
	default:
		return "Public"
	}
}

// NormalizeLanguage supplies the placeholder corral's layout uses when a
// forge reports no language, which is common for a new or
// documentation-only repository.
func NormalizeLanguage(lang string) string {
	if strings.TrimSpace(lang) == "" {
		return "Other"
	}
	return lang
}

// Filter applies the options a listing honours after the fact.
//
// Applied here rather than as API query parameters so a filter means the
// same thing on every forge. GitHub, GitLab and Gitea each spell "exclude
// forks" differently and one of them applies it before pagination, so
// pushing it down would make --include-forks behave differently depending
// on the host — which is exactly the kind of difference this package
// exists to remove.
func Filter(repos []Repo, opts Options) []Repo {
	out := repos[:0:0]
	for _, r := range repos {
		if !opts.IncludeForks && r.Fork {
			continue
		}
		if !opts.IncludeArchived && r.Archived {
			continue
		}
		switch strings.ToLower(opts.Visibility) {
		case "public":
			if r.Visibility != "Public" {
				continue
			}
		case "private":
			if r.Visibility != "Private" {
				continue
			}
		}
		out = append(out, r)
		if opts.Limit > 0 && len(out) >= opts.Limit {
			break
		}
	}
	return out
}

// Fetcher is the signature the engine's fetch seam has, expressed in this
// package's types.
type Fetcher func(ctx context.Context, owner string, opts Options) ([]Repo, error)

// Resolve picks the forge for a run.
//
// name wins when given. Otherwise the host of forgeURL decides, which is
// what makes `--forge-url https://codeberg.org/...` work without also
// naming the forge. With neither, GitHub — because that is what corral
// did before this package existed, and a default that changes under
// people is worse than a narrow one.
func Resolve(name, forgeURL string) (Forge, error) {
	if strings.TrimSpace(name) != "" {
		return Get(name)
	}
	if strings.TrimSpace(forgeURL) != "" {
		if f, ok := ForRemoteURL(forgeURL); ok {
			return f, nil
		}
		// A self-hosted instance on an unrecognised domain cannot be
		// guessed — Gitea, Forgejo and GitLab all serve arbitrary domains
		// — so this asks rather than picking one and failing obscurely
		// three requests later.
		return nil, fmt.Errorf(
			"cannot tell which forge %s is; pass --forge with one of %s",
			forgeURL, strings.Join(Names(), ", "))
	}
	return Get("github")
}

// OwnerPrefixes returns the canonical-remote prefixes that mark a local
// clone as belonging to owner on this forge.
//
// A prefix is what git.CanonicalRemote produces for one of this owner's
// repositories, up to and including the final separator —
// "codeberg.org/forgejo/", "gitlab.com/group/subgroup/". Matching on it is
// what stops one host's clone being compared against another host's
// listing.
//
// # Why the listing is the primary source
//
// The clone URLs come from the instance itself. That makes them correct
// for a self-hosted deployment nobody told corral about, and for GitLab's
// nested groups, where the owner a user types ("group") is not the
// namespace a project lives in ("group/subgroup") — neither of which a
// host-plus-owner string could produce.
//
// The declared hosts are the fallback, for an owner whose listing came
// back empty. That case matters more than it looks: an empty listing is
// exactly when every local clone is an orphan, so a prefix that fails to
// match there does not merely miss one repository, it silently disables
// the whole comparison.
func OwnerPrefixes(f Forge, owner, baseURL string, cloneURLs []string) []string {
	seen := map[string]struct{}{}
	for _, raw := range cloneURLs {
		if p := identityPrefix(raw); p != "" {
			seen[p] = struct{}{}
		}
	}

	if len(seen) == 0 {
		owner = strings.ToLower(strings.Trim(strings.TrimSpace(owner), "/"))
		if owner == "" {
			return nil
		}
		hosts := map[string]struct{}{}
		if f != nil {
			for _, h := range f.Hosts() {
				hosts[strings.ToLower(h)] = struct{}{}
			}
		}
		if h := hostFromRemote(baseURL); h != "" {
			hosts[strings.ToLower(h)] = struct{}{}
		}
		for h := range hosts {
			seen[h+"/"+owner+"/"] = struct{}{}
		}
	}

	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// identityPrefix reduces a clone URL to everything before its final path
// segment: "https://codeberg.org/forgejo/meta.git" becomes
// "codeberg.org/forgejo/".
//
// Built on git.CanonicalRemote so a prefix is produced by exactly the
// function that produces the identities it will be matched against. An
// earlier version parsed the URL here instead, which was both longer and
// a second place for the two to disagree — and they would have, over
// ports, scp-style remotes and userinfo.
func identityPrefix(raw string) string {
	identity := git.CanonicalRemote(raw)
	last := strings.LastIndexByte(identity, '/')
	if last < 0 {
		return ""
	}
	// A bare "host/repo" has no owner segment to key on, and treating the
	// host as one would match every repository on it.
	if strings.Count(identity, "/") < 2 {
		return ""
	}
	return identity[:last+1]
}

// MatchesOwner reports whether a canonical remote identity falls under any
// of the prefixes.
//
// An empty prefix set matches nothing. That is the safe direction and it is
// deliberate: prefixes are empty only when the forge could not be
// determined, and the operation these guard is deletion.
func MatchesOwner(identity string, prefixes []string) bool {
	identity = strings.ToLower(identity)
	for _, p := range prefixes {
		if strings.HasPrefix(identity, p) {
			return true
		}
	}
	return false
}
