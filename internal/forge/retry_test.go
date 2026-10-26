// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package forge

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// recordSleeps replaces the retry wait with one that records each
// requested duration and returns at once.
func recordSleeps(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	old := sleep
	sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	t.Cleanup(func() { sleep = old })
	return &waits
}

// failThenServe answers the first request with status and headers, and
// every later one with a one-repository Gitea page.
func failThenServe(t *testing.T, status int, header map[string]string) *recordingServer {
	t.Helper()
	var n atomic.Int64
	return newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		if n.Add(1) == 1 {
			for k, v := range header {
				w.Header().Set(k, v)
			}
			w.WriteHeader(status)
			return
		}
		_, _ = w.Write([]byte(giteaPage(1, 1, false)))
	})
}

func TestRESTClientHonoursRetryAfterSeconds(t *testing.T) {
	waits := recordSleeps(t)
	srv := failThenServe(t, http.StatusTooManyRequests, map[string]string{"Retry-After": "7"})
	g, _ := Get("gitea")
	if _, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if len(*waits) != 1 || (*waits)[0] != 7*time.Second {
		t.Fatalf("waits = %v, want [7s] from Retry-After", *waits)
	}
}

func TestRESTClientHonoursRetryAfterDate(t *testing.T) {
	waits := recordSleeps(t)
	at := time.Now().Add(20 * time.Second).UTC().Format(http.TimeFormat)
	srv := failThenServe(t, http.StatusServiceUnavailable, map[string]string{"Retry-After": at})
	g, _ := Get("gitea")
	if _, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	// HTTP-date has one-second resolution, so allow for the truncation
	// and the time the request took.
	if len(*waits) != 1 || (*waits)[0] < 17*time.Second || (*waits)[0] > 20*time.Second {
		t.Fatalf("waits = %v, want about 20s from the Retry-After date", *waits)
	}
}

func TestRESTClientFallsBackToBackoffWithoutRetryAfter(t *testing.T) {
	waits := recordSleeps(t)
	srv := failThenServe(t, http.StatusTooManyRequests, map[string]string{"Retry-After": "soon"})
	g, _ := Get("gitea")
	if _, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if len(*waits) != 1 || (*waits)[0] != backoff(1) {
		t.Fatalf("waits = %v, want [%v]", *waits, backoff(1))
	}
}

func TestRESTClientRefusesARetryAfterPastTheBudget(t *testing.T) {
	waits := recordSleeps(t)
	srv := failThenServe(t, http.StatusTooManyRequests, map[string]string{"Retry-After": "3600"})
	g, _ := Get("gitea")
	_, err := g.List(context.Background(), "acme", Options{BaseURL: srv.URL, TotalTimeout: time.Minute})
	if err == nil {
		t.Fatal("a wait longer than the remaining budget should be an error")
	}
	if len(*waits) != 0 {
		t.Errorf("waited %v; a wait that cannot finish in budget must not start", *waits)
	}
	for _, want := range []string{"asked to wait", "--api-total-timeout"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should contain %q", err, want)
		}
	}
}

func TestRESTClientStopsWaitingWhenCancelled(t *testing.T) {
	// The real wait, not a stub: what is under test is that Ctrl-C ends
	// a backoff instead of sitting it out.
	srv := newRecordingServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	g, _ := Get("gitea")
	start := time.Now()
	_, err := g.List(ctx, "acme", Options{BaseURL: srv.URL})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("returned after %v; cancellation should end the 1s backoff", elapsed)
	}
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		value string
		want  time.Duration
		ok    bool
	}{
		"absent":      {"", 0, false},
		"seconds":     {"5", 5 * time.Second, true},
		"zero":        {"0", 0, true},
		"negative":    {"-3", 0, false},
		"huge":        {"999999999999", maxRetryAfter, true},
		"date":        {now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second, true},
		"past date":   {now.Add(-time.Hour).Format(http.TimeFormat), 0, true},
		"far date":    {now.Add(48 * time.Hour).Format(http.TimeFormat), maxRetryAfter, true},
		"nonsense":    {"later", 0, false},
		"spaced secs": {" 2 ", 2 * time.Second, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := http.Header{}
			if tc.value != "" {
				h.Set("Retry-After", tc.value)
			}
			got, ok := retryAfter(h, now)
			if got != tc.want || ok != tc.ok {
				t.Errorf("retryAfter(%q) = %v, %t; want %v, %t", tc.value, got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestSleepHonoursTheContext(t *testing.T) {
	if err := sleep(context.Background(), time.Millisecond); err != nil {
		t.Errorf("a completed wait should not be an error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled wait = %v, want context.Canceled", err)
	}
}
