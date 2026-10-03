// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package diag carries corral's diagnostic output: the messages that explain
// what the tool is doing or why something was skipped, as distinct from the
// results it produces.
//
// The distinction matters because stdout is reserved for the selected output
// format — text, json or ndjson — and must stay parseable. Diagnostics
// therefore go to stderr, and this package is where their verbosity is
// decided.
//
// Three things motivated levelling them rather than printing everything:
//
//   - The MCP server runs long-lived over stdio with no terminal attached.
//     When it misbehaves the only evidence is stderr, and "turn on more
//     logging" was not something a user could do.
//   - A `corralctl` run over a large account emits a WARN per unmigratable
//     directory. Useful when you are debugging a layout; noise when you are
//     not.
//   - A bug report is far more useful with debug output attached, and asking
//     for it needs to be a flag, not a rebuild.
//
// The default is Info, which reproduces the output corral had before this
// package existed. Nothing is hidden by default that used to be shown.
package diag

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
)

// Format represents the encoding of diagnostic log records.
type Format int

const (
	// FormatText emits human-readable level-prefixed lines ("LEVEL: message").
	FormatText Format = iota
	// FormatJSON emits structured JSON records using log/slog.
	FormatJSON
)

// String returns the name of the format ("text" or "json").
func (f Format) String() string {
	switch f {
	case FormatJSON:
		return "json"
	case FormatText:
		return "text"
	default:
		return fmt.Sprintf("Format(%d)", int(f))
	}
}

// ParseFormat maps a format name to a Format. Names are case-insensitive.
func ParseFormat(name string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "json":
		return FormatJSON, nil
	case "text", "":
		return FormatText, nil
	default:
		return FormatText, fmt.Errorf("unknown log format %q (want text or json)", name)
	}
}

// Level is the verbosity threshold: a message is emitted when its own level
// is at or below the configured one.
type Level int

const (
	// LevelError reports only failures that stopped something from working.
	LevelError Level = iota
	// LevelWarn adds conditions corral worked around or declined to act on.
	LevelWarn
	// LevelInfo adds ordinary progress narration. This is the default.
	LevelInfo
	// LevelDebug adds detail that is only useful when diagnosing corral
	// itself, such as why a state sidecar was ignored.
	LevelDebug
)

// String returns the lowercase name of the level, as accepted by ParseLevel.
func (l Level) String() string {
	switch l {
	case LevelError:
		return "error"
	case LevelWarn:
		return "warn"
	case LevelInfo:
		return "info"
	case LevelDebug:
		return "debug"
	default:
		return fmt.Sprintf("Level(%d)", int(l))
	}
}

// ParseLevel maps a level name to a Level. Names are case-insensitive, and
// "warning" is accepted alongside "warn" because both are habitual.
func ParseLevel(name string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "error":
		return LevelError, nil
	case "warn", "warning":
		return LevelWarn, nil
	case "info", "":
		return LevelInfo, nil
	case "debug":
		return LevelDebug, nil
	default:
		return LevelInfo, fmt.Errorf("unknown log level %q (want error, warn, info or debug)", name)
	}
}

var (
	mu      sync.RWMutex
	level             = LevelInfo
	format            = FormatText
	output  io.Writer = os.Stderr
	timeNow           = time.Now
)

// SetLevel sets the verbosity threshold.
func SetLevel(l Level) {
	mu.Lock()
	defer mu.Unlock()
	level = l
}

// CurrentLevel reports the verbosity threshold in force.
func CurrentLevel() Level {
	mu.RLock()
	defer mu.RUnlock()
	return level
}

// SetFormat sets the diagnostic encoding format.
func SetFormat(f Format) {
	mu.Lock()
	defer mu.Unlock()
	format = f
}

// CurrentFormat reports the diagnostic encoding format in force.
func CurrentFormat() Format {
	mu.RLock()
	defer mu.RUnlock()
	return format
}

// SetOutput redirects diagnostics. Intended for tests; production writes to
// stderr, which is the only channel that cannot corrupt the result stream.
func SetOutput(w io.Writer) {
	mu.Lock()
	defer mu.Unlock()
	if w == nil {
		w = os.Stderr
	}
	output = w
}

