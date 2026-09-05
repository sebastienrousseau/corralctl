// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package mirror pushes a corral-organised tree out to other forges with
// parity semantics: every local branch and tag exists on the destination,
// and nothing else does.
//
// It is the engine behind `corralctl sync`, and the successor to the
// standalone corral-sync tool, whose crawler, orchestrator and safety
// checks it carries over. It owns concurrency; the forge package creates
// repositories and the git package pushes, and neither of those knows it
// is being run in parallel.
//
// # What it refuses
//
// A repository is never pushed to the forge its origin lives on. corral
// clones from six forges, so the tree can hold a clone whose origin *is*
// a destination, and mirroring it back would run `git push --prune`
// against its own upstream — with a single-branch clone, that deletes
// every branch the local copy does not carry.
//
// A destination whose existing repository has the wrong visibility is an
// error, not a silent reuse: a private clone must not be pushed into a
// public repository that happens to share its name.
package mirror

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sebastienrousseau/corral/internal/forge"
	"github.com/sebastienrousseau/corral/internal/git"
)

// Seams, so the orchestration is testable without a forge or a push.
var (
	isEmpty      = git.IsEmpty
	originURL    = git.RemoteOriginFromConfig
	ensureRemote = git.EnsureRemote
	pushMirror   = git.PushMirror
)

// Repo is one local repository to mirror.
type Repo struct {
	// Name is the directory's basename, which becomes the repository
	// name on every destination.
	Name string
	// Path is the absolute path of the working tree.
	Path string
	// Private is the visibility the destination is created with.
	Private bool
}

// Destination is one forge to mirror into.
type Destination struct {
	// Name is the forge name — "gitlab", "codeberg" — and doubles as the
	// git remote name on every local clone. It is stable on purpose:
	// changing it would leave an orphan remote in every repository.
	Name string
	// Host is the forge's hostname, for the origin check.
	Host string
	// Owner is the user, group, organisation or workspace to mirror
	// under. Empty means the account the credential belongs to.
	Owner string
	// Target is the forge's receiving side.
	Target forge.Target
	// Options carry the credential and instance for every call.
	Options forge.TargetOptions
	// Protocol selects the push transport: "https" or "ssh".
	Protocol string
}

// Options configure one run.
type Options struct {
	// Repos are the local repositories to mirror, in order.
	Repos []Repo
	// Destinations are the forges to mirror into. Each repository visits
	// every destination in sequence; repositories run in parallel.
	Destinations []Destination
	// Concurrency is the number of repositories in flight at once.
	Concurrency int
	// Timeout bounds the work for one repository on one destination.
	Timeout time.Duration
	// DryRun reports what would happen without creating or pushing.
	DryRun bool
	// Report receives every outcome as it happens. Optional.
	Report func(Result)
}

// Result is the outcome for one repository on one destination.
type Result struct {
	// Repo is the repository name; empty when the result concerns the
	// destination as a whole.
	Repo string `json:"repo,omitempty"`
	// Destination is the forge name.
	Destination string `json:"destination"`
	// Action is one of MIRROR, SKIP, DRY-RUN or ERROR.
	Action string `json:"action"`
	// Message says what happened, or why not.
	Message string `json:"message"`
	// URL is the push URL, when one was resolved.
	URL string `json:"url,omitempty"`
	// Created reports that this run created the destination repository.
	Created bool `json:"created,omitempty"`
}

// Action values.
const (
	ActionMirror = "MIRROR"
	ActionSkip   = "SKIP"
	ActionDryRun = "DRY-RUN"
	ActionError  = "ERROR"
)

// Summary counts a run.
type Summary struct {
	// Mirrored counts repository/destination pairs that were pushed, or
	// would have been on a dry run.
	Mirrored int `json:"mirrored"`
	// Skipped counts pairs deliberately not pushed: empty repositories,
	// and repositories whose origin is the destination.
	Skipped int `json:"skipped"`
	// Errors counts pairs that failed, plus destinations that could not
	// be used at all.
	Errors int `json:"errors"`
}

type counters struct {
	mirrored atomic.Int64
	skipped  atomic.Int64
	errors   atomic.Int64
	report   func(Result)
}

func (c *counters) emit(r Result) {
	switch r.Action {
	case ActionMirror, ActionDryRun:
		c.mirrored.Add(1)
	case ActionSkip:
		c.skipped.Add(1)
	case ActionError:
		c.errors.Add(1)
	}
	if c.report != nil {
		c.report(r)
	}
}

// Run mirrors every repository into every destination and returns the
// counts. It never returns an error: every failure is a Result, so one
// repository cannot stop the others, and the caller decides the exit code
// from Summary.Errors.
func Run(ctx context.Context, opts Options) Summary {
	c := &counters{report: opts.Report}
	dests := resolveDestinations(ctx, opts, c)

	workers := opts.Concurrency
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan Repo)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range jobs {
				processOne(ctx, r, dests, opts, c)
			}
		}()
	}
enqueue:
	for _, r := range opts.Repos {
		select {
		case <-ctx.Done():
			break enqueue
		case jobs <- r:
		}
	}
	close(jobs)
	wg.Wait()

	return Summary{
		Mirrored: int(c.mirrored.Load()),
		Skipped:  int(c.skipped.Load()),
		Errors:   int(c.errors.Load()),
	}
}

// resolved is a destination whose credential has been checked and whose
// owner is known.
type resolved struct {
	Destination
	login string
}

