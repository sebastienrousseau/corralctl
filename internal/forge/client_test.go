// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package forge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/github"
)

// The clients are driven against a real HTTP server rather than a stubbed
// transport. Pagination, headers, status handling and JSON decoding are
// the whole of what these clients do, and a stub that answers whatever it
// is asked would test none of them.

// noSleep removes the retry backoff so a retry test finishes immediately.
func noSleep(t *testing.T) {
	t.Helper()
	old := sleep
	sleep = func(time.Duration) {}
	t.Cleanup(func() { sleep = old })
}

// recordingServer serves handler and records every request path+query.
type recordingServer struct {
	*httptest.Server
	requests atomic.Int64
	headers  chan http.Header
}

func newRecordingServer(t *testing.T, handler http.HandlerFunc) *recordingServer {
	t.Helper()
	rs := &recordingServer{headers: make(chan http.Header, 64)}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.requests.Add(1)
		select {
		case rs.headers <- r.Header.Clone():
		default:
		}
		handler(w, r)
	}))
	t.Cleanup(rs.Close)
	return rs
}

// ---------------------------------------------------------------------------
// Gitea / Forgejo / Codeberg
// ---------------------------------------------------------------------------

// giteaPage renders one page of n repositories, named from the given
// offset, so a test can build a multi-page response without a fixture.
func giteaPage(from, n int, private bool) string {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		name := "repo" + strconv.Itoa(from+i)
		fmt.Fprintf(&b, `{
			"id": %d, "name": %q, "full_name": %q,
			"owner": {"login": "acme"},
			"language": "Go", "private": %t, "default_branch": "main",
			"clone_url": "https://git.example.com/acme/%s.git",
			"ssh_url": "git@git.example.com:acme/%s.git",
			"fork": false, "archived": false,
			"updated_at": "2026-01-02T03:04:05Z",
			"stars_count": 7, "template": false, "mirror": false
		}`, from+i, name, "acme/"+name, private, name, name)
	}
	b.WriteString("]")
	return b.String()
}

func TestGiteaListsAndMaps(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/users/acme/repos" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(giteaPage(1, 2, false)))
	})

	g, err := Get("gitea")
	if err != nil {
		t.Fatal(err)
	}
	repos, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("got %d repositories, want 2", len(repos))
	}

	got := repos[0]
	want := Repo{
		ID: 1, Owner: "acme", Name: "repo1", FullName: "acme/repo1",
		Language: "Go", Visibility: "Public", DefaultBranch: "main",
		CloneURL: "https://git.example.com/acme/repo1.git",
		SSHURL:   "git@git.example.com:acme/repo1.git",
		Stars:    7,
		PushedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}
	if got != want {
		t.Errorf("mapping differs:\n got  %+v\n want %+v", got, want)
	}
}

func TestGiteaFallsBackToTheOrgEndpoint(t *testing.T) {
	// A name is either a user or an organisation and the endpoints differ,
	// with no way to tell in advance which one it is.
	var userTried, orgTried bool
	srv := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/users/acme/repos":
			userTried = true
			w.WriteHeader(http.StatusNotFound)
		case "/api/v1/orgs/acme/repos":
			orgTried = true
			_, _ = w.Write([]byte(giteaPage(1, 1, false)))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})

	g, _ := Get("forgejo")
	repos, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !userTried || !orgTried {
		t.Errorf("both endpoints should be tried (user=%t org=%t)", userTried, orgTried)
	}
	if len(repos) != 1 {
		t.Errorf("got %d repositories, want 1", len(repos))
	}
}

// TestGiteaReportsAnOwnerThatDoesNotExist is the distinction that matters:
// "no such owner" and "this owner has nothing" lead to different actions.
func TestGiteaReportsAnOwnerThatDoesNotExist(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	g, _ := Get("gitea")
	_, err := g.List(context.Background(), "nobody", Options{BaseURL: srv.URL})
	if err == nil {
		t.Fatal("an owner that does not exist must be an error, not an empty list")
	}
	var nf *NotFoundError
	if !errors.As(err, &nf) {
		t.Errorf("err = %T %v, want *NotFoundError", err, err)
	}
	if !strings.Contains(err.Error(), "--forge-url") {
		t.Errorf("the error should suggest the self-hosted flag: %v", err)
	}
}

func TestGiteaPaginates(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		switch page {
		case 1:
			_, _ = w.Write([]byte(giteaPage(1, perPage, false)))
		case 2:
			// A short page is the last page.
			_, _ = w.Write([]byte(giteaPage(perPage+1, 3, false)))
		default:
			_, _ = w.Write([]byte("[]"))
		}
	})

	g, _ := Get("gitea")
	repos, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(repos) != perPage+3 {
		t.Errorf("got %d repositories, want %d", len(repos), perPage+3)
	}
	if n := srv.requests.Load(); n != 2 {
		t.Errorf("made %d requests, want 2 — a short page is the last page", n)
	}
}