// Enabled reports whether a message at l would be emitted. Callers use it to
// skip work that only exists to build a message.
func Enabled(l Level) bool {
	return l <= CurrentLevel()
}

// TraceInfo holds distributed tracing metadata propagated via context.
type TraceInfo struct {
	TraceID string `json:"trace_id,omitempty"`
	SpanID  string `json:"span_id,omitempty"`
}

type traceContextKey struct{}

// WithTrace returns a new Context with the provided trace ID and span ID attached.
func WithTrace(ctx context.Context, traceID, spanID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, traceContextKey{}, TraceInfo{
		TraceID: traceID,
		SpanID:  spanID,
	})
}

// FromContext extracts the TraceInfo from the context, if present.
func FromContext(ctx context.Context) (TraceInfo, bool) {
	if ctx == nil {
		return TraceInfo{}, false
	}
	ti, ok := ctx.Value(traceContextKey{}).(TraceInfo)
	return ti, ok
}

// emit writes one diagnostic line if the level permits it.
func emit(l Level, formatStr string, args ...any) {
	emitContext(context.Background(), l, formatStr, args...)
}

// emitContext writes one diagnostic line with context-derived tracing metadata if permitted.
func emitContext(ctx context.Context, l Level, formatStr string, args ...any) {
	mu.RLock()
	threshold, fmtMode, w := level, format, output
	mu.RUnlock()
	if l > threshold {
		return
	}
	msg := fmt.Sprintf(formatStr, args...)
	msg = strings.TrimRight(msg, "\n")

	ti, hasTrace := FromContext(ctx)

	if fmtMode == FormatJSON {
		var slogLvl slog.Level
		switch l {
		case LevelError:
			slogLvl = slog.LevelError
		case LevelWarn:
			slogLvl = slog.LevelWarn
		case LevelInfo:
			slogLvl = slog.LevelInfo
		case LevelDebug:
			slogLvl = slog.LevelDebug
		}
		h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: slogLvl})
		rec := slog.NewRecord(timeNow(), slogLvl, msg, 0)
		if hasTrace {
			if ti.TraceID != "" {
				rec.AddAttrs(slog.String("trace_id", ti.TraceID))
			}
			if ti.SpanID != "" {
				rec.AddAttrs(slog.String("span_id", ti.SpanID))
			}
		}
		if ctx == nil {
			ctx = context.Background()
		}
		_ = h.Handle(ctx, rec)
		return
	}

	// One line per diagnostic, prefixed with its level and optional trace ID,
	// so stderr stays greppable when it is the only evidence available.
	if hasTrace && ti.TraceID != "" {
		_, _ = fmt.Fprintf(w, "%s: [%s] %s\n", strings.ToUpper(l.String()), ti.TraceID, msg)
		return
	}
	_, _ = fmt.Fprintf(w, "%s: %s\n", strings.ToUpper(l.String()), msg)
}

// Errorf reports a failure that stopped something from working.
func Errorf(format string, args ...any) { emit(LevelError, format, args...) }

// Warnf reports a condition corral worked around or declined to act on.
func Warnf(format string, args ...any) { emit(LevelWarn, format, args...) }

// Infof reports ordinary progress.
func Infof(format string, args ...any) { emit(LevelInfo, format, args...) }

// Debugf reports detail useful only when diagnosing corral itself.
func Debugf(format string, args ...any) { emit(LevelDebug, format, args...) }

// ErrorContextf reports a failure that stopped something from working, with trace metadata from ctx.
func ErrorContextf(ctx context.Context, format string, args ...any) {
	emitContext(ctx, LevelError, format, args...)
}

// WarnContextf reports a condition corral worked around, with trace metadata from ctx.
func WarnContextf(ctx context.Context, format string, args ...any) {
	emitContext(ctx, LevelWarn, format, args...)
}

// InfoContextf reports ordinary progress, with trace metadata from ctx.
func InfoContextf(ctx context.Context, format string, args ...any) {
	emitContext(ctx, LevelInfo, format, args...)
}

// DebugContextf reports detail useful when diagnosing corral itself, with trace metadata from ctx.
func DebugContextf(ctx context.Context, format string, args ...any) {
	emitContext(ctx, LevelDebug, format, args...)
}
