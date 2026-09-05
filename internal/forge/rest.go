// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// A small paginated JSON client, shared by the hand-written forges.
//
// Not a general HTTP library. It does exactly what listing repositories
// needs — page until the forge says stop, honour two timeouts, retry the
// failures worth retrying, and refuse to read an unbounded body — and
// having it once means GitLab and Gitea cannot disagree about any of that.
//
// # The two timeouts
//
// One deadline is not enough, and this is the mistake the GitHub client
// already had to fix. A per-request timeout bounds a hung connection; a
// total timeout bounds the operation. With only the first, an
// organisation with fifty pages can run for fifty times the timeout the
// user set. With only the second, one wedged connection consumes the
// whole budget while forty-nine pages would have succeeded.

// Bounds. Each exists because a forge, or something pretending to be one,
// can otherwise make a listing unbounded.
const (
	// maxPages caps pagination. A forge that never reports the last page —
	// through a bug, a proxy, or hostility — would otherwise loop
	// forever.
	maxPages = 200
	// perPage is the page size requested. 100 is the maximum GitLab and
	// Gitea both accept.
	perPage = 100
	// maxBodyBytes caps one response body. A listing page is tens of
	// kilobytes; anything at this size is not one.
	maxBodyBytes = 32 << 20
	// maxRetries is how many times a transient failure is retried.
	maxRetries = 3
)

// defaultRequestTimeout and defaultTotalTimeout apply when Options leaves
// them zero. They match the GitHub client's defaults, so a user moving
// between forges does not find the timeouts changing under them.
const (
	defaultRequestTimeout = 30 * time.Second
	defaultTotalTimeout   = 10 * time.Minute
)

// httpClient is indirected so tests drive transport failures that a real
// server cannot be asked to produce.
var httpClient = func(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}

// sleep is indirected so a retry test does not actually wait.
var sleep = time.Sleep

// restClient issues paginated GET requests against one forge instance.
type restClient struct {
	base  *url.URL
	token string
	// tokenHeader is how this forge wants the credential presented.
	// GitLab uses PRIVATE-TOKEN, Gitea uses Authorization: token.
	tokenHeader string
	tokenPrefix string
	userAgent   string
	client      *http.Client
	deadline    time.Time
}

// newRESTClient builds a client for base, which must be an absolute URL.
func newRESTClient(base, token, tokenHeader, tokenPrefix string, opts Options) (*restClient, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return nil, fmt.Errorf("invalid base URL %q: %w", base, err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		// Not merely unsupported: a base URL with another scheme is how a
		// credential ends up somewhere unintended.
		return nil, fmt.Errorf("base URL %q must be http or https", base)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("base URL %q has no host", base)
	}

	reqTimeout := opts.RequestTimeout
	if reqTimeout <= 0 {
		reqTimeout = defaultRequestTimeout
	}
	total := opts.TotalTimeout
	if total <= 0 {
		total = defaultTotalTimeout
	}
	if total < reqTimeout {
		// A total smaller than one request's budget cannot be satisfied,
		// and silently honouring the smaller one would make a listing fail
		// in a way nobody could explain.
		total = reqTimeout
	}

	return &restClient{
		base:        u,
		token:       token,
		tokenHeader: tokenHeader,
		tokenPrefix: tokenPrefix,
		userAgent:   userAgent,
		client:      httpClient(reqTimeout),
		deadline:    time.Now().Add(total),
	}, nil
}

// userAgent identifies corral to a forge. Some instances reject requests
// without one, and an operator reading their logs deserves to know what
// is calling them.
const userAgent = "corral (+https://github.com/sebastienrousseau/corralctl)"

// getPage fetches one page and decodes it into out.
//
// Returns the number of items decoded so the caller can stop when a page
// comes back short, which is how both GitLab and Gitea signal the end
// without a cursor.
func (c *restClient) getPage(ctx context.Context, path string, query url.Values, out any) error {
	if time.Now().After(c.deadline) {
		return fmt.Errorf("total timeout reached before requesting %s", path)
	}

	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = query.Encode()

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			wait := backoff(attempt)
			if time.Now().Add(wait).After(c.deadline) {
				return fmt.Errorf("%w (no budget left to retry)", lastErr)
			}
			sleep(wait)
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		body, err := c.do(ctx, u.String())
		if err != nil {
			lastErr = err
			if retryable(err) {
				continue
			}
			return err
		}
		if err := json.Unmarshal(body, out); err != nil {
			// A body that does not parse is not retryable: the same
			// request will return the same thing.
			return fmt.Errorf("decoding %s: %w", u.Redacted(), err)
		}
		return nil
	}
	return fmt.Errorf("%s: %w", u.Redacted(), lastErr)
}

