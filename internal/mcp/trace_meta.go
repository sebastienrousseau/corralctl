// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"reflect"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// metaTraceMiddleware adopts a W3C traceparent carried in a request's
// _meta, where protocol 2026-07-28 places trace context (SEP-414). On
// stdio it is the only source; over HTTP a valid _meta value takes
// precedence over the traceparent header, because it names the MCP
// request rather than the HTTP hop that carried it. A missing or invalid
// value leaves the context as it was.
func metaTraceMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if tc, ok := metaTraceParent(req); ok {
				ctx = ContextWithTrace(ctx, tc)
			}
			return next(ctx, method, req)
		}
	}
}

// metaTraceParent returns the valid traceparent in req's _meta, if any.
// A request sent without params reaches middleware as a typed nil pointer,
// whose GetMeta would panic, so that case is checked first.
func metaTraceParent(req mcp.Request) (TraceContext, bool) {
	p := req.GetParams()
	if v := reflect.ValueOf(p); !v.IsValid() || (v.Kind() == reflect.Pointer && v.IsNil()) {
		return TraceContext{}, false
	}
	raw, _ := p.GetMeta()["traceparent"].(string)
	return ParseTraceParent(raw)
}