func TestGiteaSendsItsToken(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("[]"))
	})
	g, _ := Get("gitea")
	if _, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL, Token: "s3cr3t"}); err != nil {
		t.Fatal(err)
	}
	h := <-srv.headers
	// Gitea's own scheme. Bearer is not accepted for a personal access
	// token, which is what a developer has.
	if got := h.Get("Authorization"); got != "token s3cr3t" {
		t.Errorf("Authorization = %q, want %q", got, "token s3cr3t")
	}
	if h.Get("User-Agent") == "" {
		t.Error("a User-Agent is required by some instances, and an operator reading logs deserves one")
	}
}

func TestGiteaRequiresAnInstanceURL(t *testing.T) {
	// Gitea and Forgejo have no single public instance, so there is
	// nothing sensible to default to.
	for _, name := range []string{"gitea", "forgejo"} {
		g, _ := Get(name)
		_, err := g.List(context.Background(), "acme", Options{})
		if err == nil {
			t.Fatalf("%s should require an instance URL", name)
		}
		if !strings.Contains(err.Error(), "--forge-url") {
			t.Errorf("%s: the error should name the flag: %v", name, err)
		}
	}
	// Codeberg has one, and defaults to it.
	c, _ := Get("codeberg")
	if got := c.(gitea).defaultURL; got != DefaultCodebergURL {
		t.Errorf("codeberg default = %q, want %q", got, DefaultCodebergURL)
	}
}

func TestGiteaMapsPrivateAndInternal(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":1,"name":"p","owner":{"login":"a"},"private":true},
			{"id":2,"name":"i","owner":{"login":"a"},"internal":true},
			{"id":3,"name":"o","owner":{"login":"a"}}
		]`))
	})
	g, _ := Get("gitea")
	repos, err := g.List(context.Background(), "a", Options{BaseURL: srv.URL, Visibility: "all"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"p": "Private", "i": "Private", "o": "Public"}
	for _, r := range repos {
		if want[r.Name] != r.Visibility {
			t.Errorf("%s: visibility = %q, want %q", r.Name, r.Visibility, want[r.Name])
		}
		// A repository with no language must still land somewhere in the
		// layout.
		if r.Language != "Other" {
			t.Errorf("%s: language = %q, want Other", r.Name, r.Language)
		}
	}
}

// ---------------------------------------------------------------------------
// GitLab
// ---------------------------------------------------------------------------

func gitlabPage(from, n int, visibility string) string {
	var b strings.Builder
	b.WriteString("[")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		name := "proj" + strconv.Itoa(from+i)
		fmt.Fprintf(&b, `{
			"id": %d, "path": %q, "path_with_namespace": %q,
			"namespace": {"full_path": "group/sub"},
			"visibility": %q, "default_branch": "main",
			"http_url_to_repo": "https://gitlab.example.com/group/sub/%s.git",
			"ssh_url_to_repo": "git@gitlab.example.com:group/sub/%s.git",
			"archived": false, "last_activity_at": "2026-02-03T04:05:06Z",
			"star_count": 3
		}`, from+i, name, "group/sub/"+name, visibility, name, name)
	}
	b.WriteString("]")
	return b.String()
}

func TestGitLabListsAndMaps(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/users/acme/projects" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(gitlabPage(1, 1, "public")))
	})

	g, _ := Get("gitlab")
	repos, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("got %d repositories, want 1", len(repos))
	}
	got := repos[0]
	want := Repo{
		ID: 1, Owner: "group/sub", Name: "proj1", FullName: "group/sub/proj1",
		// GitLab's project listing carries no language at all, and finding
		// one costs a request per project.
		Language: "Other", Visibility: "Public", DefaultBranch: "main",
		CloneURL: "https://gitlab.example.com/group/sub/proj1.git",
		SSHURL:   "git@gitlab.example.com:group/sub/proj1.git",
		Stars:    3,
		PushedAt: time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC),
	}
	if got != want {
		t.Errorf("mapping differs:\n got  %+v\n want %+v", got, want)
	}
}

func TestGitLabFallsBackToGroups(t *testing.T) {
	var groupTried bool
	srv := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/users/acme/projects":
			w.WriteHeader(http.StatusNotFound)
		case "/api/v4/groups/acme/projects":
			groupTried = true
			_, _ = w.Write([]byte(gitlabPage(1, 1, "public")))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	g, _ := Get("gitlab")
	repos, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !groupTried {
		t.Error("a name that is not a user should be tried as a group")
	}
	if len(repos) != 1 {
		t.Errorf("got %d repositories, want 1", len(repos))
	}
}

func TestGitLabInternalIsPrivate(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(gitlabPage(1, 1, "internal")))
	})
	g, _ := Get("gitlab")
	repos, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL, Visibility: "all"})
	if err != nil {
		t.Fatal(err)
	}
	// Visible to any signed-in user of the instance is not public, and the
	// layout has two directories.
	if repos[0].Visibility != "Private" {
		t.Errorf("internal mapped to %q, want Private", repos[0].Visibility)
	}
}

func TestGitLabDetectsForks(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":1,"path":"a","path_with_namespace":"o/a","visibility":"public"},
			{"id":2,"path":"b","path_with_namespace":"o/b","visibility":"public",
			 "forked_from_project":{"id":99}}
		]`))
	})
	g, _ := Get("gitlab")
	repos, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL, IncludeForks: true})
	if err != nil {
		t.Fatal(err)
	}
	forks := map[string]bool{}
	for _, r := range repos {
		forks[r.Name] = r.Fork
	}
	if forks["a"] || !forks["b"] {
		t.Errorf("fork detection wrong: %+v", forks)
	}

	// And the default excludes them.
	repos, err = g.List(context.Background(), "acme", Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Name != "a" {
		t.Errorf("forks should be excluded by default, got %+v", repos)
	}
}

