// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v90/github"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newTestClient(rt http.RoundTripper) *gh.Client {
	// v90 made BaseURL a read-only accessor; the base URL is now set through
	// the WithURLs option at construction time.
	base := "https://api.test/"
	client, err := gh.NewClient(
		gh.WithHTTPClient(&http.Client{Transport: rt}),
		gh.WithURLs(&base, nil),
	)
	if err != nil {
		panic(err) // a client built from a stub transport cannot fail
	}
	return client
}

func jsonResp(req *http.Request, status int, body string, headers map[string]string) *http.Response {
	h := make(http.Header)
	for k, v := range headers {
		h.Set(k, v)
	}
	return &http.Response{
		StatusCode: status,
		Header:     h,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestFetchReposWithOptionsAuthModes(t *testing.T) {
	oldGitHub, hadGitHub := os.LookupEnv("GITHUB_TOKEN")
	oldGH, hadGH := os.LookupEnv("GH_TOKEN")
	defer func() {
		restoreEnv(t, "GITHUB_TOKEN", oldGitHub, hadGitHub)
		restoreEnv(t, "GH_TOKEN", oldGH, hadGH)
	}()
	mustUnsetenv(t, "GITHUB_TOKEN")
	mustUnsetenv(t, "GH_TOKEN")

	_, err := resolveToken(context.Background(), AuthModeToken)
	if err == nil || !strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Fatalf("expected token env error, got %v", err)
	}

	oldRunAuth := runGitHubCLIAuthToken
	defer func() { runGitHubCLIAuthToken = oldRunAuth }()
	runGitHubCLIAuthToken = func(ctx context.Context) (string, error) {
		return "gh-token", nil
	}

	token, err := resolveToken(context.Background(), AuthModeGH)
	if err != nil {
		t.Fatalf("expected gh token resolution, got %v", err)
	}
	if token != "gh-token" {
		t.Fatalf("expected gh-token, got %q", token)
	}

	mustSetenv(t, "GITHUB_TOKEN", "env-token")
	token, err = resolveToken(context.Background(), AuthModeAuto)
	if err != nil {
		t.Fatalf("expected auto token resolution, got %v", err)
	}
	if token != "env-token" {
		t.Fatalf("expected env-token, got %q", token)
	}
}

func TestFetchReposWithClientOptions(t *testing.T) {
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/users/org1":
			return jsonResp(req, http.StatusOK, `{"type":"Organization"}`, nil), nil
		case "/users/user1":
			return jsonResp(req, http.StatusOK, `{"type":"User"}`, nil), nil
		case "/users/unknown":
			return jsonResp(req, http.StatusNotFound, `{"message":"not found"}`, nil), nil
		case "/users/user_error":
			return jsonResp(req, http.StatusOK, `{"type":"User"}`, nil), nil
		case "/orgs/org1/repos":
			page := req.URL.Query().Get("page")
			if page == "2" {
				return jsonResp(req, http.StatusOK, `[
					{"name":"repo3","language":"Go","visibility":"private","default_branch":"master","clone_url":"http://clone3","ssh_url":"ssh3","archived":true}
				]`, map[string]string{"Link": `<https://api.test/orgs/org1/repos?page=2>; rel="last"`}), nil
			}
			return jsonResp(req, http.StatusOK, `[
				{"name":"repo1","language":"Go","visibility":"public","default_branch":"main","clone_url":"http://clone","ssh_url":"ssh","fork":false,"pushed_at":"2026-01-15T10:00:00Z"},
				{"name":"repo2","visibility":"internal","fork":true}
			]`, map[string]string{"Link": `<https://api.test/orgs/org1/repos?page=2>; rel="next", <https://api.test/orgs/org1/repos?page=2>; rel="last"`}), nil
		case "/users/user1/repos":
			return jsonResp(req, http.StatusOK, `[
				{"name":"userrepo","language":"Rust","visibility":"public","default_branch":"main"}
			]`, nil), nil
		case "/users/user_error/repos":
			return jsonResp(req, http.StatusInternalServerError, `{"message":"boom"}`, nil), nil
		default:
			return nil, fmt.Errorf("unexpected path: %s", req.URL.Path)
		}
	})

	client := newTestClient(rt)

	// A 404 on the owner lookup is now reported in human terms rather than as
	// the raw go-github "failed to get user/org ...: 404 Not Found []".
	// TestDescribeOwnerLookupErrorRewrites404 covers the wording in detail.
	_, err := FetchReposWithClientOptions(context.Background(), client, "unknown", FetchOptions{Limit: 10})
	if err == nil || !strings.Contains(err.Error(), `no GitHub user or organisation named "unknown"`) {
		t.Fatalf("expected owner lookup error, got: %v", err)
	}

	_, err = FetchReposWithClientOptions(context.Background(), client, "user_error", FetchOptions{Limit: 10})
	if err == nil {
		t.Fatalf("expected repo list error")
	}

	repos, err := FetchReposWithClientOptions(context.Background(), client, "org1", FetchOptions{Limit: 10, IncludeArchived: true, IncludeForks: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repos) != 3 {
		t.Fatalf("expected 3 repos, got %d", len(repos))
	}
	if repos[1].Language != "Other" {
		t.Fatalf("expected default language Other, got %q", repos[1].Language)
	}

	want := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	if !repos[0].PushedAt.Equal(want) {
		t.Errorf("expected PushedAt %v on repo1, got %v", want, repos[0].PushedAt)
	}
	if !repos[1].PushedAt.IsZero() {
		t.Errorf("expected zero PushedAt on repo2 (no pushed_at in fixture), got %v", repos[1].PushedAt)
	}

	repos, err = FetchReposWithClientOptions(context.Background(), client, "org1", FetchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("expected only 1 repo after default filters, got %d", len(repos))
	}

	repos, err = FetchReposWithClientOptions(context.Background(), client, "org1", FetchOptions{Limit: 10, IncludeArchived: true, Visibility: "private", IncludeForks: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("expected 2 private repos, got %d", len(repos))
	}

	repos, err = FetchReposWithClientOptions(context.Background(), client, "user1", FetchOptions{Limit: 10, IncludeLanguages: []string{"rust"}, IncludeArchived: true, IncludeForks: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("expected 1 repo from language include, got %d", len(repos))
	}

	repos, err = FetchReposWithClientOptions(context.Background(), client, "user1", FetchOptions{Limit: 10, ExcludeLanguages: []string{"rust"}, IncludeArchived: true, IncludeForks: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repos) != 0 {
		t.Fatalf("expected excluded language to remove all repos, got %d", len(repos))
	}

	repos, err = FetchReposWithClient(context.Background(), client, "org1", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("expected limit=1, got %d", len(repos))
	}
}

func TestFetchPaginationFailureAndCancellation(t *testing.T) {
	t.Run("concurrent page failure", func(t *testing.T) {
		rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/users/org":
				return jsonResp(req, http.StatusOK, `{"type":"Organization"}`, nil), nil
			case "/orgs/org/repos":
				if req.URL.Query().Get("page") == "2" {
					return jsonResp(req, http.StatusInternalServerError, `{"message":"boom"}`, nil), nil
				}
				return jsonResp(req, http.StatusOK, `[{"name":"one"}]`, map[string]string{
					"Link": `<https://api.test/orgs/org/repos?page=2>; rel="next", <https://api.test/orgs/org/repos?page=2>; rel="last"`,
				}), nil
			default:
				return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
			}
		})
		_, err := FetchReposWithClientOptions(context.Background(), newTestClient(rt), "org", FetchOptions{IncludeForks: true, IncludeArchived: true})
		if err == nil || !strings.Contains(err.Error(), "concurrent fetch failed") {
			t.Fatalf("expected concurrent page failure, got %v", err)
		}
	})

	t.Run("context canceled before concurrent pages", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/users/org" {
				return jsonResp(req, http.StatusOK, `{"type":"Organization"}`, nil), nil
			}
			if req.URL.Query().Get("page") == "2" {
				return nil, req.Context().Err()
			}
			cancel()
			return jsonResp(req, http.StatusOK, `[{"name":"one"}]`, map[string]string{
				"Link": `<https://api.test/orgs/org/repos?page=2>; rel="next", <https://api.test/orgs/org/repos?page=2>; rel="last"`,
			}), nil
		})
		_, err := FetchReposWithClientOptions(ctx, newTestClient(rt), "org", FetchOptions{IncludeForks: true, IncludeArchived: true})
		if err == nil || !strings.Contains(err.Error(), "concurrent fetch failed") {
			t.Fatalf("expected canceled concurrent fetch, got %v", err)
		}
	})
}

