// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/diag"
)

// TraceContext holds the parsed components of a W3C traceparent header
// (specification: W3C Trace Context Level 1).
type TraceContext struct {
	Version    string `json:"version"`
	TraceID    string `json:"trace_id"`
	ParentID   string `json:"parent_id"`
	TraceFlags string `json:"trace_flags"`
}

// String returns the canonical W3C traceparent representation:
// 00-<trace_id>-<parent_id>-<trace_flags>.
func (tc TraceContext) String() string {
	if tc.TraceID == "" || tc.ParentID == "" {
		return ""
	}
	ver := tc.Version
	if ver == "" {
		ver = "00"
	}
	flags := tc.TraceFlags
	if flags == "" {
		flags = "01"
	}
	return ver + "-" + tc.TraceID + "-" + tc.ParentID + "-" + flags
}

// isZeroHex reports whether s consists entirely of '0' ASCII characters.
func isZeroHex(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] != '0' {
			return false
		}
	}
	return true
}

// isHexChar reports whether c is a valid lowercase or uppercase hex character.
func isHexChar(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// isValidHex reports whether s has length wantLen and contains only valid hex characters.
func isValidHex(s string, wantLen int) bool {
	if len(s) != wantLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isHexChar(s[i]) {
			return false
		}
	}
	return true
}

// ParseTraceParent parses and validates a W3C traceparent header string.
// A valid traceparent has the format "00-<trace_id>-<parent_id>-<trace_flags>",
// where trace_id is 32 non-zero hex digits and parent_id is 16 non-zero hex digits.
func ParseTraceParent(raw string) (TraceContext, bool) {
	trimmed := strings.TrimSpace(raw)
	parts := strings.Split(trimmed, "-")
	if len(parts) < 4 {
		return TraceContext{}, false
	}
	ver := strings.ToLower(parts[0])
	traceID := strings.ToLower(parts[1])
	parentID := strings.ToLower(parts[2])
	flags := strings.ToLower(parts[3])

	// Version "ff" is explicitly forbidden by W3C specification.
	if ver == "ff" || !isValidHex(ver, 2) {
		return TraceContext{}, false
	}

	// For version "00", exactly 4 parts are allowed and total length is 55.
	if ver == "00" && len(parts) != 4 {
		return TraceContext{}, false
	}

	if !isValidHex(traceID, 32) || isZeroHex(traceID) {
		return TraceContext{}, false
	}
	if !isValidHex(parentID, 16) || isZeroHex(parentID) {
		return TraceContext{}, false
	}
	if !isValidHex(flags, 2) {
		return TraceContext{}, false
	}

	return TraceContext{
		Version:    ver,
		TraceID:    traceID,
		ParentID:   parentID,
		TraceFlags: flags,
	}, true
}

// NewTraceContext generates a fresh W3C TraceContext with cryptographically random
// 16-byte trace ID and 8-byte parent ID with recording flag enabled (01).
func NewTraceContext() TraceContext {
	var traceID [16]byte
	var parentID [8]byte
	_, _ = rand.Read(traceID[:])
	_, _ = rand.Read(parentID[:])

	// Ensure non-zero IDs by setting at least one bit.
	traceID[0] |= 0x01
	parentID[0] |= 0x01

	return TraceContext{
		Version:    "00",
		TraceID:    hex.EncodeToString(traceID[:]),
		ParentID:   hex.EncodeToString(parentID[:]),
		TraceFlags: "01",
	}
}

type traceContextKey struct{}

// ContextWithTrace attaches a TraceContext to the given context and propagates
// it to diag.WithTrace for structured logging.
func ContextWithTrace(ctx context.Context, tc TraceContext) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = diag.WithTrace(ctx, tc.TraceID, tc.ParentID)
	return context.WithValue(ctx, traceContextKey{}, tc)
}

// TraceFromContext returns the TraceContext associated with ctx, if any.
func TraceFromContext(ctx context.Context) (TraceContext, bool) {
	if ctx == nil {
		return TraceContext{}, false
	}
	tc, ok := ctx.Value(traceContextKey{}).(TraceContext)
	return tc, ok
}

// EventType classifies the category of an agentic interaction trace event.
type EventType string

const (
	// EventToolCall records a tool invocation by an agent.
	EventToolCall EventType = "tool_call"
	// EventResourceRead records a resource read operation.
	EventResourceRead EventType = "resource_read"
	// EventRequest records an incoming JSON-RPC protocol request.
	EventRequest EventType = "request"
)

// TraceEvent captures structured metadata for an agentic interaction.
type TraceEvent struct {
	Timestamp  string         `json:"ts"`
	TraceID    string         `json:"trace_id,omitempty"`
	ParentID   string         `json:"parent_id,omitempty"`
	Type       EventType      `json:"type"`
	Name       string         `json:"name"`
	DurationMs float64        `json:"duration_ms"`
	Success    bool           `json:"success"`
	Error      string         `json:"error,omitempty"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// EventTracer writes structured JSONL events to an append-only trace log.
type EventTracer struct {
	path string
	mu   sync.Mutex
}

var (
	openTraceFile     = func(name string, flag int, perm os.FileMode) (auditFile, error) {
		// #nosec G304 -- destination configured by operator flag or option.
		return os.OpenFile(name, flag, perm)
	}
	mkdirAllTraceDir = os.MkdirAll
)

// NewEventTracer constructs an EventTracer writing to path. Returns nil if path is empty.
func NewEventTracer(path string) *EventTracer {
	if path == "" {
		return nil
	}
	return &EventTracer{path: path}
}

// Path returns the destination file path.
func (t *EventTracer) Path() string {
	if t == nil {
		return ""
	}
	return t.path
}

// Record appends a TraceEvent to the trace file in JSONL format.
func (t *EventTracer) Record(ctx context.Context, ev TraceEvent) error {
	if t == nil || t.path == "" {
		return nil
	}
	if ev.Timestamp == "" {
		ev.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if tc, ok := TraceFromContext(ctx); ok {
		if ev.TraceID == "" {
			ev.TraceID = tc.TraceID
		}
		if ev.ParentID == "" {
			ev.ParentID = tc.ParentID
		}
	}

	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	t.mu.Lock()
	defer t.mu.Unlock()

	dir := filepath.Dir(t.path)
	if err := mkdirAllTraceDir(dir, 0o700); err != nil {
		return err
	}

	f, err := openTraceFile(t.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	_, err = f.Write(data)
	return err
}

