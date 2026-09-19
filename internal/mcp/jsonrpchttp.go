// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// maxInterceptBody bounds how much of a request body this layer will buffer
// to recover the JSON-RPC id. Anything larger is passed straight through:
// the id lives in the first few bytes of any real request, and a body big
// enough to exceed this is not one whose id we need to guess at.
const maxInterceptBody = 1 << 20 // 1 MiB

// jsonrpcMethods is every JSON-RPC method the MCP server accepts.
//
// It is used only to choose an error CODE, never to decide whether a request
// is served: the wrapper below delegates to the SDK first and looks at this
// list only after the SDK has already refused. That ordering matters. If the
// SDK gains a method and this list goes stale, the worst outcome is a
// less-specific code on a request that was failing anyway — never a working
// method turned off by a list that drifted.
//
// TestJSONRPCMethodsMatchSDK probes each of these against a live server and
// fails if one is no longer accepted, so drift is visible rather than silent.
var jsonrpcMethods = map[string]bool{
	"completion/complete":              true,
	"initialize":                       true,
	"ping":                             true,
	"prompts/list":                     true,
	"prompts/get":                      true,
	"tools/list":                       true,
	"tools/call":                       true,
	"resources/list":                   true,
	"resources/templates/list":         true,
	"resources/read":                   true,
	"resources/subscribe":              true,
	"resources/unsubscribe":            true,
	"logging/setLevel":                 true,
	"notifications/cancelled":          true,
	"notifications/initialized":        true,
	"notifications/progress":           true,
	"notifications/roots/list_changed": true,
	"subscriptions/listen":             true,
	"discover":                         true,
}

// jsonrpcError is the error object defined by JSON-RPC 2.0 §5.1.
type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// jsonrpcErrorResponse is a complete JSON-RPC error response.
type jsonrpcErrorResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   jsonrpcError    `json:"error"`
}

// JSON-RPC 2.0 §5.1 error codes.
const (
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
)

// captureWriter buffers a handler's response so it can be inspected and, if
// it turns out to be an HTTP error page, replaced. Anything successful is
// forwarded untouched — see flush.
type captureWriter struct {
	http.ResponseWriter
	status  int
	body    bytes.Buffer
	passing bool // true once bytes have gone straight to the client
}

func (c *captureWriter) WriteHeader(status int) {
	if c.status != 0 {
		return
	}
	c.status = status
	// A success is forwarded immediately and never buffered: streaming
	// responses (the SSE the transport uses for tool results) must reach
	// the client as they are produced, not when the handler returns.
	if status < 400 {
		c.passing = true
		c.ResponseWriter.WriteHeader(status)
	}
}

func (c *captureWriter) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.WriteHeader(http.StatusOK)
	}
	if c.passing {
		return c.ResponseWriter.Write(b)
	}
	return c.body.Write(b)
}

// Flush forwards the streaming flush for responses being passed through.
// Without it the SSE transport buffers until the handler returns.
func (c *captureWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok && c.passing {
		f.Flush()
	}
}

// flush writes a buffered error response through unchanged. Used when the
// wrapper decides not to rewrite.
func (c *captureWriter) flush() {
	if c.passing {
		return
	}
	if c.status == 0 {
		c.status = http.StatusOK
	}
	c.ResponseWriter.WriteHeader(c.status)
	_, _ = c.ResponseWriter.Write(c.body.Bytes())
}

// jsonrpcHTTP wraps the MCP streamable handler so that a JSON-RPC request
// naming a method the server does not implement gets a JSON-RPC error
// response rather than an HTTP error page.
//
// The SDK answers an unknown method with `http.Error(w, ..., 400)` — a
// plain-text body a JSON-RPC client cannot parse — and, from protocol
// 2026-07-28, with a JSON-RPC error carrying HTTP 404. Neither is what a
// client expects: in JSON-RPC over HTTP the transport succeeded, so the
// status is 200 and the failure is reported in the body. A client that gets
// 400 with a text body has to guess whether it sent something malformed or
// asked for something unsupported, and the usual guess is to retry.
//
// The wrapper delegates first and only rewrites what the SDK refused at the
// JSON-RPC layer: an envelope it parsed and then would not dispatch. A
// refusal of the HTTP request itself — an unsupported Mcp-Protocol-Version,
// a session id the server does not know, a body it will not read — is the
// transport failing, and keeps the status that says so; a client is meant
// to react to a 404 by starting a new session, and would not if it were
// handed 200. A successful response is streamed through untouched, so nothing
// on the working path passes through this logic.
func jsonrpcHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}

		// Buffer the body so the id and method survive being read by the
		// inner handler.
		body, err := readAndRestore(r)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}

		cw := &captureWriter{ResponseWriter: w}
		next.ServeHTTP(cw, r)

		if cw.passing || cw.status < 400 {
			cw.flush()
			return
		}
		// A JSON body from the SDK is already a JSON-RPC error (the
		// 2026-07-28 path). When it reports the method unknown it needs
		// only the status corrected; any other JSON-RPC error it chose to
		// send with a 4xx — a header that disagrees with the body, say —
		// is passed on as the SDK shaped it.
		if isJSONResponse(cw) {
			if isMethodNotFound(cw.body.Bytes()) {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(cw.body.Bytes())
				return
			}
			cw.flush()
			return
		}

		id, method, ok := jsonrpcCall(body)
		if !ok || !refusedEnvelope(cw) {
			cw.flush()
			return
		}
		if id == nil {
			// A notification gets no response body at all. MCP's HTTP
			// transport says the server returns 202 with an empty body
			// once it has accepted one.
			w.WriteHeader(http.StatusAccepted)
			return
		}

		code, msg := codeInvalidRequest, "Invalid Request"
		if !jsonrpcMethods[method] {
			code, msg = codeMethodNotFound, "Method not found: "+method
		}
		writeJSONRPCError(w, id, code, msg)
	})
}

