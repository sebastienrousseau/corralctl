// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package diag

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// capture installs a buffer as the diagnostic sink for one test and restores
// the previous level, format and sink afterwards, so tests cannot leak verbosity
// or formatting into each other.
func capture(t *testing.T, l Level) *bytes.Buffer {
	t.Helper()
	prevLevel := CurrentLevel()
	prevFormat := CurrentFormat()
	var buf bytes.Buffer
	SetOutput(&buf)
	SetLevel(l)
	SetFormat(FormatText)
	t.Cleanup(func() {
		SetLevel(prevLevel)
		SetFormat(prevFormat)
		SetOutput(nil)
	})
	return &buf
}

func TestLevelNames(t *testing.T) {
	cases := map[Level]string{
		LevelError: "error",
		LevelWarn:  "warn",
		LevelInfo:  "info",
		LevelDebug: "debug",
	}
	for level, want := range cases {
		if got := level.String(); got != want {
			t.Errorf("Level(%d).String() = %q, want %q", int(level), got, want)
		}
	}
	if got := Level(99).String(); !strings.Contains(got, "99") {
		t.Errorf("an out-of-range level rendered as %q, which names no level", got)
	}
}

func TestParseLevel(t *testing.T) {
	cases := []struct {
		in      string
		want    Level
		wantErr bool
	}{
		{"error", LevelError, false},
		{"ERROR", LevelError, false},
		{"warn", LevelWarn, false},
		{"warning", LevelWarn, false},
		{" Warn ", LevelWarn, false},
		{"info", LevelInfo, false},
		{"", LevelInfo, false},
		{"debug", LevelDebug, false},
		{"verbose", LevelInfo, true},
	}
	for _, tc := range cases {
		got, err := ParseLevel(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseLevel(%q) accepted an unknown level", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseLevel(%q) failed: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseLevel(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestDefaultLevelShowsWhatCorralAlwaysShowed(t *testing.T) {
	buf := capture(t, LevelInfo)

	Errorf("clone failed: %v", errors.New("network unreachable"))
	Warnf("not migrating %s", "~/Code/go/tools")
	Infof("fetching repositories")
	Debugf("state sidecar absent")

	out := buf.String()
	for _, want := range []string{"ERROR: clone failed", "WARN: not migrating", "INFO: fetching"} {
		if !strings.Contains(out, want) {
			t.Errorf("default level dropped %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "DEBUG") {
		t.Errorf("debug output appeared at the default level:\n%s", out)
	}
}

func TestLevelsFilterFromTheBottomUp(t *testing.T) {
	cases := []struct {
		level   Level
		visible []string
		hidden  []string
	}{
		{LevelError, []string{"ERROR"}, []string{"WARN", "INFO", "DEBUG"}},
		{LevelWarn, []string{"ERROR", "WARN"}, []string{"INFO", "DEBUG"}},
		{LevelInfo, []string{"ERROR", "WARN", "INFO"}, []string{"DEBUG"}},
		{LevelDebug, []string{"ERROR", "WARN", "INFO", "DEBUG"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.level.String(), func(t *testing.T) {
			buf := capture(t, tc.level)
			Errorf("e")
			Warnf("w")
			Infof("i")
			Debugf("d")
			out := buf.String()
			for _, want := range tc.visible {
				if !strings.Contains(out, want) {
					t.Errorf("level %s hid %s:\n%s", tc.level, want, out)
				}
			}
			for _, unwanted := range tc.hidden {
				if strings.Contains(out, unwanted) {
					t.Errorf("level %s showed %s:\n%s", tc.level, unwanted, out)
				}
			}
		})
	}
}

func TestEnabledMatchesWhatIsEmitted(t *testing.T) {
	capture(t, LevelWarn)
	if !Enabled(LevelError) || !Enabled(LevelWarn) {
		t.Error("Enabled denied a level that is emitted at warn")
	}
	if Enabled(LevelInfo) || Enabled(LevelDebug) {
		t.Error("Enabled permitted a level that is filtered at warn")
	}
}

func TestEmitWritesOneTrimmedLine(t *testing.T) {
	buf := capture(t, LevelInfo)
	Infof("trailing newlines are the caller's habit, not the format's\n\n")

	out := buf.String()
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("expected exactly one line, got %q", out)
	}
	if !strings.HasSuffix(out, "format's\n") {
		t.Fatalf("trailing newlines were not trimmed: %q", out)
	}
}

func TestSetOutputNilRestoresStderr(t *testing.T) {
	previous := CurrentLevel()
	t.Cleanup(func() { SetLevel(previous); SetOutput(nil) })

	SetOutput(&bytes.Buffer{})
	SetOutput(nil)

	mu.RLock()
	restored := output
	mu.RUnlock()
	if restored != io.Writer(os.Stderr) {
		t.Fatalf("SetOutput(nil) left the sink at %#v, want os.Stderr", restored)
	}
}

func TestFormatNames(t *testing.T) {
	if got := FormatText.String(); got != "text" {
		t.Errorf("FormatText.String() = %q, want %q", got, "text")
	}
	if got := FormatJSON.String(); got != "json" {
		t.Errorf("FormatJSON.String() = %q, want %q", got, "json")
	}
	if got := Format(99).String(); !strings.Contains(got, "99") {
		t.Errorf("out-of-range format string = %q, want it to contain 99", got)
	}
}

func TestParseFormat(t *testing.T) {
	cases := []struct {
		in      string
		want    Format
		wantErr bool
	}{
		{"text", FormatText, false},
		{"TEXT", FormatText, false},
		{"", FormatText, false},
		{"json", FormatJSON, false},
		{"JSON", FormatJSON, false},
		{" xml ", FormatText, true},
	}
	for _, tc := range cases {
		got, err := ParseFormat(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseFormat(%q) accepted unknown format", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseFormat(%q) returned unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseFormat(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestEmitJSON(t *testing.T) {
	buf := capture(t, LevelDebug)
	SetFormat(FormatJSON)

	fixedTime := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	origTimeNow := timeNow
	timeNow = func() time.Time { return fixedTime }
	t.Cleanup(func() { timeNow = origTimeNow })

	Errorf("failure: %s", "disk full")
	Warnf("warning: %s\n", "quota near")
	Infof("info: %s", "syncing")
	Debugf("debug: %s", "tracing")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines of JSON, got %d:\n%s", len(lines), buf.String())
	}

	expectedLevels := []string{"ERROR", "WARN", "INFO", "DEBUG"}
	expectedMsgs := []string{"failure: disk full", "warning: quota near", "info: syncing", "debug: tracing"}

	for i, line := range lines {
		var record struct {
			Time  string `json:"time"`
			Level string `json:"level"`
			Msg   string `json:"msg"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("line %d is not valid JSON (%v): %s", i, err, line)
		}
		if record.Level != expectedLevels[i] {
			t.Errorf("line %d: level = %q, want %q", i, record.Level, expectedLevels[i])
		}
		if record.Msg != expectedMsgs[i] {
			t.Errorf("line %d: msg = %q, want %q", i, record.Msg, expectedMsgs[i])
		}
		if record.Time == "" {
			t.Errorf("line %d: time is empty", i)
		}
	}
}

func TestEmitJSONFiltering(t *testing.T) {
	buf := capture(t, LevelWarn)
	SetFormat(FormatJSON)

	Errorf("fatal error")
	Warnf("subsystem warning")
	Infof("routine progress")
	Debugf("verbose detail")

	out := buf.String()
	if !strings.Contains(out, "fatal error") || !strings.Contains(out, "subsystem warning") {
		t.Fatalf("expected error and warning in JSON output, got:\n%s", out)
	}
	if strings.Contains(out, "routine progress") || strings.Contains(out, "verbose detail") {
		t.Fatalf("unexpected info/debug in warn-level JSON output, got:\n%s", out)
	}
}