// do performs one request and returns its body.
func (c *restClient) do(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if c.token != "" {
		req.Header.Set(c.tokenHeader, c.tokenPrefix+c.token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, &transientError{err: err}
	}
	defer func() { _ = resp.Body.Close() }()

	// Bounded, so a hostile or broken instance cannot exhaust memory.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, &transientError{err: err}
	}

	switch {
	case resp.StatusCode == http.StatusNotFound:
		// Distinguished from an empty list on purpose: "this owner does
		// not exist" and "this owner has nothing" lead to different
		// actions, and conflating them hands the caller the wrong one.
		return nil, &NotFoundError{URL: redact(rawURL)}
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return nil, &AuthError{Status: resp.StatusCode, URL: redact(rawURL), Body: snippet(body)}
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, &transientError{err: fmt.Errorf("rate limited (HTTP 429)")}
	case resp.StatusCode >= 500:
		return nil, &transientError{err: fmt.Errorf("HTTP %d from %s", resp.StatusCode, redact(rawURL))}
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, redact(rawURL), snippet(body))
	}
	return body, nil
}

// NotFoundError reports an owner or instance that does not exist.
type NotFoundError struct {
	// URL is the request that returned 404, with any credential removed.
	URL string
}

// Error implements error. It names the two things that are usually
// wrong: a mistyped owner, and a self-hosted instance nobody told corral
// about.
func (e *NotFoundError) Error() string {
	return fmt.Sprintf("not found: %s (check the owner name, and --forge-url for a self-hosted instance)", e.URL)
}

// AuthError reports a credential that was rejected or is missing.
type AuthError struct {
	// Status is the HTTP status returned.
	Status int
	// URL is the request, with any credential removed.
	URL string
	// Body is a short excerpt of the response, for the forge's own
	// explanation.
	Body string
}

// Error implements error, carrying the forge's own explanation alongside
// the flag that supplies a credential.
func (e *AuthError) Error() string {
	return fmt.Sprintf("HTTP %d from %s: %s (private repositories need a token; see --auth)",
		e.Status, e.URL, e.Body)
}

// transientError marks a failure worth retrying.
type transientError struct{ err error }

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

// retryable reports whether an error is worth another attempt.
func retryable(err error) bool {
	var t *transientError
	return errors.As(err, &t)
}

// backoff is the delay before attempt n, doubling and capped.
//
// No jitter, because corral runs one listing at a time against one host:
// jitter exists to de-synchronise a fleet, and there is no fleet here.
func backoff(attempt int) time.Duration {
	d := time.Second << (attempt - 1)
	if d > 8*time.Second {
		return 8 * time.Second
	}
	return d
}

// redact removes a credential that a caller put in the URL itself.
func redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "the request URL"
	}
	return u.Redacted()
}

// snippet bounds a forge's error body before it reaches a message.
//
// The body is written by whoever runs the instance, and it ends up in a
// terminal and in an agent's context.
func snippet(b []byte) string {
	const max = 200
	s := strings.TrimSpace(string(b))
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) > max {
		return s[:max] + "…"
	}
	if s == "" {
		return "(no message)"
	}
	return s
}

// pageQuery builds the page/per_page parameters GitLab and Gitea use.
func pageQuery(page int, extra url.Values) url.Values {
	return pageQueryNamed(page, "per_page", extra)
}

// pageQueryNamed is pageQuery with the page-size parameter named
// explicitly, for a forge that spells it differently.
//
// Bitbucket calls it pagelen and silently ignores per_page, falling back
// to a default of ten — which is not an error and not visible in the
// output, just ten times the requests. Verified against the live API.
func pageQueryNamed(page int, sizeParam string, extra url.Values) url.Values {
	q := url.Values{}
	for k, vs := range extra {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	q.Set("page", strconv.Itoa(page))
	q.Set(sizeParam, strconv.Itoa(perPage))
	return q
}

// StatusError reports a response the caller has to interpret: a 404 that
// means "create it", a 409 that means "somebody already did", or anything
// else that means stop.
type StatusError struct {
	// Status is the HTTP status returned.
	Status int
	// URL is the request, with any credential removed.
	URL string
	// Body is a short excerpt of the response.
	Body string
}

// Error implements error.
func (e *StatusError) Error() string {
	return fmt.Sprintf("HTTP %d from %s: %s", e.Status, e.URL, e.Body)
}

// statusIs reports whether err is a StatusError with the given status.
func statusIs(err error, status int) bool {
	var se *StatusError
	return errors.As(err, &se) && se.Status == status
}

// call performs one request — GET, POST, whatever — with an optional JSON
// body and decodes a JSON response into out. It returns the status code
// alongside the error so a caller can act on a 404 or a 409.
//
// Unlike getPage it never retries. A listing can be asked again; a create
// cannot be, because the first attempt may have succeeded on the far side
// after the response was lost, and the retry then fails on a conflict the
// caller has to resolve anyway.
func (c *restClient) call(ctx context.Context, method, path string, in, out any) (int, error) {
	u := *c.base
	u.Path = strings.TrimRight(u.Path, "/") + path

	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, fmt.Errorf("encoding the request to %s: %w", u.Redacted(), err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set(c.tokenHeader, c.tokenPrefix+c.token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return resp.StatusCode, err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return resp.StatusCode, &AuthError{Status: resp.StatusCode, URL: u.Redacted(), Body: snippet(raw)}
	case resp.StatusCode >= 400:
		return resp.StatusCode, &StatusError{Status: resp.StatusCode, URL: u.Redacted(), Body: snippet(raw)}
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, fmt.Errorf("decoding %s: %w", u.Redacted(), err)
		}
	}
	return resp.StatusCode, nil
}