func TestAcquirePageSlot(t *testing.T) {
	sem := make(chan struct{}, 1)
	if err := acquirePageSlot(context.Background(), sem); err != nil {
		t.Fatal(err)
	}
	<-sem

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sem <- struct{}{}
	if err := acquirePageSlot(ctx, sem); !errors.Is(err, context.Canceled) {
		t.Fatalf("acquirePageSlot() error = %v", err)
	}
	<-sem
}

func TestFetchSequentialPaginationAndSorts(t *testing.T) {
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/users/person":
			return jsonResp(req, http.StatusOK, `{"type":"User","login":"person"}`, nil), nil
		case "/user":
			return jsonResp(req, http.StatusForbidden, `{"message":"no auth"}`, nil), nil
		case "/users/person/repos":
			page := req.URL.Query().Get("page")
			switch page {
			case "2":
				return jsonResp(req, http.StatusOK, `[{"name":"alpha","stargazers_count":9,"pushed_at":"2026-02-01T00:00:00Z"}]`, map[string]string{
					"Link": `<https://api.test/users/person/repos?page=3>; rel="next"`,
				}), nil
			case "3":
				return jsonResp(req, http.StatusOK, `[{"name":"middle","stargazers_count":3,"pushed_at":"2026-01-15T00:00:00Z"}]`, nil), nil
			default:
				return jsonResp(req, http.StatusOK, `[{"name":"zulu","stargazers_count":1,"pushed_at":"2026-01-01T00:00:00Z"}]`, map[string]string{
					"Link": `<https://api.test/users/person/repos?page=2>; rel="next"`,
				}), nil
			}
		default:
			return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
		}
	})
	client := newTestClient(rt)
	for _, tc := range []struct {
		sort, first string
	}{{"name", "alpha"}, {"stars", "alpha"}, {"updated", "alpha"}, {"last updated", "alpha"}} {
		repos, err := FetchReposWithClientOptions(context.Background(), client, "person", FetchOptions{
			Sort: tc.sort, IncludeForks: true, IncludeArchived: true,
		})
		if err != nil || len(repos) != 3 || repos[0].Name != tc.first {
			t.Fatalf("sort %q result = %+v, %v", tc.sort, repos, err)
		}
	}
}

func TestMapRepositoryBuildsFullName(t *testing.T) {
	repo := mapRepository(&gh.Repository{Name: gh.Ptr("repo"), Owner: &gh.User{Login: gh.Ptr("owner")}})
	if repo.FullName != "owner/repo" || repo.Owner != "owner" {
		t.Fatalf("mapped identity = %+v", repo)
	}
}

func TestRemainingPaginationBranches(t *testing.T) {
	t.Run("search error", func(t *testing.T) {
		client := newTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return jsonResp(req, http.StatusInternalServerError, `{"message":"boom"}`, nil), nil
		}))
		if _, err := FetchReposWithClientOptions(context.Background(), client, "topic:test", FetchOptions{}); err == nil {
			t.Fatal("expected search error")
		}
	})

	t.Run("concurrent page limit", func(t *testing.T) {
		client := newTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/users/org" {
				return jsonResp(req, http.StatusOK, `{"type":"Organization"}`, nil), nil
			}
			if req.URL.Query().Get("page") == "2" {
				return jsonResp(req, http.StatusOK, `[{"name":"two"},{"name":"three"}]`, nil), nil
			}
			return jsonResp(req, http.StatusOK, `[{"name":"one"}]`, map[string]string{
				"Link": `<https://api.test/orgs/org/repos?page=2>; rel="next", <https://api.test/orgs/org/repos?page=2>; rel="last"`,
			}), nil
		}))
		repos, err := FetchReposWithClientOptions(context.Background(), client, "org", FetchOptions{Limit: 2, IncludeForks: true, IncludeArchived: true})
		if err != nil || len(repos) != 2 {
			t.Fatalf("limited concurrent repos = %+v, %v", repos, err)
		}
	})

	t.Run("sequential error and filtered limit", func(t *testing.T) {
		failing := newTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/users/person" {
				return jsonResp(req, http.StatusOK, `{"type":"User"}`, nil), nil
			}
			if req.URL.Path == "/user" {
				return jsonResp(req, http.StatusForbidden, `{}`, nil), nil
			}
			if req.URL.Query().Get("page") == "2" {
				return jsonResp(req, http.StatusInternalServerError, `{"message":"boom"}`, nil), nil
			}
			return jsonResp(req, http.StatusOK, `[{"name":"one"}]`, map[string]string{"Link": `<https://api.test/users/person/repos?page=2>; rel="next"`}), nil
		}))
		if _, err := FetchReposWithClientOptions(context.Background(), failing, "person", FetchOptions{}); err == nil || !strings.Contains(err.Error(), "fallback fetch failed") {
			t.Fatalf("expected fallback error, got %v", err)
		}

		filtered := newTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path == "/users/person" {
				return jsonResp(req, http.StatusOK, `{"type":"User"}`, nil), nil
			}
			if req.URL.Path == "/user" {
				return jsonResp(req, http.StatusForbidden, `{}`, nil), nil
			}
			if req.URL.Query().Get("page") == "2" {
				return jsonResp(req, http.StatusOK, `[{"name":"skip","archived":true},{"name":"two"},{"name":"three"}]`, map[string]string{"Link": `<https://api.test/users/person/repos?page=3>; rel="next"`}), nil
			}
			return jsonResp(req, http.StatusOK, `[{"name":"one"}]`, map[string]string{"Link": `<https://api.test/users/person/repos?page=2>; rel="next"`}), nil
		}))
		repos, err := FetchReposWithClientOptions(context.Background(), filtered, "person", FetchOptions{Limit: 2, IncludeForks: true})
		if err != nil || len(repos) != 2 {
			t.Fatalf("filtered sequential repos = %+v, %v", repos, err)
		}
	})
}

