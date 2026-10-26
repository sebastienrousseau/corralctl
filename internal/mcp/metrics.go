// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MetricsEndpoint is the HTTP path serving Prometheus telemetry.
const MetricsEndpoint = "/metrics"

// Metrics maintains thread-safe Prometheus counters and gauges for the MCP server.
type Metrics struct {
	mu sync.RWMutex

	requestsTotal    map[string]*atomic.Uint64 // key: method|endpoint|status
	toolCallsTotal   map[string]*atomic.Uint64 // key: tool|status
	cacheInvalidates atomic.Uint64
}

// newMetrics initializes an empty Metrics instance.
func newMetrics() *Metrics {
	return &Metrics{
		requestsTotal:  make(map[string]*atomic.Uint64),
		toolCallsTotal: make(map[string]*atomic.Uint64),
	}
}

// Every label value below is chosen from a fixed set, never copied from the
// request. The counters are bumped before authentication, so a value taken
// from the client would let anyone who can reach the socket add a series per
// request (memory that is never freed) and, through a decoded `"` or newline
// in the path, write arbitrary lines into the exposition.

// otherLabel is the bucket for a path or method outside the fixed sets.
const otherLabel = "other"

// unknownToolLabel is the bucket for a tool name this server never registered.
const unknownToolLabel = "unknown"

// routeLabel maps a request path to one of the routes this server serves.
func routeLabel(path string) string {
	switch path {
	case "/", StreamableEndpoint, SSEEndpoint, MetricsEndpoint:
		return path
	}
	return otherLabel
}

// methodLabel maps an HTTP method to the ones the transports answer.
func methodLabel(method string) string {
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodDelete, http.MethodOptions, http.MethodHead:
		return method
	}
	return strings.ToUpper(otherLabel)
}

// labelEscaper applies the exposition format's escaping for label values.
var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// escapeLabel escapes a label value. The values here come from fixed sets,
// so this is the second line, not the first: a future label fed from
// somewhere less careful still cannot close the quote.
func escapeLabel(v string) string {
	return labelEscaper.Replace(v)
}

// toolLabel returns name when this server registered a tool by that name,
// and unknownToolLabel otherwise.
func (s *Server) toolLabel(name string) string {
	if slices.Contains(s.toolNames, name) {
		return name
	}
	return unknownToolLabel
}

// incRequest increments the request counter for the specified HTTP method, endpoint, and status code.
// Method and endpoint are folded into fixed label sets; see routeLabel.
func (m *Metrics) incRequest(method, endpoint string, status int) {
	key := fmt.Sprintf(`endpoint="%s",method="%s",status="%d"`,
		escapeLabel(routeLabel(endpoint)), escapeLabel(methodLabel(method)), status)
	m.mu.RLock()
	c, ok := m.requestsTotal[key]
	m.mu.RUnlock()
	if !ok {
		m.mu.Lock()
		c, ok = m.requestsTotal[key]
		if !ok {
			c = new(atomic.Uint64)
			m.requestsTotal[key] = c
		}
		m.mu.Unlock()
	}
	c.Add(1)
}

// incToolCall increments the invocation counter for a specific MCP tool name and execution status.
// The caller passes a name from toolLabel, never the client's raw one.
func (m *Metrics) incToolCall(tool, status string) {
	key := fmt.Sprintf(`status="%s",tool="%s"`, escapeLabel(status), escapeLabel(tool))
	m.mu.RLock()
	c, ok := m.toolCallsTotal[key]
	m.mu.RUnlock()
	if !ok {
		m.mu.Lock()
		c, ok = m.toolCallsTotal[key]
		if !ok {
			c = new(atomic.Uint64)
			m.toolCallsTotal[key] = c
		}
		m.mu.Unlock()
	}
	c.Add(1)
}

// incCacheInvalidation records an invalidation of the in-memory workspace index cache.
func (m *Metrics) incCacheInvalidation() {
	m.cacheInvalidates.Add(1)
}

