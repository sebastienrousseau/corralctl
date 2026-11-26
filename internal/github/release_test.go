// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package github

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	gh "github.com/google/go-github/v90/github"
)

// route is one canned answer: status and body for "METHOD /path".
type route struct {
	status int
	body   string
}

// releaseAPI answers from routes; an unknown request fails the test.
// It records each request's method, path, query and body in seen.
func releaseAPI(t *testing.T, routes map[string]route, seen *[]string) http.RoundTripper {
	t.Helper()
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		key := req.Method + " " + req.URL.Path
		body := ""
		if req.Body != nil {
			b, _ := io.ReadAll(req.Body)
			body = string(b)
		}
		if seen != nil {
			*seen = append(*seen, key+"?"+req.URL.RawQuery+" "+strings.TrimSpace(body))
		}
		r, ok := routes[key]
		if !ok {
			t.Errorf("unexpected request %s", key)
			return jsonResp(req, http.StatusNotFound, `{"message":"Not Found"}`, nil), nil
		}
		return jsonResp(req, r.status, r.body, nil), nil
	})
}

var bg = context.Background()

func TestReadReleaseStatePicksTheHighestReleaseTag(t *testing.T) {
	client := newTestClient(releaseAPI(t, map[string]route{
		"GET /repos/o/r": {200, `{"default_branch": "master"}`},
		"GET /repos/o/r/git/matching-refs/tags/v": {200, `[
			{"ref": "refs/tags/v0.0.9"}, {"ref": "refs/tags/v0.0.10"},
			{"ref": "refs/tags/v0.0.2"}, {"ref": "refs/tags/vnext"}, {"ref": "refs/tags/v1.0.0-rc1"}]`},
	}, nil))
	got, err := ReadReleaseState(bg, client, "o", "r")
	if err != nil || got != (ReleaseState{DefaultBranch: "master", LatestTag: "v0.0.10"}) {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestReadReleaseStateWithNoTag(t *testing.T) {
	client := newTestClient(releaseAPI(t, map[string]route{
		"GET /repos/o/r": {200, `{"default_branch": "main"}`},
		"GET /repos/o/r/git/matching-refs/tags/v": {200, `[]`},
	}, nil))
	got, err := ReadReleaseState(bg, client, "o", "r")
	if err != nil || got.LatestTag != "" {
		t.Errorf("got %+v, %v", got, err)
	}
}

func TestNewestTagComparesEveryComponent(t *testing.T) {
	refs := func(tags ...string) (out []*gh.Reference) {
		for _, tag := range tags {
			r := "refs/tags/" + tag
			out = append(out, &gh.Reference{Ref: &r})
		}
		return out
	}
	cases := map[string][]string{
		"v1.0.0": {"v0.9.9", "v1.0.0", "v0.10.0"},
		"v0.2.0": {"v0.1.9", "v0.2.0", "v0.1.10"},
		"v0.1.3": {"v0.1.2", "v0.1.3", "v0.0.99"},
	}
	for want, tags := range cases {
		if got := newestTag(refs(tags...)); got != want {
			t.Errorf("newestTag(%v) = %q, want %q", tags, got, want)
		}
	}
}

func TestReadReleaseStateNamesWhatFailed(t *testing.T) {
	cases := map[string]map[string]route{
		"read o/r": {"GET /repos/o/r": {500, `{"message":"boom"}`}},
		"list tags in o/r": {
			"GET /repos/o/r": {200, `{"default_branch": "main"}`},
			"GET /repos/o/r/git/matching-refs/tags/v": {500, `{"message":"boom"}`},
		},
	}
	for want, routes := range cases {
		_, err := ReadReleaseState(bg, newTestClient(releaseAPI(t, routes, nil)), "o", "r")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want %q", err, want)
		}
	}
}

func TestBranchExistsTellsMissingFromBroken(t *testing.T) {
	for status, want := range map[int]bool{200: true, 404: false} {
		client := newTestClient(releaseAPI(t, map[string]route{
			"GET /repos/o/r/git/ref/heads/feat/v0.0.58": {status, `{"ref": "refs/heads/feat/v0.0.58"}`},
		}, nil))
		got, err := BranchExists(bg, client, "o", "r", "feat/v0.0.58")
		if err != nil || got != want {
			t.Errorf("status %d: got %v, %v; want %v", status, got, err, want)
		}
	}
	client := newTestClient(releaseAPI(t, map[string]route{
		"GET /repos/o/r/git/ref/heads/feat/v0.0.58": {500, `{"message":"boom"}`},
	}, nil))
	if _, err := BranchExists(bg, client, "o", "r", "feat/v0.0.58"); err == nil || !strings.Contains(err.Error(), "read branch") {
		t.Errorf("a server error is an error, got %v", err)
	}
}

func TestReleasePullRequestsReadsOpenOtherAndMerged(t *testing.T) {
	var seen []string
	api := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		seen = append(seen, req.URL.RawQuery)
		q := req.URL.Query()
		switch {
		case q.Get("state") == "open" && q.Get("page") == "":
			return jsonResp(req, 200, `[{"number": 3, "head": {"ref": "dependabot/x"}},
				{"number": 7, "head": {"ref": "feat/v0.0.60"}}]`,
				map[string]string{"Link": `<https://api.test/repos/o/r/pulls?page=2>; rel="next"`}), nil
		case q.Get("state") == "open":
			return jsonResp(req, 200, `[{"number": 201, "head": {"ref": "feat/v0.0.58"}},
				{"number": 8, "head": {"ref": "feat/v0.0.61"}}]`, nil), nil
		default:
			return jsonResp(req, 200, `[{"number": 150, "head": {"ref": "feat/v0.0.58"}},
				{"number": 151, "head": {"ref": "feat/v0.0.58"}, "merged_at": "2026-11-22T07:40:41Z"}]`, nil), nil
		}
	})
	got, err := ReleasePullRequests(bg, newTestClient(api), "o", "r", "feat/v0.0.58", "main")
	want := ReleasePRs{Open: 201, Other: 7, OtherBranch: "feat/v0.0.60", Merged: 151}
	if err != nil || got != want {
		t.Errorf("got %+v, %v; want %+v", got, err, want)
	}
	if !strings.Contains(seen[0], "base=main") || !strings.Contains(seen[2], "head=o%3Afeat%2Fv0.0.58") || !strings.Contains(seen[2], "state=closed") {
		t.Errorf("queries = %v", seen)
	}
}

