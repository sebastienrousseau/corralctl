// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package forge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeEnterprise serves the two GitHub Enterprise Server endpoints a
// listing of an organisation needs, under /api/v3 as GHES does.
func fakeEnterprise(t *testing.T, auth *[]string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*auth = append(*auth, r.Header.Get("Authorization"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/users/acme":
			_, _ = w.Write([]byte(`{"login":"acme","type":"Organization"}`))
		case "/api/v3/orgs/acme/repos":
			_, _ = w.Write([]byte(`[{"id":1,"name":"tool","full_name":"acme/tool","owner":{"login":"acme"},` +
				`"visibility":"public","clone_url":"https://ghe.example/acme/tool.git"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// onlyTo lets requests reach allowed and fails, and records, every other
// host, so a listing that strays to api.github.com is caught without
// touching the network.
func onlyTo(t *testing.T, allowed string) *[]string {
	t.Helper()
	var strays []string
	var mu sync.Mutex
	real := http.DefaultTransport
	http.DefaultTransport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == allowed {
			return real.RoundTrip(r)
		}
		mu.Lock()
		strays = append(strays, r.URL.Host)
		mu.Unlock()
		return nil, errors.New("blocked: " + r.URL.Host)
	})
	t.Cleanup(func() { http.DefaultTransport = real })
	return &strays
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGitHubListsFromEnterpriseServer(t *testing.T) {
	var auth []string
	srv := fakeEnterprise(t, &auth)
	host := strings.TrimPrefix(srv.URL, "http://")
	strays := onlyTo(t, host)
	t.Setenv("GITHUB_TOKEN", "dotcom-token")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GH_ENTERPRISE_TOKEN", "ghes-token")
	t.Setenv("GITHUB_ENTERPRISE_TOKEN", "")

	f, err := Resolve("github", srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	repos, err := f.List(context.Background(), "acme", Options{BaseURL: srv.URL})
	if len(*strays) > 0 {
		t.Fatalf("--forge-url %s was ignored: requests went to %v", srv.URL, *strays)
	}
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].CloneURL != "https://ghe.example/acme/tool.git" {
		t.Fatalf("repos = %+v", repos)
	}
	// The github.com credential belongs to github.com. Sending it to
	// another host would hand it to whoever runs that host.
	for _, a := range auth {
		if a != "Bearer ghes-token" {
			t.Fatalf("Authorization = %q, want the enterprise token", a)
		}
	}
}

func TestGitHubPublicURLIsNotEnterprise(t *testing.T) {
	strays := onlyTo(t, "nowhere.invalid")
	t.Setenv("GITHUB_TOKEN", "dotcom-token")
	for _, raw := range []string{"https://github.com", "https://github.com/acme", "https://api.github.com/"} {
		f, _ := Get("github")
		_, _ = f.List(context.Background(), "acme", Options{BaseURL: raw})
		for _, h := range *strays {
			if h != "api.github.com" {
				t.Fatalf("%s should list from api.github.com, went to %s", raw, h)
			}
		}
	}
	if len(*strays) == 0 {
		t.Fatal("no request was made")
	}
}

func TestGitHubRefusesAnUnusableForgeURL(t *testing.T) {
	_ = onlyTo(t, "nowhere.invalid")
	t.Setenv("GITHUB_TOKEN", "dotcom-token")
	for _, raw := range []string{"ftp://ghe.example", "https://", "://bad"} {
		f, _ := Get("github")
		if _, err := f.List(context.Background(), "acme", Options{BaseURL: raw}); err == nil ||
			!strings.Contains(err.Error(), "forge URL") {
			t.Errorf("%q: err = %v, want a refusal naming the forge URL", raw, err)
		}
	}
}