func TestRetryTransport(t *testing.T) {
	var calls int32
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return jsonResp(req, http.StatusServiceUnavailable, `{"message":"retry"}`, nil), nil
		}
		return jsonResp(req, http.StatusOK, `{"ok":true}`, nil), nil
	})

	client := &http.Client{
		Transport: &retryTransport{
			base:       base,
			maxRetries: 2,
			minBackoff: 10 * time.Millisecond,
			maxBackoff: 20 * time.Millisecond,
		},
		Timeout: time.Second,
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.test/foo", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_ = resp.Body.Close()

	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("expected one retry, got %d calls", calls)
	}
}

func TestShouldRetryHelpers(t *testing.T) {
	resp := &http.Response{Header: make(http.Header), StatusCode: http.StatusForbidden}
	resp.Header.Set("X-RateLimit-Remaining", "0")
	resp.Header.Set("X-RateLimit-Reset", fmt.Sprintf("%d", time.Now().Add(time.Second).Unix()))
	retry, wait := shouldRetry(resp, nil, 0, 1)
	if !retry || wait <= 0 {
		t.Fatalf("expected retry with positive wait for rate limit")
	}

	retry, _ = shouldRetry(nil, errors.New("plain"), 0, 1)
	if retry {
		t.Fatalf("did not expect retry for non-network context error")
	}
}

// netError is a minimal net.Error implementation for exercising the
// retryable-network-error path deterministically.
type netError struct{}

func (netError) Error() string   { return "net error" }
func (netError) Timeout() bool   { return true }
func (netError) Temporary() bool { return true }

func TestShouldRetryAllBranches(t *testing.T) {
	// attempt >= maxRetries: no retry.
	if retry, _ := shouldRetry(nil, nil, 2, 2); retry {
		t.Fatalf("expected no retry when attempt >= maxRetries")
	}

	// Network error -> retry with zero wait.
	if retry, wait := shouldRetry(nil, netError{}, 0, 3); !retry || wait != 0 {
		t.Fatalf("expected retry with zero wait for network error, got %v %v", retry, wait)
	}

	// io.EOF -> retry.
	if retry, _ := shouldRetry(nil, io.EOF, 0, 3); !retry {
		t.Fatalf("expected retry for io.EOF")
	}

	// Non-retryable error -> no retry.
	if retry, _ := shouldRetry(nil, errors.New("boom"), 0, 3); retry {
		t.Fatalf("did not expect retry for non-network error")
	}

	// nil response and nil error -> no retry.
	if retry, _ := shouldRetry(nil, nil, 0, 3); retry {
		t.Fatalf("did not expect retry for nil response")
	}

	// Retry-After header takes precedence.
	respRA := &http.Response{Header: make(http.Header), StatusCode: http.StatusOK}
	respRA.Header.Set("Retry-After", "1")
	if retry, wait := shouldRetry(respRA, nil, 0, 3); !retry || wait != time.Second {
		t.Fatalf("expected retry with Retry-After wait, got %v %v", retry, wait)
	}

	// Retryable status codes.
	for _, code := range []int{
		http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout,
	} {
		resp := &http.Response{Header: make(http.Header), StatusCode: code}
		if retry, _ := shouldRetry(resp, nil, 0, 3); !retry {
			t.Fatalf("expected retry for status %d", code)
		}
	}

	// Forbidden with rate-limit remaining 0 but no reset header -> retry, 1 minute.
	respFB := &http.Response{Header: make(http.Header), StatusCode: http.StatusForbidden}
	respFB.Header.Set("X-RateLimit-Remaining", "0")
	if retry, wait := shouldRetry(respFB, nil, 0, 3); !retry || wait != time.Minute {
		t.Fatalf("expected retry with one minute wait, got %v %v", retry, wait)
	}

	// Forbidden without rate-limit exhaustion -> no retry.
	respFB2 := &http.Response{Header: make(http.Header), StatusCode: http.StatusForbidden}
	respFB2.Header.Set("X-RateLimit-Remaining", "5")
	if retry, _ := shouldRetry(respFB2, nil, 0, 3); retry {
		t.Fatalf("did not expect retry for forbidden with remaining quota")
	}

	// Non-retryable status -> no retry.
	respOK := &http.Response{Header: make(http.Header), StatusCode: http.StatusNotFound}
	if retry, _ := shouldRetry(respOK, nil, 0, 3); retry {
		t.Fatalf("did not expect retry for 404")
	}
}

func TestIsRetryableNetworkError(t *testing.T) {
	if !isRetryableNetworkError(netError{}) {
		t.Fatalf("expected net.Error to be retryable")
	}
	if !isRetryableNetworkError(io.EOF) {
		t.Fatalf("expected io.EOF to be retryable")
	}
	if isRetryableNetworkError(errors.New("plain")) {
		t.Fatalf("did not expect plain error to be retryable")
	}
}

