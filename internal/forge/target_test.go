// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gh "github.com/google/go-github/v90/github"
)

// The targets are driven against real HTTP servers, like the listing
// clients: status handling, the create-after-404 sequence and the
// lost-race re-read are the whole of what they do.

func targetOpts(base string) TargetOptions {
	return TargetOptions{Token: "tok", BaseURL: base, Login: "me"}
}

func mustTarget(t *testing.T, name string) Target {
	t.Helper()
	f, err := Get(name)
	if err != nil {
		t.Fatal(err)
	}
	tg, ok := AsTarget(f)
	if !ok {
		t.Fatalf("%s is not a Target", name)
	}
	return tg
}

// jsonHandler routes "METHOD /path" to a status and body, and records the
// decoded JSON body of every request so a test can assert the payload.
type jsonHandler struct {
	t      *testing.T
	routes map[string]func(w http.ResponseWriter, r *http.Request)
	bodies map[string]map[string]any
}

func newJSONHandler(t *testing.T) *jsonHandler {
	return &jsonHandler{t: t, routes: map[string]func(http.ResponseWriter, *http.Request){}, bodies: map[string]map[string]any{}}
}

func (h *jsonHandler) on(route string, status int, body string) *jsonHandler {
	h.routes[route] = func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
	return h
}

func (h *jsonHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.Path
	if r.Body != nil {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			h.bodies[key] = body
		}
	}
	if fn, ok := h.routes[key]; ok {
		fn(w, r)
		return
	}
	h.t.Errorf("unexpected request %s", key)
	w.WriteHeader(http.StatusTeapot)
}

func serve(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv.URL
}

// ---------------------------------------------------------------------------
// The shared pieces
// ---------------------------------------------------------------------------

func TestEveryForgeIsATarget(t *testing.T) {
	for _, name := range Names() {
		f, _ := Get(name)
		if _, ok := AsTarget(f); !ok {
			t.Errorf("%s cannot receive a mirror", name)
		}
	}
	if _, ok := AsTarget(listOnly{}); ok {
		t.Error("a list-only forge must not pass as a Target")
	}
}

// listOnly is a Forge that cannot receive a mirror, for the AsTarget test.
type listOnly struct{}

func (listOnly) Name() string                                          { return "list-only" }
func (listOnly) Hosts() []string                                       { return nil }
func (listOnly) List(context.Context, string, Options) ([]Repo, error) { return nil, nil }

func TestTargetsRefuseAnEmptyToken(t *testing.T) {
	for _, name := range Names() {
		tg := mustTarget(t, name)
		// With an instance named, and — for the forges that have a public
		// instance — without one, so the default-instance branch is the
		// one refusing rather than a request that would have left the
		// machine.
		bases := []string{"https://example.test"}
		if name != "gitea" && name != "forgejo" {
			bases = append(bases, "")
		}
		for _, base := range bases {
			opts := TargetOptions{BaseURL: base}
			if _, err := tg.Whoami(context.Background(), opts); !errors.Is(err, ErrNoToken) {
				t.Errorf("%s Whoami without a token (base %q): %v", name, base, err)
			}
			if _, err := tg.EnsureRepo(context.Background(), "me", RepoSpec{Name: "r"}, opts); !errors.Is(err, ErrNoToken) {
				t.Errorf("%s EnsureRepo without a token (base %q): %v", name, base, err)
			}
		}
	}
}

func TestTargetsRejectABadBaseURL(t *testing.T) {
	for _, name := range []string{"gitlab", "gitea", "bitbucket"} {
		tg := mustTarget(t, name)
		opts := TargetOptions{Token: "tok", BaseURL: "ftp://example.test"}
		if _, err := tg.Whoami(context.Background(), opts); err == nil {
			t.Errorf("%s accepted an ftp base URL", name)
		}
		if _, err := tg.EnsureRepo(context.Background(), "me", RepoSpec{Name: "r"}, opts); err == nil {
			t.Errorf("%s accepted an ftp base URL for a create", name)
		}
	}
}