// writeJSONRPCError emits a JSON-RPC error response with HTTP 200.
func writeJSONRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(jsonrpcErrorResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   jsonrpcError{Code: code, Message: msg},
	})
}

// isJSONResponse reports whether the captured response is already JSON, in
// which case it is the SDK's own JSON-RPC error and at most the status is
// wrong.
func isJSONResponse(cw *captureWriter) bool {
	if strings.Contains(cw.Header().Get("Content-Type"), "application/json") {
		return true
	}
	return json.Valid(bytes.TrimSpace(cw.body.Bytes()))
}

// isMethodNotFound reports whether a JSON-RPC error response carries
// -32601, the one code this layer changes the HTTP status for.
func isMethodNotFound(body []byte) bool {
	var res struct {
		Error *jsonrpcError `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(body), &res); err != nil || res.Error == nil {
		return false
	}
	return res.Error.Code == codeMethodNotFound
}

// sdkEnvelopeRefusals are the openings of the plain-text 400s the SDK writes
// when it has parsed a JSON-RPC request and declines to dispatch it: an
// unknown method, or an envelope wrong for the method it names (a call
// without an id, a notification with one, params missing). They are the
// messages of jsonrpc2.ErrNotHandled and jsonrpc2.ErrInvalidRequest,
// which the SDK wraps and hands to http.Error.
//
// Every other text 400 is about the HTTP request, not the envelope, and is
// left alone. TestUnknownMethodReturnsJSONRPCError runs against the real SDK,
// so a change in this wording fails a test rather than silently turning the
// rewrite off.
var sdkEnvelopeRefusals = []string{
	"JSON RPC not handled",
	"invalid request",
}

// refusedEnvelope reports whether the captured plain-text refusal is one of
// the SDK's JSON-RPC-layer refusals, as opposed to a transport-level one.
func refusedEnvelope(cw *captureWriter) bool {
	if cw.status != http.StatusBadRequest {
		return false
	}
	text := strings.TrimSpace(cw.body.String())
	for _, prefix := range sdkEnvelopeRefusals {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}

// readAndRestore reads the request body and puts it back, so the inner
// handler still sees a complete body.
//
// Bodies larger than maxInterceptBody are refused rather than buffered: this
// layer only needs the id, and buffering an arbitrary upload to recover it
// would hand a caller an easy way to make the server hold whatever it sent.
func readAndRestore(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, errNoBody
	}
	if r.ContentLength > maxInterceptBody {
		return nil, errBodyTooLarge
	}
	var buf bytes.Buffer
	limited := io.LimitReader(r.Body, maxInterceptBody+1)
	if _, err := buf.ReadFrom(limited); err != nil {
		return nil, err
	}
	if buf.Len() > maxInterceptBody {
		// Put back what was read so the inner handler is not handed a
		// truncated body, then decline to interpret it.
		r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(buf.Bytes()), r.Body))
		return nil, errBodyTooLarge
	}
	body := buf.Bytes()
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

var (
	errNoBody       = errors.New("mcp: request has no body")
	errBodyTooLarge = errors.New("mcp: request body too large to inspect")
)

// jsonrpcCall extracts the id and method of a single JSON-RPC request.
//
// A batch (a JSON array) returns false and is left to the SDK. Batching was
// removed from MCP in protocol 2025-06-18, so a current client does not send
// one; the array path here is legacy and keeps the behaviour it had.
//
// A missing id means a notification, which is distinct from an id of null —
// hence json.RawMessage rather than any, so the two do not collapse.
func jsonrpcCall(body []byte) (id json.RawMessage, method string, ok bool) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, "", false
	}
	var req struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if err := json.Unmarshal(trimmed, &req); err != nil {
		return nil, "", false
	}
	if req.Method == "" {
		return nil, "", false
	}
	return req.ID, req.Method, true
}