func TestRetryAfterDuration(t *testing.T) {
	// Missing header.
	resp := &http.Response{Header: make(http.Header)}
	if _, ok := retryAfterDuration(resp); ok {
		t.Fatalf("expected no duration for missing header")
	}

	// Numeric seconds.
	resp.Header.Set("Retry-After", "5")
	if d, ok := retryAfterDuration(resp); !ok || d != 5*time.Second {
		t.Fatalf("expected 5s, got %v %v", d, ok)
	}

	// Negative numeric clamps to 0.
	resp.Header.Set("Retry-After", "-3")
	if d, ok := retryAfterDuration(resp); !ok || d != 0 {
		t.Fatalf("expected 0 for negative, got %v %v", d, ok)
	}

	// HTTP date in the future.
	resp.Header.Set("Retry-After", time.Now().Add(2*time.Hour).UTC().Format(http.TimeFormat))
	if d, ok := retryAfterDuration(resp); !ok || d <= 0 {
		t.Fatalf("expected positive duration for future date, got %v %v", d, ok)
	}

	// HTTP date in the past clamps to 0.
	resp.Header.Set("Retry-After", time.Now().Add(-2*time.Hour).UTC().Format(http.TimeFormat))
	if d, ok := retryAfterDuration(resp); !ok || d != 0 {
		t.Fatalf("expected 0 for past date, got %v %v", d, ok)
	}

	// Unparseable value.
	resp.Header.Set("Retry-After", "not-a-date")
	if _, ok := retryAfterDuration(resp); ok {
		t.Fatalf("expected no duration for unparseable value")
	}
}

func TestRateLimitResetDuration(t *testing.T) {
	resp := &http.Response{Header: make(http.Header)}
	// Missing header.
	if _, ok := rateLimitResetDuration(resp); ok {
		t.Fatalf("expected no duration for missing reset header")
	}

	// Invalid integer.
	resp.Header.Set("X-RateLimit-Reset", "abc")
	if _, ok := rateLimitResetDuration(resp); ok {
		t.Fatalf("expected no duration for invalid reset header")
	}

	// Future reset.
	resp.Header.Set("X-RateLimit-Reset", fmt.Sprintf("%d", time.Now().Add(time.Hour).Unix()))
	if d, ok := rateLimitResetDuration(resp); !ok || d <= 0 {
		t.Fatalf("expected positive reset duration, got %v %v", d, ok)
	}

	// Past reset clamps to 0.
	resp.Header.Set("X-RateLimit-Reset", fmt.Sprintf("%d", time.Now().Add(-time.Hour).Unix()))
	if d, ok := rateLimitResetDuration(resp); !ok || d != 0 {
		t.Fatalf("expected 0 reset duration for past, got %v %v", d, ok)
	}
}

func TestBackoff(t *testing.T) {
	// Defaults applied when zero values supplied.
	tr := &retryTransport{}
	d := tr.backoff(0)
	if d < defaultRetryMinBackoff {
		t.Fatalf("expected at least min backoff, got %v", d)
	}

	// Custom backoff with capping at maxBackoff.
	tr2 := &retryTransport{minBackoff: time.Millisecond, maxBackoff: 2 * time.Millisecond}
	capped := tr2.backoff(10) // 1ms << 10 far exceeds maxBackoff
	if capped < 2*time.Millisecond || capped > 2*time.Millisecond+time.Millisecond {
		t.Fatalf("expected backoff capped around maxBackoff, got %v", capped)
	}
}

func TestRoundTripContextCancellation(t *testing.T) {
	base := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return jsonResp(req, http.StatusServiceUnavailable, `{"message":"retry"}`, nil), nil
	})
	tr := &retryTransport{
		base:       base,
		maxRetries: 5,
		minBackoff: time.Hour, // force a long wait so cancellation wins
		maxBackoff: time.Hour,
	}

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.test/foo", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	cancel() // cancel before the wait so the select picks ctx.Done

	if _, err := tr.RoundTrip(req); err == nil {
		t.Fatalf("expected context cancellation error")
	}
}

func TestRoundTripNilBase(t *testing.T) {
	// base is nil -> RoundTrip falls back to http.DefaultTransport but the
	// request fails to connect; shouldRetry sees a network error and retries
	// up to maxRetries, then returns the final error.
	tr := &retryTransport{base: nil, maxRetries: 0}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		"http://127.0.0.1:0/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if _, err := tr.RoundTrip(req); err == nil {
		t.Fatalf("expected connection error")
	}
}

func TestEnvToken(t *testing.T) {
	oldGitHub, hadGitHub := os.LookupEnv("GITHUB_TOKEN")
	oldGH, hadGH := os.LookupEnv("GH_TOKEN")
	defer func() {
		restoreEnv(t, "GITHUB_TOKEN", oldGitHub, hadGitHub)
		restoreEnv(t, "GH_TOKEN", oldGH, hadGH)
	}()

	mustUnsetenv(t, "GITHUB_TOKEN")
	mustUnsetenv(t, "GH_TOKEN")
	if got := envToken(); got != "" {
		t.Fatalf("expected empty token, got %q", got)
	}

	mustSetenv(t, "GH_TOKEN", "gh-env")
	if got := envToken(); got != "gh-env" {
		t.Fatalf("expected gh-env, got %q", got)
	}

	mustSetenv(t, "GITHUB_TOKEN", "github-env")
	if got := envToken(); got != "github-env" {
		t.Fatalf("expected github-env precedence, got %q", got)
	}
}

func TestResolveTokenAutoFallback(t *testing.T) {
	oldGitHub, hadGitHub := os.LookupEnv("GITHUB_TOKEN")
	oldGH, hadGH := os.LookupEnv("GH_TOKEN")
	oldRunAuth := runGitHubCLIAuthToken
	defer func() {
		restoreEnv(t, "GITHUB_TOKEN", oldGitHub, hadGitHub)
		restoreEnv(t, "GH_TOKEN", oldGH, hadGH)
		runGitHubCLIAuthToken = oldRunAuth
	}()

	mustUnsetenv(t, "GITHUB_TOKEN")
	mustUnsetenv(t, "GH_TOKEN")

	// Auto mode with no env falls through to gh CLI failure.
	runGitHubCLIAuthToken = func(ctx context.Context) (string, error) {
		return "", errors.New("gh failed")
	}
	if _, err := resolveToken(context.Background(), AuthModeAuto); err == nil ||
		!strings.Contains(err.Error(), "auto mode") {
		t.Fatalf("expected auto-mode failure, got %v", err)
	}

	// Auto mode with successful gh CLI fallback.
	runGitHubCLIAuthToken = func(ctx context.Context) (string, error) {
		return "cli-token", nil
	}
	tok, err := resolveToken(context.Background(), AuthModeAuto)
	if err != nil || tok != "cli-token" {
		t.Fatalf("expected cli-token, got %q %v", tok, err)
	}
}

func TestFetchReposEmptyOwner(t *testing.T) {
	if _, err := FetchRepos(context.Background(), "", 5); err == nil ||
		!strings.Contains(err.Error(), "owner must not be empty") {
		t.Fatalf("expected empty owner error, got %v", err)
	}
}