func TestReleasePullRequestsNamesWhatFailed(t *testing.T) {
	for state, want := range map[string]string{"open": "list pull requests into main", "closed": "list closed pull requests from feat/v0.0.58"} {
		api := roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Query().Get("state") == state {
				return jsonResp(req, 500, `{}`, nil), nil
			}
			return jsonResp(req, 200, `[]`, nil), nil
		})
		_, err := ReleasePullRequests(bg, newTestClient(api), "o", "r", "feat/v0.0.58", "main")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("failing %s: err = %v, want %q", state, err, want)
		}
	}
}

func TestCreateBranchStartsAtTheBaseHead(t *testing.T) {
	var seen []string
	routes := map[string]route{
		"GET /repos/o/r/git/ref/heads/main": {200, `{"ref": "refs/heads/main", "object": {"sha": "abc123"}}`},
		"POST /repos/o/r/git/refs":          {201, `{"ref": "refs/heads/feat/v0.0.58"}`},
	}
	if err := CreateBranch(bg, newTestClient(releaseAPI(t, routes, &seen)), "o", "r", "feat/v0.0.58", "main"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen[1], `"ref":"refs/heads/feat/v0.0.58"`) || !strings.Contains(seen[1], `"sha":"abc123"`) {
		t.Errorf("create body = %s", seen[1])
	}
	for path, want := range map[string]string{
		"GET /repos/o/r/git/ref/heads/main": "read branch main",
		"POST /repos/o/r/git/refs":          "create branch feat/v0.0.58",
	} {
		broken := map[string]route{}
		for k, v := range routes {
			broken[k] = v
		}
		broken[path] = route{422, `{"message":"no"}`}
		err := CreateBranch(bg, newTestClient(releaseAPI(t, broken, nil)), "o", "r", "feat/v0.0.58", "main")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("failing %s: err = %v, want %q", path, err, want)
		}
	}
}