func TestGitLabSendsItsToken(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("[]"))
	})
	g, _ := Get("gitlab")
	if _, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL, Token: "glpat-x"}); err != nil {
		t.Fatal(err)
	}
	h := <-srv.headers
	// GitLab's own header. Authorization: Bearer works for OAuth tokens
	// but not for a personal access token.
	if got := h.Get("PRIVATE-TOKEN"); got != "glpat-x" {
		t.Errorf("PRIVATE-TOKEN = %q, want %q", got, "glpat-x")
	}
}

func TestGitLabDerivesOwnerWithoutANamespace(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"id":1,"path":"p","path_with_namespace":"group/sub/p","visibility":"public"}]`))
	})
	g, _ := Get("gitlab")
	repos, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	// GitLab allows nested groups, which GitHub does not, so the owner is
	// everything before the final segment.
	if repos[0].Owner != "group/sub" {
		t.Errorf("owner = %q, want group/sub", repos[0].Owner)
	}
}

// ---------------------------------------------------------------------------
// the shared REST client
// ---------------------------------------------------------------------------

func TestRESTClientRejectsABadBaseURL(t *testing.T) {
	for name, base := range map[string]string{
		"unparseable":   "://",
		"wrong scheme":  "ftp://example.com",
		"file scheme":   "file:///etc/passwd",
		"no host":       "https://",
		"scheme absent": "example.com",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := newRESTClient(base, "", "H", "", Options{}); err == nil {
				t.Errorf("base URL %q should be rejected", base)
			}
		})
	}
}

func TestRESTClientRetriesTransientFailures(t *testing.T) {
	noSleep(t)
	var attempts atomic.Int64
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(giteaPage(1, 1, false)))
	})

	g, _ := Get("gitea")
	repos, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("a transient failure should be retried: %v", err)
	}
	if len(repos) != 1 {
		t.Errorf("got %d repositories, want 1", len(repos))
	}
	if attempts.Load() != 3 {
		t.Errorf("made %d attempts, want 3", attempts.Load())
	}
}

func TestRESTClientGivesUpAfterTheRetryBudget(t *testing.T) {
	noSleep(t)
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	g, _ := Get("gitea")
	if _, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL}); err == nil {
		t.Fatal("a permanently failing instance should be an error")
	}
	// One initial attempt plus maxRetries, and no more.
	if n := srv.requests.Load(); n != int64(maxRetries+1) {
		t.Errorf("made %d requests, want %d", n, maxRetries+1)
	}
}

func TestRESTClientDoesNotRetryARejectedCredential(t *testing.T) {
	noSleep(t)
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"401 Unauthorized"}`))
	})
	g, _ := Get("gitea")
	_, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL, Token: "wrong"})
	if err == nil {
		t.Fatal("a rejected credential should be an error")
	}
	var ae *AuthError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %T %v, want *AuthError", err, err)
	}
	// Retrying a rejected credential would three more times fail
	// identically, and on some instances would lock the account.
	if n := srv.requests.Load(); n != 1 {
		t.Errorf("made %d requests; a bad credential must not be retried", n)
	}
	if !strings.Contains(err.Error(), "--auth") {
		t.Errorf("the error should say how to supply one: %v", err)
	}
}

func TestRESTClientDoesNotRetryABadResponseBody(t *testing.T) {
	noSleep(t)
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("this is not json"))
	})
	g, _ := Get("gitea")
	if _, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL}); err == nil {
		t.Fatal("an undecodable body should be an error")
	}
	// The same request returns the same thing, so retrying is pure delay.
	if n := srv.requests.Load(); n != 1 {
		t.Errorf("made %d requests; an undecodable body must not be retried", n)
	}
}

func TestRESTClientReportsAnUnexpectedStatus(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("nope"))
	})
	g, _ := Get("gitea")
	_, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL})
	if err == nil {
		t.Fatal("an unexpected status should be an error")
	}
	if !strings.Contains(err.Error(), "418") {
		t.Errorf("the status should reach the caller: %v", err)
	}
}

func TestRESTClientHonoursCancellation(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(giteaPage(1, perPage, false)))
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	g, _ := Get("gitea")
	if _, err := g.List(ctx, "acme", Options{BaseURL: srv.URL}); err == nil {
		t.Error("a cancelled listing should report cancellation")
	}
}