func TestFetchReposWithClientOptionsGuards(t *testing.T) {
	// Nil client guard.
	if _, err := FetchReposWithClientOptions(context.Background(), nil, "owner", FetchOptions{}); err == nil ||
		!strings.Contains(err.Error(), "github client must not be nil") {
		t.Fatalf("expected nil client error, got %v", err)
	}
	// Empty owner guard.
	client := newTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return jsonResp(req, http.StatusOK, `{}`, nil), nil
	}))
	if _, err := FetchReposWithClientOptions(context.Background(), client, "  ", FetchOptions{}); err == nil ||
		!strings.Contains(err.Error(), "owner must not be empty") {
		t.Fatalf("expected empty owner error, got %v", err)
	}
}

func TestFetchReposSearch(t *testing.T) {
	client := newTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/search/repositories") {
			body := `{
				"total_count": 2,
				"incomplete_results": false,
				"items": [
					{"id": 1, "name": "repo1", "clone_url": "https://github.com/owner/repo1", "visibility": "public"},
					{"id": 2, "name": "repo2", "clone_url": "https://github.com/owner/repo2", "visibility": "public"}
				]
			}`
			return jsonResp(req, http.StatusOK, body, nil), nil
		}
		return jsonResp(req, http.StatusBadRequest, `{}`, nil), nil
	}))

	repos, err := FetchReposWithClientOptions(context.Background(), client, "topic:ai", FetchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("expected search to succeed, got %v", err)
	}
	if len(repos) != 2 {
		t.Errorf("expected 2 repos, got %d", len(repos))
	}
	if repos[0].Name != "repo1" || repos[1].Name != "repo2" {
		t.Errorf("expected repo1 and repo2, got %+v", repos)
	}
}

