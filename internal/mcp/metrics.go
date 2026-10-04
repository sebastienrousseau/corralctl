// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
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

// incRequest increments the request counter for the specified HTTP method, endpoint, and status code.
func (m *Metrics) incRequest(method, endpoint string, status int) {
	key := fmt.Sprintf(`endpoint="%s",method="%s",status="%d"`, endpoint, method, status)
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
func (m *Metrics) incToolCall(tool, status string) {
	key := fmt.Sprintf(`status="%s",tool="%s"`, status, tool)
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

			if method == "tools/call" {
				if call, ok := req.(*mcp.CallToolRequest); ok {
					status := "ok"
					errMsg := ""
					if err != nil {
						status = "error"
						errMsg = err.Error()
					} else if callRes, ok := res.(*mcp.CallToolResult); ok && callRes != nil && callRes.IsError {
						status = "error"
						errMsg = "tool returned error result"
					}
					if s.metrics != nil {
						s.metrics.incToolCall(call.Params.Name, status)
					}
					if s.tracer != nil {
						_ = s.tracer.Record(ctx, TraceEvent{
							Type:       EventToolCall,
							Name:       call.Params.Name,
							DurationMs: dur,
							Success:    status == "ok",
							Error:      errMsg,
						})
					}
				}
			} else if s.tracer != nil {
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
			return res, err
		}
	}
}
