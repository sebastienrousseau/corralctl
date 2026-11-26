// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gh "github.com/google/go-github/v90/github"
	"github.com/sebastienrousseau/corralctl/internal/deps"
	"github.com/sebastienrousseau/corralctl/internal/github"
)

// stubDeps stands in for GitHub: two repositories, one Dependabot pull
// request each. It restores the seams and flags when the test ends.
func stubDeps(t *testing.T) {
	t.Helper()
	fetch, newClient, list := depsFetchRepos, depsNewClient, depsListPRs
	output, overrides := depsOutput, depsOverrides
	t.Cleanup(func() {
		depsFetchRepos, depsNewClient, depsListPRs = fetch, newClient, list
		depsOutput, depsOverrides = output, overrides
		rootCmd.SetOut(nil)
		rootCmd.SetArgs(nil)
	})
	depsFetchRepos = func(_ context.Context, owner string, opts github.FetchOptions) ([]github.Repo, error) {
		if opts.IncludeForks || opts.IncludeArchived {
			t.Errorf("forks and archived repositories are excluded by default: %+v", opts)
		}
		return []github.Repo{{Owner: owner, Name: "a"}, {Owner: owner, Name: "b"}}, nil
	}
	depsNewClient = func(context.Context, github.FetchOptions) (*gh.Client, error) { return nil, nil }
	depsListPRs = func(_ context.Context, _ *gh.Client, owner, name string) ([]github.PullRequest, error) {
		title := "bump x from 1.0.0 to 1.0.1"
		if name == "b" {
			title = "bump kit from 0.0.8 to 0.0.9"
		}
		return []github.PullRequest{{Repo: owner + "/" + name, Number: 1, Title: title,
			URL:    "https://github.com/" + owner + "/" + name + "/pull/1",
			Branch: "dependabot/npm_and_yarn/x", CI: "success", Mergeable: "clean"}}, nil
	}
}

// runDeps runs `corralctl deps <args>` and returns its stdout.
func runDeps(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetArgs(append([]string{"deps"}, args...))
	err := rootCmd.Execute()
	return out.String(), err
}

func TestDepsInventoryListsEveryPullRequest(t *testing.T) {
	stubDeps(t)
	out, err := runDeps(t, "inventory", "o", "--output", "text")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"o/a#1\tsuccess\tclean\tbump x from 1.0.0 to 1.0.1", "o/b#1", "2 open Dependabot pull request(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("inventory lacks %q:\n%s", want, out)
		}
	}
}

func TestDepsInventoryWritesJSON(t *testing.T) {
	stubDeps(t)
	out, err := runDeps(t, "inventory", "o", "--output", "json")
	if err != nil {
		t.Fatal(err)
	}
	var prs []deps.PR
	if err := json.Unmarshal([]byte(out), &prs); err != nil || len(prs) != 2 || prs[1].Repo != "o/b" {
		t.Errorf("inventory JSON = %v (%v):\n%s", prs, err, out)
	}
}

func TestDepsPlanAppliesOverrides(t *testing.T) {
	stubDeps(t)
	file := filepath.Join(t.TempDir(), "overrides.json")
	if err := os.WriteFile(file, []byte(`[{"package": "kit", "skip": "0.0.9", "use": "0.0.10"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runDeps(t, "plan", "o", "--output", "text", "--overrides", file)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Dependabot triage", "* Tier 1, fold as is: 1", "adopt 0.0.10 on feat/v<next>"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan lacks %q:\n%s", want, out)
		}
	}
}

func TestDepsPlanWritesJSON(t *testing.T) {
	stubDeps(t)
	out, err := runDeps(t, "plan", "o", "--output", "json", "--overrides", "")
	if err != nil {
		t.Fatal(err)
	}
	var p deps.Plan
	if err := json.Unmarshal([]byte(out), &p); err != nil || len(p.Decisions) != 2 {
		t.Errorf("plan JSON = %+v (%v)", p, err)
	}
}

func TestDepsRefusesAnUnknownOutputFormat(t *testing.T) {
	stubDeps(t)
	if _, err := runDeps(t, "inventory", "o", "--output", "yaml"); err == nil || !strings.Contains(err.Error(), "--output must be") {
		t.Errorf("err = %v", err)
	}
}

func TestDepsReportsWhatFailed(t *testing.T) {
	boom := errors.New("boom")
	cases := map[string]func(){
		"listing repositories": func() {
			depsFetchRepos = func(context.Context, string, github.FetchOptions) ([]github.Repo, error) { return nil, boom }
		},
		"building the client": func() {
			depsNewClient = func(context.Context, github.FetchOptions) (*gh.Client, error) { return nil, boom }
		},
		"listing pull requests": func() {
			depsListPRs = func(context.Context, *gh.Client, string, string) ([]github.PullRequest, error) { return nil, boom }
		},
	}
	for name, breakIt := range cases {
		for _, sub := range []string{"inventory", "plan"} {
			stubDeps(t)
			breakIt()
			args := []string{sub, "o", "--output", "text"}
			if sub == "plan" {
				args = append(args, "--overrides", "")
			}
			if _, err := runDeps(t, args...); !errors.Is(err, boom) {
				t.Errorf("%s %s: err = %v, want boom", sub, name, err)
			}
		}
	}
}

func TestDepsPlanRejectsBadOverrides(t *testing.T) {
	stubDeps(t)
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]string{filepath.Join(dir, "missing.json"): "read overrides", bad: "parse overrides"} {
		if _, err := runDeps(t, "plan", "o", "--output", "text", "--overrides", file); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", file, err, want)
		}
	}
}

func TestDepsWritersReportAClosedPipe(t *testing.T) {
	prs := []deps.PR{{Repo: "o/a", Number: 1}}
	if err := writeInventory(errorWriter{}, prs, "text"); err == nil {
		t.Error("a failed row write should be reported")
	}
	if err := writeInventory(errorWriter{}, nil, "text"); err == nil {
		t.Error("a failed summary write should be reported")
	}
}
