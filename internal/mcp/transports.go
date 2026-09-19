// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The HTTP transports this server offers, and the protocol revisions each
// one speaks. Three ways in, one server behind them:
//
//   - stdio, the default: the client launches the process and owns the pipe.
//   - Streamable HTTP at StreamableEndpoint, speaking both current revisions
//     of the spec on that one path — see eraRouter.
//   - The legacy HTTP+SSE transport (protocol 2024-11-05) at SSEEndpoint,
//     for a host that has not moved on yet.
//
// None of them carries authentication. The cmd layer refuses a routable
// bind without --allow-remote; the server itself only knows how to listen.
const (
	// StreamableEndpoint is the path the Streamable HTTP transport serves.
	StreamableEndpoint = "/mcp"
	// SSEEndpoint is the path the legacy HTTP+SSE transport serves. A GET
	// here opens the event stream; the `endpoint` event it sends names where
	// the client POSTs its messages (the same path, with a session query).
	SSEEndpoint = "/sse"

	// statelessRevision is the first protocol revision with no session: the
	// request carries its own identity in `_meta`, mirrored in the
	// Mcp-Protocol-Version header, and `server/discover` replaces the
	// initialize handshake. The SDK serves it only from a stateless handler.
	statelessRevision = "2026-07-28"
	// sessionRevision is the newest revision that still initialises a session
	// and identifies it with Mcp-Session-Id. What most shipped clients speak.
	sessionRevision = "2025-11-25"

	// protocolVersionHeader mirrors the request's protocol revision, which
	// is how the two revisions are told apart before the body is opened.
	protocolVersionHeader = "Mcp-Protocol-Version"
	// sessionIDHeader names an established 2025-11-25 session.
	sessionIDHeader = "Mcp-Session-Id"
	// methodInitialize opens a session; it is the one POST that has to
	// reach the session handler without a header to say so.
	methodInitialize = "initialize"

	// sessionIdleTimeout is how long a 2025-11-25 session outlives its last
	// request. A client is meant to DELETE its session when it is done, but
	// most do not, and without a timeout every abandoned one would hold its
	// goroutines until the process exits. An hour is long enough that an
	// agent pausing to think does not lose its session, and a client that
	// does lose one is told 404 and re-initialises.
	sessionIdleTimeout = time.Hour
)

// eraRouter serves both current revisions of the Streamable HTTP transport
// on one endpoint.
//
// The SDK will not serve them from one handler: 2026-07-28 is only accepted
// by a handler configured stateless, and a stateless handler neither issues
// nor honours Mcp-Session-Id, which is the whole of 2025-11-25's session
// model. So there are two handlers over the same *mcp.Server, and each
// request is sent to the one that speaks its revision. A client sees a
// single URL that answers whichever it sends.
type eraRouter struct {
	// stateless serves 2026-07-28: no session, per-request _meta,
	// server/discover, GET and DELETE refused with 405.
	stateless http.Handler
	// session serves 2025-11-25 and earlier: initialize, Mcp-Session-Id,
	// GET for the server-to-client stream, DELETE to end the session.
	session http.Handler
}

func (e *eraRouter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if servedWithoutSession(r) {
		e.stateless.ServeHTTP(w, r)
		return
	}
	e.session.ServeHTTP(w, r)
}

// servedWithoutSession reports whether a request goes to the stateless
// handler.
//
// The header decides when it is present: the spec requires every request
// after the first to carry it, and the SDK compares revisions as strings,
// which the ISO dates make correct. Without one, the session handler gets
// exactly what concerns a session — the GET that opens its stream, the
// DELETE that ends it, a POST that names one in Mcp-Session-Id, and the
// `initialize` that asks for one. Every other POST is served on its own:
// `server/discover` and anything carrying `_meta`, which is 2026-07-28;
// and a bare call from a client that never initialised, or a batch from
// one older than sessions, which the stateless handler answers as it did
// before there was a choice. The one thing a sessionless request must not
// do is create a session nobody will end.
func servedWithoutSession(r *http.Request) bool {
	if v := r.Header.Get(protocolVersionHeader); v != "" {
		return v >= statelessRevision
	}
	if r.Method != http.MethodPost || r.Header.Get(sessionIDHeader) != "" {
		return false
	}
	body, err := readAndRestore(r)
	if err != nil {
		// Too large to look at, or nothing there: neither opens a
		// session, and the stateless handler says what is wrong.
		return true
	}
	return jsonrpcMethod(body) != methodInitialize
}

// jsonrpcMethod reads the method of a single JSON-RPC request. Empty for a
// batch, a notification without a method, or anything that is not an
// object — the SDK is left to answer those.
func jsonrpcMethod(body []byte) string {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return ""
	}
	var req struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(trimmed, &req); err != nil {
		return ""
	}
	return req.Method
}

// streamableHandler builds the dual-revision Streamable HTTP handler over
// this server, wrapped so that a refused method comes back as JSON-RPC.
func (s *Server) streamableHandler() http.Handler {
	getServer := func(*http.Request) *mcp.Server { return s.mcp }
	router := &eraRouter{
		stateless: mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{
			// Every request carries its own identity, so any instance can
			// serve any request and there is no session to leak between
			// clients. This is the only mode in which the SDK accepts
			// 2026-07-28.
			Stateless: true,
		}),
		session: mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{
			SessionTimeout: sessionIdleTimeout,
		}),
	}
	// jsonrpcHTTP turns the transport's HTTP error pages for unknown methods
	// back into JSON-RPC error responses, which is what a client can parse.
	// It delegates first and only rewrites what the SDK refused, so a served
	// request never passes through its logic.
	return jsonrpcHTTP(router)
}

// httpHandler is the mux ServeHTTP serves: the Streamable HTTP transport at
// StreamableEndpoint, and at the root for a client configured against the
// address alone, which is what `--http` documented before the endpoint had
// a name.
//
// Separate from ServeHTTP so a test can drive the real chain rather than
// assemble a lookalike: the whole point of the jsonrpcHTTP wrapper is what it
// does to responses the SDK produced, which a hand-rolled stand-in would not
// reproduce.
func (s *Server) httpHandler() http.Handler {
	h := s.streamableHandler()
	mux := http.NewServeMux()
	mux.Handle(StreamableEndpoint, h)
	mux.Handle("/", h)
	return mux
}

// sseHandler is the mux ServeSSE serves: the SDK's legacy HTTP+SSE transport
// at SSEEndpoint. The SDK issues the message endpoint relative to the path
// the stream was opened on, so the one handler serves both halves.
func (s *Server) sseHandler() http.Handler {
	h := mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return s.mcp }, nil)
	mux := http.NewServeMux()
	mux.Handle(SSEEndpoint, h)
	return mux
}

// ServeSSE runs the server on the legacy HTTP+SSE transport (protocol
// 2024-11-05), for a host that still expects it. Blocks until the listener
// fails or ctx is cancelled. Everything ServeHTTP says about binding applies
// here too: no authentication, so stay on loopback.
func (s *Server) ServeSSE(ctx context.Context, addr string) error {
	return s.serve(ctx, addr, s.sseHandler())
}
