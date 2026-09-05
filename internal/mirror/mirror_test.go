// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mirror

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/forge"
	"github.com/sebastienrousseau/corralctl/internal/git"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fakeTarget is a forge.Target whose every answer is scripted.
type fakeTarget struct {
	login     string
	loginErr  error
	remote    forge.Remote
	ensureErr error
	mu        sync.Mutex
	ensured   []string
}

func (f *fakeTarget) Whoami(context.Context, forge.TargetOptions) (string, error) {
	return f.login, f.loginErr
}

func (f *fakeTarget) EnsureRepo(_ context.Context, owner string, spec forge.RepoSpec, _ forge.TargetOptions) (forge.Remote, error) {
	f.mu.Lock()
	f.ensured = append(f.ensured, owner+"/"+spec.Name)
	f.mu.Unlock()
	return f.remote, f.ensureErr
}

func (f *fakeTarget) PushAuth(opts forge.TargetOptions) (string, string) { return "user", opts.Token }

func (f *fakeTarget) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.ensured)
}

func withSeams(t *testing.T) {
	t.Helper()
	oldEmpty, oldOrigin, oldRemote, oldPush := isEmpty, originURL, ensureRemote, pushMirror
	t.Cleanup(func() { isEmpty, originURL, ensureRemote, pushMirror = oldEmpty, oldOrigin, oldRemote, oldPush })
	isEmpty = func(context.Context, string) bool { return false }
	originURL = func(string) (string, error) { return "git@github.com:me/repo.git", nil }
	ensureRemote = func(context.Context, string, string, string) error { return nil }
	pushMirror = func(context.Context, string, string, string, *git.PushCredential) error { return nil }
}

func okRemote() forge.Remote {
	return forge.Remote{HTTPSURL: "https://gitlab.example.com/me/repo.git", SSHURL: "git@gitlab.example.com:me/repo.git", Private: true}
}

func dest(name, host string, tg forge.Target) Destination {
	return Destination{Name: name, Host: host, Target: tg, Options: forge.TargetOptions{Token: "tok"}, Protocol: "https"}
}

// collect returns a Report callback and the slice it fills.
func collect() (func(Result), *[]Result) {
	var mu sync.Mutex
	var out []Result
	return func(r Result) {
		mu.Lock()
		out = append(out, r)
		mu.Unlock()
	}, &out
}

func find(results []Result, repo, dest string) (Result, bool) {
	for _, r := range results {
		if r.Repo == repo && r.Destination == dest {
			return r, true
		}
	}
	return Result{}, false
}

func TestRunMirrorsEveryRepositoryIntoEveryDestination(t *testing.T) {
	withSeams(t)
	var pushed []string
	var mu sync.Mutex
	pushMirror = func(_ context.Context, path, remote, url string, cred *git.PushCredential) error {
		mu.Lock()
		defer mu.Unlock()
		pushed = append(pushed, remote+" "+url+" "+cred.Username+":"+cred.Secret)
		return nil
	}
	gl := &fakeTarget{login: "me", remote: okRemote()}
	created := okRemote()
	created.Created = true
	gt := &fakeTarget{login: "me", remote: created}
	report, results := collect()
	got := Run(context.Background(), Options{
		Repos:        []Repo{{Name: "a", Path: "/a", Private: true}, {Name: "b", Path: "/b", Private: true}},
		Destinations: []Destination{dest("gitlab", "gitlab.example.com", gl), dest("gitea", "gitea.example.com", gt)},
		Concurrency:  3,
		Timeout:      time.Second,
		Report:       report,
	})
	if got != (Summary{Mirrored: 4}) {
		t.Fatalf("summary = %+v", got)
	}
	if gl.calls() != 2 || gt.calls() != 2 || len(pushed) != 4 {
		t.Fatalf("calls = %d/%d, pushes = %v", gl.calls(), gt.calls(), pushed)
	}
	if r, ok := find(*results, "a", "gitea"); !ok || r.Action != ActionMirror || !r.Created || r.Message != "created and mirrored" || r.URL != created.HTTPSURL {
		t.Fatalf("gitea result = %+v", r)
	}
	if r, _ := find(*results, "a", "gitlab"); r.Message != "mirrored" || r.Created {
		t.Fatalf("gitlab result = %+v", r)
	}
	for _, p := range pushed {
		if p != "gitlab https://gitlab.example.com/me/repo.git user:tok" && p != "gitea https://gitlab.example.com/me/repo.git user:tok" {
			t.Fatalf("unexpected push %q", p)
		}
	}
}