// WritePrometheus serializes current telemetry into Prometheus text exposition format (v0.0.4).
func (m *Metrics) WritePrometheus(w io.Writer, s *Server) {
	_, _ = fmt.Fprintln(w, "# HELP mcp_requests_total Total number of HTTP requests served.")
	_, _ = fmt.Fprintln(w, "# TYPE mcp_requests_total counter")
	m.mu.RLock()
	reqKeys := make([]string, 0, len(m.requestsTotal))
	for k := range m.requestsTotal {
		reqKeys = append(reqKeys, k)
	}
	sort.Strings(reqKeys)
	for _, k := range reqKeys {
		_, _ = fmt.Fprintf(w, "mcp_requests_total{%s} %d\n", k, m.requestsTotal[k].Load())
	}

	_, _ = fmt.Fprintln(w, "# HELP mcp_tool_calls_total Total number of MCP tool calls executed.")
	_, _ = fmt.Fprintln(w, "# TYPE mcp_tool_calls_total counter")
	toolKeys := make([]string, 0, len(m.toolCallsTotal))
	for k := range m.toolCallsTotal {
		toolKeys = append(toolKeys, k)
	}
	sort.Strings(toolKeys)
	for _, k := range toolKeys {
		_, _ = fmt.Fprintf(w, "mcp_tool_calls_total{%s} %d\n", k, m.toolCallsTotal[k].Load())
	}
	m.mu.RUnlock()

	_, _ = fmt.Fprintln(w, "# HELP mcp_cache_invalidations_total Total workspace scan cache invalidations.")
	_, _ = fmt.Fprintln(w, "# TYPE mcp_cache_invalidations_total counter")
	_, _ = fmt.Fprintf(w, "mcp_cache_invalidations_total %d\n", m.cacheInvalidates.Load())

	if s != nil {
		activeSessions := 0
		if s.mcp != nil {
			for range s.mcp.Sessions() {
				activeSessions++
			}
		}
		_, _ = fmt.Fprintln(w, "# HELP mcp_active_sessions Number of active MCP sessions.")
		_, _ = fmt.Fprintln(w, "# TYPE mcp_active_sessions gauge")
		_, _ = fmt.Fprintf(w, "mcp_active_sessions %d\n", activeSessions)

		s.scanMu.Lock()
		repoCount := 0
		if s.scanIndex != nil {
			repoCount = len(s.scanIndex.Repos)
		}
		s.scanMu.Unlock()
		_, _ = fmt.Fprintln(w, "# HELP mcp_workspace_repos_total Number of repositories in workspace index.")
		_, _ = fmt.Fprintln(w, "# TYPE mcp_workspace_repos_total gauge")
		_, _ = fmt.Fprintf(w, "mcp_workspace_repos_total %d\n", repoCount)
	}
}

// metricsHandler serves Prometheus metrics over HTTP.
func (s *Server) metricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		s.metrics.WritePrometheus(w, s)
	})
}

// toolInstrumentationMiddleware records invocation counts, outcomes, and event traces.
func (s *Server) toolInstrumentationMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			start := time.Now()
			res, err := next(ctx, method, req)
			dur := float64(time.Since(start).Microseconds()) / 1000.0

			if method != "tools/call" {
				s.recordRequest(ctx, method, dur, err)
			} else if call, ok := req.(*mcp.CallToolRequest); ok {
				s.recordToolCall(ctx, call.Params.Name, dur, res, err)
			}
			return res, err
		}
	}
}

// toolOutcome classifies a tool call as "ok" or "error", with the reason
// for an error.
func toolOutcome(res mcp.Result, err error) (status, errMsg string) {
	if err != nil {
		return "error", err.Error()
	}
	if callRes, ok := res.(*mcp.CallToolResult); ok && callRes != nil && callRes.IsError {
		return "error", "tool returned error result"
	}
	return "ok", ""
}

// recordToolCall counts one tool call and traces it. The counter is keyed
// on toolLabel, so a name the client invented shares the unknown series.
func (s *Server) recordToolCall(ctx context.Context, name string, dur float64, res mcp.Result, err error) {
	status, errMsg := toolOutcome(res, err)
	if s.metrics != nil {
		s.metrics.incToolCall(s.toolLabel(name), status)
	}
	if s.tracer != nil {
		_ = s.tracer.Record(ctx, TraceEvent{
			Type:       EventToolCall,
			Name:       name,
			DurationMs: dur,
			Success:    status == "ok",
			Error:      errMsg,
		})
	}
}

// recordRequest traces a JSON-RPC request that is not a tool call.
func (s *Server) recordRequest(ctx context.Context, method string, dur float64, err error) {
	if s.tracer == nil {
		return
	}
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	_ = s.tracer.Record(ctx, TraceEvent{
		Type:       EventRequest,
		Name:       method,
		DurationMs: dur,
		Success:    err == nil,
		Error:      errMsg,
	})
}
