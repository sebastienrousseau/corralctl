// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package github

import (
	"errors"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type retryTransport struct {
	base       http.RoundTripper
	maxRetries int
	minBackoff time.Duration
	maxBackoff time.Duration
}

// RoundTrip implements http.RoundTripper, retrying transient failures and
// rate-limit responses with backoff until the request succeeds, becomes
// non-retryable, the retry budget is exhausted, or the context is cancelled.
func (t *retryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}

	for attempt := 0; ; attempt++ {
		clonedReq := req.Clone(req.Context())
		resp, err := base.RoundTrip(clonedReq)

		retry, wait := shouldRetry(resp, err, attempt, t.maxRetries)
		if !retry {
			return resp, err
		}
		if resp != nil && resp.Body != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}

		if wait <= 0 {
			wait = t.backoff(attempt)
		}

		// Sleeping past the operation's deadline helps nobody: the wait is
		// guaranteed to lose the race below, and the caller then sees a bare
		// "context deadline exceeded" that names neither the rate limit nor
		// when it lifts. Report it now, while the reason is still known.
		//
		// This is the branch that could not previously be reached at all:
		// with one 30s budget covering both the request and the whole
		// operation, a reset further out than the remaining time always lost,
		// so the rate-limit handling never completed a single wait.
		if deadline, ok := req.Context().Deadline(); ok {
			if remaining := time.Until(deadline); wait > remaining {
				return nil, &RetryBudgetError{
					Wait:      wait,
					Remaining: remaining,
					Status:    statusOf(resp),
				}
			}
		}

		timer := time.NewTimer(wait)
		select {
		case <-req.Context().Done():
			timer.Stop()
			return nil, req.Context().Err()
		case <-timer.C:
		}
	}
}

// statusOf returns resp's status code, or 0 when there is no response.
func statusOf(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}

func (t *retryTransport) backoff(attempt int) time.Duration {
	minBackoff := t.minBackoff
	maxBackoff := t.maxBackoff
	if minBackoff <= 0 {
		minBackoff = defaultRetryMinBackoff
	}
	if maxBackoff <= 0 {
		maxBackoff = defaultRetryMaxBackoff
	}

	backoff := minBackoff << attempt
	if backoff > maxBackoff {
		backoff = maxBackoff
	}
	// Non-cryptographic randomness is acceptable for retry backoff jitter.
	jitter := time.Duration(rand.Int63n(int64(minBackoff) + 1)) //nolint:gosec // G404: jitter does not require crypto/rand
	return backoff + jitter
}

func shouldRetry(resp *http.Response, err error, attempt, maxRetries int) (bool, time.Duration) {
	if attempt >= maxRetries {
		return false, 0
	}

	if err != nil {
		if isRetryableNetworkError(err) {
			return true, 0
		}
		return false, 0
	}
	if resp == nil {
		return false, 0
	}

	if d, ok := retryAfterDuration(resp); ok {
		return true, d
	}

	switch resp.StatusCode {
	case http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true, 0
	case http.StatusForbidden:
		if strings.TrimSpace(resp.Header.Get("X-RateLimit-Remaining")) == "0" {
			if d, ok := rateLimitResetDuration(resp); ok {
				return true, d
			}
			return true, time.Minute
		}
	}

	return false, 0
}

func isRetryableNetworkError(err error) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return errors.Is(err, io.EOF)
}

func retryAfterDuration(resp *http.Response) (time.Duration, bool) {
	h := strings.TrimSpace(resp.Header.Get("Retry-After"))
	if h == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(h); err == nil {
		if seconds < 0 {
			seconds = 0
		}
		return time.Duration(seconds) * time.Second, true
	}
	if t, err := http.ParseTime(h); err == nil {
		d := time.Until(t)
		if d < 0 {
			d = 0
		}
		return d, true
	}
	return 0, false
}

func rateLimitResetDuration(resp *http.Response) (time.Duration, bool) {
	h := strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset"))
	if h == "" {
		return 0, false
	}
	sec, err := strconv.ParseInt(h, 10, 64)
	if err != nil {
		return 0, false
	}
	resetAt := time.Unix(sec, 0)
	d := time.Until(resetAt)
	if d < 0 {
		d = 0
	}
	return d, true
}