// TestRESTClientHonoursTheTotalTimeout is the bound that a per-request
// timeout cannot provide: fifty pages at thirty seconds each is
// twenty-five minutes, whatever the per-request deadline says.
func TestRESTClientHonoursTheTotalTimeout(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(giteaPage(1, perPage, false)))
	})
	// Both, because a total below one request's budget is deliberately
	// raised to it — see TestRESTClientTotalTimeoutIsAtLeastOneRequest.
	c, err := newRESTClient(srv.URL, "", "H", "", Options{
		RequestTimeout: time.Nanosecond,
		TotalTimeout:   time.Nanosecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	var out []giteaRepo
	if err := c.getPage(context.Background(), "/api/v1/users/a/repos", nil, &out); err == nil {
		t.Error("a spent total budget should stop the listing")
	}
	if n := srv.requests.Load(); n != 0 {
		t.Errorf("made %d requests after the budget was spent", n)
	}
}

func TestRESTClientTotalTimeoutIsAtLeastOneRequest(t *testing.T) {
	// A total smaller than one request's budget cannot be satisfied, and
	// honouring it silently would fail in a way nobody could explain.
	c, err := newRESTClient("https://example.com", "", "H", "", Options{
		RequestTimeout: 30 * time.Second,
		TotalTimeout:   time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if remaining := time.Until(c.deadline); remaining < 25*time.Second {
		t.Errorf("total deadline is %v away, want at least the request timeout", remaining)
	}
}

func TestRESTClientCapsPagination(t *testing.T) {
	// An instance that never reports a last page must not loop forever.
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(giteaPage(1, perPage, false)))
	})
	g, _ := Get("gitea")
	repos, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL, IncludeForks: true})
	if err != nil {
		t.Fatal(err)
	}
	if n := srv.requests.Load(); n > maxPages {
		t.Errorf("made %d requests, above the page cap of %d", n, maxPages)
	}
	if len(repos) == 0 {
		t.Error("expected the pages it did fetch")
	}
}

func TestRESTClientStopsEarlyForASmallLimit(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(giteaPage(1, perPage, false)))
	})
	g, _ := Get("gitea")
	repos, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 5 {
		t.Errorf("got %d repositories, want the limit of 5", len(repos))
	}
	// Twice the limit is fetched before stopping, so filtering out forks
	// and archived repositories afterwards does not leave the answer
	// short. One page already covers that.
	if n := srv.requests.Load(); n != 1 {
		t.Errorf("made %d requests for a limit of 5", n)
	}
}

func TestRESTClientDoesNotLeakACredentialInErrors(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	// A credential in the URL itself, which is a thing people do.
	base := strings.Replace(srv.URL, "http://", "http://user:hunter2@", 1)
	c, err := newRESTClient(base, "", "H", "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	var out []giteaRepo
	err = c.getPage(context.Background(), "/api/v1/users/a/repos", nil, &out)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("a credential reached the error message: %v", err)
	}
}

func TestPageQuery(t *testing.T) {
	q := pageQuery(3, nil)
	if q.Get("page") != "3" || q.Get("per_page") != strconv.Itoa(perPage) {
		t.Errorf("pageQuery = %v", q)
	}
	// Extra parameters survive, and page/per_page win over any collision.
	extra := map[string][]string{"archived": {"false"}, "page": {"99"}}
	q = pageQuery(1, extra)
	if q.Get("archived") != "false" {
		t.Errorf("extra parameters should survive: %v", q)
	}
	if q.Get("page") != "1" {
		t.Errorf("page = %q, want the argument to win", q.Get("page"))
	}
}

// ---------------------------------------------------------------------------
// the GitHub adapter
// ---------------------------------------------------------------------------

func TestGitHubAdapterMapsAndDelegates(t *testing.T) {
	var gotOwner string
	var gotOpts github.FetchOptions
	old := githubFetch
	githubFetch = func(_ context.Context, owner string, opts github.FetchOptions) ([]github.Repo, error) {
		gotOwner, gotOpts = owner, opts
		return []github.Repo{
			{
				ID: 5, Owner: "acme", Name: "tool", FullName: "acme/tool",
				Language: "Go", Visibility: "Public", DefaultBranch: "main",
				CloneURL: "https://github.com/acme/tool.git",
				SSHURL:   "git@github.com:acme/tool.git",
				Stars:    12, IsTemplate: true,
				PushedAt: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC),
			},
			// A repository with no language must still land in the layout.
			{ID: 6, Owner: "acme", Name: "docs", FullName: "acme/docs"},
		}, nil
	}
	t.Cleanup(func() { githubFetch = old })

	g, _ := Get("github")
	repos, err := g.List(context.Background(), "acme", Options{
		Limit: 10, Visibility: "all", IncludeForks: true, IncludeArchived: true,
		RequestTimeout: time.Second, TotalTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotOwner != "acme" {
		t.Errorf("owner = %q", gotOwner)
	}
	// The options must reach the existing client, or its limit and
	// timeouts silently stop applying.
	if gotOpts.Limit != 10 || gotOpts.RequestTimeout != time.Second || gotOpts.TotalTimeout != time.Minute {
		t.Errorf("options did not reach the client: %+v", gotOpts)
	}
	if len(repos) != 2 {
		t.Fatalf("got %d repositories, want 2", len(repos))
	}
	want := Repo{
		ID: 5, Owner: "acme", Name: "tool", FullName: "acme/tool",
		Language: "Go", Visibility: "Public", DefaultBranch: "main",
		CloneURL: "https://github.com/acme/tool.git",
		SSHURL:   "git@github.com:acme/tool.git",
		Stars:    12, IsTemplate: true,
		PushedAt: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC),
	}
	if repos[0] != want {
		t.Errorf("mapping differs:\n got  %+v\n want %+v", repos[0], want)
	}
	if repos[1].Language != "Other" {
		t.Errorf("a repository with no language mapped to %q, want Other", repos[1].Language)
	}
}

