// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/github"
)

// withForgeSelection sets the run's forge and restores it afterwards. The
// seam carries the selection in package state rather than in fetchRepos's
// signature, so that widening it would not have rewritten every test that
// replaces that seam.
func withForgeSelection(t *testing.T, name, url string) {
	t.Helper()
	oldName, oldURL := fetchForgeName, fetchForgeURL
	fetchForgeName, fetchForgeURL = name, url
	t.Cleanup(func() { fetchForgeName, fetchForgeURL = oldName, oldURL })
}

// TestFetchFromForgeConvertsEveryFieldTheEngineReads is the seam's real
// job: a forge.Repo has to arrive as the shape the engine, the layout and
// the TUI all read, with nothing quietly dropped.
//
// Driven against a real Gitea-shaped server rather than GitHub, because
// GitHub's path delegates to a client this package does not own — and
// because a non-GitHub forge reaching the engine at all is the change
// being tested.
func TestFetchFromForgeConvertsEveryFieldTheEngineReads(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/users/acme/repos") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`[{
			"id": 42, "name": "tool", "full_name": "acme/tool",
			"owner": {"login": "acme"},
			"language": "Rust", "private": true, "default_branch": "trunk",
			"clone_url": "https://git.example.com/acme/tool.git",
			"ssh_url": "git@git.example.com:acme/tool.git",
			"fork": false, "archived": false,
			"updated_at": "2026-05-06T07:08:09Z",
			"stars_count": 4, "template": true, "mirror": true
		}]`))
	}))
	defer srv.Close()

	withForgeSelection(t, "gitea", srv.URL)

	repos, err := fetchFromForge(context.Background(), "acme", github.FetchOptions{
		Limit: 7, Visibility: "all",
	})
	if err != nil {
		t.Fatalf("fetchFromForge: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("got %d repositories, want 1", len(repos))
	}

	r := repos[0]
	want := github.Repo{
		ID: 42, Owner: "acme", Name: "tool", FullName: "acme/tool",
		Language: "Rust", Visibility: "Private", DefaultBranch: "trunk",
		CloneURL: "https://git.example.com/acme/tool.git",
		SSHURL:   "git@git.example.com:acme/tool.git",
		Stars:    4, IsTemplate: true, IsMirror: true,
		PushedAt: r.PushedAt, // asserted separately, below
	}
	if r != want {
		t.Errorf("a field was lost in the conversion:\n got  %+v\n want %+v", r, want)
	}
	// PushedAt drives the decision to skip a pull, so a zero here would
	// silently make every sync unconditional.
	if r.PushedAt.IsZero() {
		t.Error("PushedAt was dropped; the sync decision depends on it")
	}
	if got := r.PushedAt.UTC().Format("2006-01-02T15:04:05Z"); got != "2026-05-06T07:08:09Z" {
		t.Errorf("PushedAt = %s", got)
	}
}

// TestFetchFromForgeDefaultsToGitHub: the selection is empty when nothing
// asked for a forge, and the default must not have moved.
func TestFetchFromForgeDefaultsToGitHub(t *testing.T) {
	withForgeSelection(t, "", "")
	// Resolving is all this asserts — actually listing would reach the
	// network. An unknown forge fails before any request, which is what
	// distinguishes the two outcomes here.
	_, err := fetchFromForge(context.Background(), "", github.FetchOptions{Limit: 1})
	if err != nil && strings.Contains(err.Error(), "unknown forge") {
		t.Errorf("an empty selection should resolve to github, got %v", err)
	}
}

func TestFetchFromForgeReportsAnUnknownForge(t *testing.T) {
	withForgeSelection(t, "gitub", "")
	_, err := fetchFromForge(context.Background(), "acme", github.FetchOptions{})
	if err == nil {
		t.Fatal("an unknown forge should be an error, not a silent GitHub fetch")
	}
	if !strings.Contains(err.Error(), "gitub") {
		t.Errorf("the error should quote what was typed: %v", err)
	}
}

func TestFetchFromForgePropagatesAListingError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	withForgeSelection(t, "gitea", srv.URL)
	if _, err := fetchFromForge(context.Background(), "nobody", github.FetchOptions{}); err == nil {
		t.Error("an owner that does not exist should be an error, not an empty list")
	}
}

func TestForgeTokenForSync(t *testing.T) {
	// The exported resolver is what `corralctl sync` uses. It differs from
	// the listing path in one way: GitHub's token is resolved here, because
	// the target does not carry the listing client's auth ladder.
	t.Setenv("GITHUB_TOKEN", "gh-env")
	t.Setenv("GH_TOKEN", "")
	if got := ForgeToken(context.Background(), "github", github.AuthModeToken); got != "gh-env" {
		t.Errorf("github sync token = %q", got)
	}
	t.Setenv("CORRAL_GITLAB_TOKEN", "")
	t.Setenv("GITLAB_TOKEN", "gl-env")
	if got := ForgeToken(context.Background(), "gitlab", github.AuthModeAuto); got != "gl-env" {
		t.Errorf("gitlab sync token = %q", got)
	}
}

func TestForgeTokenSources(t *testing.T) {
	// GitHub's credential is resolved inside its own client, which knows
	// the ladder from an explicit token through the environment to the gh
	// CLI. Supplying one here would bypass all of that.
	if got := forgeToken(context.Background(), "github", github.AuthModeAuto); got != "" {
		t.Errorf("github token = %q, want empty — its client resolves its own", got)
	}

	t.Setenv("CORRAL_GITLAB_TOKEN", "")
	t.Setenv("GITLAB_TOKEN", "from-gitlab-env")
	t.Setenv("CI_JOB_TOKEN", "")
	if got := forgeToken(context.Background(), "gitlab", github.AuthModeAuto); got != "from-gitlab-env" {
		t.Errorf("gitlab token = %q", got)
	}
	// A corral-specific name wins, so somebody can point corral at a
	// different instance than their shell is already configured for.
	t.Setenv("CORRAL_GITLAB_TOKEN", "from-corral")
	if got := forgeToken(context.Background(), "gitlab", github.AuthModeAuto); got != "from-corral" {
		t.Errorf("gitlab token = %q, want the corral-specific variable to win", got)
	}

	t.Setenv("CORRAL_BITBUCKET_TOKEN", "")
	t.Setenv("BITBUCKET_TOKEN", "bb-env")
	if got := forgeToken(context.Background(), "bitbucket", github.AuthModeAuto); got != "bb-env" {
		t.Errorf("bitbucket token = %q", got)
	}
	t.Setenv("CORRAL_BITBUCKET_TOKEN", "bb-corral")
	if got := forgeToken(context.Background(), "bitbucket", github.AuthModeAuto); got != "bb-corral" {
		t.Errorf("bitbucket token = %q, want the corral-specific variable to win", got)
	}

	t.Setenv("CORRAL_FORGE_TOKEN", "")
	t.Setenv("FORGEJO_TOKEN", "")
	t.Setenv("CODEBERG_TOKEN", "")
	t.Setenv("GITEA_TOKEN", "gitea-env")
	for _, name := range []string{"gitea", "forgejo", "codeberg"} {
		if got := forgeToken(context.Background(), name, github.AuthModeAuto); got != "gitea-env" {
			t.Errorf("%s token = %q", name, got)
		}
	}
}

func TestFirstEnv(t *testing.T) {
	t.Setenv("CORRAL_TEST_A", "")
	t.Setenv("CORRAL_TEST_B", "   ")
	t.Setenv("CORRAL_TEST_C", "value")
	if got := firstEnv("CORRAL_TEST_A", "CORRAL_TEST_B", "CORRAL_TEST_C"); got != "value" {
		t.Errorf("firstEnv = %q, want value — empty and blank should be skipped", got)
	}
	if got := firstEnv("CORRAL_TEST_MISSING"); got != "" {
		t.Errorf("firstEnv = %q, want empty", got)
	}
	if got := firstEnv(); got != "" {
		t.Errorf("firstEnv() = %q, want empty", got)
	}
}

// TestRunEFailsFastOnAnUnknownForge: validating at the fetch would mean a
// typo costs a workspace scan first, and then reports a failure that looks
// like it came from the network.
func TestRunEFailsFastOnAnUnknownForge(t *testing.T) {
	called := false
	old := fetchRepos
	fetchRepos = func(context.Context, string, github.FetchOptions) ([]github.Repo, error) {
		called = true
		return nil, nil
	}
	t.Cleanup(func() { fetchRepos = old })

	err := RunE(context.Background(), RunOptions{
		Owner:       "acme",
		BaseDir:     t.TempDir(),
		Concurrency: 1,
		Protocol:    "https",
		Forge:       "gitub",
	})
	if err == nil {
		t.Fatal("an unknown forge should fail the run")
	}
	if !strings.Contains(err.Error(), "gitub") {
		t.Errorf("the error should quote what was typed: %v", err)
	}
	if called {
		t.Error("the fetch should not have been attempted")
	}
}

// TestFindOrphansIsForgeAware is the gap multi-forge cloning left behind.
//
// Orphan detection matched a hardcoded "github.com/<owner>/" prefix, so a
// Codeberg clone was skipped rather than compared — `--orphans` reported
// nothing and looked like it had run.
func TestFindOrphansIsForgeAware(t *testing.T) {
	base := t.TempDir()
	mk := func(rel, remote string) string {
		t.Helper()
		dir := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o750); err != nil {
			t.Fatal(err)
		}
		cfg := "[remote \"origin\"]\n\turl = " + remote + "\n"
		if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	orphan := mk("Public/other/deleted-upstream", "https://codeberg.org/acme/deleted-upstream.git")
	mk("Public/other/kept", "https://codeberg.org/acme/kept.git")
	// The same owner name on a different host. Matching it is the bug the
	// hardcoded prefix was originally added to prevent, so it must stay
	// prevented.
	mk("Public/other/elsewhere", "https://github.com/acme/elsewhere.git")

	upstream := []github.Repo{
		{Name: "kept", FullName: "acme/kept", CloneURL: "https://codeberg.org/acme/kept.git"},
	}

	got := findOrphansOn("acme", base, upstream, "codeberg", "")
	if len(got) != 1 {
		t.Fatalf("found %v, want exactly the Codeberg orphan", got)
	}
	if got[0] != orphan {
		t.Errorf("found %q, want %q", got[0], orphan)
	}
}

// TestFindOrphansStillScopesByHost: the fix must not reintroduce the
// cross-host false positive it replaced.
func TestFindOrphansStillScopesByHost(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "Public", "go", "elsewhere")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := "[remote \"origin\"]\n\turl = https://gitlab.com/acme/elsewhere.git\n"
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	// Listing GitHub. A GitLab clone under the same owner name is not a
	// GitHub orphan.
	upstream := []github.Repo{
		{Name: "something", FullName: "acme/something", CloneURL: "https://github.com/acme/something.git"},
	}
	if got := findOrphansOn("acme", base, upstream, "github", ""); len(got) != 0 {
		t.Errorf("a clone on another host was reported as an orphan: %v", got)
	}
}

// TestFindOrphansWithNoPrefixesFindsNothing: no prefixes means nothing can
// be identified as this owner's, and the caller's answer to an orphan is
// deletion.
func TestFindOrphansWithNoPrefixesFindsNothing(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "Public", "go", "anything")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := "[remote \"origin\"]\n\turl = https://git.example.com/acme/anything.git\n"
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	// A self-hosted forge with no instance URL and an empty listing: there
	// is nothing to derive a prefix from.
	if got := findOrphansOn("acme", base, nil, "gitea", ""); len(got) != 0 {
		t.Errorf("expected nothing without a prefix, got %v", got)
	}
	// An unresolvable forge likewise, rather than a panic or a default.
	if got := findOrphansOn("acme", base, nil, "gitub", ""); len(got) != 0 {
		t.Errorf("expected nothing for an unknown forge, got %v", got)
	}
}

// TestFetchAnnouncementNamesTheForge: the announcement is the only
// confirmation a user gets that --forge took effect, and it said "GitHub"
// unconditionally — so a Codeberg run reported the wrong service.
func TestFetchAnnouncementNamesTheForge(t *testing.T) {
	for name, tc := range map[string]struct {
		opts RunOptions
		want string
	}{
		"default": {
			RunOptions{}, "Fetching repositories from github...",
		},
		"named forge": {
			RunOptions{Forge: "codeberg"}, "Fetching repositories from codeberg...",
		},
		// For a self-hosted deployment the instance matters more than the
		// software: two of them run the same forge.
		"self-hosted names the instance": {
			RunOptions{Forge: "gitea", ForgeURL: "https://git.example.com"},
			"Fetching repositories from gitea (https://git.example.com)...",
		},
		"inferred from the url": {
			RunOptions{ForgeURL: "https://codeberg.org"},
			"Fetching repositories from codeberg (https://codeberg.org)...",
		},
		// An unresolvable forge is rejected by RunE long before here, so
		// this only has to stay readable rather than be right.
		"unknown forge falls back": {
			RunOptions{Forge: "gitub"}, "Fetching repositories from GitHub...",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := fetchAnnouncement(tc.opts); got != tc.want {
				t.Errorf("fetchAnnouncement = %q, want %q", got, tc.want)
			}
		})
	}
}

// makeCloneAt creates a fixture clone with a real origin, so a test that
// syncs it exercises the origin check rather than skipping it.
//
// Fixtures used to create a bare ".git" directory with no config. That
// silently disabled originMismatch — the guard against pulling one
// project's history into another project's directory — so tests asserting
// "dry run pull" were asserting it without the check having run.
func makeCloneAt(t *testing.T, dir, remote string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := "[remote \"origin\"]\n\turl = " + remote + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestOriginIdentityUsesTheCloneURL is the bug v0.0.30 shipped: the
// identity was "github.com/" + FullName, which only ever agreed with a
// real remote because GitHub's clone URLs happen to take that shape.
//
// On any other forge it disagreed with every clone, and originMismatch
// reads a disagreement as an origin collision — so cloning worked and the
// second run failed with "expected github.com/...".
func TestOriginIdentityUsesTheCloneURL(t *testing.T) {
	for name, tc := range map[string]struct {
		repo github.Repo
		want string
	}{
		"codeberg": {
			github.Repo{
				FullName: "forgejo/meta",
				CloneURL: "https://codeberg.org/forgejo/meta.git",
			},
			"codeberg.org/forgejo/meta",
		},
		"gitlab nested group": {
			github.Repo{
				FullName: "group/sub/proj",
				CloneURL: "https://gitlab.com/group/sub/proj.git",
			},
			"gitlab.com/group/sub/proj",
		},
		"self-hosted": {
			github.Repo{
				FullName: "team/tool",
				CloneURL: "https://git.example.com/team/tool.git",
			},
			"git.example.com/team/tool",
		},
		"github is unchanged": {
			github.Repo{
				FullName: "acme/tool",
				CloneURL: "https://github.com/acme/tool.git",
			},
			"github.com/acme/tool",
		},
		// The fallbacks assume github.com because nothing else can be
		// inferred without a URL. No forge adapter omits one; this pins
		// that they stay behind the URL rather than in front of it.
		"no clone url falls back": {
			github.Repo{FullName: "acme/tool"}, "github.com/acme/tool",
		},
		"owner and name fall back": {
			github.Repo{Owner: "Acme", Name: "Tool"}, "github.com/acme/tool",
		},
		"nothing to go on": {github.Repo{}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := repoRemoteIdentity(tc.repo); got != tc.want {
				t.Errorf("repoRemoteIdentity = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestOriginMismatchAcceptsAMatchingNonGitHubClone is the end-to-end half:
// the identity has to agree with what CanonicalRemote produces for the
// local clone, or every sync off GitHub reports a collision.
func TestOriginMismatchAcceptsAMatchingNonGitHubClone(t *testing.T) {
	dir := t.TempDir()
	makeCloneAt(t, dir, "https://codeberg.org/forgejo/meta.git")

	repo := github.Repo{
		Name: "meta", FullName: "forgejo/meta",
		CloneURL: "https://codeberg.org/forgejo/meta.git",
	}
	if msg, mismatch := originMismatch(repo, dir); mismatch {
		t.Errorf("a matching Codeberg clone was reported as a collision: %s", msg)
	}

	// And a genuine collision is still caught, with both hosts named.
	other := t.TempDir()
	makeCloneAt(t, other, "https://codeberg.org/someone-else/different.git")
	msg, mismatch := originMismatch(repo, other)
	if !mismatch {
		t.Fatal("a clone pointing elsewhere must be reported")
	}
	for _, want := range []string{"someone-else/different", "forgejo/meta"} {
		if !strings.Contains(msg, want) {
			t.Errorf("the message should name %q: %s", want, msg)
		}
	}
}

// TestSearchSelectorsAreRejectedOffGitHub: topic: and language: are
// GitHub search queries. Passed to another forge they were read as an
// owner name, and the 404 blamed the owner — sending somebody to look for
// a typo that was not there.
func TestSearchSelectorsAreRejectedOffGitHub(t *testing.T) {
	for _, owner := range []string{"topic:forge", "language:go"} {
		err := RunE(context.Background(), RunOptions{
			Owner:       owner,
			BaseDir:     t.TempDir(),
			Concurrency: 1,
			Protocol:    "https",
			Forge:       "codeberg",
		})
		if err == nil {
			t.Fatalf("%s should be rejected on a forge without search", owner)
		}
		for _, want := range []string{"codeberg", owner, "owner"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: the error should mention %q: %v", owner, want, err)
			}
		}
	}
}

// TestCloneURLDerivesTheSSHHost: the fallback used to be
// "git@github.com:owner/name.git", which is reachable — a Gitea or
// Forgejo instance with SSH disabled returns an empty ssh_url — and whose
// consequence is not a failure but something worse: cloning a different
// repository, from a host the user never named, that happens to share an
// owner and name.
func TestCloneURLDerivesTheSSHHost(t *testing.T) {
	for name, tc := range map[string]struct {
		repo     github.Repo
		protocol string
		want     string
	}{
		"https uses the clone url": {
			github.Repo{CloneURL: "https://codeberg.org/acme/tool.git", SSHURL: "git@codeberg.org:acme/tool.git"},
			"https", "https://codeberg.org/acme/tool.git",
		},
		"ssh prefers the forge's own": {
			github.Repo{CloneURL: "https://codeberg.org/acme/tool.git", SSHURL: "git@codeberg.org:acme/tool.git"},
			"ssh", "git@codeberg.org:acme/tool.git",
		},
		"ssh derives the host when the forge gives none": {
			github.Repo{Name: "tool", CloneURL: "https://git.example.com/team/tool.git"},
			"ssh", "git@git.example.com:team/tool.git",
		},
		"a nested gitlab group keeps its full path": {
			github.Repo{Name: "proj", CloneURL: "https://gitlab.com/group/sub/proj.git"},
			"ssh", "git@gitlab.com:group/sub/proj.git",
		},
		// Nothing to derive from: the conventional GitHub form is the only
		// remaining guess, and it is the one that was correct before other
		// forges existed.
		"no urls at all": {
			github.Repo{Name: "tool"}, "ssh", "git@github.com:acme/tool.git",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := cloneURL(tc.repo, "acme", tc.protocol); got != tc.want {
				t.Errorf("cloneURL = %q, want %q", got, tc.want)
			}
		})
	}
}
