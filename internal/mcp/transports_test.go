// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestServedWithoutSession pins the routing rule that lets one endpoint
// serve both current revisions. Each case is a request shape a real client
// sends; a wrong answer here would hand a 2025-11-25 client to a handler
// that refuses sessions, or a 2026-07-28 client a session it never asked
// for.
func TestServedWithoutSession(t *testing.T) {
	const discover = `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{}}`
	const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`
	const withMeta = `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28"}}}`
	const bare = `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`

	cases := []struct {
		name    string
		method  string
		headers map[string]string
		body    string
		want    bool
	}{
		{"header names the stateless revision", http.MethodPost, map[string]string{protocolVersionHeader: statelessRevision}, initialize, true},
		{"header names a later revision", http.MethodPost, map[string]string{protocolVersionHeader: "2027-01-01"}, initialize, true},
		{"header names the session revision", http.MethodPost, map[string]string{protocolVersionHeader: sessionRevision}, discover, false},
		{"GET with the stateless header", http.MethodGet, map[string]string{protocolVersionHeader: statelessRevision}, "", true},
		{"GET without a header opens the session stream", http.MethodGet, nil, "", false},
		{"DELETE without a header ends a session", http.MethodDelete, nil, "", false},
		{"a POST naming its session", http.MethodPost, map[string]string{sessionIDHeader: "abc"}, bare, false},
		{"initialize without a header asks for a session", http.MethodPost, nil, initialize, false},
		{"server/discover without a header", http.MethodPost, nil, discover, true},
		{"_meta without a header", http.MethodPost, nil, withMeta, true},
		{"a bare call from a client that never initialised", http.MethodPost, nil, bare, true},
		{"not JSON", http.MethodPost, nil, "not json", true},
		{"an object that does not parse", http.MethodPost, nil, `{"method":`, true},
		{"a batch", http.MethodPost, nil, "[" + bare + "]", true},
		{"empty body", http.MethodPost, nil, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/mcp", strings.NewReader(tc.body))
			for k, v := range tc.headers {
				r.Header.Set(k, v)
			}
			if got := servedWithoutSession(r); got != tc.want {
				t.Errorf("servedWithoutSession = %t, want %t", got, tc.want)
			}
			// The body must still be there for the handler the request is
			// routed to.
			rest, _ := io.ReadAll(r.Body)
			if string(rest) != tc.body {
				t.Errorf("body after routing = %q, want %q", rest, tc.body)
			}
		})
	}

	t.Run("a body too large to inspect does not get a session", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(initialize))
		r.ContentLength = maxInterceptBody + 1
		if !servedWithoutSession(r) {
			t.Error("an uninspected body was handed to the session handler")
		}
	})
	t.Run("no body at all", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		r.Body = nil
		if !servedWithoutSession(r) {
			t.Error("a bodiless POST was handed to the session handler")
		}
	})
}