func TestGitHubAdapterPropagatesAnError(t *testing.T) {
	want := errors.New("rate limited")
	old := githubFetch
	githubFetch = func(context.Context, string, github.FetchOptions) ([]github.Repo, error) {
		return nil, want
	}
	t.Cleanup(func() { githubFetch = old })

	g, _ := Get("github")
	if _, err := g.List(context.Background(), "acme", Options{}); !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
}

// TestGitHubAdapterAppliesTheSharedFilter: the GitHub client already
// filters, so this is a no-op in practice — but running it anyway is what
// keeps one definition of what a filter means, and stops GitHub drifting
// away from every other forge.
func TestGitHubAdapterAppliesTheSharedFilter(t *testing.T) {
	old := githubFetch
	githubFetch = func(context.Context, string, github.FetchOptions) ([]github.Repo, error) {
		return []github.Repo{
			{ID: 1, Name: "keep", Visibility: "Public"},
			{ID: 2, Name: "fork", Visibility: "Public", Fork: true},
		}, nil
	}
	t.Cleanup(func() { githubFetch = old })

	g, _ := Get("github")
	repos, err := g.List(context.Background(), "acme", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Name != "keep" {
		t.Errorf("the shared filter did not run: %+v", repos)
	}
}

// ---------------------------------------------------------------------------
// remaining error paths
// ---------------------------------------------------------------------------

func TestGiteaReportsANonNotFoundError(t *testing.T) {
	// A failure that is not "no such owner" must abort rather than falling
	// through to the org endpoint and reporting the wrong cause.
	noSleep(t)
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	g, _ := Get("gitea")
	_, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL})
	var ae *AuthError
	if !errors.As(err, &ae) {
		t.Errorf("err = %T %v, want *AuthError", err, err)
	}
	if n := srv.requests.Load(); n != 1 {
		t.Errorf("made %d requests; a rejected credential must not fall through to /orgs", n)
	}
}

func TestGiteaRejectsABadInstanceURL(t *testing.T) {
	g, _ := Get("gitea")
	if _, err := g.List(context.Background(), "acme", Options{BaseURL: "ftp://example.com"}); err == nil {
		t.Error("a non-HTTP instance URL should be rejected")
	}
}

func TestGitLabRejectsABadInstanceURL(t *testing.T) {
	g, _ := Get("gitlab")
	if _, err := g.List(context.Background(), "acme", Options{BaseURL: "ftp://example.com"}); err == nil {
		t.Error("a non-HTTP instance URL should be rejected")
	}
}

func TestGitLabReportsANonNotFoundError(t *testing.T) {
	noSleep(t)
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	g, _ := Get("gitlab")
	_, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL})
	var ae *AuthError
	if !errors.As(err, &ae) {
		t.Errorf("err = %T %v, want *AuthError", err, err)
	}
	if n := srv.requests.Load(); n != 1 {
		t.Errorf("made %d requests; a rejected credential must not fall through to /groups", n)
	}
}

func TestGitLabReportsAnOwnerThatIsNeitherUserNorGroup(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	g, _ := Get("gitlab")
	_, err := g.List(context.Background(), "nobody", Options{BaseURL: srv.URL})
	var nf *NotFoundError
	if !errors.As(err, &nf) {
		t.Errorf("err = %T %v, want *NotFoundError", err, err)
	}
	if n := srv.requests.Load(); n != 2 {
		t.Errorf("made %d requests; both a user and a group should be tried", n)
	}
}

func TestGitLabExcludesArchivedAtTheAPI(t *testing.T) {
	// Applied at the API as well as in Filter: an owner with hundreds of
	// archived projects would otherwise spend the page budget on them.
	var sawArchivedParam string
	srv := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		sawArchivedParam = r.URL.Query().Get("archived")
		_, _ = w.Write([]byte("[]"))
	})
	g, _ := Get("gitlab")
	if _, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if sawArchivedParam != "false" {
		t.Errorf("archived=%q, want false", sawArchivedParam)
	}

	if _, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL, IncludeArchived: true}); err != nil {
		t.Fatal(err)
	}
	if sawArchivedParam != "" {
		t.Errorf("archived=%q, want the parameter omitted when archived are wanted", sawArchivedParam)
	}
}

