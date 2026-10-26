// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const metaTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"

// TestToolCallAdoptsMetaTraceparent: protocol 2026-07-28 carries W3C trace
// context in a request's _meta (SEP-414), and on stdio there is no HTTP
// header to read it from. A tool call that names a traceparent there must
// be recorded under that trace.
func TestToolCallAdoptsMetaTraceparent(t *testing.T) {
	traces := filepath.Join(t.TempDir(), "traces.jsonl")
	h := newHarness(t, ServerOptions{Root: t.TempDir(), TraceLogPath: traces})
	params := &mcp.CallToolParams{Name: "corral_list_repos", Arguments: map[string]any{}}
	params.SetMeta(map[string]any{"traceparent": "00-" + metaTraceID + "-00f067aa0ba902b7-01"})
	if _, err := h.session.CallTool(context.Background(), params); err != nil {
		t.Fatalf("call: %v", err)
	}
	ev := lastToolCallEvent(t, traces)
	if ev.TraceID != metaTraceID || ev.ParentID != "00f067aa0ba902b7" {
		t.Errorf("tool call recorded under trace %q/%q, want the _meta traceparent", ev.TraceID, ev.ParentID)
	}
}

// lastToolCallEvent returns the last tool_call event in a trace log.
func lastToolCallEvent(t *testing.T, path string) TraceEvent {
	t.Helper()
	f, err := os.Open(path) // #nosec G304 -- test temp file
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var last TraceEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev TraceEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err == nil && ev.Type == EventToolCall {
			last = ev
		}
	}
	if last.Type == "" {
		t.Fatal("no tool_call event recorded")
	}
	return last
}

func TestMetaTraceParentIgnoresWhatItCannotUse(t *testing.T) {
	cases := map[string]mcp.Request{
		"params absent":  &mcp.ServerRequest[*mcp.ListToolsParams]{},
		"no _meta":       &mcp.ServerRequest[*mcp.ListToolsParams]{Params: &mcp.ListToolsParams{}},
		"not a string":   withMeta(map[string]any{"traceparent": 42}),
		"malformed":      withMeta(map[string]any{"traceparent": "00-zz-00f067aa0ba902b7-01"}),
		"all-zero trace": withMeta(map[string]any{"traceparent": "00-00000000000000000000000000000000-00f067aa0ba902b7-01"}),
	}
	for name, req := range cases {
		if tc, ok := metaTraceParent(req); ok {
			t.Errorf("%s: adopted %+v", name, tc)
		}
	}
	tc, ok := metaTraceParent(withMeta(map[string]any{"traceparent": "00-" + metaTraceID + "-00f067aa0ba902b7-01"}))
	if !ok || tc.TraceID != metaTraceID {
		t.Errorf("valid traceparent not adopted: %+v %v", tc, ok)
	}
}

func withMeta(meta map[string]any) mcp.Request {
	p := &mcp.ListToolsParams{}
	p.SetMeta(meta)
	return &mcp.ServerRequest[*mcp.ListToolsParams]{Params: p}
}
