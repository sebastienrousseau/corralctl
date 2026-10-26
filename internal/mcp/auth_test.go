// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPTransportBearerAuth(t *testing.T) {
	dir := t.TempDir()
	const secretToken = "super-secret-mcp-token"
	srv, err := NewServer(ServerOptions{
		Root:      dir,
		Version:   "test",
		AuthToken: secretToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.httpHandler())
	t.Cleanup(func() {
		ts.Close()
		for session := range srv.mcp.Sessions() {
			_ = session.Close()
		}
	})

	url := ts.URL + StreamableEndpoint
	body := `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`

	// 1. Missing Authorization header
	noAuth := map[string]string{protocolVersionHeader: statelessRevision}
	status, hdr, b := do(t, http.MethodPost, url, body, noAuth)
	if status != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for missing auth, got %d: %s", status, b)
	}
	if !strings.Contains(hdr.Get("WWW-Authenticate"), "Bearer") {
		t.Errorf("expected WWW-Authenticate Bearer header, got %q", hdr.Get("WWW-Authenticate"))
	}

	// 2. Non-Bearer Authorization header
	basicAuth := map[string]string{
		protocolVersionHeader: statelessRevision,
		"Authorization":       "Basic dXNlcjpwYXNz",
	}
	status, _, _ = do(t, http.MethodPost, url, body, basicAuth)
	if status != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for Basic auth, got %d", status)
	}

	// 3. Invalid Bearer token
	badToken := map[string]string{
		protocolVersionHeader: statelessRevision,
		"Authorization":       "Bearer wrong-token",
	}
	status, hdr, _ = do(t, http.MethodPost, url, body, badToken)
	if status != http.StatusUnauthorized {
		t.Errorf("expected 401 Unauthorized for bad token, got %d", status)
	}
	if got := hdr.Get("WWW-Authenticate"); !strings.Contains(got, `error="invalid_token"`) {
		t.Errorf("a bad token should be challenged with error=\"invalid_token\", got %q", got)
	}

	// 4. Valid Bearer token
	goodToken := map[string]string{
		protocolVersionHeader: statelessRevision,
		"Authorization":       "Bearer " + secretToken,
	}
	status, _, b = do(t, http.MethodPost, url, body, goodToken)
	if status != http.StatusOK {
		t.Errorf("expected 200 OK for valid token, got %d: %s", status, b)
	}
}

func TestSSETransportAuthAndOrigin(t *testing.T) {
	dir := t.TempDir()
	const secretToken = "sse-secret-token"
	srv, err := NewServer(ServerOptions{
		Root:           dir,
		Version:        "test",
		AuthToken:      secretToken,
		AllowedOrigins: []string{"https://allowed.sse.io"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.sseHandler())
	t.Cleanup(func() {
		ts.Close()
	})

	url := ts.URL + SSEEndpoint

	// 1. Rejected origin on SSE
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Origin", "https://unauthorized.org")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for bad origin on SSE, got %d", res.StatusCode)
	}

	// 2. Missing token on SSE
	req, _ = http.NewRequest(http.MethodGet, url, nil)
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing token on SSE, got %d", res.StatusCode)
	}

	// 3. Invalid token on SSE
	req, _ = http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer invalid-secret")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid token on SSE, got %d", res.StatusCode)
	}
	if !strings.HasPrefix(res.Header.Get("WWW-Authenticate"), "Bearer ") {
		t.Errorf("expected a Bearer challenge for invalid token on SSE, got %q", res.Header.Get("WWW-Authenticate"))
	}

	// 4. Valid token and allowed origin on SSE
	req, _ = http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+secretToken)
	req.Header.Set("Origin", "https://allowed.sse.io")
	req.Header.Set("Accept", "text/event-stream")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Errorf("expected 200 OK for valid SSE connection, got %d", res.StatusCode)
	}
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		t.Errorf("expected text/event-stream, got %q", res.Header.Get("Content-Type"))
	}
}