func TestGitLabPaginatesAndCapsForALimit(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(gitlabPage(1, perPage, "public")))
	})
	g, _ := Get("gitlab")
	repos, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 5 {
		t.Errorf("got %d repositories, want the limit of 5", len(repos))
	}
	if n := srv.requests.Load(); n != 1 {
		t.Errorf("made %d requests for a limit of 5", n)
	}
}

func TestRESTClientRejectsARequestItCannotBuild(t *testing.T) {
	c, err := newRESTClient("https://example.com", "", "H", "", Options{})
	if err != nil {
		t.Fatal(err)
	}
	// A NUL in the URL is rejected by url.Parse inside NewRequest, before
	// anything is sent. Driven against do directly, because getPage builds
	// its URL from an already-parsed base and escapes what it appends —
	// so this arm is unreachable from there, and a test that went through
	// getPage would be exercising the network instead.
	if _, err := c.do(context.Background(), "http://\x00bad"); err == nil {
		t.Error("an unbuildable request should be an error")
	}
}

func TestRESTClientGivesUpWhenNoBudgetRemainsToRetry(t *testing.T) {
	// A transient failure with no time left must report the failure rather
	// than sleep past the deadline.
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	c, err := newRESTClient(srv.URL, "", "H", "", Options{
		RequestTimeout: 50 * time.Millisecond,
		TotalTimeout:   50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	var out []giteaRepo
	err = c.getPage(context.Background(), "/api/v1/users/a/repos", nil, &out)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "no budget left") {
		t.Errorf("the error should say why it stopped retrying: %v", err)
	}
}

func TestRESTClientReportsATransportFailure(t *testing.T) {
	noSleep(t)
	// A server that is closed before the request: the transport fails
	// rather than the server answering.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	g, _ := Get("gitea")
	if _, err := g.List(context.Background(), "acme", Options{BaseURL: url}); err == nil {
		t.Error("an unreachable instance should be an error")
	}
}

func TestRESTClientRetriesRateLimiting(t *testing.T) {
	noSleep(t)
	var attempts atomic.Int64
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(giteaPage(1, 1, false)))
	})
	g, _ := Get("gitea")
	repos, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("rate limiting should be retried, not fatal: %v", err)
	}
	if len(repos) != 1 {
		t.Errorf("got %d repositories, want 1", len(repos))
	}
}

// TestRESTClientTreatsATruncatedBodyAsTransient: a connection that dies
// mid-body is a network failure, and the same request may well succeed.
func TestRESTClientTreatsATruncatedBodyAsTransient(t *testing.T) {
	noSleep(t)
	var attempts atomic.Int64
	srv := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			// Promise more than is sent, then hang up: the read fails.
			w.Header().Set("Content-Length", "4096")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("[{"))
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, err := hj.Hijack()
				if err == nil {
					_ = conn.Close()
				}
			}
			return
		}
		_, _ = w.Write([]byte(giteaPage(1, 1, false)))
	})

	g, _ := Get("gitea")
	repos, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("a truncated body should be retried: %v", err)
	}
	if len(repos) != 1 {
		t.Errorf("got %d repositories, want 1", len(repos))
	}
	if attempts.Load() < 2 {
		t.Errorf("made %d attempts; the first should have been retried", attempts.Load())
	}
}

// TestGitLabAbortsOnAGroupError: a user 404 falls through to groups, but a
// real failure there must surface rather than be reported as "not found".
func TestGitLabAbortsOnAGroupError(t *testing.T) {
	noSleep(t)
	srv := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/users/acme/projects":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusForbidden)
		}
	})
	g, _ := Get("gitlab")
	_, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL})
	var ae *AuthError
	if !errors.As(err, &ae) {
		t.Errorf("err = %T %v, want the group endpoint's *AuthError", err, err)
	}
}

// TestForgesUseTheirPublicInstanceByDefault pins the default base URL for
// the two forges that have one, without making a network call: the
// transport is replaced, and the request it is handed is the assertion.
func TestForgesUseTheirPublicInstanceByDefault(t *testing.T) {
	for _, tc := range []struct {
		forge string
		want  string
	}{
		{"gitlab", DefaultGitLabURL},
		{"codeberg", DefaultCodebergURL},
	} {
		t.Run(tc.forge, func(t *testing.T) {
			var got string
			old := httpClient
			httpClient = func(time.Duration) *http.Client {
				return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					got = r.URL.Scheme + "://" + r.URL.Host
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader("[]")),
						Header:     http.Header{},
					}, nil
				})}
			}
			t.Cleanup(func() { httpClient = old })

			f, _ := Get(tc.forge)
			if _, err := f.List(context.Background(), "acme", Options{}); err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("requested %q, want the public instance %q", got, tc.want)
			}
		})
	}
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// ---------------------------------------------------------------------------
// Bitbucket
// ---------------------------------------------------------------------------

