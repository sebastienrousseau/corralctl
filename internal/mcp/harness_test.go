// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Test harness: a real client and server connected over the SDK's in-memory
// transport.
//
// Every test in this package used to call handler functions directly with a
// hand-built request. That skips the router, and it is precisely why a broken
// resource template survived: the handler was always correct, the route never
// matched, and no test could tell. Driving a real ClientSession exercises URI
// template matching, argument decoding against the generated schema, annotation
// serialisation and error shape — all the parts that live between "the handler
// is right" and "the tool works".
type harness struct {
	t       *testing.T
	server  *Server
	session *mcp.ClientSession
}

// newHarness starts a server over the given root and connects a client to it.
func newHarness(t *testing.T, opts ServerOptions) *harness {
	t.Helper()
	return newHarnessWithClient(t, opts, nil)
}

// newHarnessWithClient is newHarness with control over the client half.
//
// Elicitation is a server-to-client request, so a test that exercises it has
// to configure the client: the SDK infers the capability from the presence of
// an ElicitationHandler, which is exactly the signal the server reads before
// deciding whether anyone can be asked.
func newHarnessWithClient(t *testing.T, opts ServerOptions, clientOpts *mcp.ClientOptions) *harness {
	t.Helper()
	if opts.Version == "" {
		opts.Version = "test"
	}
	// Never the real one. Without this every test run writes into the
	// developer's own ~/.cache, and a test that asserts a cache miss would
	// pass or fail depending on what an earlier run left behind.
	if opts.SymbolCacheDir == "" {
		opts.SymbolCacheDir = t.TempDir()
	}
	// Same reasoning as the symbol cache: without this every test run writes
	// index files into ~/.cache, and a test asserting a cold index would pass
	// or fail depending on what an earlier run left there.
	if opts.IndexCacheDir == "" {
		opts.IndexCacheDir = t.TempDir()
	}
	srv, err := NewServer(opts)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverDone := make(chan error, 1)
	go func() { serverDone <- srv.mcp.Run(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "harness", Version: "test"}, clientOpts)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() {
		_ = session.Close()
		<-serverDone
		// Unmap only once the server has stopped and nothing can still be
		// reading an index. This runs before any TempDir this function created
		// is removed (cleanups are LIFO) and before one passed in as an
		// argument, since that TempDir was registered before the call. Windows
		// cannot delete a mapped file.
		srv.indexCache.closeAll()
	})
	return &harness{t: t, server: srv, session: session}
}

// callTool invokes a tool and returns its first text content. It fails the test
// on a protocol error, and returns isErr for a tool-execution error so callers
// can assert refusals.
func (h *harness) callTool(name string, args map[string]any) (text string, isErr bool) {
	h.t.Helper()
	res, err := h.session.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		h.t.Fatalf("call %s: %v", name, err)
	}
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String(), res.IsError
}

// callToolJSON invokes a tool and decodes its text content as JSON.
func (h *harness) callToolJSON(name string, args map[string]any, into any) {
	h.t.Helper()
	text, isErr := h.callTool(name, args)
	if isErr {
		h.t.Fatalf("call %s returned an error result: %s", name, text)
	}
	if err := json.Unmarshal([]byte(text), into); err != nil {
		h.t.Fatalf("decode %s result: %v\n%s", name, err, text)
	}
}

// readResource reads a resource URI through the router, so URI template
// matching is exercised.
func (h *harness) readResource(uri string) (text string, err error) {
	h.t.Helper()
	res, rerr := h.session.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
	if rerr != nil {
		return "", rerr
	}
	var b strings.Builder
	for _, c := range res.Contents {
		b.WriteString(c.Text)
	}
	return b.String(), nil
}

// tools lists the registered tools as the client sees them, including
// annotations as serialised on the wire.
func (h *harness) tools() map[string]*mcp.Tool {
	h.t.Helper()
	res, err := h.session.ListTools(context.Background(), nil)
	if err != nil {
		h.t.Fatalf("tools/list: %v", err)
	}
	out := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		out[tool.Name] = tool
	}
	return out
}

// prompt fetches a prompt through the router.
func (h *harness) prompt(name string, args map[string]string) *mcp.GetPromptResult {
	h.t.Helper()
	res, err := h.session.GetPrompt(context.Background(), &mcp.GetPromptParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		h.t.Fatalf("prompts/get %s: %v", name, err)
	}
	return res
}

// asToolResult applies the SDK's own rule for a handler's return values, so a
// test asserts what a client receives rather than which of two equivalent
// conventions the handler happened to use.
//
// AddTool turns a returned error into a CallToolResult with IsError set and the
// error's text as content (see CallToolResult.SetError), so a handler may refuse
// either by returning a toolError result or by returning an error — both reach
// the client as the same bytes. Tests that hard-coded the first form failed when
// handlers moved to the second to gain typed output, though nothing observable
// had changed.
// Generic over the output value so a handler's three return values can be
// passed straight in: asToolResult(s.handleX(...)).
func asToolResult[T any](res *mcp.CallToolResult, _ T, err error) *mcp.CallToolResult {
	if err != nil {
		var out mcp.CallToolResult
		out.SetError(err)
		return &out
	}
	return res
}

// newTestServer builds a Server without a client session, for tests that poke
// internals (scan caching, option validation) rather than protocol behaviour.
func newTestServer(t *testing.T, base string) *Server {
	t.Helper()
	srv, err := NewServer(ServerOptions{
		Root: base, Version: "test",
		SymbolCacheDir: t.TempDir(), IndexCacheDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Registered last, so it runs first: cleanups are LIFO, and on Windows a
	// mapped file cannot be deleted, so every mapping has to go before the
	// TempDir holding the index files is removed.
	t.Cleanup(srv.indexCache.closeAll)
	return srv
}

// marshalToMap round-trips a value through JSON into a map, for asserting on
// generated schemas without depending on the SDK's internal representation.
func marshalToMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// promptText extracts the text of a prompt message.
func promptText(m *mcp.PromptMessage) string {
	if tc, ok := m.Content.(*mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}