// resolveDestinations asks each destination who the credential belongs
// to, once. A destination that cannot answer is reported and dropped, so
// a bad token costs one error rather than one per repository — and,
// unlike a failure discovered mid-run, it is reported before any push.
func resolveDestinations(ctx context.Context, opts Options, c *counters) []resolved {
	out := make([]resolved, 0, len(opts.Destinations))
	for _, d := range opts.Destinations {
		login, err := d.Target.Whoami(ctx, d.Options)
		if err != nil {
			c.emit(Result{Destination: d.Name, Action: ActionError, Message: "cannot authenticate: " + err.Error()})
			continue
		}
		d.Options.Login = login
		if d.Owner == "" {
			d.Owner = login
		}
		out = append(out, resolved{Destination: d, login: login})
	}
	return out
}

// processOne mirrors one repository into every destination in sequence.
// Destinations are sequential per repository so two pushes never contend
// on the same .git directory.
//
// The two read-only inspections run in dry-run mode as well, so a dry run
// previews exactly the decisions a real run makes.
func processOne(ctx context.Context, r Repo, dests []resolved, opts Options, c *counters) {
	if err := forge.ValidateRepoName(r.Name); err != nil {
		for _, d := range dests {
			c.emit(Result{Repo: r.Name, Destination: d.Name, Action: ActionError, Message: err.Error()})
		}
		return
	}
	if isEmpty(ctx, r.Path) {
		for _, d := range dests {
			c.emit(Result{Repo: r.Name, Destination: d.Name, Action: ActionSkip, Message: "empty repository (no commits yet)"})
		}
		return
	}
	// An unreadable origin is treated as no origin: the guard cannot
	// fire, and a repository that broken fails at the push with git's own
	// message rather than silently here.
	origin, _ := originURL(r.Path)
	originHost := hostOf(origin)

	for _, d := range dests {
		if originHost != "" && strings.EqualFold(originHost, d.Host) {
			c.emit(Result{Repo: r.Name, Destination: d.Name, Action: ActionSkip,
				Message: "origin is on " + originHost + "; a repository is not mirrored onto its own forge"})
			continue
		}
		if opts.DryRun {
			c.emit(Result{Repo: r.Name, Destination: d.Name, Action: ActionDryRun,
				Message: fmt.Sprintf("would ensure %s/%s and push every branch and tag", d.Owner, r.Name)})
			continue
		}
		opCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
		res := mirrorOne(opCtx, r, d)
		cancel()
		c.emit(res)
	}
}

// mirrorOne is the unit of work: ensure the destination repository,
// point a remote at it, push.
func mirrorOne(ctx context.Context, r Repo, d resolved) Result {
	fail := func(err error) Result {
		return Result{Repo: r.Name, Destination: d.Name, Action: ActionError, Message: err.Error()}
	}
	remote, err := d.Target.EnsureRepo(ctx, d.Owner, forge.RepoSpec{Name: r.Name, Private: r.Private}, d.Options)
	if err != nil {
		return fail(err)
	}
	if remote.Private != r.Private {
		return fail(fmt.Errorf("%s/%s exists with the wrong visibility (private=%t, local is private=%t); refusing to push into it",
			d.Owner, r.Name, remote.Private, r.Private))
	}
	pushURL, cred := d.pushTarget(remote)
	if err := forge.ValidatePushURL(pushURL); err != nil {
		return fail(fmt.Errorf("%s returned an unusable push URL: %w", d.Name, err))
	}
	if err := ensureRemote(ctx, r.Path, d.Name, pushURL); err != nil {
		return fail(err)
	}
	if err := pushMirror(ctx, r.Path, d.Name, pushURL, cred); err != nil {
		return fail(err)
	}
	msg := "mirrored"
	if remote.Created {
		msg = "created and mirrored"
	}
	return Result{Repo: r.Name, Destination: d.Name, Action: ActionMirror, Message: msg, URL: pushURL, Created: remote.Created}
}

// pushTarget picks the URL for the configured protocol and, for HTTPS,
// the credential git should present to it.
func (d resolved) pushTarget(remote forge.Remote) (string, *git.PushCredential) {
	if strings.EqualFold(d.Protocol, "ssh") {
		return remote.SSHURL, nil
	}
	user, secret := d.Target.PushAuth(d.Options)
	return remote.HTTPSURL, &git.PushCredential{Username: user, Secret: secret}
}

// hostOf is the lower-cased hostname of a remote URL, in either of the
// spellings git accepts, or "" when there is none — a local path, a
// file:// URL, a Windows drive.
func hostOf(remote string) string {
	remote = strings.TrimSpace(remote)
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil {
			return ""
		}
		return strings.ToLower(u.Hostname())
	}
	// scp-like: [user@]host:path. A slash before the colon is a path, as
	// git reads it, and a single character before it is a drive letter.
	colon := strings.IndexByte(remote, ':')
	if colon <= 0 || strings.Contains(remote[:colon], "/") {
		return ""
	}
	host := remote[:colon]
	if at := strings.LastIndexByte(host, '@'); at >= 0 {
		host = host[at+1:]
	}
	if len(host) <= 1 || strings.ContainsAny(host, " \\") {
		return ""
	}
	return strings.ToLower(host)
}

// ErrNoDestinations is returned by callers that were given nothing to
// mirror into.
var ErrNoDestinations = errors.New("no destinations: pass --to at least once")