// bitbucketPageJSON renders one page, with next set when more follow.
// Bitbucket signals the end by omitting next, not by returning a short
// page — a filtered listing can be short and still have more.
func bitbucketPageJSON(from, n int, next string) string {
	var b strings.Builder
	b.WriteString(`{"values":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		slug := "repo" + strconv.Itoa(from+i)
		fmt.Fprintf(&b, `{
			"uuid": "{%d}", "slug": %q, "name": "Display Name",
			"full_name": %q, "scm": "git", "is_private": false,
			"language": "go", "mainbranch": {"name": "main"},
			"updated_on": "2026-04-05T06:07:08.123456+00:00",
			"links": {"clone": [
				{"name": "https", "href": "https://bitbucket.org/acme/%s.git"},
				{"name": "ssh", "href": "git@bitbucket.org:acme/%s.git"}
			]}
		}`, from+i, slug, "acme/"+slug, slug, slug)
	}
	b.WriteString(`]`)
	if next != "" {
		fmt.Fprintf(&b, `,"next":%q`, next)
	}
	b.WriteString(`}`)
	return b.String()
}

func TestBitbucketListsAndMaps(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/2.0/repositories/acme" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(bitbucketPageJSON(1, 1, "")))
	})

	f, _ := Get("bitbucket")
	repos, err := f.List(context.Background(), "acme", Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("got %d repositories, want 1", len(repos))
	}
	want := Repo{
		Owner: "acme", Name: "repo1", FullName: "acme/repo1",
		Language: "go", Visibility: "Public", DefaultBranch: "main",
		CloneURL: "https://bitbucket.org/acme/repo1.git",
		SSHURL:   "git@bitbucket.org:acme/repo1.git",
		PushedAt: time.Date(2026, 4, 5, 6, 7, 8, 123456000, time.UTC),
	}
	got := repos[0]
	got.PushedAt = got.PushedAt.UTC()
	if got != want {
		t.Errorf("mapping differs:\n got  %+v\n want %+v", got, want)
	}
}

// TestBitbucketPrefersTheSlug: "name" is a display name and may contain
// spaces and capitals — "Atlassian Event" for the repository cloned as
// atlassian-event — so a directory named from it would not match the
// clone URL.
func TestBitbucketPrefersTheSlug(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"values":[{
			"slug":"atlassian-event","name":"Atlassian Event",
			"full_name":"atlassian/atlassian-event","scm":"git",
			"links":{"clone":[{"name":"https","href":"https://bitbucket.org/atlassian/atlassian-event.git"}]}
		}]}`))
	})
	f, _ := Get("bitbucket")
	repos, err := f.List(context.Background(), "atlassian", Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if repos[0].Name != "atlassian-event" {
		t.Errorf("name = %q, want the slug", repos[0].Name)
	}

	// A repository with no slug at all still needs a name.
	srv2 := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"values":[{"name":"Fallback","full_name":"acme/fallback","scm":"git"}]}`))
	})
	repos, err = f.List(context.Background(), "acme", Options{BaseURL: srv2.URL})
	if err != nil {
		t.Fatal(err)
	}
	if repos[0].Name != "Fallback" {
		t.Errorf("name = %q, want the display name as a fallback", repos[0].Name)
	}
}

// TestBitbucketSkipsMercurial: Bitbucket hosted Mercurial until 2020 and
// an old workspace can still list one. It has no git clone URL, so
// cloning it is impossible — and reporting that on every run would be
// noise, since it is not an error.
func TestBitbucketSkipsMercurial(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"values":[
			{"slug":"g","full_name":"acme/g","scm":"git","links":{"clone":[{"name":"https","href":"https://bitbucket.org/acme/g.git"}]}},
			{"slug":"h","full_name":"acme/h","scm":"hg","links":{"clone":[{"name":"https","href":"https://bitbucket.org/acme/h"}]}}
		]}`))
	})
	f, _ := Get("bitbucket")
	repos, err := f.List(context.Background(), "acme", Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Name != "g" {
		t.Errorf("expected only the git repository, got %+v", repos)
	}
}

