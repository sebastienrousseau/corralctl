// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package forge

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// The receiving side.
//
// A Forge lists what an owner has, which is what `corralctl clone` needs.
// `corralctl sync` needs the opposite: given a local repository, make sure
// a place for it exists on a forge and say where to push. That is a second,
// smaller contract — two calls — and every forge here implements both, so
// the same six names serve as sources and as destinations.
//
// The contract is deliberately narrow. A Target creates a repository and
// reports its clone URLs and visibility; it does not push, because pushing
// is git's job and is done identically for every forge. It does not delete,
// rename or change visibility, because a mirror that reconciles remote
// settings is a different tool with a different blast radius.

// Target is a forge that can hold a mirror of a local repository.
type Target interface {
	// Whoami returns the login the credential belongs to. It is the owner
	// a mirror lands under when the user names none, and what decides
	// whether a repository is created under a person or an organisation.
	Whoami(ctx context.Context, opts TargetOptions) (string, error)

	// EnsureRepo returns owner/spec.Name on the forge, creating it when it
	// does not exist. An existing repository is not an error; the caller
	// compares the returned visibility with what it wanted.
	EnsureRepo(ctx context.Context, owner string, spec RepoSpec, opts TargetOptions) (Remote, error)

	// PushAuth returns the HTTP basic-auth pair git should present when
	// pushing over HTTPS with this credential. Each forge spells the
	// username differently; the secret is always the token.
	PushAuth(opts TargetOptions) (username, secret string)
}

// RepoSpec describes the repository a Target should ensure.
type RepoSpec struct {
	// Name is the repository name, which becomes its path on the forge.
	Name string
	// Description is optional and is only applied on creation.
	Description string
	// Private is the visibility to create with.
	Private bool
}

// Remote is what a Target knows about a repository it ensured.
type Remote struct {
	// HTTPSURL and SSHURL are the clone URLs, for the two push protocols.
	HTTPSURL string
	SSHURL   string
	// Private is the repository's actual visibility, whether it was just
	// created or already there.
	Private bool
	// Created reports that this call created the repository.
	Created bool
}

// TargetOptions carries the credential and instance for a Target call.
type TargetOptions struct {
	// Token authenticates every request. Creating a repository is never
	// anonymous, so unlike Options.Token this one is required.
	Token string
	// BaseURL overrides the host, for a self-hosted instance. Empty means
	// the forge's public instance.
	BaseURL string
	// RequestTimeout bounds one HTTP request.
	RequestTimeout time.Duration
	// Login is the authenticated user, as Whoami reported it. The caller
	// resolves it once and passes it back so EnsureRepo can tell a
	// personal owner from an organisation without a request per
	// repository.
	Login string
}

// AsTarget reports whether f can receive a mirror, and returns it as one.
//
// Every registered forge does today. The check exists so a future forge
// that can only list — a read-only archive, say — is refused with a clear
// message rather than a panic at the first push.
func AsTarget(f Forge) (Target, bool) {
	t, ok := f.(Target)
	return t, ok
}

// ErrNoToken is returned when a Target is asked to act without a
// credential. Listing public repositories can be anonymous; creating one
// never is, and the request would only fail later with a less useful
// message.
var ErrNoToken = errors.New("no token: creating a repository requires a credential")

// ValidateRepoName rejects a name that could not be a repository path on
// any forge, or that would be read as something else by one of them.
//
// The set is the intersection of what the six forges accept, and it is
// checked here rather than left to each API because a bad name produces a
// different, and differently unhelpful, error on each.
func ValidateRepoName(name string) error {
	switch {
	case name == "", name == ".", name == "..":
		return fmt.Errorf("invalid repository name %q", name)
	case len(name) > 100:
		return fmt.Errorf("repository name %q is longer than 100 characters", name)
	case strings.HasPrefix(name, "-"):
		return fmt.Errorf("repository name %q starts with a dash, which git would read as an option", name)
	case strings.ContainsAny(name, "/\\ \t\r\n\x00"):
		return fmt.Errorf("repository name %q contains a separator or whitespace", name)
	}
	return nil
}

// ValidatePushURL accepts the two URL shapes a forge returns for a push and
// nothing else: credential-free HTTPS, and SSH in either its URL or its
// scp-like form. A local path, plain HTTP, an embedded credential or an
// option-like value is refused before it can reach git.
func ValidatePushURL(raw string) error {
	if raw == "" || strings.HasPrefix(raw, "-") || strings.ContainsAny(raw, "\r\n\x00") {
		return errors.New("invalid push URL")
	}
	if !strings.Contains(raw, "://") {
		// scp-like: user@host:path, with all three parts present.
		at := strings.IndexByte(raw, '@')
		colon := strings.IndexByte(raw, ':')
		if at <= 0 || colon <= at+1 || colon == len(raw)-1 || strings.HasPrefix(raw[colon+1:], "/") {
			return errors.New("invalid SSH push URL")
		}
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("invalid push URL")
	}
	switch u.Scheme {
	case "https":
		if u.User != nil {
			return errors.New("HTTPS push URL must not carry a credential")
		}
	case "ssh":
		if u.User == nil || u.User.Username() == "" {
			return errors.New("SSH push URL must name a user")
		}
	default:
		return fmt.Errorf("unsupported push URL scheme %q", u.Scheme)
	}
	return nil
}

// targetClient builds the REST client a Target call uses. A missing token
// is refused here, once, rather than discovered as a 401 on every forge.
func targetClient(base, tokenHeader, tokenPrefix string, opts TargetOptions) (*restClient, error) {
	if strings.TrimSpace(opts.Token) == "" {
		return nil, ErrNoToken
	}
	return newRESTClient(base, opts.Token, tokenHeader, tokenPrefix, Options{RequestTimeout: opts.RequestTimeout})
}

// lostCreateRace reports a create response that means the repository
// appeared between the existence check and the create — a 409, or the
// 400 GitLab and Bitbucket use for the same thing. The caller re-reads.
func lostCreateRace(err error) bool {
	var se *StatusError
	if !errors.As(err, &se) {
		return false
	}
	if se.Status == 409 {
		return true
	}
	body := strings.ToLower(se.Body)
	return se.Status == 400 && (strings.Contains(body, "already been taken") || strings.Contains(body, "already exists"))
}