func TestRunUsesSSHWhenAsked(t *testing.T) {
	withSeams(t)
	var url string
	var cred *git.PushCredential
	pushMirror = func(_ context.Context, _, _, u string, c *git.PushCredential) error { url, cred = u, c; return nil }
	d := dest("gitlab", "gitlab.example.com", &fakeTarget{login: "me", remote: okRemote()})
	d.Protocol = "ssh"
	got := Run(context.Background(), Options{Repos: []Repo{{Name: "a", Path: "/a", Private: true}}, Destinations: []Destination{d}, Timeout: time.Second})
	if got.Mirrored != 1 || url != "git@gitlab.example.com:me/repo.git" || cred != nil {
		t.Fatalf("ssh push = %+v %q %v", got, url, cred)
	}
}

func TestRunDefaultsTheOwnerToTheLogin(t *testing.T) {
	withSeams(t)
	tg := &fakeTarget{login: "alice", remote: okRemote()}
	named := dest("gitlab", "gitlab.example.com", tg)
	named.Owner = "team"
	Run(context.Background(), Options{
		Repos:        []Repo{{Name: "a", Path: "/a", Private: true}},
		Destinations: []Destination{dest("codeberg", "codeberg.org", tg), named},
		Timeout:      time.Second,
	})
	if len(tg.ensured) != 2 || tg.ensured[0] != "alice/a" || tg.ensured[1] != "team/a" {
		t.Fatalf("ensured = %v", tg.ensured)
	}
}

func TestRunReportsADestinationThatCannotAuthenticate(t *testing.T) {
	withSeams(t)
	bad := &fakeTarget{loginErr: errors.New("401")}
	good := &fakeTarget{login: "me", remote: okRemote()}
	report, results := collect()
	got := Run(context.Background(), Options{
		Repos:        []Repo{{Name: "a", Path: "/a", Private: true}, {Name: "b", Path: "/b", Private: true}},
		Destinations: []Destination{dest("gitlab", "gitlab.example.com", bad), dest("gitea", "gitea.example.com", good)},
		Timeout:      time.Second,
		Report:       report,
	})
	// One error for the destination, not one per repository; the other
	// destination still receives both.
	if got != (Summary{Mirrored: 2, Errors: 1}) || bad.calls() != 0 || good.calls() != 2 {
		t.Fatalf("summary = %+v, calls = %d/%d", got, bad.calls(), good.calls())
	}
	if r, ok := find(*results, "", "gitlab"); !ok || r.Action != ActionError || r.Message != "cannot authenticate: 401" {
		t.Fatalf("destination error = %+v", r)
	}
}

func TestRunSkipsAndGuards(t *testing.T) {
	withSeams(t)
	gl := &fakeTarget{login: "me", remote: okRemote()}
	gh := &fakeTarget{login: "me", remote: okRemote()}
	dests := []Destination{dest("gitlab", "gitlab.example.com", gl), dest("github", "github.com", gh)}
	report, results := collect()

	// The origin guard: the seam says origin is github.com, so github is
	// skipped and gitlab is not — in a real run and in a dry run.
	for _, dry := range []bool{false, true} {
		gl.ensured, gh.ensured = nil, nil
		*results = nil
		got := Run(context.Background(), Options{Repos: []Repo{{Name: "a", Path: "/a", Private: true}}, Destinations: dests, Timeout: time.Second, DryRun: dry, Report: report})
		if got.Skipped != 1 || got.Mirrored != 1 || got.Errors != 0 || gh.calls() != 0 {
			t.Fatalf("dry=%v: summary = %+v, github calls = %d", dry, got, gh.calls())
		}
		want := ActionMirror
		if dry {
			want = ActionDryRun
			if gl.calls() != 0 {
				t.Fatalf("a dry run must not create: %v", gl.ensured)
			}
		}
		if r, _ := find(*results, "a", "gitlab"); r.Action != want {
			t.Fatalf("dry=%v: gitlab = %+v", dry, r)
		}
		if r, _ := find(*results, "a", "github"); r.Action != ActionSkip {
			t.Fatalf("dry=%v: github = %+v", dry, r)
		}
	}

	// An empty repository is skipped everywhere, before any request.
	isEmpty = func(context.Context, string) bool { return true }
	gl.ensured = nil
	got := Run(context.Background(), Options{Repos: []Repo{{Name: "a", Path: "/a"}}, Destinations: dests, Timeout: time.Second})
	if got.Skipped != 2 || gl.calls() != 0 {
		t.Fatalf("empty: summary = %+v", got)
	}
	isEmpty = func(context.Context, string) bool { return false }

	// An unreadable origin cannot fire the guard; the push proceeds.
	originURL = func(string) (string, error) { return "", errors.New("unreadable") }
	got = Run(context.Background(), Options{Repos: []Repo{{Name: "a", Path: "/a", Private: true}}, Destinations: dests, Timeout: time.Second})
	if got.Mirrored != 2 {
		t.Fatalf("unreadable origin: summary = %+v", got)
	}

	// A repository the walk marked as colliding is an error on every
	// destination, and nothing is asked of any forge.
	gl.ensured = nil
	got = Run(context.Background(), Options{Repos: []Repo{{Name: "dup", Path: "/x", Conflict: "not mirrored: shares the name"}}, Destinations: dests, Timeout: time.Second})
	if got.Errors != 2 || gl.calls() != 0 {
		t.Fatalf("conflict: summary = %+v", got)
	}

	// A name no forge could hold is an error on every destination, and
	// nothing is asked of any forge.
	gl.ensured = nil
	got = Run(context.Background(), Options{Repos: []Repo{{Name: "bad name", Path: "/x"}}, Destinations: dests, Timeout: time.Second})
	if got.Errors != 2 || gl.calls() != 0 {
		t.Fatalf("bad name: summary = %+v", got)
	}
}