// do sends one request with the MCP headers a client sends, plus any extra,
// and returns status, headers and body.
func do(t *testing.T, method, url, body string, extra map[string]string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	// A 2026-07-28 request mirrors its method, and a tool call its name,
	// in headers the server checks against the body.
	if extra[protocolVersionHeader] >= statelessRevision && method == http.MethodPost {
		var call struct {
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		_ = json.Unmarshal([]byte(body), &call)
		req.Header.Set("Mcp-Method", call.Method)
		if call.Params.Name != "" {
			req.Header.Set("Mcp-Name", call.Params.Name)
		}
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	// A standalone stream never ends, so read only what a header check
	// needs from one; everything else is a complete body.
	if strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") && method == http.MethodGet {
		return res.StatusCode, res.Header, ""
	}
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, res.Header, string(b)
}

// TestStreamableServesTheSessionRevision walks a 2025-11-25 client through
// the endpoint: initialize issues a session, the session serves calls and
// the standalone GET stream, an unknown session is 404 — the status a
// client re-initialises on, which is why the wrapper must not turn it into
// 200 — and DELETE ends it.
func TestStreamableServesTheSessionRevision(t *testing.T) {
	ts := newHTTPTestServer(t)
	url := ts.URL + StreamableEndpoint

	status, hdr, body := do(t, http.MethodPost, url,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"era-test","version":"0"}}}`, nil)
	if status != http.StatusOK {
		t.Fatalf("initialize: status %d\n%s", status, body)
	}
	sid := hdr.Get("Mcp-Session-Id")
	if sid == "" {
		t.Fatal("initialize did not issue Mcp-Session-Id")
	}
	if !strings.Contains(body, `"protocolVersion":"2025-11-25"`) {
		t.Errorf("the server should negotiate the revision the client asked for:\n%s", body)
	}
	session := map[string]string{"Mcp-Session-Id": sid, protocolVersionHeader: sessionRevision}

	status, _, body = do(t, http.MethodPost, url,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`, session)
	if status != http.StatusAccepted {
		t.Errorf("initialized notification: status %d, want 202\n%s", status, body)
	}

	status, _, body = do(t, http.MethodPost, url,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`, session)
	if status != http.StatusOK || !strings.Contains(body, "corral_list_repos") {
		t.Errorf("tools/list on the session: status %d\n%s", status, body)
	}

	status, hdr, _ = do(t, http.MethodGet, url, "", session)
	if status != http.StatusOK || !strings.HasPrefix(hdr.Get("Content-Type"), "text/event-stream") {
		t.Errorf("GET on a session should open the event stream: status %d, type %q", status, hdr.Get("Content-Type"))
	}

	status, _, body = do(t, http.MethodPost, url,
		`{"jsonrpc":"2.0","id":3,"method":"no/such","params":{}}`, session)
	if status != http.StatusOK || !strings.Contains(body, `"code":-32601`) {
		t.Errorf("unknown method on a session should be a JSON-RPC -32601 with 200: status %d\n%s", status, body)
	}

	unknown := map[string]string{"Mcp-Session-Id": "not-a-session", protocolVersionHeader: sessionRevision}
	status, _, body = do(t, http.MethodPost, url,
		`{"jsonrpc":"2.0","id":4,"method":"ping","params":{}}`, unknown)
	if status != http.StatusNotFound {
		t.Errorf("an unknown session must keep its 404: status %d\n%s", status, body)
	}

	status, _, body = do(t, http.MethodDelete, url, "", session)
	if status != http.StatusNoContent {
		t.Errorf("DELETE should end the session: status %d\n%s", status, body)
	}
}

// TestStreamableServesTheStatelessRevision walks a 2026-07-28 client
// through the same endpoint: server/discover instead of initialize, no
// session header, GET refused, and the mirrored-header check answered
// with the JSON-RPC error the spec prescribes rather than a 200.
func TestStreamableServesTheStatelessRevision(t *testing.T) {
	ts := newHTTPTestServer(t)
	url := ts.URL + StreamableEndpoint
	meta := `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}`
	stateless := map[string]string{protocolVersionHeader: statelessRevision}

	status, hdr, body := do(t, http.MethodPost, url,
		`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{`+meta+`}}`, stateless)
	if status != http.StatusOK || !strings.Contains(body, `"name":"corral"`) {
		t.Fatalf("server/discover: status %d\n%s", status, body)
	}
	if hdr.Get("Mcp-Session-Id") != "" {
		t.Error("the stateless revision must not issue a session")
	}

	// The opening move without a header: routed by its name, and the SDK
	// says what is missing.
	status, _, body = do(t, http.MethodPost, url,
		`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{`+meta+`}}`, nil)
	if status != http.StatusBadRequest || !strings.Contains(body, protocolVersionHeader) {
		t.Errorf("discover without the header: status %d\n%s", status, body)
	}

	status, _, body = do(t, http.MethodPost, url,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{`+meta+`}}`, stateless)
	if status != http.StatusOK || !strings.Contains(body, "corral_list_repos") {
		t.Errorf("tools/list stateless: status %d\n%s", status, body)
	}

	status, _, body = do(t, http.MethodGet, url, "", stateless)
	if status != http.StatusMethodNotAllowed {
		t.Errorf("GET is not part of the stateless revision: status %d\n%s", status, body)
	}

	// A tool the server does not have is an application-level mistake and
	// gets a result the agent can read — under this revision the SDK's
	// -32602 would carry HTTP 400, which a transport retries rather than
	// reads.
	status, _, body = do(t, http.MethodPost, url,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"no_such_tool","arguments":{},`+meta+`}}`, stateless)
	if status != http.StatusOK || !strings.Contains(body, `"isError":true`) || !strings.Contains(body, "no_such_tool") {
		t.Errorf("unknown tool should be an isError result with 200: status %d\n%s", status, body)
	}
	// No name at all is a malformed call, not an unknown tool, and stays the
	// SDK's Invalid params.
	_, _, body = do(t, http.MethodPost, url,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"arguments":{},`+meta+`}}`, stateless)
	if strings.Contains(body, `"isError"`) || !strings.Contains(body, `"error"`) {
		t.Errorf("a nameless call should be a JSON-RPC error, not a tool result:\n%s", body)
	}

	status, _, body = do(t, http.MethodPost, url,
		`{"jsonrpc":"2.0","id":3,"method":"no/such","params":{`+meta+`}}`, stateless)
	if status != http.StatusOK || !strings.Contains(body, `"code":-32601`) {
		t.Errorf("unknown method stateless should be a JSON-RPC -32601 with 200: status %d\n%s", status, body)
	}

	// Header says one revision, body another: a JSON-RPC error, and not a
	// 200 — the wrapper only corrects the status of a method-not-found.
	status, _, body = do(t, http.MethodPost, url,
		`{"jsonrpc":"2.0","id":4,"method":"ping","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2025-11-25"}}}`, stateless)
	if status != http.StatusBadRequest || !strings.Contains(body, `"error"`) {
		t.Errorf("mirrored header mismatch: status %d\n%s", status, body)
	}
}

