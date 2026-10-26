// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package forge

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxRetryAfter caps the wait a server can ask for. A Retry-After of a
// billion seconds is either a bug or hostility, and as a duration it would
// overflow. Ten minutes is the default total budget, so a capped wait
// that still does not fit is reported against --api-total-timeout like
// any other.
const maxRetryAfter = 10 * time.Minute

// sleep waits for d, or until ctx ends, whichever comes first.
//
// A plain time.Sleep here made Ctrl-C wait out the backoff, up to eight
// seconds per retry, before the listing noticed. Indirected so a retry
// test can record the waits without spending them.
var sleep = func(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// retryAfter reads a Retry-After header in either form RFC 9110 allows:
// delay-seconds, or an HTTP-date. ok is false when the header is absent
// or unreadable, and the caller falls back to its own backoff. A date in
// the past means "now". The result is capped at maxRetryAfter.
func retryAfter(h http.Header, now time.Time) (time.Duration, bool) {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0, false
	}
	var d time.Duration
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs < 0 {
			return 0, false
		}
		if secs > int64(maxRetryAfter/time.Second) {
			return maxRetryAfter, true
		}
		d = time.Duration(secs) * time.Second
	} else if at, err := http.ParseTime(v); err == nil {
		d = max(at.Sub(now), 0)
	} else {
		return 0, false
	}
	return min(d, maxRetryAfter), true
}

// retryWait is how long to wait before attempt n after lastErr: what the
// server asked for when it said, the doubling backoff otherwise. asked
// reports which, so a refusal can say whose number it was.
func retryWait(attempt int, lastErr error) (wait time.Duration, asked bool) {
	var t *transientError
	if errors.As(lastErr, &t) && t.retryAfter > 0 {
		return t.retryAfter, true
	}
	return backoff(attempt), false
}