func TestFetchReposTypeAndSort(t *testing.T) {
	client := newTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{
			"total_count": 3,
			"items": [
				{"id": 1, "name": "c-repo", "stargazers_count": 10, "fork": false, "archived": false, "pushed_at": "2026-06-29T10:00:00Z"},
				{"id": 2, "name": "a-repo", "stargazers_count": 50, "fork": true, "archived": false, "pushed_at": "2026-06-29T12:00:00Z"},
				{"id": 3, "name": "b-repo", "stargazers_count": 30, "fork": false, "archived": true, "pushed_at": "2026-06-29T11:00:00Z"}
			]
		}`
		return jsonResp(req, http.StatusOK, body, nil), nil
	}))

	// Test type filter (sources - filters out forks)
	repos, err := FetchReposWithClientOptions(context.Background(), client, "topic:ai", FetchOptions{Type: "sources"})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(repos) != 1 || repos[0].Name != "c-repo" {
		t.Errorf("expected only c-repo, got %+v", repos)
	}

	// Test sort by stars (descending)
	repos, err = FetchReposWithClientOptions(context.Background(), client, "topic:ai", FetchOptions{
		Type:            "all",
		Sort:            "stars",
		IncludeArchived: true,
		IncludeForks:    true,
	})
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if len(repos) != 3 {
		t.Fatalf("expected 3 repos, got %d", len(repos))
	}
	if repos[0].Name != "a-repo" || repos[1].Name != "b-repo" || repos[2].Name != "c-repo" {
		t.Errorf("expected order: a-repo, b-repo, c-repo; got %+v", repos)
	}
}

func TestRunGitHubCLIAuthToken(t *testing.T) {
	if runtime.GOOS == "windows" {
		// The fixture installs a POSIX shell script named "gh" on PATH, which
		// Windows cannot execute (it requires a .exe/.bat/.cmd). The closure is
		// fully covered on the POSIX CI runners.
		t.Skip("fake gh PATH executable fixture is POSIX-specific")
	}

	// Snapshot the real default implementation so other tests that swap the
	// package var cannot interfere with this one.
	realRun := runGitHubCLIAuthToken

	// Error path: a fake gh that exits non-zero.
	dirErr := t.TempDir()
	writeFakeGH(t, dirErr, "#!/bin/sh\nexit 1\n")
	withPath(t, dirErr, func() {
		if _, err := realRun(context.Background()); err == nil ||
			!strings.Contains(err.Error(), "gh auth token failed") {
			t.Fatalf("expected gh failure error, got %v", err)
		}
	})

	// Success path: a fake gh that prints a token.
	dirOK := t.TempDir()
	writeFakeGH(t, dirOK, "#!/bin/sh\necho '  my-token  '\n")
	withPath(t, dirOK, func() {
		tok, err := realRun(context.Background())
		if err != nil || tok != "my-token" {
			t.Fatalf("expected trimmed my-token, got %q %v", tok, err)
		}
	})

	// Empty-token path: a fake gh that prints only whitespace.
	dirEmpty := t.TempDir()
	writeFakeGH(t, dirEmpty, "#!/bin/sh\necho '   '\n")
	withPath(t, dirEmpty, func() {
		if _, err := realRun(context.Background()); err == nil ||
			!strings.Contains(err.Error(), "empty token") {
			t.Fatalf("expected empty token error, got %v", err)
		}
	})
}

func writeFakeGH(t *testing.T, dir, script string) {
	t.Helper()
	path := filepath.Join(dir, "gh")
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { //nolint:gosec // test fixture
		t.Fatalf("write fake gh: %v", err)
	}
}

func withPath(t *testing.T, dir string, fn func()) {
	t.Helper()
	oldPath, hadPath := os.LookupEnv("PATH")
	mustSetenv(t, "PATH", dir)
	defer restoreEnv(t, "PATH", oldPath, hadPath)
	fn()
}

func TestResolveTokenModeToken(t *testing.T) {
	oldGitHub, hadGitHub := os.LookupEnv("GITHUB_TOKEN")
	defer restoreEnv(t, "GITHUB_TOKEN", oldGitHub, hadGitHub)
	mustSetenv(t, "GITHUB_TOKEN", "token-mode")
	tok, err := resolveToken(context.Background(), AuthModeToken)
	if err != nil || tok != "token-mode" {
		t.Fatalf("expected token-mode, got %q %v", tok, err)
	}
}

func TestFetchReposWithOptionsTokenError(t *testing.T) {
	oldGitHub, hadGitHub := os.LookupEnv("GITHUB_TOKEN")
	oldGH, hadGH := os.LookupEnv("GH_TOKEN")
	defer func() {
		restoreEnv(t, "GITHUB_TOKEN", oldGitHub, hadGitHub)
		restoreEnv(t, "GH_TOKEN", oldGH, hadGH)
	}()
	mustUnsetenv(t, "GITHUB_TOKEN")
	mustUnsetenv(t, "GH_TOKEN")

	if _, err := FetchReposWithOptions(context.Background(), "someowner",
		FetchOptions{Limit: 5, AuthMode: AuthModeToken}); err == nil ||
		!strings.Contains(err.Error(), "GITHUB_TOKEN") {
		t.Fatalf("expected token resolution error, got %v", err)
	}
}

func TestFetchReposWithOptionsRequestPath(t *testing.T) {
	// Drive the full FetchReposWithOptions path (token resolution, client
	// construction, retryTransport, and the ctx == nil branch) without any real
	// network by swapping http.DefaultTransport for a stub that fails fast.
	oldGitHub, hadGitHub := os.LookupEnv("GITHUB_TOKEN")
	oldRunAuth := runGitHubCLIAuthToken
	oldDefault := http.DefaultTransport
	defer func() {
		restoreEnv(t, "GITHUB_TOKEN", oldGitHub, hadGitHub)
		runGitHubCLIAuthToken = oldRunAuth
		http.DefaultTransport = oldDefault
	}()
	mustSetenv(t, "GITHUB_TOKEN", "env-token")

	http.DefaultTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("stubbed transport failure")
	})

	// Pass a nil context to exercise the ctx == nil branch, with zero retries
	// and tiny backoff so it returns immediately.
	opts := FetchOptions{
		Limit:           1,
		RetryMax:        -1,
		RetryMinBackoff: time.Millisecond,
		RetryMaxBackoff: time.Millisecond,
	}
	// A typed nil context exercises the ctx == nil guard without tripping
	// staticcheck's SA1012 (which only flags an untyped nil literal).
	var nilCtx context.Context
	if _, err := FetchReposWithOptions(nilCtx, "someowner", opts); err == nil {
		t.Fatalf("expected request error from stubbed transport")
	}
}

func TestNormalizeFetchOptions(t *testing.T) {
	// Negative limit -> 0, empty visibility -> all, invalid visibility -> all,
	// negative retry -> 0, retry backoff coercion (max < min).
	got := normalizeFetchOptions(FetchOptions{
		Limit:           -1,
		Visibility:      "weird",
		RetryMax:        -5,
		RetryMinBackoff: 10 * time.Millisecond,
		RetryMaxBackoff: time.Millisecond, // less than min -> bumped to min
	})
	if got.Limit != 0 {
		t.Fatalf("expected limit 0, got %d", got.Limit)
	}
	if got.Visibility != "all" {
		t.Fatalf("expected visibility all, got %q", got.Visibility)
	}
	if got.RetryMax != 0 {
		t.Fatalf("expected RetryMax 0, got %d", got.RetryMax)
	}
	if got.RetryMaxBackoff != got.RetryMinBackoff {
		t.Fatalf("expected max backoff bumped to min, got %v", got.RetryMaxBackoff)
	}

	// RetryMax == 0 -> default; default backoffs applied.
	def := normalizeFetchOptions(FetchOptions{Visibility: "PUBLIC"})
	if def.RetryMax != defaultRetryMax {
		t.Fatalf("expected default RetryMax, got %d", def.RetryMax)
	}
	if def.Visibility != "public" {
		t.Fatalf("expected lowercased visibility, got %q", def.Visibility)
	}
	if def.RetryMinBackoff != defaultRetryMinBackoff || def.RetryMaxBackoff != defaultRetryMaxBackoff {
		t.Fatalf("expected default backoffs, got %v %v", def.RetryMinBackoff, def.RetryMaxBackoff)
	}
}

func TestOrgTypeForVisibility(t *testing.T) {
	cases := map[string]string{
		"public":  "public",
		"private": "private",
		"all":     "all",
		"other":   "all",
	}
	for in, want := range cases {
		if got := orgTypeForVisibility(in); got != want {
			t.Fatalf("orgTypeForVisibility(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToLookupSet(t *testing.T) {
	if got := toLookupSet(nil); got != nil {
		t.Fatalf("expected nil for empty input")
	}
	// All-blank entries collapse to nil.
	if got := toLookupSet([]string{"", "  "}); got != nil {
		t.Fatalf("expected nil for all-blank input, got %v", got)
	}
	got := toLookupSet([]string{"Go", " Rust ", ""})
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	if _, ok := got["go"]; !ok {
		t.Fatalf("expected normalized 'go' key")
	}
	if _, ok := got["rust"]; !ok {
		t.Fatalf("expected trimmed normalized 'rust' key")
	}
}

func TestMatchesFilters(t *testing.T) {
	base := normalizeFetchOptions(FetchOptions{IncludeForks: true, IncludeArchived: true})

	// Fork excluded when IncludeForks false.
	if matchesFilters(Repo{Fork: true}, nil, nil, normalizeFetchOptions(FetchOptions{})) {
		t.Fatalf("expected fork to be filtered out")
	}
	// Archived excluded when IncludeArchived false.
	if matchesFilters(Repo{Archived: true}, nil, nil, normalizeFetchOptions(FetchOptions{IncludeForks: true})) {
		t.Fatalf("expected archived to be filtered out")
	}
	// Visibility public mismatch.
	pubOpts := base
	pubOpts.Visibility = "public"
	if matchesFilters(Repo{Visibility: "Private"}, nil, nil, pubOpts) {
		t.Fatalf("expected private repo filtered when visibility public")
	}
	// Visibility private mismatch.
	privOpts := base
	privOpts.Visibility = "private"
	if matchesFilters(Repo{Visibility: "Public"}, nil, nil, privOpts) {
		t.Fatalf("expected public repo filtered when visibility private")
	}
	// Include language mismatch.
	if matchesFilters(Repo{Language: "Go"}, toLookupSet([]string{"rust"}), nil, base) {
		t.Fatalf("expected non-matching include language to filter out")
	}
	// Exclude language match.
	if matchesFilters(Repo{Language: "Go"}, nil, toLookupSet([]string{"go"}), base) {
		t.Fatalf("expected excluded language to filter out")
	}
	// Passing case.
	if !matchesFilters(Repo{Language: "Go", Visibility: "Public"},
		toLookupSet([]string{"go"}), toLookupSet([]string{"rust"}), base) {
		t.Fatalf("expected repo to pass filters")
	}
}

// TestMatchesFiltersTypeSwitch covers the opts.Type switch branch that
// TestMatchesFilters didn't reach: sources / forks / archived / mirrors
// / templates / can-be-sponsored / public / private aliases. Without
// this the function sat at ~56% coverage despite being on every
// fetched repo's hot path.
func TestMatchesFiltersTypeSwitch(t *testing.T) {
	base := normalizeFetchOptions(FetchOptions{IncludeForks: true, IncludeArchived: true})
	// Ready-made repo variants for each Type branch.
	fork := Repo{Visibility: "Public", Fork: true}
	src := Repo{Visibility: "Public"}
	arch := Repo{Visibility: "Public", Archived: true}
	spons := Repo{Visibility: "Public", CanBeSponsored: true}
	mirr := Repo{Visibility: "Public", IsMirror: true}
	tmpl := Repo{Visibility: "Public", IsTemplate: true}
	priv := Repo{Visibility: "Private"}

	cases := []struct {
		name    string
		repo    Repo
		typeArg string
		want    bool
	}{
		// public / private aliases inside Type — they short-circuit
		// separately from the top-level Visibility filter.
		{"type=public matches Public", src, "public", true},
		{"type=public rejects Private", priv, "public", false},
		{"type=private matches Private", priv, "PRIVATE", true},
		{"type=private rejects Public", src, "private", false},
		// sources = "not a fork"
		{"type=sources rejects forks", fork, "sources", false},
		{"type=sources accepts non-forks", src, "sources", true},
		// forks = "is a fork"
		{"type=forks rejects non-forks", src, "forks", false},
		{"type=forks accepts forks", fork, "forks", true},
		// archived
		{"type=archived rejects live repos", src, "archived", false},
		{"type=archived accepts archived", arch, "archived", true},
		// sponsored (two synonymous keys)
		{"type='can be sponsored' rejects non-sponsorable", src, "can be sponsored", false},
		{"type=sponsored accepts sponsorable", spons, "sponsored", true},
		// mirrors
		{"type=mirrors rejects non-mirror", src, "mirrors", false},
		{"type=mirrors accepts mirror", mirr, "mirrors", true},
		// templates
		{"type=templates rejects non-template", src, "templates", false},
		{"type=templates accepts template", tmpl, "templates", true},
		// unknown type falls through: everything passes the Type gate.
		{"type=unknown falls through", src, "gibberish", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := base
			opts.Type = tc.typeArg
			got := matchesFilters(tc.repo, nil, nil, opts)
			if got != tc.want {
				t.Errorf("matchesFilters(%+v, type=%q) = %v, want %v", tc.repo, tc.typeArg, got, tc.want)
			}
		})
	}
}

func restoreEnv(t *testing.T, key, val string, had bool) {
	t.Helper()
	if had {
		if err := os.Setenv(key, val); err != nil {
			t.Fatalf("restore %s: %v", key, err)
		}
		return
	}
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
}

func mustSetenv(t *testing.T, key, val string) {
	t.Helper()
	if err := os.Setenv(key, val); err != nil {
		t.Fatalf("setenv %s: %v", key, err)
	}
}

func mustUnsetenv(t *testing.T, key string) {
	t.Helper()
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unsetenv %s: %v", key, err)
	}
}

func TestFetchReposAuthenticatedUser(t *testing.T) {
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/user":
			// The authenticated user is "me".
			return jsonResp(req, http.StatusOK, `{"login":"me","type":"User"}`, nil), nil
		case "/users/me":
			return jsonResp(req, http.StatusOK, `{"login":"me","type":"User"}`, nil), nil
		case "/user/repos":
			// The authenticated-user endpoint exposes private repositories.
			return jsonResp(req, http.StatusOK, `[
				{"name":"secret","language":"Go","visibility":"private","default_branch":"main"}
			]`, nil), nil
		case "/users/other":
			return jsonResp(req, http.StatusOK, `{"login":"other","type":"User"}`, nil), nil
		case "/users/other/repos":
			return jsonResp(req, http.StatusOK, `[
				{"name":"public-only","language":"Go","visibility":"public","default_branch":"main"}
			]`, nil), nil
		default:
			return nil, fmt.Errorf("unexpected path: %s", req.URL.Path)
		}
	})
	client := newTestClient(rt)

	// Owner is the authenticated user: list via /user/repos so private repos appear.
	repos, err := FetchReposWithClientOptions(context.Background(), client, "me", FetchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repos) != 1 || repos[0].Name != "secret" || repos[0].Visibility != "Private" {
		t.Fatalf("expected the authenticated user's private repo, got %+v", repos)
	}

	// Owner differs from the authenticated user: fall back to the public listing.
	repos, err = FetchReposWithClientOptions(context.Background(), client, "other", FetchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repos) != 1 || repos[0].Name != "public-only" {
		t.Fatalf("expected fallback to public listing, got %+v", repos)
	}
}

func TestToken(t *testing.T) {
	oldGitHub, hadGitHub := os.LookupEnv("GITHUB_TOKEN")
	oldGH, hadGH := os.LookupEnv("GH_TOKEN")
	defer func() {
		restoreEnv(t, "GITHUB_TOKEN", oldGitHub, hadGitHub)
		restoreEnv(t, "GH_TOKEN", oldGH, hadGH)
	}()

	mustSetenv(t, "GITHUB_TOKEN", "tok-123")
	if got := Token(context.Background(), AuthModeToken); got != "tok-123" {
		t.Errorf("Token() = %q, want tok-123", got)
	}

	mustUnsetenv(t, "GITHUB_TOKEN")
	mustUnsetenv(t, "GH_TOKEN")
	if got := Token(context.Background(), AuthModeToken); got != "" {
		t.Errorf("Token() with no credentials = %q, want empty", got)
	}
}

// TestDescribeOwnerLookupErrorRewrites404 covers the message a user sees after
// mistyping an owner — the single most likely way this command fails.
//
// The raw go-github text was:
//
//	failed to get user/org 'sebastienrouseau': GET https://api.github.com/users/sebastienrouseau: 404 Not Found []
//
// which leaks the transport, ends in an empty bracket pair, and offers no
// remedy for what is almost always a one-character typo.
func TestDescribeOwnerLookupErrorRewrites404(t *testing.T) {
	// A transport that 404s the mistyped owner and returns a login for the
	// authenticated-user lookup, mirroring the real API.
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/users/sebastienrouseau":
			return jsonResp(req, http.StatusNotFound, `{"message":"Not Found"}`, nil), nil
		case "/user", "/users/":
			return jsonResp(req, http.StatusOK, `{"login":"sebastienrousseau","type":"User"}`, nil), nil
		}
		return jsonResp(req, http.StatusNotFound, `{"message":"Not Found"}`, nil), nil
	})
	client := newTestClient(rt)

	_, err := FetchReposWithClientOptions(context.Background(), client, "sebastienrouseau", FetchOptions{})
	if err == nil {
		t.Fatal("expected an error for a nonexistent owner")
	}
	got := err.Error()

	if !strings.Contains(got, `no GitHub user or organisation named "sebastienrouseau"`) {
		t.Errorf("error should say plainly that the owner does not exist, got:\n%s", got)
	}
	if !strings.Contains(got, "sebastienrousseau") {
		t.Errorf("error should suggest the near-miss login, got:\n%s", got)
	}
	// The noise the old message carried must be gone.
	for _, noise := range []string{"api.github.com", "404 Not Found", "[]", "failed to get user/org"} {
		if strings.Contains(got, noise) {
			t.Errorf("error still leaks %q:\n%s", noise, got)
		}
	}
}

// A 404 with no close match falls back to spelling plus an auth hint, because a
// private organisation the credentials cannot see also 404s.
func TestDescribeOwnerLookupError404NoSuggestion(t *testing.T) {
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/user" || req.URL.Path == "/users/" {
			return jsonResp(req, http.StatusOK, `{"login":"someone-entirely-different","type":"User"}`, nil), nil
		}
		return jsonResp(req, http.StatusNotFound, `{"message":"Not Found"}`, nil), nil
	})
	client := newTestClient(rt)

	_, err := FetchReposWithClientOptions(context.Background(), client, "acme-private", FetchOptions{})
	if err == nil {
		t.Fatal("expected an error")
	}
	got := err.Error()
	if !strings.Contains(got, "Check the spelling") || !strings.Contains(got, "gh auth status") {
		t.Errorf("expected spelling + auth guidance, got:\n%s", got)
	}
	if strings.Contains(got, "Did you mean") {
		t.Errorf("must not suggest an unrelated login:\n%s", got)
	}
}

// Non-404 failures keep their original text: a rate limit or a network error is
// already the actionable part and must not be reworded into a spelling hint.
func TestDescribeOwnerLookupErrorPreservesNon404(t *testing.T) {
	rt := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return jsonResp(req, http.StatusInternalServerError, `{"message":"upstream exploded"}`, nil), nil
	})
	client := newTestClient(rt)

	_, err := FetchReposWithClientOptions(context.Background(), client, "acme", FetchOptions{})
	if err == nil {
		t.Fatal("expected an error")
	}
	got := err.Error()
	if strings.Contains(got, "Check the spelling") {
		t.Errorf("a 500 must not be reported as a typo:\n%s", got)
	}
	if !strings.Contains(got, "cannot look up") {
		t.Errorf("expected the lookup wrapper, got:\n%s", got)
	}
}

func TestLevenshtein(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"a", "", 1},
		{"sebastienrouseau", "sebastienrousseau", 1},
		{"kitten", "sitting", 3},
		{"café", "cafe", 1}, // multi-byte: distance is in runes, not bytes
	}
	for _, tc := range cases {
		if got := levenshtein(tc.a, tc.b); got != tc.want {
			t.Errorf("levenshtein(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestMapRepositoryFieldsSurviveMajorUpgrades pins every field corral reads out
// of go-github, decoded from a realistic API payload through go-github's own
// struct tags.
//
// This exists because of the v74 -> v90 jump: sixteen majors of a generated API
// client is exactly where a renamed JSON tag or a retyped field silently starts
// yielding zero values. Most such regressions would not fail a build — corral
// would simply start reporting every repository as language "Other", visibility
// "Public", or with a zero PushedAt, which disables smart-sync by making every
// repo look never-synced. A full sync of every repository looks like working
// software, which is why this needs an explicit assertion rather than an
// end-to-end smoke test.
func TestMapRepositoryFieldsSurviveMajorUpgrades(t *testing.T) {
	const payload = `{
		"id": 1296269,
		"name": "corral",
		"full_name": "sebastienrousseau/corralctl",
		"owner": {"login": "sebastienrousseau"},
		"language": "Go",
		"visibility": "private",
		"default_branch": "trunk",
		"clone_url": "https://github.com/sebastienrousseau/corralctl.git",
		"ssh_url": "git@github.com:sebastienrousseau/corralctl.git",
		"fork": true,
		"archived": true,
		"is_template": true,
		"mirror_url": "https://example.com/mirror",
		"stargazers_count": 42,
		"pushed_at": "2026-08-18T10:11:12Z"
	}`

	var raw gh.Repository
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		t.Fatalf("go-github failed to decode a standard repository payload: %v", err)
	}
	got := mapRepository(&raw)

	wantPushed := time.Date(2026, 8, 18, 10, 11, 12, 0, time.UTC)
	checks := []struct {
		field string
		got   any
		want  any
	}{
		{"ID", got.ID, int64(1296269)},
		{"Name", got.Name, "corral"},
		{"FullName", got.FullName, "sebastienrousseau/corralctl"},
		{"Owner", got.Owner, "sebastienrousseau"},
		{"Language", got.Language, "Go"},
		{"Visibility", got.Visibility, "Private"},
		{"DefaultBranch", got.DefaultBranch, "trunk"},
		{"CloneURL", got.CloneURL, "https://github.com/sebastienrousseau/corralctl.git"},
		{"SSHURL", got.SSHURL, "git@github.com:sebastienrousseau/corralctl.git"},
		{"Fork", got.Fork, true},
		{"Archived", got.Archived, true},
		{"IsTemplate", got.IsTemplate, true},
		{"Stars", got.Stars, 42},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.field, c.got, c.want)
		}
	}
	if !got.PushedAt.Equal(wantPushed) {
		// Called out separately: a zero PushedAt does not fail anything
		// visibly, it just silently disables smart-sync.
		t.Errorf("PushedAt = %v, want %v — a zero value here disables smart-sync", got.PushedAt, wantPushed)
	}
	if !got.IsMirror {
		t.Error("IsMirror = false, want true (mirror_url was set)")
	}
}

// The defaults matter as much as the mappings: an absent language must become
// "Other" and an absent visibility must not silently mark a repo Private.
func TestMapRepositoryDefaultsForAbsentFields(t *testing.T) {
	var raw gh.Repository
	if err := json.Unmarshal([]byte(`{"name":"minimal","owner":{"login":"o"}}`), &raw); err != nil {
		t.Fatal(err)
	}
	got := mapRepository(&raw)
	if got.Language != "Other" {
		t.Errorf("Language = %q, want %q", got.Language, "Other")
	}
	if got.Visibility != "Public" {
		t.Errorf("Visibility = %q, want %q", got.Visibility, "Public")
	}
	if got.DefaultBranch != "main" {
		t.Errorf("DefaultBranch = %q, want %q", got.DefaultBranch, "main")
	}
	if got.FullName != "o/minimal" {
		t.Errorf("FullName = %q, want it synthesised as %q", got.FullName, "o/minimal")
	}
	if !got.PushedAt.IsZero() {
		t.Errorf("PushedAt = %v, want zero when absent", got.PushedAt)
	}
}
