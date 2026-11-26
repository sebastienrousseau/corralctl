// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package github

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// pullsAPI answers the four endpoints DependabotPullRequests reads, from
// canned bodies keyed by path; fail names a path that answers 500.
func pullsAPI(t *testing.T, bodies map[string]string, fail string) http.RoundTripper {
	t.Helper()
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		key := req.URL.Path
		if req.URL.Query().Get("page") == "2" {
			key += "?page=2"
		}
		if key == fail {
			return jsonResp(req, http.StatusInternalServerError, `{"message":"boom"}`, nil), nil
		}
		body, ok := bodies[key]
		if !ok {
			t.Errorf("unexpected request %s", key)
			return jsonResp(req, http.StatusNotFound, `{}`, nil), nil
		}
		headers := map[string]string{}
		if key == "/repos/o/r/pulls" {
			headers["Link"] = `<https://api.test/repos/o/r/pulls?page=2>; rel="next"`
		}
		return jsonResp(req, http.StatusOK, body, headers), nil
	})
}

func fixture() map[string]string {
	return map[string]string{
		"/repos/o/r/pulls": `[
			{"number": 5, "user": {"login": "dependabot[bot]"}},
			{"number": 6, "user": {"login": "someone"}}]`,
		"/repos/o/r/pulls?page=2": `[{"number": 7, "user": {"login": "dependabot[bot]"}}]`,
		"/repos/o/r/pulls/5": `{"number": 5, "title": "bump a from 1.0.0 to 1.0.1",
			"html_url": "https://github.com/o/r/pull/5", "mergeable_state": "clean",
			"head": {"ref": "dependabot/npm_and_yarn/a-1.0.1", "sha": "s5"}}`,
		"/repos/o/r/pulls/7": `{"number": 7, "title": "bump the github-actions group with 2 updates",
			"html_url": "https://github.com/o/r/pull/7", "mergeable_state": "behind",
			"head": {"ref": "dependabot/github_actions/x", "sha": "s7"}}`,
		"/repos/o/r/commits/s5/check-runs": `{"total_count": 1, "check_runs": [{"status": "completed", "conclusion": "success"}]}`,
		"/repos/o/r/commits/s5/status":     `{"state": "success", "statuses": [{"state": "success"}]}`,
		"/repos/o/r/commits/s7/check-runs": `{"total_count": 1, "check_runs": [{"status": "completed", "conclusion": "failure"}]}`,
		"/repos/o/r/commits/s7/status":     `{"state": "success", "statuses": []}`,
	}
}

func TestDependabotPullRequestsReadsEveryPageAndOnlyDependabot(t *testing.T) {
	client := newTestClient(pullsAPI(t, fixture(), ""))
	got, err := DependabotPullRequests(context.Background(), client, "o", "r")
	if err != nil {
		t.Fatal(err)
	}
	want := []PullRequest{
		{Repo: "o/r", Number: 5, Title: "bump a from 1.0.0 to 1.0.1", URL: "https://github.com/o/r/pull/5",
			Branch: "dependabot/npm_and_yarn/a-1.0.1", HeadSHA: "s5", Mergeable: "clean", CI: "success"},
		{Repo: "o/r", Number: 7, Title: "bump the github-actions group with 2 updates", URL: "https://github.com/o/r/pull/7",
			Branch: "dependabot/github_actions/x", HeadSHA: "s7", Mergeable: "behind", CI: "failure"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v\nwant %+v", got, want)
	}
}

func TestDependabotPullRequestsNamesWhatFailed(t *testing.T) {
	cases := map[string]string{
		"/repos/o/r/pulls":                 "list pull requests in o/r",
		"/repos/o/r/pulls/5":               "read o/r#5",
		"/repos/o/r/commits/s5/check-runs": "read checks for o/r#5",
		"/repos/o/r/commits/s5/status":     "read checks for o/r#5",
	}
	for path, want := range cases {
		client := newTestClient(pullsAPI(t, fixture(), path))
		_, err := DependabotPullRequests(context.Background(), client, "o", "r")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("failing %s: err = %v, want it to contain %q", path, err, want)
		}
	}
}

func TestVerdictCombinesRunsAndStatuses(t *testing.T) {
	type run struct{ status, conclusion string }
	cases := []struct {
		name     string
		runs     []run
		statuses []string
		want     string
	}{
		{"nothing", nil, nil, "none"},
		{"all green", []run{{"completed", "success"}, {"completed", "skipped"}}, []string{"success"}, "success"},
		{"neutral counts as green", []run{{"completed", "neutral"}}, nil, "success"},
		{"a run in progress", []run{{"in_progress", ""}}, nil, "pending"},
		{"a pending status", nil, []string{"pending"}, "pending"},
		{"failure beats pending", []run{{"queued", ""}, {"completed", "failure"}}, nil, "failure"},
		{"cancelled", []run{{"completed", "cancelled"}}, nil, "failure"},
		{"timed out", []run{{"completed", "timed_out"}}, nil, "failure"},
		{"action required", []run{{"completed", "action_required"}}, nil, "failure"},
		{"startup failure", []run{{"completed", "startup_failure"}}, nil, "failure"},
		{"status error", nil, []string{"error"}, "failure"},
		{"status failure", []run{{"completed", "success"}}, []string{"failure"}, "failure"},
	}
	for _, c := range cases {
		v := verdict{}
		for _, r := range c.runs {
			v.addRun(r.status, r.conclusion)
		}
		for _, s := range c.statuses {
			v.addStatus(s)
		}
		if got := v.String(); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
