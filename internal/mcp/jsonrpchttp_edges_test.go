// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The transport wrapper's edges.
//
// This layer sits in front of every request, so the paths it declines to touch
// matter as much as the one it rewrites: anything it mishandles here breaks a
// working call rather than fixing a broken one.

// failingBody is a request body that errors part-way through.
type failingBody struct{ n int }

func (f *failingBody) Read(p []byte) (int, error) {
	if f.n > 0 {
		f.n--
		p[0] = '{'
		return 1, nil
	}
	return 0, errors.New("read failed")
}
func (f *failingBody) Close() error { return nil }

func TestJSONRPCHTTPLeavesNonPostAlone(t *testing.T) {
	h := jsonrpcHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusMethodNotAllowed)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/mcp", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("a GET was rewritten: status %d", rec.Code)
	}
}

func TestJSONRPCHTTPPassesThroughAnUnreadableBody(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad", http.StatusBadRequest)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", &failingBody{n: 1})
	jsonrpcHTTP(inner).ServeHTTP(rec, req)
	// The id cannot be recovered, so the transport's own answer stands.
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want the transport's 400 to survive", rec.Code)
	}
}

func TestJSONRPCHTTPRejectsAnOversizedBody(t *testing.T) {
	// Larger than the inspection bound, so the wrapper declines to buffer it
	// and the inner handler still receives the whole body.
	big := bytes.Repeat([]byte("a"), maxInterceptBody+64)
	var got int
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, len(big)+16)
		n, _ := readFull(r.Body, b)
		got = n
		http.Error(w, "bad", http.StatusBadRequest)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(big))
	req.ContentLength = -1 // force the read path rather than the length check
	jsonrpcHTTP(inner).ServeHTTP(rec, req)
	if got != len(big) {
		t.Errorf("inner handler saw %d bytes of %d; a declined body must still arrive whole", got, len(big))
	}
}

func readFull(r interface{ Read([]byte) (int, error) }, b []byte) (int, error) {
	total := 0
	for total < len(b) {
		n, err := r.Read(b[total:])
		total += n
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, nil
		}
	}
	return total, nil
}

func TestJSONRPCHTTPDeclaredLengthOverBound(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad", http.StatusBadRequest)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader("{}"))
	req.ContentLength = maxInterceptBody + 1
	jsonrpcHTTP(inner).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want the transport's own answer", rec.Code)
	}
}

func TestJSONRPCHTTPLeavesSuccessUntouched(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{}}`))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	jsonrpcHTTP(inner).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"result"`) {
		t.Errorf("a served response was altered: %d %s", rec.Code, rec.Body.String())
	}
}

func TestJSONRPCHTTPCorrectsAJSONErrorStatus(t *testing.T) {
	// The SDK's newer path answers with a JSON-RPC error carrying HTTP 404.
	// The body is already right; only the status needs correcting.
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"nope"}}`))
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"nope/nope"}`))
	jsonrpcHTTP(inner).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status %d, want 200", rec.Code)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if _, ok := out["error"]; !ok {
		t.Errorf("the SDK's error object was lost: %s", rec.Body.String())
	}
}

func TestJSONRPCHTTPKeepsAKnownMethodsInvalidRequest(t *testing.T) {
	// A known method that the transport still refuses — a missing id, say —
	// is an invalid request, not a missing method, and must not be reported
	// as -32601. The wording is the SDK's own (jsonrpc2.ErrInvalidRequest,
	// as checkRequest wraps it), since that is what the wrapper keys on.
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `invalid request: missing id for "tools/list"`, http.StatusBadRequest)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/list"}`))
	jsonrpcHTTP(inner).ServeHTTP(rec, req)
	var got rpcError
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v — %s", err, rec.Body.String())
	}
	if got.Error == nil || got.Error.Code != codeInvalidRequest {
		t.Errorf("code = %+v, want %d for a known method", got.Error, codeInvalidRequest)
	}
}

// TestJSONRPCHTTPLeavesATransportRefusalAlone: a text 400 about the HTTP
// request rather than the envelope — an unsupported protocol header, a bad
// Accept — is not the wrapper's to reshape. A client is meant to read that
// status, and handed 200 with a JSON-RPC error it would carry on with a
// version or a content type the server does not speak.
func TestJSONRPCHTTPLeavesATransportRefusalAlone(t *testing.T) {
	const refusal = "Bad Request: Unsupported protocol version (supported versions: 2026-07-28)"
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, refusal, http.StatusBadRequest)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/list"}`))
	jsonrpcHTTP(inner).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status %d, want the transport's own 400", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != refusal {
		t.Errorf("body was rewritten: %s", rec.Body.String())
	}
}

func TestJSONRPCCallParsing(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		ok   bool
	}{
		{"batch array", `[{"jsonrpc":"2.0","id":1,"method":"x"}]`, false},
		{"empty", ``, false},
		{"not json", `hello`, false},
		{"no method", `{"jsonrpc":"2.0","id":1}`, false},
		{"malformed object", `{"jsonrpc":`, false},
		{"single call", `{"jsonrpc":"2.0","id":1,"method":"x"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, ok := jsonrpcCall([]byte(tc.body))
			if ok != tc.ok {
				t.Errorf("jsonrpcCall(%q) ok = %v, want %v", tc.body, ok, tc.ok)
			}
		})
	}
}

func TestIsJSONResponseFallsBackToSniffing(t *testing.T) {
	cw := &captureWriter{ResponseWriter: httptest.NewRecorder()}
	cw.body.WriteString(`{"a":1}`)
	if !isJSONResponse(cw) {
		t.Error("a JSON body without a Content-Type should still be recognised")
	}
	cw2 := &captureWriter{ResponseWriter: httptest.NewRecorder()}
	cw2.body.WriteString("plain text")
	if isJSONResponse(cw2) {
		t.Error("plain text was taken for JSON")
	}
}

func TestCaptureWriterIgnoresASecondHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	cw := &captureWriter{ResponseWriter: rec}
	cw.WriteHeader(http.StatusBadRequest)
	cw.WriteHeader(http.StatusOK) // must not change the recorded status
	if cw.status != http.StatusBadRequest {
		t.Errorf("status = %d, want the first one written", cw.status)
	}
	// Flush on a buffered (non-passing) writer must be a no-op, not a panic.
	cw.Flush()
	cw.flush()
	if rec.Code != http.StatusBadRequest {
		t.Errorf("flushed status = %d", rec.Code)
	}
	// Flushing twice must not write the body twice.
	cw.flush()
}

func TestCaptureWriterWriteWithoutHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	cw := &captureWriter{ResponseWriter: rec}
	if _, err := cw.Write([]byte("body")); err != nil {
		t.Fatal(err)
	}
	if cw.status != http.StatusOK {
		t.Errorf("an implicit write should imply 200, got %d", cw.status)
	}
	cw.Flush()
}

func TestJSONRPCHTTPHandlesAHandlerThatWritesNothing(t *testing.T) {
	// A handler that returns without touching the writer leaves no status at
	// all. Forwarding that as zero would produce an invalid response, so the
	// wrapper has to settle on 200 — the same thing net/http would have done.
	inner := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	jsonrpcHTTP(inner).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for a handler that wrote nothing", rec.Code)
	}
}