func TestRunReportsFailures(t *testing.T) {
	withSeams(t)
	repos := []Repo{{Name: "a", Path: "/a", Private: true}}
	run := func(tg *fakeTarget) Result {
		report, results := collect()
		Run(context.Background(), Options{Repos: repos, Destinations: []Destination{dest("gitlab", "gitlab.example.com", tg)}, Timeout: time.Second, Report: report})
		r, _ := find(*results, "a", "gitlab")
		return r
	}
	if r := run(&fakeTarget{login: "me", ensureErr: errors.New("quota")}); r.Action != ActionError || r.Message != "quota" {
		t.Fatalf("ensure error = %+v", r)
	}
	public := okRemote()
	public.Private = false
	if r := run(&fakeTarget{login: "me", remote: public}); r.Action != ActionError || !contains(r.Message, "wrong visibility") {
		t.Fatalf("visibility mismatch = %+v", r)
	}
	local := okRemote()
	local.HTTPSURL = "file:///tmp/repo"
	if r := run(&fakeTarget{login: "me", remote: local}); r.Action != ActionError || !contains(r.Message, "unusable push URL") {
		t.Fatalf("bad url = %+v", r)
	}
	ensureRemote = func(context.Context, string, string, string) error { return errors.New("remote") }
	if r := run(&fakeTarget{login: "me", remote: okRemote()}); r.Action != ActionError || r.Message != "remote" {
		t.Fatalf("remote error = %+v", r)
	}
	ensureRemote = func(context.Context, string, string, string) error { return nil }
	pushMirror = func(context.Context, string, string, string, *git.PushCredential) error { return errors.New("push") }
	if r := run(&fakeTarget{login: "me", remote: okRemote()}); r.Action != ActionError || r.Message != "push" {
		t.Fatalf("push error = %+v", r)
	}
}

func TestRunHonoursCancellationAndNilReport(t *testing.T) {
	withSeams(t)
	tg := &fakeTarget{login: "me", remote: okRemote()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := Run(ctx, Options{Repos: []Repo{{Name: "a", Path: "/a"}}, Destinations: []Destination{dest("gitlab", "gitlab.example.com", tg)}, Concurrency: 0, Timeout: time.Second})
	if got.Mirrored != 0 || tg.calls() != 0 {
		t.Fatalf("cancelled run did work: %+v", got)
	}
}

func TestHostOf(t *testing.T) {
	cases := map[string]string{
		"https://GitLab.com/o/r.git":               "gitlab.com",
		"ssh://git@gitea.example.com:2222/o/r.git": "gitea.example.com",
		"git@github.com:o/r.git":                   "github.com",
		"https://gitea.example.com:3000/o/r":       "gitea.example.com",
		"":                                         "",
		"/srv/git/repo.git":                        "",
		"../relative":                              "",
		"C:\\repos\\r":                             "",
		"file:///srv/git/r.git":                    "",
		"http://[::1/x":                            "",
		"user@host:o/r":                            "host",
		"@:o/r":                                    "",
		":o/r":                                     "",
		"a b:o/r":                                  "",
	}
	for raw, want := range cases {
		if got := hostOf(raw); got != want {
			t.Errorf("hostOf(%q) = %q, want %q", raw, got, want)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
