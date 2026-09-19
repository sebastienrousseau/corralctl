// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newHTTPTestServer starts the real handler chain over loopback.
//
// It deliberately goes through Server.httpHandler rather than wrapping a stub:
// this layer exists to reshape responses the SDK produced, so a stand-in inner
// handler would test the wrapper against fiction.
//
// A bare `initialize` opens a 2025-11-25 session that nothing in these tests
// ends, so the cleanup closes whatever sessions are left: the package's
// goroutine-leak check would otherwise find the session's reader.
func newHTTPTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := newTestServer(t, t.TempDir())
	ts := httptest.NewServer(srv.httpHandler())
	t.Cleanup(func() {
		ts.Close()
		for session := range srv.mcp.Sessions() {
			_ = session.Close()
		}
	})
	return ts
}

// postRPC sends a raw JSON body and returns status and body.
func postRPC(t *testing.T, ts *httptest.Server, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(b)
}

// rpcError is the error half of a JSON-RPC response, for assertions.
type rpcError struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// TestUnknownMethodReturnsJSONRPCError is the regression for the reported
// defect: an unknown method answered with HTTP 400 and a plain-text body.
//
// In JSON-RPC over HTTP the transport succeeded — the server received and
// understood the envelope — so the status is 200 and the failure is reported
// in the body. A client handed 400 with text cannot tell "you asked for
// something I do not have" from "your request was malformed", and the usual
// response to a 4xx is to retry.
func TestUnknownMethodReturnsJSONRPCError(t *testing.T) {
	ts := newHTTPTestServer(t)

	status, body := postRPC(t, ts,
		`{"jsonrpc":"2.0","id":1,"method":"does/notExist","params":{}}`)

	if status != http.StatusOK {
		t.Errorf("status = %d, want 200 (JSON-RPC reports method errors in the body)\nbody: %s", status, body)
	}

	var got rpcError
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("response is not JSON: %v\nbody: %s", err, body)
	}
	if got.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want \"2.0\"", got.JSONRPC)
	}
	if got.Error == nil {
		t.Fatalf("no error object in response: %s", body)
	}
	if got.Error.Code != codeMethodNotFound {
		t.Errorf("code = %d, want %d (Method not found)", got.Error.Code, codeMethodNotFound)
	}
	// The id must come back, or a client cannot match the response to the
	// call it is waiting on.
	if id, ok := got.ID.(float64); !ok || id != 1 {
		t.Errorf("id = %v, want 1", got.ID)
	}
}

// TestUnknownMethodIDShapesPreserved checks the id round-trips as sent.
//
// JSON-RPC ids may be strings or numbers, and a null id is distinct from an
// absent one. Decoding into `any` would turn 1 into a float and risk emitting
// 1e+00, so the wrapper keeps the raw bytes.
func TestUnknownMethodIDShapesPreserved(t *testing.T) {
	ts := newHTTPTestServer(t)

	for _, tc := range []struct{ name, id, want string }{
		{"number", `7`, `7`},
		{"string", `"abc"`, `"abc"`},
		{"large number", `9007199254740993`, `9007199254740993`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body := postRPC(t, ts,
				`{"jsonrpc":"2.0","id":`+tc.id+`,"method":"nope/nope"}`)
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200", status)
			}
			var raw map[string]json.RawMessage
			if err := json.Unmarshal([]byte(body), &raw); err != nil {
				t.Fatalf("not JSON: %v\n%s", err, body)
			}
			if got := string(raw["id"]); got != tc.want {
				t.Errorf("id = %s, want %s (ids must round-trip unchanged)", got, tc.want)
			}
		})
	}
}

// TestUnknownNotificationIsAccepted checks that a notification — a request
// with no id — gets no response body.
//
// JSON-RPC forbids replying to a notification, so there is nothing to put an
// error in; MCP's HTTP transport says the server answers 202 once it has
// accepted one. Before the fix this was also a 400 with a text body.
func TestUnknownNotificationIsAccepted(t *testing.T) {
	ts := newHTTPTestServer(t)

	status, body := postRPC(t, ts, `{"jsonrpc":"2.0","method":"does/notExist"}`)

	if status != http.StatusAccepted {
		t.Errorf("status = %d, want 202 for a notification\nbody: %s", status, body)
	}
	if strings.TrimSpace(body) != "" {
		t.Errorf("body = %q, want empty (a notification gets no response)", body)
	}
}

// TestKnownMethodPassesThroughUntouched is the other half of the contract.
//
// The wrapper must not change a served response. This is the assertion that
// makes the rest safe: if it broke the working path, every other test here
// could still pass while the server became useless.
func TestKnownMethodPassesThroughUntouched(t *testing.T) {
	ts := newHTTPTestServer(t)

	status, body := postRPC(t, ts,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200\nbody: %s", status, body)
	}
	if !strings.Contains(body, `"tools"`) {
		t.Errorf("tools/list did not return a tool list\nbody: %s", body)
	}
	if strings.Contains(body, `"error"`) {
		t.Errorf("tools/list returned an error\nbody: %s", body)
	}
}

// TestJSONRPCMethodsMatchSDK pins the method list against the SDK.
//
// jsonrpcMethods only picks an error code, so a stale entry cannot disable a
// working method — but a stale list would still mislabel a real method as
// "Method not found", and nothing else in the build would notice. Probing each
// one turns an SDK rename into a failing test.
//
// The assertion is deliberately weak on purpose: it checks only that the
// method is not reported as unknown. Most of these need a session or params
// this request does not supply, so they answer with some other error — which
// is fine. What must never happen is -32601.
func TestJSONRPCMethodsMatchSDK(t *testing.T) {
	ts := newHTTPTestServer(t)

	for method := range jsonrpcMethods {
		t.Run(method, func(t *testing.T) {
			_, body := postRPC(t, ts,
				`{"jsonrpc":"2.0","id":1,"method":"`+method+`","params":{}}`)

			var got rpcError
			if err := json.Unmarshal([]byte(body), &got); err != nil {
				// A non-JSON body here means the SSE transport answered,
				// which only happens for a method it served.
				return
			}
			if got.Error != nil && got.Error.Code == codeMethodNotFound {
				t.Errorf("%q is in jsonrpcMethods but the server reports it unknown; "+
					"the SDK's method set has changed and the list needs updating\nbody: %s",
					method, body)
			}
		})
	}
}

// TestNonJSONRPCBodyIsLeftAlone checks the wrapper declines what it cannot
// interpret, rather than inventing an id for it.
func TestNonJSONRPCBodyIsLeftAlone(t *testing.T) {
	ts := newHTTPTestServer(t)

	status, body := postRPC(t, ts, `not json at all`)

	if status == http.StatusOK {
		t.Errorf("malformed body got 200; it should keep the transport's own error\nbody: %s", body)
	}
}

// TestBatchIsLeftToTheSDK documents the one case this layer does not reshape.
//
// Batching was removed from MCP in protocol 2025-06-18, so a current client
// does not send one. The SDK still parses arrays, and splitting a mixed batch
// to answer only the unknown elements would mean merging two response streams
// for a shape no compliant client produces. This asserts the known-good half
// still works, so the decision is recorded rather than assumed.
func TestBatchIsLeftToTheSDK(t *testing.T) {
	ts := newHTTPTestServer(t)

	status, body := postRPC(t, ts,
		`[{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}]`)

	if status != http.StatusOK {
		t.Fatalf("batch of known methods: status = %d, want 200\nbody: %s", status, body)
	}
	if !strings.Contains(body, `"tools"`) {
		t.Errorf("batch did not return a tool list\nbody: %s", body)
	}
}