func TestGiteaFamilyTargetNeedsAnInstance(t *testing.T) {
	for _, name := range []string{"gitea", "forgejo"} {
		tg := mustTarget(t, name)
		if _, err := tg.Whoami(context.Background(), TargetOptions{Token: "tok"}); err == nil || !strings.Contains(err.Error(), "--to") {
			t.Errorf("%s without an instance: %v", name, err)
		}
	}
}

func TestPushAuth(t *testing.T) {
	opts := TargetOptions{Token: "tok", Login: "me"}
	cases := map[string]string{"github": "x-access-token", "gitlab": "oauth2", "gitea": "me", "forgejo": "me", "codeberg": "me", "bitbucket": "x-token-auth"}
	for name, wantUser := range cases {
		user, secret := mustTarget(t, name).PushAuth(opts)
		if user != wantUser || secret != "tok" {
			t.Errorf("%s PushAuth = %q/%q, want %q/tok", name, user, secret, wantUser)
		}
	}
}

func TestValidateRepoName(t *testing.T) {
	if err := ValidateRepoName("corral-sync"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", ".", "..", strings.Repeat("a", 101), "-x", "a/b", "a b", "a\\b", "a\nb"} {
		if err := ValidateRepoName(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestValidatePushURL(t *testing.T) {
	for _, ok := range []string{"git@example.com:owner/repo.git", "ssh://git@example.com/owner/repo.git", "https://example.com/owner/repo.git"} {
		if err := ValidatePushURL(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "-bad", "git@example.com", "git@:repo", "@host:repo", "git@example.com:", "git@example.com:/repo",
		"http://example.com/repo", "file:///tmp/repo", "/tmp/repo", "https://user:pass@example.com/repo", "ssh://example.com/repo", "https://example.com/repo\nnext", "https://%zz"} {
		if err := ValidatePushURL(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestLostCreateRace(t *testing.T) {
	cases := map[error]bool{
		&StatusError{Status: 409}: true,
		&StatusError{Status: 400, Body: `{"message":{"name":["has already been taken"]}}`}: true,
		&StatusError{Status: 400, Body: "Repository already exists."}:                      true,
		&StatusError{Status: 400, Body: "name is invalid"}:                                 false,
		&StatusError{Status: 500}: false,
		errors.New("plain"):       false,
	}
	for err, want := range cases {
		if got := lostCreateRace(err); got != want {
			t.Errorf("lostCreateRace(%v) = %v, want %v", err, got, want)
		}
	}
}

func TestStatusErrorMessage(t *testing.T) {
	err := &StatusError{Status: 418, URL: "https://x/y", Body: "short and stout"}
	if !strings.Contains(err.Error(), "418") || !strings.Contains(err.Error(), "short and stout") || !statusIs(err, 418) || statusIs(err, 419) {
		t.Fatalf("unexpected: %v", err)
	}
}

// ---------------------------------------------------------------------------
// restClient.call
// ---------------------------------------------------------------------------

func TestCallBranches(t *testing.T) {
	h := newJSONHandler(t).
		on("GET /ok", 200, `{"a":1}`).
		on("GET /empty", 204, ``).
		on("GET /unauthorized", 401, `nope`).
		on("GET /teapot", 418, `short`).
		on("GET /bad-json", 200, `{`)
	base := serve(t, h)
	c, err := newRESTClient(base, "tok", "Authorization", "token ", Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var out map[string]int
	if status, err := c.call(ctx, http.MethodGet, "/ok", nil, &out); err != nil || status != 200 || out["a"] != 1 {
		t.Fatalf("ok = %d, %v, %v", status, err, out)
	}
	if status, err := c.call(ctx, http.MethodGet, "/empty", nil, &out); err != nil || status != 204 {
		t.Fatalf("empty = %d, %v", status, err)
	}
	if _, err := c.call(ctx, http.MethodGet, "/unauthorized", nil, nil); err == nil || !errors.As(err, new(*AuthError)) {
		t.Fatalf("unauthorized = %v", err)
	}
	if _, err := c.call(ctx, http.MethodGet, "/teapot", nil, nil); !statusIs(err, 418) {
		t.Fatalf("teapot = %v", err)
	}
	if _, err := c.call(ctx, http.MethodGet, "/bad-json", nil, &out); err == nil || !strings.Contains(err.Error(), "decoding") {
		t.Fatalf("bad json = %v", err)
	}
	if _, err := c.call(ctx, http.MethodPost, "/ok", map[string]any{"ch": make(chan int)}, nil); err == nil || !strings.Contains(err.Error(), "encoding") {
		t.Fatalf("unencodable body = %v", err)
	}
	if _, err := c.call(ctx, "BAD METHOD", "/ok", nil, nil); err == nil {
		t.Fatal("expected an invalid method to fail to build a request")
	}
	// A server that closes the connection mid-body.
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("short"))
	}))
	t.Cleanup(broken.Close)
	bc, _ := newRESTClient(broken.URL, "tok", "Authorization", "token ", Options{})
	if _, err := bc.call(ctx, http.MethodGet, "/", nil, nil); err == nil {
		t.Fatal("expected a truncated body to fail")
	}
	// A server that is gone.
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	gc, _ := newRESTClient(gone.URL, "tok", "Authorization", "token ", Options{})
	if _, err := gc.call(ctx, http.MethodGet, "/", nil, nil); err == nil {
		t.Fatal("expected a transport failure")
	}
}

// ---------------------------------------------------------------------------
// GitLab
// ---------------------------------------------------------------------------

const gitlabProjectJSON = `{"id":9,"path":"repo","path_with_namespace":"me/repo","ssh_url_to_repo":"git@gl.test:me/repo.git","http_url_to_repo":"https://gl.test/me/repo.git","visibility":"private"}`

func TestGitLabTarget(t *testing.T) {
	ctx := context.Background()
	tg := mustTarget(t, "gitlab")

	t.Run("whoami", func(t *testing.T) {
		base := serve(t, newJSONHandler(t).on("GET /api/v4/user", 200, `{"username":"me"}`))
		if got, err := tg.Whoami(ctx, targetOpts(base)); err != nil || got != "me" {
			t.Fatalf("whoami = %q, %v", got, err)
		}
		base = serve(t, newJSONHandler(t).on("GET /api/v4/user", 200, `{}`))
		if _, err := tg.Whoami(ctx, targetOpts(base)); err == nil {
			t.Fatal("expected an empty username to fail")
		}
		base = serve(t, newJSONHandler(t).on("GET /api/v4/user", 401, `{}`))
		if _, err := tg.Whoami(ctx, targetOpts(base)); err == nil {
			t.Fatal("expected 401 to fail")
		}
	})

	t.Run("existing", func(t *testing.T) {
		base := serve(t, newJSONHandler(t).on("GET /api/v4/projects/me%2Frepo", 200, gitlabProjectJSON))
		got, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo", Private: true}, targetOpts(base))
		if err != nil || got.Created || !got.Private || got.SSHURL != "git@gl.test:me/repo.git" || got.HTTPSURL != "https://gl.test/me/repo.git" {
			t.Fatalf("existing = %+v, %v", got, err)
		}
	})

	t.Run("create under the user", func(t *testing.T) {
		h := newJSONHandler(t).
			on("GET /api/v4/projects/me%2Frepo", 404, `{"message":"404 Project Not Found"}`).
			on("POST /api/v4/projects", 201, strings.Replace(gitlabProjectJSON, "private", "public", 1))
		base := serve(t, h)
		got, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo", Description: "d"}, targetOpts(base))
		if err != nil || !got.Created || got.Private {
			t.Fatalf("created = %+v, %v", got, err)
		}
		body := h.bodies["POST /api/v4/projects"]
		if body["visibility"] != "public" || body["path"] != "repo" || body["description"] != "d" || body["initialize_with_readme"] != false {
			t.Fatalf("payload = %v", body)
		}
		if _, hasNS := body["namespace_id"]; hasNS {
			t.Fatalf("a personal project must not carry namespace_id: %v", body)
		}
	})

	t.Run("create under a group", func(t *testing.T) {
		h := newJSONHandler(t).
			on("GET /api/v4/projects/team%2Frepo", 404, ``).
			on("GET /api/v4/namespaces/team", 200, `{"id":42,"full_path":"team"}`).
			on("POST /api/v4/projects", 201, gitlabProjectJSON)
		base := serve(t, h)
		if _, err := tg.EnsureRepo(ctx, "team", RepoSpec{Name: "repo", Private: true}, targetOpts(base)); err != nil {
			t.Fatal(err)
		}
		if h.bodies["POST /api/v4/projects"]["namespace_id"] != float64(42) {
			t.Fatalf("payload = %v", h.bodies["POST /api/v4/projects"])
		}
	})

	t.Run("group lookup failures", func(t *testing.T) {
		base := serve(t, newJSONHandler(t).on("GET /api/v4/projects/team%2Frepo", 404, ``).on("GET /api/v4/namespaces/team", 404, ``))
		if _, err := tg.EnsureRepo(ctx, "team", RepoSpec{Name: "repo"}, targetOpts(base)); err == nil || !strings.Contains(err.Error(), "resolving namespace") {
			t.Fatalf("missing namespace = %v", err)
		}
		base = serve(t, newJSONHandler(t).on("GET /api/v4/projects/team%2Frepo", 404, ``).on("GET /api/v4/namespaces/team", 200, `{}`))
		if _, err := tg.EnsureRepo(ctx, "team", RepoSpec{Name: "repo"}, targetOpts(base)); err == nil || !strings.Contains(err.Error(), "no id") {
			t.Fatalf("namespace without id = %v", err)
		}
	})

	t.Run("lost race", func(t *testing.T) {
		calls := 0
		h := newJSONHandler(t).on("POST /api/v4/projects", 400, `{"message":{"name":["has already been taken"]}}`)
		h.routes["GET /api/v4/projects/me%2Frepo"] = func(w http.ResponseWriter, _ *http.Request) {
			calls++
			if calls == 1 {
				w.WriteHeader(404)
				return
			}
			_, _ = w.Write([]byte(gitlabProjectJSON))
		}
		base := serve(t, h)
		got, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo", Private: true}, targetOpts(base))
		if err != nil || got.Created || calls != 2 {
			t.Fatalf("race = %+v, %v, calls=%d", got, err, calls)
		}
	})

	t.Run("failures", func(t *testing.T) {
		base := serve(t, newJSONHandler(t).on("GET /api/v4/projects/me%2Frepo", 500, `boom`))
		if _, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo"}, targetOpts(base)); !statusIs(err, 500) {
			t.Fatalf("get failure = %v", err)
		}
		base = serve(t, newJSONHandler(t).on("GET /api/v4/projects/me%2Frepo", 404, ``).on("POST /api/v4/projects", 403, `forbidden`))
		if _, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo"}, targetOpts(base)); err == nil || !errors.As(err, new(*AuthError)) {
			t.Fatalf("create forbidden = %v", err)
		}
		base = serve(t, newJSONHandler(t).on("GET /api/v4/projects/me%2Frepo", 404, ``).on("POST /api/v4/projects", 409, `dup`))
		if _, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo"}, targetOpts(base)); !statusIs(err, 404) {
			t.Fatalf("race then missing = %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Gitea family
// ---------------------------------------------------------------------------

const giteaRepoJSON = `{"id":3,"name":"repo","full_name":"me/repo","owner":{"login":"me"},"private":true,"clone_url":"https://gt.test/me/repo.git","ssh_url":"git@gt.test:me/repo.git"}`

func TestGiteaTarget(t *testing.T) {
	ctx := context.Background()
	tg := mustTarget(t, "gitea")

	t.Run("whoami", func(t *testing.T) {
		base := serve(t, newJSONHandler(t).on("GET /api/v1/user", 200, `{"login":"me"}`))
		if got, err := tg.Whoami(ctx, targetOpts(base)); err != nil || got != "me" {
			t.Fatalf("whoami = %q, %v", got, err)
		}
		base = serve(t, newJSONHandler(t).on("GET /api/v1/user", 200, `{}`))
		if _, err := tg.Whoami(ctx, targetOpts(base)); err == nil {
			t.Fatal("expected an empty login to fail")
		}
		base = serve(t, newJSONHandler(t).on("GET /api/v1/user", 500, ``))
		if _, err := tg.Whoami(ctx, targetOpts(base)); err == nil {
			t.Fatal("expected 500 to fail")
		}
	})

	t.Run("codeberg defaults its instance", func(t *testing.T) {
		cb := mustTarget(t, "codeberg")
		user, secret := cb.PushAuth(targetOpts(""))
		if user != "me" || secret != "tok" {
			t.Fatalf("push auth = %q/%q", user, secret)
		}
		// Whoami against the default instance would need the network; the
		// point is that it gets past the "no instance" check.
		if _, err := cb.Whoami(ctx, TargetOptions{Token: "tok", RequestTimeout: 1}); err == nil || strings.Contains(err.Error(), "--to") {
			t.Fatalf("codeberg should not demand an instance: %v", err)
		}
	})

	t.Run("existing", func(t *testing.T) {
		base := serve(t, newJSONHandler(t).on("GET /api/v1/repos/me/repo", 200, giteaRepoJSON))
		got, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo", Private: true}, targetOpts(base))
		if err != nil || got.Created || !got.Private || got.HTTPSURL != "https://gt.test/me/repo.git" {
			t.Fatalf("existing = %+v, %v", got, err)
		}
	})

	t.Run("create personal and org", func(t *testing.T) {
		h := newJSONHandler(t).on("GET /api/v1/repos/me/repo", 404, ``).on("POST /api/v1/user/repos", 201, giteaRepoJSON)
		base := serve(t, h)
		got, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo", Private: true, Description: "d"}, targetOpts(base))
		if err != nil || !got.Created {
			t.Fatalf("personal = %+v, %v", got, err)
		}
		if b := h.bodies["POST /api/v1/user/repos"]; b["private"] != true || b["auto_init"] != false || b["description"] != "d" {
			t.Fatalf("payload = %v", b)
		}
		h = newJSONHandler(t).on("GET /api/v1/repos/org/repo", 404, ``).on("POST /api/v1/orgs/org/repos", 201, giteaRepoJSON)
		base = serve(t, h)
		if _, err := tg.EnsureRepo(ctx, "org", RepoSpec{Name: "repo"}, targetOpts(base)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("lost race and failures", func(t *testing.T) {
		calls := 0
		h := newJSONHandler(t).on("POST /api/v1/user/repos", 409, `exists`)
		h.routes["GET /api/v1/repos/me/repo"] = func(w http.ResponseWriter, _ *http.Request) {
			calls++
			if calls == 1 {
				w.WriteHeader(404)
				return
			}
			_, _ = w.Write([]byte(giteaRepoJSON))
		}
		base := serve(t, h)
		if got, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo"}, targetOpts(base)); err != nil || got.Created {
			t.Fatalf("race = %+v, %v", got, err)
		}
		base = serve(t, newJSONHandler(t).on("GET /api/v1/repos/me/repo", 404, ``).on("POST /api/v1/user/repos", 409, ``))
		if _, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo"}, targetOpts(base)); !statusIs(err, 404) {
			t.Fatalf("race then missing = %v", err)
		}
		base = serve(t, newJSONHandler(t).on("GET /api/v1/repos/me/repo", 502, ``))
		if _, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo"}, targetOpts(base)); !statusIs(err, 502) {
			t.Fatalf("get failure = %v", err)
		}
		base = serve(t, newJSONHandler(t).on("GET /api/v1/repos/me/repo", 404, ``).on("POST /api/v1/user/repos", 422, `bad`))
		if _, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo"}, targetOpts(base)); !statusIs(err, 422) {
			t.Fatalf("create failure = %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// Bitbucket
// ---------------------------------------------------------------------------

const bitbucketRepoJSON = `{"slug":"repo","name":"Repo","full_name":"me/repo","scm":"git","is_private":true,
"links":{"clone":[{"name":"https","href":"https://bitbucket.org/me/repo.git"},{"name":"ssh","href":"git@bitbucket.org:me/repo.git"}]}}`

func TestBitbucketTarget(t *testing.T) {
	ctx := context.Background()
	tg := mustTarget(t, "bitbucket")

	t.Run("whoami", func(t *testing.T) {
		base := serve(t, newJSONHandler(t).on("GET /2.0/user", 200, `{"username":"me"}`))
		if got, err := tg.Whoami(ctx, targetOpts(base)); err != nil || got != "me" {
			t.Fatalf("whoami = %q, %v", got, err)
		}
		base = serve(t, newJSONHandler(t).on("GET /2.0/user", 200, `{"nickname":"nick"}`))
		if got, err := tg.Whoami(ctx, targetOpts(base)); err != nil || got != "nick" {
			t.Fatalf("nickname fallback = %q, %v", got, err)
		}
		base = serve(t, newJSONHandler(t).on("GET /2.0/user", 200, `{}`))
		if _, err := tg.Whoami(ctx, targetOpts(base)); err == nil {
			t.Fatal("expected an empty user to fail")
		}
		base = serve(t, newJSONHandler(t).on("GET /2.0/user", 403, ``))
		if _, err := tg.Whoami(ctx, targetOpts(base)); err == nil {
			t.Fatal("expected 403 to fail")
		}
	})

	t.Run("existing, create, race, failures", func(t *testing.T) {
		base := serve(t, newJSONHandler(t).on("GET /2.0/repositories/me/repo", 200, bitbucketRepoJSON))
		got, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo", Private: true}, targetOpts(base))
		if err != nil || got.Created || !got.Private || got.SSHURL != "git@bitbucket.org:me/repo.git" || got.HTTPSURL != "https://bitbucket.org/me/repo.git" {
			t.Fatalf("existing = %+v, %v", got, err)
		}

		h := newJSONHandler(t).on("GET /2.0/repositories/me/repo", 404, ``).on("POST /2.0/repositories/me/repo", 200, bitbucketRepoJSON)
		base = serve(t, h)
		if got, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo", Private: true}, targetOpts(base)); err != nil || !got.Created {
			t.Fatalf("created = %+v, %v", got, err)
		}
		if b := h.bodies["POST /2.0/repositories/me/repo"]; b["scm"] != "git" || b["is_private"] != true {
			t.Fatalf("payload = %v", b)
		}

		calls := 0
		h = newJSONHandler(t).on("POST /2.0/repositories/me/repo", 400, `Repository with this Slug and Owner already exists.`)
		h.routes["GET /2.0/repositories/me/repo"] = func(w http.ResponseWriter, _ *http.Request) {
			calls++
			if calls == 1 {
				w.WriteHeader(404)
				return
			}
			_, _ = w.Write([]byte(bitbucketRepoJSON))
		}
		base = serve(t, h)
		if got, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo"}, targetOpts(base)); err != nil || got.Created {
			t.Fatalf("race = %+v, %v", got, err)
		}
		base = serve(t, newJSONHandler(t).on("GET /2.0/repositories/me/repo", 404, ``).on("POST /2.0/repositories/me/repo", 409, ``))
		if _, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo"}, targetOpts(base)); !statusIs(err, 404) {
			t.Fatalf("race then missing = %v", err)
		}
		base = serve(t, newJSONHandler(t).on("GET /2.0/repositories/me/repo", 500, ``))
		if _, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo"}, targetOpts(base)); !statusIs(err, 500) {
			t.Fatalf("get failure = %v", err)
		}
		base = serve(t, newJSONHandler(t).on("GET /2.0/repositories/me/repo", 404, ``).on("POST /2.0/repositories/me/repo", 400, `bad slug`))
		if _, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo"}, targetOpts(base)); !statusIs(err, 400) {
			t.Fatalf("create failure = %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// GitHub
// ---------------------------------------------------------------------------

const githubRepoJSON = `{"name":"repo","full_name":"me/repo","private":true,"clone_url":"https://github.com/me/repo.git","ssh_url":"git@github.com:me/repo.git"}`

func TestGitHubTarget(t *testing.T) {
	ctx := context.Background()
	tg := mustTarget(t, "github")

	t.Run("whoami", func(t *testing.T) {
		base := serve(t, newJSONHandler(t).on("GET /api/v3/user", 200, `{"login":"me"}`))
		if got, err := tg.Whoami(ctx, targetOpts(base)); err != nil || got != "me" {
			t.Fatalf("whoami = %q, %v", got, err)
		}
		base = serve(t, newJSONHandler(t).on("GET /api/v3/user", 200, `{}`))
		if _, err := tg.Whoami(ctx, targetOpts(base)); err == nil {
			t.Fatal("expected an empty login to fail")
		}
		base = serve(t, newJSONHandler(t).on("GET /api/v3/user", 401, `{"message":"bad credentials"}`))
		if _, err := tg.Whoami(ctx, targetOpts(base)); err == nil {
			t.Fatal("expected 401 to fail")
		}
	})

	t.Run("existing and create", func(t *testing.T) {
		base := serve(t, newJSONHandler(t).on("GET /api/v3/repos/me/repo", 200, githubRepoJSON))
		got, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo", Private: true}, targetOpts(base))
		if err != nil || got.Created || !got.Private || got.SSHURL != "git@github.com:me/repo.git" {
			t.Fatalf("existing = %+v, %v", got, err)
		}
		h := newJSONHandler(t).on("GET /api/v3/repos/me/repo", 404, `{"message":"Not Found"}`).on("POST /api/v3/user/repos", 201, githubRepoJSON)
		base = serve(t, h)
		if got, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo", Private: true, Description: "d"}, targetOpts(base)); err != nil || !got.Created {
			t.Fatalf("personal = %+v, %v", got, err)
		}
		if b := h.bodies["POST /api/v3/user/repos"]; b["private"] != true || b["auto_init"] != false || b["description"] != "d" {
			t.Fatalf("payload = %v", b)
		}
		h = newJSONHandler(t).on("GET /api/v3/repos/org/repo", 404, `{}`).on("POST /api/v3/orgs/org/repos", 201, githubRepoJSON)
		base = serve(t, h)
		if _, err := tg.EnsureRepo(ctx, "org", RepoSpec{Name: "repo"}, targetOpts(base)); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("lost race and failures", func(t *testing.T) {
		calls := 0
		h := newJSONHandler(t).on("POST /api/v3/user/repos", 422, `{"message":"Repository creation failed.","errors":[{"resource":"Repository","code":"custom","field":"name","message":"name already exists on this account"}]}`)
		h.routes["GET /api/v3/repos/me/repo"] = func(w http.ResponseWriter, _ *http.Request) {
			calls++
			if calls == 1 {
				w.WriteHeader(404)
				_, _ = w.Write([]byte(`{}`))
				return
			}
			_, _ = w.Write([]byte(githubRepoJSON))
		}
		base := serve(t, h)
		if got, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo"}, targetOpts(base)); err != nil || got.Created {
			t.Fatalf("race = %+v, %v", got, err)
		}
		base = serve(t, newJSONHandler(t).on("GET /api/v3/repos/me/repo", 404, `{}`).on("POST /api/v3/user/repos", 422, `{"message":"name already exists on this account"}`))
		if _, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo"}, targetOpts(base)); !githubStatusIs(err, 404) {
			t.Fatalf("race then missing = %v", err)
		}
		base = serve(t, newJSONHandler(t).on("GET /api/v3/repos/me/repo", 500, `{}`))
		if _, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo"}, targetOpts(base)); !githubStatusIs(err, 500) {
			t.Fatalf("get failure = %v", err)
		}
		base = serve(t, newJSONHandler(t).on("GET /api/v3/repos/me/repo", 404, `{}`).on("POST /api/v3/user/repos", 422, `{"message":"name is too long"}`))
		if _, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo"}, targetOpts(base)); !githubStatusIs(err, 422) {
			t.Fatalf("create failure = %v", err)
		}
		if githubStatusIs(errors.New("plain"), 404) || githubStatusIs(&gh.ErrorResponse{}, 404) {
			t.Fatal("githubStatusIs must need a response")
		}
	})

	t.Run("client construction failure", func(t *testing.T) {
		old := newTargetGitHubClient
		newTargetGitHubClient = func(...gh.ClientOptionsFunc) (*gh.Client, error) { return nil, fmt.Errorf("broken") }
		t.Cleanup(func() { newTargetGitHubClient = old })
		if _, err := tg.Whoami(ctx, TargetOptions{Token: "tok"}); err == nil || err.Error() != "broken" {
			t.Fatalf("whoami = %v", err)
		}
		if _, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo"}, TargetOptions{Token: "tok"}); err == nil {
			t.Fatal("expected EnsureRepo to fail")
		}
	})
}
