// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMetricsPrometheusOutput(t *testing.T) {
	m := newMetrics()
	m.incRequest("GET", "/metrics", 200)
	m.incRequest("POST", "/mcp", 200)
	m.incToolCall("corral_search", "ok")
	m.incToolCall("corral_search", "error")
	m.incCacheInvalidation()

	var buf bytes.Buffer
	m.WritePrometheus(&buf, nil)
	out := buf.String()

	if !strings.Contains(out, `mcp_requests_total{endpoint="/metrics",method="GET",status="200"} 1`) {
		t.Errorf("missing requests total in output:\n%s", out)
	}
	if !strings.Contains(out, `mcp_tool_calls_total{status="ok",tool="corral_search"} 1`) {
		t.Errorf("missing tool calls total in output:\n%s", out)
	}
	if !strings.Contains(out, `mcp_tool_calls_total{status="error",tool="corral_search"} 1`) {
		t.Errorf("missing tool call error in output:\n%s", out)
	}
	if !strings.Contains(out, `mcp_cache_invalidations_total 1`) {
		t.Errorf("missing cache invalidations in output:\n%s", out)
	}
}

func TestMetricsEndpointHTTP(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "repo1", "", "")

	srv, err := NewServer(ServerOptions{Root: base})
	if err != nil {
		t.Fatal(err)
	}

	// Warm scan cache to populate repo count
	if _, err := srv.scan(); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.httpHandler())
	defer ts.Close()

	res, err := http.Get(ts.URL + MetricsEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
	ct := res.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("expected text/plain, got %q", ct)
	}

	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)

	if !strings.Contains(body, "mcp_workspace_repos_total 1") {
		t.Errorf("expected mcp_workspace_repos_total 1 in body:\n%s", body)
	}
	if !strings.Contains(body, "mcp_active_sessions 0") {
		t.Errorf("expected mcp_active_sessions in body:\n%s", body)
	}

	// Test with active session via test harness
	h := newHarness(t, ServerOptions{Root: base})
	var hBuf bytes.Buffer
	h.server.metrics.WritePrometheus(&hBuf, h.server)
	if !strings.Contains(hBuf.String(), "mcp_active_sessions 1") {
		t.Errorf("expected active session count 1, got:\n%s", hBuf.String())
	}
}

func TestMetricsSSEEndpointIncludesMetrics(t *testing.T) {
	base := t.TempDir()
	srv, err := NewServer(ServerOptions{Root: base})
	if err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(srv.sseHandler())
	defer ts.Close()

	res, err := http.Get(ts.URL + MetricsEndpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
}

func TestToolInstrumentationMiddleware(t *testing.T) {
	tmpDir := t.TempDir()
	srv, err := NewServer(ServerOptions{
		Root:         tmpDir,
		TraceLogPath: filepath.Join(tmpDir, "traces.jsonl"),
	})
	if err != nil {
		t.Fatal(err)
	}

	mw := srv.toolInstrumentationMiddleware()

	// 1. Success case
	nextOK := mw(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	_, _ = nextOK(context.Background(), "tools/call", &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "my_tool"},
	})

	// 2. IsError tool result case
	nextIsError := mw(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		return &mcp.CallToolResult{IsError: true}, nil
	})
	_, _ = nextIsError(context.Background(), "tools/call", &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "my_tool"},
	})

	// 3. Error return case
	nextErr := mw(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		return nil, errors.New("boom")
	})
	_, _ = nextErr(context.Background(), "tools/call", &mcp.CallToolRequest{
		Params: &mcp.CallToolParamsRaw{Name: "my_tool"},
	})

	// 4. Non-tools/call method success
	nextOther := mw(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		return nil, nil
	})
	_, _ = nextOther(context.Background(), "tools/list", &mcp.ListToolsRequest{})

	// 5. Non-tools/call method error
	nextOtherErr := mw(func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		return nil, errors.New("non-tool error")
	})
	_, _ = nextOtherErr(context.Background(), "tools/list", &mcp.ListToolsRequest{})

	var buf bytes.Buffer
	srv.metrics.WritePrometheus(&buf, srv)
	out := buf.String()

	if !strings.Contains(out, `mcp_tool_calls_total{status="ok",tool="my_tool"} 1`) {
		t.Errorf("missing ok status in:\n%s", out)
	}
	if !strings.Contains(out, `mcp_tool_calls_total{status="error",tool="my_tool"} 2`) {
		t.Errorf("missing error status in:\n%s", out)
	}
}

func TestStatusRecorderFlush(t *testing.T) {
	rec := &statusRecorder{ResponseWriter: httptest.NewRecorder(), status: http.StatusOK}
	rec.Flush()
}
