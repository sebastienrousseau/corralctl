// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/git"
)

// cloneAuthSeen runs a real `git clone` against a TLS server standing in
// for a forge and returns the Authorization header git sent, or "".
//
// The server answers 401 to an anonymous request and 404 otherwise, so
// the clone always fails; what matters is whether git had a credential
// to present.
func cloneAuthSeen(t *testing.T, srv *httptest.Server, seen *[]string, credential git.CredentialFunc) string {
	t.Helper()
	t.Setenv("GIT_SSL_NO_VERIFY", "true")
	_ = git.Clone(context.Background(), srv.URL+"/acme/private.git",
		filepath.Join(t.TempDir(), "private"), git.CloneOptions{Credential: credential})
	for _, a := range *seen {
		if a != "" {
			return a
		}
	}
	return ""
}

func fakeForge(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Authorization"))
		mu.Unlock()
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Basic realm="forge"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func basic(user, secret string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+secret))
}

func TestPrivateCloneFromGitLabPresentsTheForgeToken(t *testing.T) {
	srv, seen := fakeForge(t)
	t.Setenv("CORRAL_GITLAB_TOKEN", "gl-token")

	credential := runCloneCredential(context.Background(), RunOptions{Forge: "gitlab", ForgeURL: srv.URL})
	if got := cloneAuthSeen(t, srv, seen, credential); got != basic("oauth2", "gl-token") {
		t.Fatalf("Authorization = %q, want GitLab's oauth2 basic pair", got)
	}
}

func TestPrivateCloneFromGiteaPresentsTheForgeToken(t *testing.T) {
	srv, seen := fakeForge(t)
	t.Setenv("CORRAL_FORGE_TOKEN", "gt-token")

	credential := runCloneCredential(context.Background(), RunOptions{Forge: "forgejo", ForgeURL: srv.URL})
	if got := cloneAuthSeen(t, srv, seen, credential); got != basic("x-access-token", "gt-token") {
		t.Fatalf("Authorization = %q, want the token as the password", got)
	}
}

// The credential is scoped to the forge the run listed from. A clone URL
// on any other host, which a listing can contain, is sent nothing.
func TestCloneCredentialStaysOnItsForge(t *testing.T) {
	srv, seen := fakeForge(t)
	t.Setenv("CORRAL_GITLAB_TOKEN", "gl-token")

	credential := runCloneCredential(context.Background(), RunOptions{Forge: "gitlab"})
	if got := cloneAuthSeen(t, srv, seen, credential); got != "" {
		t.Fatalf("a gitlab.com credential reached %s: %q", srv.URL, got)
	}
}

func TestCloneCredentialEdges(t *testing.T) {

	// An unresolvable forge installs nothing; the run fails on it anyway.
	if runCloneCredential(context.Background(), RunOptions{Forge: "nope"}) != nil {
		t.Error("an unknown forge should yield no credential")
	}

	// Gitea with no instance has no origin to scope a token to.
	t.Setenv("CORRAL_FORGE_TOKEN", "gt-token")
	if origin, cred := runCloneCredential(context.Background(), RunOptions{Forge: "gitea"})(); origin != "" || cred != nil {
		t.Errorf("gitea without --forge-url = %q, %+v; want nothing", origin, cred)
	}

	// No token: an origin, but no credential.
	t.Setenv("CORRAL_BITBUCKET_TOKEN", "")
	t.Setenv("BITBUCKET_TOKEN", "")
	if origin, cred := runCloneCredential(context.Background(), RunOptions{Forge: "bitbucket"})(); origin != "https://bitbucket.org/" || cred != nil {
		t.Errorf("bitbucket without a token = %q, %+v", origin, cred)
	}
}
