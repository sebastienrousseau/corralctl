// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package github

import (
	"net/http"
	"reflect"
	"strings"
	"testing"

	gh "github.com/google/go-github/v90/github"
)

// paged answers a listing's first page with a link to a second.
func paged(t *testing.T, path, page1, page2 string) http.RoundTripper {
	t.Helper()
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path != path {
			t.Errorf("unexpected request %s", req.URL.Path)
			return jsonResp(req, 404, `{}`, nil), nil
		}
		if req.URL.Query().Get("page") == "2" {
			return jsonResp(req, 200, page2, nil), nil
		}
		return jsonResp(req, 200, page1, map[string]string{"Link": `<https://api.test` + path + `?page=2>; rel="next"`}), nil
	})
}

func TestLabeledPullRequestsReadsEveryPage(t *testing.T) {
	api := paged(t, "/search/issues",
		`{"items": [{"number": 202, "title": "chore(release): v0.0.58", "repository_url": "https://api.test/repos/o/a"}]}`,
		`{"items": [{"number": 12, "title": "chore(release): v0.0.10", "repository_url": "https://api.test/repos/o/b"}]}`)
	got, err := LabeledPullRequests(bg, newTestClient(api), "o", "corral-deps")
	want := []LabeledPullRequest{{Repo: "o/a", Number: 202, Title: "chore(release): v0.0.58"}, {Repo: "o/b", Number: 12, Title: "chore(release): v0.0.10"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, %v", got, err)
	}
	client := newTestClient(releaseAPI(t, map[string]route{"GET /search/issues": {422, `{}`}}, nil))
	if _, err := LabeledPullRequests(bg, client, "o", "corral-deps"); err == nil || !strings.Contains(err.Error(), `search "is:pr is:open label:corral-deps user:o"`) {
		t.Errorf("err = %v", err)
	}
	if repoOf("x") != "x" {
		t.Errorf("repoOf of a malformed URL returns it unchanged")
	}
}

func TestPullRequestCommitsReadsEveryPage(t *testing.T) {
	api := paged(t, "/repos/o/r/pulls/202/commits",
		`[{"sha": "a1", "commit": {"message": "Merge #5 into feat/v0.0.58\n\nbody"}, "author": {"login": "web-flow"}, "parents": [{}, {}]}]`,
		`[{"sha": "b2", "commit": {"message": "chore(release): v0.0.58"}, "author": null, "parents": [{}]}]`)
	got, err := PullRequestCommits(bg, newTestClient(api), "o", "r", 202)
	want := []Commit{{SHA: "a1", Subject: "Merge #5 into feat/v0.0.58", Author: "web-flow", Parents: 2}, {SHA: "b2", Subject: "chore(release): v0.0.58", Parents: 1}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, %v", got, err)
	}
	client := newTestClient(releaseAPI(t, map[string]route{"GET /repos/o/r/pulls/202/commits": {500, `{}`}}, nil))
	if _, err := PullRequestCommits(bg, client, "o", "r", 202); err == nil || !strings.Contains(err.Error(), "list commits on o/r#202") {
		t.Errorf("err = %v", err)
	}
}

func TestAddLabel(t *testing.T) {
	var seen []string
	client := newTestClient(releaseAPI(t, map[string]route{"POST /repos/o/r/issues/202/labels": {200, `[]`}}, &seen))
	if err := AddLabel(bg, client, "o", "r", 202, "corral-deps"); err != nil || !strings.Contains(seen[0], `["corral-deps"]`) {
		t.Errorf("err = %v, request %v", err, seen)
	}
	client = newTestClient(releaseAPI(t, map[string]route{"POST /repos/o/r/issues/202/labels": {500, `{}`}}, nil))
	if err := AddLabel(bg, client, "o", "r", 202, "corral-deps"); err == nil || !strings.Contains(err.Error(), "label o/r#202") {
		t.Errorf("err = %v", err)
	}
}

// mergeRoutes is a draft release pull request in a repository that allows
// merge commits.
func mergeRoutes() map[string]route {
	return map[string]route{
		"GET /repos/o/r/pulls/202":       {200, `{"number": 202, "draft": true, "node_id": "PR_n1"}`},
		"POST /graphql":                  {200, `{"data": {"markPullRequestReadyForReview": {"pullRequest": {"isDraft": false}}}}`},
		"GET /repos/o/r":                 {200, `{"allow_merge_commit": true, "allow_squash_merge": true}`},
		"PUT /repos/o/r/pulls/202/merge": {200, `{"merged": true}`},
	}
}

func TestMergeReleasePullRequestReadiesThenMerges(t *testing.T) {
	var seen []string
	if err := MergeReleasePullRequest(bg, newTestClient(releaseAPI(t, mergeRoutes(), &seen)), "o", "r", 202, "chore(release): v0.0.58 (#202)"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 4 || !strings.Contains(seen[1], `"id":"PR_n1"`) || !strings.Contains(seen[1], "markPullRequestReadyForReview") ||
		!strings.Contains(seen[3], `"merge_method":"merge"`) || !strings.Contains(seen[3], `"commit_message":"chore(release): v0.0.58 (#202)"`) {
		t.Errorf("requests:\n%s", strings.Join(seen, "\n"))
	}
	// A pull request that is not a draft goes straight to the merge.
	routes := mergeRoutes()
	routes["GET /repos/o/r/pulls/202"] = route{200, `{"number": 202, "draft": false}`}
	delete(routes, "POST /graphql")
	if err := MergeReleasePullRequest(bg, newTestClient(releaseAPI(t, routes, nil)), "o", "r", 202, "m"); err != nil {
		t.Errorf("non-draft: %v", err)
	}
}

func TestMergeReleasePullRequestNamesWhatFailed(t *testing.T) {
	cases := map[string]route{
		"GET /repos/o/r/pulls/202":       {500, `{}`},
		"POST /graphql":                  {502, `{}`},
		"GET /repos/o/r":                 {500, `{}`},
		"PUT /repos/o/r/pulls/202/merge": {405, `{"message": "Pull Request is not mergeable"}`},
	}
	wants := map[string]string{
		"GET /repos/o/r/pulls/202":       "read o/r#202",
		"POST /graphql":                  "mark o/r#202 ready",
		"GET /repos/o/r":                 "read o/r",
		"PUT /repos/o/r/pulls/202/merge": "merge o/r#202",
	}
	for key, r := range cases {
		routes := mergeRoutes()
		routes[key] = r
		err := MergeReleasePullRequest(bg, newTestClient(releaseAPI(t, routes, nil)), "o", "r", 202, "m")
		if err == nil || !strings.Contains(err.Error(), wants[key]) {
			t.Errorf("failing %s: err = %v, want %q", key, err, wants[key])
		}
	}
	// GraphQL reports a refusal in a 200 response's errors.
	routes := mergeRoutes()
	routes["POST /graphql"] = route{200, `{"errors": [{"message": "Resource not accessible by integration"}]}`}
	if err := MergeReleasePullRequest(bg, newTestClient(releaseAPI(t, routes, nil)), "o", "r", 202, "m"); err == nil || !strings.Contains(err.Error(), "Resource not accessible by integration") {
		t.Errorf("graphql errors: %v", err)
	}
	routes = mergeRoutes()
	routes["GET /repos/o/r"] = route{200, `{}`}
	if err := MergeReleasePullRequest(bg, newTestClient(releaseAPI(t, routes, nil)), "o", "r", 202, "m"); err == nil || !strings.Contains(err.Error(), "allows no merge method") {
		t.Errorf("no method: %v", err)
	}
}

func TestMarkReadyReportsARequestItCannotBuild(t *testing.T) {
	// An endpoint that does not parse is the one way NewRequest fails for
	// this fixed body. The stub transport fails the test if anything is sent.
	client := newTestClient(roundTripFunc(func(req *http.Request) (*http.Response, error) {
		t.Errorf("nothing should be sent, got %s", req.URL)
		return jsonResp(req, 500, `{}`, nil), nil
	}))
	if err := markReadyAt(bg, client, "%zz", "PR_n1"); err == nil {
		t.Error("a request that cannot be built should be an error")
	}
}

func TestMergeMethodPrefersAMergeCommit(t *testing.T) {
	yes := true
	cases := map[string]*gh.Repository{
		"merge":  {AllowMergeCommit: &yes, AllowSquashMerge: &yes, AllowRebaseMerge: &yes},
		"squash": {AllowSquashMerge: &yes, AllowRebaseMerge: &yes},
		"rebase": {AllowRebaseMerge: &yes},
	}
	for want, repo := range cases {
		if got, err := mergeMethod(repo); err != nil || got != want {
			t.Errorf("got %q, %v; want %q", got, err, want)
		}
	}
}

func TestGraphqlURLFollowsTheRESTBase(t *testing.T) {
	cases := map[string]string{
		"https://api.github.com/":  "https://api.github.com/graphql",
		"https://ghe.corp/api/v3/": "https://ghe.corp/api/graphql",
	}
	for base, want := range cases {
		if got := graphqlURL(base); got != want {
			t.Errorf("graphqlURL(%q) = %q, want %q", base, got, want)
		}
	}
}