func TestBitbucketDetectsForksAndPrivate(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"values":[
			{"slug":"a","full_name":"acme/a","scm":"git","is_private":true},
			{"slug":"b","full_name":"acme/b","scm":"git","parent":{"slug":"upstream"}}
		]}`))
	})
	f, _ := Get("bitbucket")
	repos, err := f.List(context.Background(), "acme", Options{
		BaseURL: srv.URL, Visibility: "all", IncludeForks: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Repo{}
	for _, r := range repos {
		byName[r.Name] = r
	}
	if byName["a"].Visibility != "Private" {
		t.Errorf("is_private did not map to Private: %+v", byName["a"])
	}
	// Bitbucket has no fork boolean; the presence of a parent is the
	// signal.
	if byName["a"].Fork || !byName["b"].Fork {
		t.Errorf("fork detection wrong: a=%t b=%t", byName["a"].Fork, byName["b"].Fork)
	}
}

// TestBitbucketPaginatesOnNext is the difference from every other forge
// here: a short page does not mean the last page.
func TestBitbucketPaginatesOnNext(t *testing.T) {
	var srv *recordingServer
	srv = newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("page") {
		case "1":
			// Deliberately short, and still not the end.
			_, _ = w.Write([]byte(bitbucketPageJSON(1, 3, srv.URL+"/2.0/repositories/acme?page=2")))
		case "2":
			_, _ = w.Write([]byte(bitbucketPageJSON(4, 2, "")))
		default:
			_, _ = w.Write([]byte(`{"values":[]}`))
		}
	})

	f, _ := Get("bitbucket")
	repos, err := f.List(context.Background(), "acme", Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 5 {
		t.Errorf("got %d repositories, want 5 — a short page is not the last page", len(repos))
	}
	if n := srv.requests.Load(); n != 2 {
		t.Errorf("made %d requests, want 2", n)
	}
}

// TestBitbucketAsksForAFullPage: Bitbucket names its page size pagelen
// and silently ignores per_page, defaulting to ten — not an error, just
// ten times the requests.
func TestBitbucketAsksForAFullPage(t *testing.T) {
	var seen string
	srv := newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"values":[]}`))
	})
	f, _ := Get("bitbucket")
	if _, err := f.List(context.Background(), "acme", Options{BaseURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen, "pagelen="+strconv.Itoa(perPage)) {
		t.Errorf("query %q does not ask for a full page with pagelen", seen)
	}
}

// TestBitbucketAPIBase covers the web-host/API-host split. A user naming
// the forge by URL names the one they can see, and sent as-is that 404s
// on every request.
func TestBitbucketAPIBase(t *testing.T) {
	for in, want := range map[string]string{
		"":                           DefaultBitbucketURL,
		"https://bitbucket.org":      "https://api.bitbucket.org",
		"https://BitBucket.org":      "https://api.bitbucket.org",
		"https://www.bitbucket.org":  "https://api.bitbucket.org",
		"https://bitbucket.org/acme": "https://api.bitbucket.org",
		"https://api.bitbucket.org":  "https://api.bitbucket.org",
		"https://mirror.example.com": "https://mirror.example.com",
		"://not a url":               "://not a url",
	} {
		if got := bitbucketAPIBase(in); got != want {
			t.Errorf("bitbucketAPIBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBitbucketSendsItsToken(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"values":[]}`))
	})
	f, _ := Get("bitbucket")
	if _, err := f.List(context.Background(), "acme", Options{BaseURL: srv.URL, Token: "bb-token"}); err != nil {
		t.Fatal(err)
	}
	h := <-srv.headers
	// Bearer rather than Basic: an app password would also need the
	// username, which a bearer token does not.
	if got := h.Get("Authorization"); got != "Bearer bb-token" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestBitbucketReportsAnUnknownWorkspace(t *testing.T) {
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	f, _ := Get("bitbucket")
	_, err := f.List(context.Background(), "nobody", Options{BaseURL: srv.URL})
	var nf *NotFoundError
	if !errors.As(err, &nf) {
		t.Errorf("err = %T %v, want *NotFoundError", err, err)
	}
	// A workspace is a workspace — there is no user/org split to fall back
	// through, so exactly one request is made.
	if n := srv.requests.Load(); n != 1 {
		t.Errorf("made %d requests, want 1", n)
	}
}

func TestBitbucketRejectsABadInstanceURL(t *testing.T) {
	f, _ := Get("bitbucket")
	if _, err := f.List(context.Background(), "acme", Options{BaseURL: "ftp://example.com"}); err == nil {
		t.Error("a non-HTTP instance URL should be rejected")
	}
}

// TestBitbucketStopsEarlyForASmallLimit: because Bitbucket paginates on
// `next` rather than on a short page, nothing else would stop the walk,
// and a workspace with thousands of repositories would be fetched in full
// to answer a request for five.
func TestBitbucketStopsEarlyForASmallLimit(t *testing.T) {
	var srv *recordingServer
	srv = newRecordingServer(t, func(w http.ResponseWriter, r *http.Request) {
		page := r.URL.Query().Get("page")
		n, _ := strconv.Atoi(page)
		// Always another page, so only the limit can end this.
		_, _ = w.Write([]byte(bitbucketPageJSON(n*10, 10,
			srv.URL+"/2.0/repositories/acme?page="+strconv.Itoa(n+1))))
	})

	f, _ := Get("bitbucket")
	repos, err := f.List(context.Background(), "acme", Options{BaseURL: srv.URL, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 5 {
		t.Errorf("got %d repositories, want the limit of 5", len(repos))
	}
	// Twice the limit is fetched before stopping, so filtering afterwards
	// does not leave the answer short. Ten per page covers that in one.
	if n := srv.requests.Load(); n > 2 {
		t.Errorf("made %d requests for a limit of 5", n)
	}
}