func TestMergeBranchReadsEveryOutcome(t *testing.T) {
	cases := []struct {
		status  int
		sha     string
		wantErr string
	}{
		{201, "m1", ""},
		{204, "", ""},
		{409, "", "merge conflict"},
		{500, "", "merge dependabot/x into feat/v0.0.58 in o/r"},
	}
	for _, c := range cases {
		var seen []string
		client := newTestClient(releaseAPI(t, map[string]route{
			"POST /repos/o/r/merges": {c.status, `{"sha": "m1", "message": "x"}`},
		}, &seen))
		sha, err := MergeBranch(bg, client, "o", "r", "feat/v0.0.58", "dependabot/x", "Merge #5\n\nSigned-off-by: A <a@b>")
		if c.wantErr == "" && (err != nil || sha != c.sha) {
			t.Errorf("status %d: got %q, %v; want %q", c.status, sha, err, c.sha)
		}
		if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
			t.Errorf("status %d: err = %v, want %q", c.status, err, c.wantErr)
		}
		if !strings.Contains(seen[0], `"commit_message":"Merge #5\n\nSigned-off-by: A <a@b>"`) {
			t.Errorf("merge body = %s", seen[0])
		}
	}
	client := newTestClient(releaseAPI(t, map[string]route{"POST /repos/o/r/merges": {409, `{}`}}, nil))
	if _, err := MergeBranch(bg, client, "o", "r", "b", "h", "m"); !errors.Is(err, ErrMergeConflict) {
		t.Errorf("a conflict is ErrMergeConflict, got %v", err)
	}
}

func TestCreateDraftPullRequestReturnsItsNumber(t *testing.T) {
	var seen []string
	client := newTestClient(releaseAPI(t, map[string]route{"POST /repos/o/r/pulls": {201, `{"number": 202}`}}, &seen))
	if n, err := CreateDraftPullRequest(bg, client, "o", "r", "feat/v0.0.58", "main", "t", "b"); err != nil || n != 202 {
		t.Errorf("got %d, %v", n, err)
	}
	if !strings.Contains(seen[0], `"draft":true`) {
		t.Errorf("a release pull request opens as a draft: %s", seen[0])
	}
	client = newTestClient(releaseAPI(t, map[string]route{"POST /repos/o/r/pulls": {422, `{}`}}, nil))
	if _, err := CreateDraftPullRequest(bg, client, "o", "r", "feat/v0.0.58", "main", "t", "b"); err == nil {
		t.Error("a refused pull request is an error")
	}
}

func TestClosePullRequestCommentsThenCloses(t *testing.T) {
	var seen []string
	routes := map[string]route{
		"POST /repos/o/r/issues/5/comments": {201, `{}`},
		"PATCH /repos/o/r/pulls/5":          {200, `{}`},
	}
	if err := ClosePullRequest(bg, newTestClient(releaseAPI(t, routes, &seen)), "o", "r", 5, "Folded into #202"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(seen[0], "POST") || !strings.Contains(seen[1], `"state":"closed"`) {
		t.Errorf("requests = %v", seen)
	}
	for path, want := range map[string]string{
		"POST /repos/o/r/issues/5/comments": "comment on o/r#5",
		"PATCH /repos/o/r/pulls/5":          "close o/r#5",
	} {
		broken := map[string]route{}
		for k, v := range routes {
			broken[k] = v
		}
		broken[path] = route{500, `{}`}
		err := ClosePullRequest(bg, newTestClient(releaseAPI(t, broken, nil)), "o", "r", 5, "c")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("failing %s: err = %v, want %q", path, err, want)
		}
	}
}

func TestAPIStatusIsZeroForATransportError(t *testing.T) {
	client := newTestClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	}))
	if _, err := BranchExists(bg, client, "o", "r", "b"); err == nil || apiStatus(err) != 0 {
		t.Errorf("err = %v, status %d; want an error with status 0", err, apiStatus(err))
	}
}