// TestTransportRefusalsKeepTheirStatus: the wrapper reshapes what the SDK
// refused at the JSON-RPC layer and nothing else. An unsupported protocol
// header is the HTTP request failing, and a client that got 200 for it
// would carry on with a version the server does not speak.
func TestTransportRefusalsKeepTheirStatus(t *testing.T) {
	ts := newHTTPTestServer(t)
	url := ts.URL + StreamableEndpoint

	status, _, body := do(t, http.MethodPost, url,
		`{"jsonrpc":"2.0","id":1,"method":"ping","params":{}}`,
		map[string]string{protocolVersionHeader: "1999-01-01"})
	if status != http.StatusBadRequest {
		t.Errorf("bad protocol version: status %d, want 400\n%s", status, body)
	}
	if json.Valid([]byte(body)) {
		t.Errorf("the transport's own refusal should pass through as it was:\n%s", body)
	}

	// The root path still serves, for a client configured against the
	// address alone.
	status, _, body = do(t, http.MethodPost, ts.URL+"/",
		`{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`,
		map[string]string{protocolVersionHeader: statelessRevision})
	if status != http.StatusOK {
		t.Errorf("root path: status %d\n%s", status, body)
	}
}

// TestRefusalClassifiers covers the two decisions the wrapper makes about
// a refused response, on inputs the live tests above cannot easily produce.
func TestRefusalClassifiers(t *testing.T) {
	for _, tc := range []struct {
		body string
		want bool
	}{
		{`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"x"}}`, true},
		{`{"jsonrpc":"2.0","id":1,"error":{"code":-32020,"message":"x"}}`, false},
		{`{"jsonrpc":"2.0","id":1,"result":{}}`, false},
		{`not json`, false},
	} {
		if got := isMethodNotFound([]byte(tc.body)); got != tc.want {
			t.Errorf("isMethodNotFound(%s) = %t, want %t", tc.body, got, tc.want)
		}
	}

	for _, tc := range []struct {
		status int
		body   string
		want   bool
	}{
		{http.StatusBadRequest, `JSON RPC not handled: "x" unsupported`, true},
		{http.StatusBadRequest, `invalid request: missing id for "ping"`, true},
		{http.StatusBadRequest, `Bad Request: Unsupported protocol version`, false},
		{http.StatusNotFound, `JSON RPC not handled`, false},
	} {
		cw := &captureWriter{ResponseWriter: httptest.NewRecorder(), status: tc.status}
		cw.body.WriteString(tc.body)
		if got := refusedEnvelope(cw); got != tc.want {
			t.Errorf("refusedEnvelope(%d, %q) = %t, want %t", tc.status, tc.body, got, tc.want)
		}
	}
}

