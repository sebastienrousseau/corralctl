// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/engine"
	"github.com/sebastienrousseau/corralctl/internal/github"
)

// callerHost stands in for a host an MCP caller names in forge_url. It
// records every header value it is sent.
func callerHost(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		for _, vs := range r.Header {
			seen = append(seen, vs...)
		}
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), seen...)
	}
}

// forgeSecrets puts a distinct credential in each forge's environment
// variable, so a leak of any of them is visible.
func forgeSecrets(t *testing.T) []string {
	t.Helper()
	env := map[string]string{
		"GITHUB_TOKEN":           "secret-github",
		"CORRAL_GITLAB_TOKEN":    "secret-gitlab",
		"CORRAL_FORGE_TOKEN":     "secret-gitea",
		"CORRAL_BITBUCKET_TOKEN": "secret-bitbucket",
	}
	var out []string
	for k, v := range env {
		t.Setenv(k, v)
		out = append(out, v)
	}
	return out
}

// An MCP caller must not be able to send the operator's forge
// credentials to a host of its choosing.
func TestAdoptRepoRefusesACallerChosenForgeURL(t *testing.T) {
	secrets := forgeSecrets(t)
	for _, name := range []string{"github", "gitlab", "gitea", "forgejo", "codeberg", "bitbucket"} {
		t.Run(name, func(t *testing.T) {
			srv, seen := callerHost(t)
			base := t.TempDir()
			if err := os.MkdirAll(filepath.Join(base, "repo1"), 0o750); err != nil {
				t.Fatal(err)
			}
			h, auditPath := mutationHarness(t, base, false)
			res, isErr := h.callTool("corral_adopt_repo", map[string]any{
				"repo_name":  "r",
				"local_path": "repo1",
				"forge":      name,
				"forge_url":  srv.URL,
			})
			for _, v := range seen() {
				for _, s := range secrets {
					if strings.Contains(v, s) {
						t.Fatalf("%s token reached the caller's host %s", s, srv.URL)
					}
				}
			}
			if len(seen()) != 0 {
				t.Errorf("a request reached the caller's host before the refusal")
			}
			if !isErr || !strings.Contains(res, "forge_url") {
				t.Fatalf("result = %s (error=%t), want a refusal naming forge_url", res, isErr)
			}
			recs := auditRecords(t, auditPath)
			if len(recs) != 1 || recs[0].Result != "refused" || recs[0].Tool != "corral_adopt_repo" {
				t.Fatalf("audit = %+v, want one refused record", recs)
			}
		})
	}
}

// An instance the operator named when starting the server is accepted,
// in any equivalent spelling, and reaches the adoption unchanged.
func TestAdoptRepoAcceptsAnOperatorConfiguredForgeURL(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "repo1"), 0o750); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, ServerOptions{
		Root:            base,
		EnableMutations: true,
		AuditLogPath:    filepath.Join(t.TempDir(), "audit.log"),
		ForgeURLs:       []string{"not a url", "https://Git.Example.com:8443/forge/"},
	})
	var got engine.AdoptOptions
	stubSeam(t, &adoptForgeToken, func(context.Context, string, github.AuthMode) string { return "tok" })
	stubSeam(t, &adoptExecFunc, func(_ context.Context, o engine.AdoptOptions) (engine.AdoptResult, error) {
		got = o
		return engine.AdoptResult{RepoName: o.RepoName, Action: "ADOPTED"}, nil
	})
	res, isErr := h.callTool("corral_adopt_repo", map[string]any{
		"repo_name": "r", "local_path": "repo1", "forge": "gitea",
		"forge_url": "https://git.example.com:8443/forge",
	})
	if isErr || got.ForgeOptions.BaseURL != "https://git.example.com:8443/forge" {
		t.Fatalf("result %s (error=%t), base URL %q", res, isErr, got.ForgeOptions.BaseURL)
	}
}

func TestInstanceKey(t *testing.T) {
	same := []string{"https://git.example.com/x", "https://GIT.example.com/x/", " https://git.example.com/x "}
	for _, raw := range same {
		if instanceKey(raw) != "https://git.example.com/x" {
			t.Errorf("instanceKey(%q) = %q", raw, instanceKey(raw))
		}
	}
	for _, raw := range []string{"", "git.example.com", "ftp://git.example.com", "https://user:pw@git.example.com", "://bad"} {
		if k := instanceKey(raw); k != "" {
			t.Errorf("instanceKey(%q) = %q, want no match", raw, k)
		}
	}
	// A different host, scheme or path is a different instance.
	s := &Server{opts: ServerOptions{ForgeURLs: []string{"https://git.example.com/x"}}}
	for _, raw := range []string{"http://git.example.com/x", "https://git.example.com", "https://git.example.com.evil/x", "https://user@git.example.com/x"} {
		if s.allowedForgeURL(raw) == nil {
			t.Errorf("%q was accepted", raw)
		}
	}
}

// A refusal whose audit record cannot be written still refuses.
func TestAdoptRepoForgeURLRefusalWithLostAudit(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "repo1"), 0o750); err != nil {
		t.Fatal(err)
	}
	h, _ := mutationHarness(t, base, false)
	failAuditAfter(t, 1)
	res, isErr := h.callTool("corral_adopt_repo", map[string]any{
		"repo_name": "r", "local_path": "repo1", "forge": "gitlab", "forge_url": "https://elsewhere.example",
	})
	if !isErr || !strings.Contains(res, "audit refusal failed") {
		t.Fatalf("result = %s (error=%t)", res, isErr)
	}
}
