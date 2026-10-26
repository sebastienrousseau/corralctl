// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package github

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestEnterpriseBase(t *testing.T) {
	cases := map[string]struct{ base, host string }{
		"":                         {"", ""},
		"  ":                       {"", ""},
		"https://github.com":       {"", ""},
		"https://GitHub.com/acme":  {"", ""},
		"https://www.github.com/":  {"", ""},
		"https://api.github.com/":  {"", ""},
		"https://ghe.corp":         {"https://ghe.corp", "ghe.corp"},
		"https://ghe.corp/acme":    {"https://ghe.corp", "ghe.corp"},
		"http://ghe.corp:8080/x/y": {"http://ghe.corp:8080", "ghe.corp:8080"},
	}
	for raw, want := range cases {
		base, host, err := enterpriseBase(raw)
		if err != nil || base != want.base || host != want.host {
			t.Errorf("enterpriseBase(%q) = %q, %q, %v; want %q, %q", raw, base, host, err, want.base, want.host)
		}
	}
	for _, raw := range []string{"ftp://ghe.corp", "https://", "://bad", "ghe.corp"} {
		if _, _, err := enterpriseBase(raw); err == nil || !strings.Contains(err.Error(), "forge URL") {
			t.Errorf("enterpriseBase(%q) err = %v, want a refusal", raw, err)
		}
	}
}

func TestEnterpriseTokenNeverFallsBackToGitHubDotCom(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "dotcom")
	t.Setenv("GH_TOKEN", "dotcom")
	t.Setenv("GH_ENTERPRISE_TOKEN", "")
	t.Setenv("GITHUB_ENTERPRISE_TOKEN", "ghes")
	if got := envToken("ghe.corp"); got != "ghes" {
		t.Errorf("envToken(enterprise) = %q, want ghes", got)
	}
	t.Setenv("GITHUB_ENTERPRISE_TOKEN", "")
	if got := envToken("ghe.corp"); got != "" {
		t.Errorf("envToken(enterprise) = %q; a github.com token must not be used for another host", got)
	}
	_, err := resolveTokenFor(context.Background(), AuthModeToken, "ghe.corp")
	if err == nil || !strings.Contains(err.Error(), "GH_ENTERPRISE_TOKEN") {
		t.Errorf("err = %v, want it to name GH_ENTERPRISE_TOKEN", err)
	}
}

func TestTokenFor(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "dotcom")
	t.Setenv("GH_ENTERPRISE_TOKEN", "ghes")
	if got := TokenFor(context.Background(), AuthModeToken, ""); got != "dotcom" {
		t.Errorf("TokenFor(github.com) = %q", got)
	}
	if got := TokenFor(context.Background(), AuthModeToken, "https://ghe.corp"); got != "ghes" {
		t.Errorf("TokenFor(ghe) = %q", got)
	}
	if got := TokenFor(context.Background(), AuthModeToken, "ftp://ghe.corp"); got != "" {
		t.Errorf("TokenFor(bad URL) = %q, want empty", got)
	}
	t.Setenv("GH_ENTERPRISE_TOKEN", "")
	old := runGitHubCLIAuthToken
	t.Cleanup(func() { runGitHubCLIAuthToken = old })
	var asked string
	runGitHubCLIAuthToken = func(_ context.Context, host string) (string, error) {
		asked = host
		return "", errors.New("not logged in")
	}
	if got := TokenFor(context.Background(), AuthModeAuto, "https://ghe.corp"); got != "" || asked != "ghe.corp" {
		t.Errorf("TokenFor(auto) = %q, asked gh for %q; want empty, ghe.corp", got, asked)
	}
}

func TestRunGitHubCLIAuthTokenNamesTheHost(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake gh PATH executable fixture is POSIX-specific")
	}
	realRun := runGitHubCLIAuthToken
	dir := t.TempDir()
	writeFakeGH(t, dir, "#!/bin/sh\necho \"$*\"\n")
	withPath(t, dir, func() {
		got, err := realRun(context.Background(), "ghe.corp")
		if err != nil || got != "auth token --hostname ghe.corp" {
			t.Fatalf("gh was run as %q (%v), want --hostname ghe.corp", got, err)
		}
		got, _ = realRun(context.Background(), "")
		if got != "auth token" {
			t.Fatalf("gh was run as %q for github.com", got)
		}
	})
}

func TestFetchReposWithOptionsUsesTheEnterpriseServer(t *testing.T) {
	var paths, auth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		auth = append(auth, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/api/v3/users/acme":
			_, _ = w.Write([]byte(`{"login":"acme","type":"Organization"}`))
		case "/api/v3/orgs/acme/repos":
			_, _ = w.Write([]byte(`[{"id":1,"name":"tool","full_name":"acme/tool","owner":{"login":"acme"}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("GH_ENTERPRISE_TOKEN", "ghes")

	repos, err := FetchReposWithOptions(context.Background(), "acme",
		FetchOptions{BaseURL: srv.URL, AuthMode: AuthModeToken})
	if err != nil || len(repos) != 1 {
		t.Fatalf("repos = %+v, err = %v", repos, err)
	}
	if paths[0] != "/api/v3/users/acme" || auth[0] != "Bearer ghes" {
		t.Errorf("first request %s with %q; want /api/v3/users/acme with the enterprise token", paths[0], auth[0])
	}
}

func TestFetchReposWithOptionsRefusesABadBaseURL(t *testing.T) {
	_, err := FetchReposWithOptions(context.Background(), "acme", FetchOptions{BaseURL: "ftp://ghe.corp"})
	if err == nil || !strings.Contains(err.Error(), "forge URL") {
		t.Fatalf("err = %v, want a refusal", err)
	}
}
