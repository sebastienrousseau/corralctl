// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/diag"
)

func TestParseTraceParent(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantOk  bool
		wantVer string
		wantTID string
		wantPID string
		wantFlg string
	}{
		{
			name:    "valid traceparent",
			raw:     "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			wantOk:  true,
			wantVer: "00",
			wantTID: "4bf92f3577b34da6a3ce929d0e0e4736",
			wantPID: "00f067aa0ba902b7",
			wantFlg: "01",
		},
		{
			name:    "valid uppercase hex converted to lowercase",
			raw:     "00-4BF92F3577B34DA6A3CE929D0E0E4736-00F067AA0BA902B7-00",
			wantOk:  true,
			wantVer: "00",
			wantTID: "4bf92f3577b34da6a3ce929d0e0e4736",
			wantPID: "00f067aa0ba902b7",
			wantFlg: "00",
		},
		{
			name:   "too few parts",
			raw:    "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7",
			wantOk: false,
		},
		{
			name:   "invalid version ff",
			raw:    "ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			wantOk: false,
		},
		{
			name:   "invalid version non-hex",
			raw:    "zz-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
			wantOk: false,
		},
		{
			name:   "all zero trace_id is invalid",
			raw:    "00-00000000000000000000000000000000-00f067aa0ba902b7-01",
			wantOk: false,
		},
		{
			name:   "all zero parent_id is invalid",
			raw:    "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
			wantOk: false,
		},
		{
			name:   "trace_id wrong length",
			raw:    "00-4bf92f3577b34da6a3ce929d0e0e473-00f067aa0ba902b7-01",
			wantOk: false,
		},
		{
			name:   "parent_id wrong length",
			raw:    "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902-01",
			wantOk: false,
		},
		{
			name:   "flags wrong length",
			raw:    "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-1",
			wantOk: false,
		},
		{
			name:   "trace_id contains invalid character",
			raw:    "00-4bf92f3577b34da6a3ce929d0e0e473g-00f067aa0ba902b7-01",
			wantOk: false,
		},
		{
			name:   "parent_id contains invalid character",
			raw:    "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902bz-01",
			wantOk: false,
		},
		{
			name:   "flags contains invalid character",
			raw:    "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-0g",
			wantOk: false,
		},
		{
			name:   "version 00 with extra parts is invalid",
			raw:    "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra",
			wantOk: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseTraceParent(tc.raw)
			if ok != tc.wantOk {
				t.Fatalf("ParseTraceParent(%q) ok = %v, want %v", tc.raw, ok, tc.wantOk)
			}
			if ok {
				if got.Version != tc.wantVer || got.TraceID != tc.wantTID || got.ParentID != tc.wantPID || got.TraceFlags != tc.wantFlg {
					t.Fatalf("ParseTraceParent(%q) = %+v, want %+v", tc.raw, got, tc)
				}
				if s := got.String(); s != strings.ToLower(tc.raw) {
					t.Fatalf("String() = %q, want %q", s, strings.ToLower(tc.raw))
				}
			}
		})
	}
}

func TestTraceContextStringAndGeneration(t *testing.T) {
	// Empty traceID or parentID returns empty string
	empty := TraceContext{}
	if s := empty.String(); s != "" {
		t.Fatalf("expected empty string for empty TraceContext, got %q", s)
	}

	partial := TraceContext{TraceID: "abc"}
	if s := partial.String(); s != "" {
		t.Fatalf("expected empty string for partial TraceContext, got %q", s)
	}

	withDefaults := TraceContext{
		TraceID:  "4bf92f3577b34da6a3ce929d0e0e4736",
		ParentID: "00f067aa0ba902b7",
	}
	expectedDefaults := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	if s := withDefaults.String(); s != expectedDefaults {
		t.Fatalf("expected default string %q, got %q", expectedDefaults, s)
	}

	tc := NewTraceContext()
	if tc.Version != "00" {
		t.Errorf("expected version 00, got %s", tc.Version)
	}
	if len(tc.TraceID) != 32 {
		t.Errorf("expected 32-char trace ID, got len %d", len(tc.TraceID))
	}
	if len(tc.ParentID) != 16 {
		t.Errorf("expected 16-char parent ID, got len %d", len(tc.ParentID))
	}
	if tc.TraceFlags != "01" {
		t.Errorf("expected trace flags 01, got %s", tc.TraceFlags)
	}
	str := tc.String()
	if len(str) != 55 {
		t.Errorf("expected 55-char traceparent string, got %d (%q)", len(str), str)
	}

	// Parsing the generated string must succeed and match
	parsed, ok := ParseTraceParent(str)
	if !ok {
		t.Fatalf("failed to parse generated traceparent %q", str)
	}
	if parsed.TraceID != tc.TraceID || parsed.ParentID != tc.ParentID {
		t.Fatalf("parsed trace context does not match generated: %+v vs %+v", parsed, tc)
	}
}

func TestTraceContextStorageAndRetrieval(t *testing.T) {
	ctx := context.Background()
	if _, ok := TraceFromContext(ctx); ok {
		t.Error("expected false for context without trace")
	}
	var nilCtx context.Context
	if _, ok := TraceFromContext(nilCtx); ok { //nolint:staticcheck // SA1012: defensive check for nil context
		t.Error("expected false for nil context")
	}

	tc := TraceContext{
		Version:    "00",
		TraceID:    "4bf92f3577b34da6a3ce929d0e0e4736",
		ParentID:   "00f067aa0ba902b7",
		TraceFlags: "01",
	}
	ctxWithTrace := ContextWithTrace(ctx, tc)
	retrieved, ok := TraceFromContext(ctxWithTrace)
	if !ok {
		t.Fatal("expected trace context to be retrieved")
	}
	if retrieved != tc {
		t.Fatalf("retrieved %+v, want %+v", retrieved, tc)
	}

	ti, ok := diag.FromContext(ctxWithTrace)
	if !ok || ti.TraceID != tc.TraceID || ti.SpanID != tc.ParentID {
		t.Fatalf("diag.FromContext = %+v, %v; want traceID=%s, spanID=%s", ti, ok, tc.TraceID, tc.ParentID)
	}

	// ContextWithTrace with nil ctx
	nilWithTrace := ContextWithTrace(nilCtx, tc)
	if r, ok := TraceFromContext(nilWithTrace); !ok || r != tc {
		t.Errorf("ContextWithTrace(nil, tc) = %+v, %v; want %+v, true", r, ok, tc)
	}
}

func TestTraceparentEchoedInHTTPResponse(t *testing.T) {
	srv, err := NewServer(ServerOptions{Root: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	h := srv.httpHandler()

	// 1. When incoming request includes valid traceparent, it must be echoed back and in context.
	const incomingTP = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("traceparent", incomingTP)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if got := rec.Header().Get("traceparent"); got != incomingTP {
		t.Errorf("expected echoed traceparent %q, got %q", incomingTP, got)
	}

	// 2. When incoming request does not include traceparent, a new valid one is generated.
	req2 := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)

	newTP := rec2.Header().Get("traceparent")
	if newTP == "" {
		t.Fatal("expected traceparent header in response, got none")
	}
	if _, ok := ParseTraceParent(newTP); !ok {
		t.Fatalf("response traceparent %q is not a valid W3C traceparent", newTP)
	}
}