// TestSSEServesTheLegacyTransport drives the SDK's own SSE client against
// the handler: a GET opens the stream, the endpoint event names where to
// POST, and the session serves a real call.
func TestSSEServesTheLegacyTransport(t *testing.T) {
	srv := newTestServer(t, t.TempDir())
	ts := httptest.NewServer(srv.sseHandler())
	t.Cleanup(func() {
		ts.Close()
		http.DefaultTransport.(*http.Transport).CloseIdleConnections()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "sse-test", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.SSEClientTransport{Endpoint: ts.URL + SSEEndpoint}, nil)
	if err != nil {
		t.Fatalf("connect over SSE: %v", err)
	}
	defer func() { _ = session.Close() }()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list over SSE: %v", err)
	}
	if len(tools.Tools) == 0 {
		t.Error("no tools over SSE")
	}

	// The endpoint is the only path served; anything else is not the
	// transport.
	res, err := http.Get(ts.URL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("GET /mcp on the SSE listener: status %d, want 404", res.StatusCode)
	}
}

// TestServeSSEListensAndStops covers the listener end to end: a real
// client over SSE, and a clean stop on context cancellation.
func TestServeSSEListensAndStops(t *testing.T) {
	s := newTestServer(t, t.TempDir())
	addr := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- s.ServeSSE(ctx, addr) }()
	waitForListener(t, addr)

	client := mcp.NewClient(&mcp.Implementation{Name: "sse-test", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.SSEClientTransport{Endpoint: "http://" + addr + SSEEndpoint}, nil)
	if err != nil {
		t.Fatalf("connect over SSE: %v", err)
	}
	if err := session.Ping(ctx, nil); err != nil {
		t.Errorf("ping over SSE: %v", err)
	}
	_ = session.Close()
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()

	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("ServeSSE returned %v on cancellation", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ServeSSE did not return within 10s of the context being cancelled")
	}
}

// TestServeHTTPEndsLeftoverSessions: a 2025-11-25 client that never sends
// DELETE leaves a session behind. Stopping the listener has to end it, or
// the goroutines behind it outlive the server that made them.
func TestServeHTTPEndsLeftoverSessions(t *testing.T) {
	s := newTestServer(t, t.TempDir())
	addr := freePort(t)
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- s.ServeHTTP(ctx, addr) }()
	waitForListener(t, addr)

	status, hdr, body := do(t, http.MethodPost, "http://"+addr+StreamableEndpoint,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"era-test","version":"0"}}}`, nil)
	if status != http.StatusOK || hdr.Get("Mcp-Session-Id") == "" {
		t.Fatalf("initialize: status %d\n%s", status, body)
	}
	http.DefaultTransport.(*http.Transport).CloseIdleConnections()

	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("ServeHTTP returned %v on cancellation", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ServeHTTP did not return within 10s of the context being cancelled")
	}
	for range s.mcp.Sessions() {
		t.Error("a session survived the listener stopping")
	}
}
