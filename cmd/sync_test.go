// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/forge"
	"github.com/sebastienrousseau/corralctl/internal/github"
	"github.com/sebastienrousseau/corralctl/internal/mirror"
)

// captureSyncStdout runs fn with os.Stdout redirected and returns what it
// printed. The printer writes with fmt.Printf and writeJSON(os.Stdout),
// which is the contract stdout carries the selected output format.
func captureSyncStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	os.Stdout = old
	_ = w.Close()
	return <-done
}

func withSyncSeams(t *testing.T) {
	t.Helper()
	oldWalk, oldRun, oldToken := syncWalk, syncRun, syncToken
	oldTo, oldProto, oldConc, oldTimeout, oldOut, oldDry, oldBase := syncTo, syncProtocol, syncConcurrency, syncTimeout, syncOutput, dryRun, baseDir
	t.Cleanup(func() {
		syncWalk, syncRun, syncToken = oldWalk, oldRun, oldToken
		syncTo, syncProtocol, syncConcurrency, syncTimeout, syncOutput, dryRun, baseDir = oldTo, oldProto, oldConc, oldTimeout, oldOut, oldDry, oldBase
	})
	syncTo = []string{"gitlab"}
	syncProtocol, syncConcurrency, syncTimeout, syncOutput = "https", 2, time.Second, "text"
	syncToken = func(_ context.Context, name string, _ github.AuthMode) string { return "tok-" + name }
	syncWalk = func(string) ([]mirror.Repo, error) {
		return []mirror.Repo{{Name: "a", Path: "/a", Private: true}}, nil
	}
	syncRun = func(_ context.Context, opts mirror.Options) mirror.Summary {
		return mirror.Summary{Mirrored: len(opts.Repos) * len(opts.Destinations)}
	}
}

func TestParseDestination(t *testing.T) {
	ok := map[string][3]string{ // spec → name, owner, host
		"gitlab":                          {"gitlab", "", "gitlab.com"},
		" GitLab:my-group ":               {"gitlab", "my-group", "gitlab.com"},
		"github":                          {"github", "", "github.com"},
		"github:org@https://ghe.corp":     {"github", "org", "ghe.corp"},
		"codeberg":                        {"codeberg", "", "codeberg.org"},
		"bitbucket:work":                  {"bitbucket", "work", "bitbucket.org"},
		"gitea@https://git.example.com":   {"gitea", "", "git.example.com"},
		"forgejo:team@http://forge:3000/": {"forgejo", "team", "forge"},
	}
	for raw, want := range ok {
		got, err := parseDestination(raw)
		if err != nil {
			t.Errorf("%q: %v", raw, err)
			continue
		}
		if got.name != want[0] || got.owner != want[1] || got.host != want[2] {
			t.Errorf("%q = %s/%s/%s, want %v", raw, got.name, got.owner, got.host, want)
		}
	}
	bad := []string{"", "   ", "gitub", "gitea", "forgejo:org", "gitlab@ftp://x", "gitlab@https://", "gitlab@://bad", "@https://x"}
	for _, raw := range bad {
		if _, err := parseDestination(raw); err == nil {
			t.Errorf("%q was accepted", raw)
		}
	}
	oldAs := asTarget
	asTarget = func(forge.Forge) (forge.Target, bool) { return nil, false }
	if _, err := parseDestination("gitlab"); err == nil || !strings.Contains(err.Error(), "not mirrored to") {
		t.Fatalf("a list-only forge = %v", err)
	}
	asTarget = oldAs
	if _, err := parseDestinations([]string{"gitlab", "GitLab:other"}); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("duplicate = %v", err)
	}
	if _, err := parseDestinations([]string{"gitlab", "nope"}); err == nil {
		t.Fatal("a bad member must fail the list")
	}
	if specs, err := parseDestinations([]string{"gitlab", "codeberg"}); err != nil || len(specs) != 2 {
		t.Fatalf("list = %v, %v", specs, err)
	}
}

func TestTokenHint(t *testing.T) {
	for name, want := range map[string]string{"github": "GITHUB_TOKEN", "gitlab": "GITLAB_TOKEN", "bitbucket": "BITBUCKET_TOKEN", "gitea": "GITEA_TOKEN", "codeberg": "CODEBERG_TOKEN"} {
		if !strings.Contains(tokenHint(name), want) {
			t.Errorf("hint for %s = %q, want it to name %s", name, tokenHint(name), want)
		}
	}
}

func TestSyncPreRunValidation(t *testing.T) {
	withSyncSeams(t)
	cases := []struct {
		name string
		mut  func()
		want string
	}{
		{"no destinations", func() { syncTo = nil }, "--to"},
		{"protocol", func() { syncProtocol = "ftp" }, "--protocol"},
		{"concurrency", func() { syncConcurrency = 0 }, "--concurrency"},
		{"timeout", func() { syncTimeout = 0 }, "--timeout"},
		{"output", func() { syncOutput = "xml" }, "--output"},
		{"bad destination", func() { syncTo = []string{"gitub"} }, "unknown forge"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withSyncSeams(t)
			tc.mut()
			err := syncCmd.PreRunE(syncCmd, nil)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
	syncTo, syncProtocol, syncOutput = []string{" SSH-Case "}, " SSH ", " NDJSON "
	syncTo = []string{"gitea@https://git.example.com", "gitlab:group"}
	if err := syncCmd.PreRunE(syncCmd, nil); err != nil {
		t.Fatalf("valid flags rejected: %v", err)
	}
	if syncProtocol != "ssh" || syncOutput != "ndjson" {
		t.Fatalf("normalised = %q %q", syncProtocol, syncOutput)
	}
}

func TestSyncRunPassesEverythingThrough(t *testing.T) {
	withSyncSeams(t)
	syncTo = []string{"gitlab:group", "gitea:org@https://git.example.com"}
	syncProtocol = "ssh"
	dryRun = true
	var got mirror.Options
	syncRun = func(_ context.Context, opts mirror.Options) mirror.Summary {
		got = opts
		opts.Report(mirror.Result{Repo: "a", Destination: "gitlab", Action: mirror.ActionDryRun, Message: "would push"})
		opts.Report(mirror.Result{Destination: "gitea", Action: mirror.ActionError, Message: "cannot authenticate"})
		return mirror.Summary{Mirrored: 1, Errors: 1}
	}
	var err error
	out := captureSyncStdout(t, func() { err = syncCmd.RunE(syncCmd, []string{t.TempDir()}) })
	if err == nil || !strings.Contains(err.Error(), "1 mirror operation(s) failed") {
		t.Fatalf("err = %v", err)
	}
	if len(got.Repos) != 1 || len(got.Destinations) != 2 || !got.DryRun || got.Concurrency != 2 || got.Timeout != time.Second {
		t.Fatalf("options = %+v", got)
	}
	gl, gt := got.Destinations[0], got.Destinations[1]
	if gl.Name != "gitlab" || gl.Owner != "group" || gl.Host != "gitlab.com" || gl.Protocol != "ssh" || gl.Options.Token != "tok-gitlab" || gl.Options.BaseURL != "" {
		t.Fatalf("gitlab destination = %+v", gl)
	}
	if gt.Name != "gitea" || gt.Owner != "org" || gt.Host != "git.example.com" || gt.Options.BaseURL != "https://git.example.com" || gt.Options.Token != "tok-gitea" {
		t.Fatalf("gitea destination = %+v", gt)
	}
	if !strings.Contains(out, "DRY-RUN  gitlab     a") || !strings.Contains(out, "ERROR    gitea      -") {
		t.Fatalf("text output = %q", out)
	}
}

func TestSyncRunOutputFormats(t *testing.T) {
	withSyncSeams(t)
	syncRun = func(_ context.Context, opts mirror.Options) mirror.Summary {
		opts.Report(mirror.Result{Repo: "a", Destination: "gitlab", Action: mirror.ActionMirror, Message: "mirrored", URL: "https://gitlab.com/me/a.git"})
		return mirror.Summary{Mirrored: 1}
	}
	syncOutput = "ndjson"
	out := captureSyncStdout(t, func() {
		if err := syncCmd.RunE(syncCmd, nil); err != nil {
			t.Errorf("ndjson: %v", err)
		}
	})
	var line mirror.Result
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &line); err != nil || line.Repo != "a" || line.URL != "https://gitlab.com/me/a.git" {
		t.Fatalf("ndjson = %q: %v", out, err)
	}

	syncOutput = "json"
	out = captureSyncStdout(t, func() {
		if err := syncCmd.RunE(syncCmd, nil); err != nil {
			t.Errorf("json: %v", err)
		}
	})
	var doc struct {
		Results []mirror.Result `json:"results"`
		Summary mirror.Summary  `json:"summary"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || len(doc.Results) != 1 || doc.Summary.Mirrored != 1 {
		t.Fatalf("json = %q: %v", out, err)
	}

	// A JSON document with no results still has a results array, not null.
	syncRun = func(context.Context, mirror.Options) mirror.Summary { return mirror.Summary{} }
	out = captureSyncStdout(t, func() {
		if err := syncCmd.RunE(syncCmd, nil); err != nil {
			t.Errorf("json empty: %v", err)
		}
	})
	if !strings.Contains(out, `"results": []`) {
		t.Fatalf("empty json = %q", out)
	}
}

func TestSyncRunFailures(t *testing.T) {
	withSyncSeams(t)
	syncTo = []string{"gitub"}
	if err := syncCmd.RunE(syncCmd, nil); err == nil {
		t.Fatal("a bad destination must fail the run")
	}
	syncTo = []string{"gitlab"}
	syncToken = func(context.Context, string, github.AuthMode) string { return "" }
	if err := syncCmd.RunE(syncCmd, nil); err == nil || !strings.Contains(err.Error(), "GITLAB_TOKEN") {
		t.Fatalf("missing token = %v", err)
	}
	syncToken = func(context.Context, string, github.AuthMode) string { return "tok" }
	syncWalk = func(string) ([]mirror.Repo, error) { return nil, errors.New("unreadable") }
	if err := syncCmd.RunE(syncCmd, nil); err == nil || !strings.Contains(err.Error(), "scanning") {
		t.Fatalf("walk error = %v", err)
	}
	syncWalk = func(string) ([]mirror.Repo, error) { return nil, nil }
	ran := false
	syncRun = func(context.Context, mirror.Options) mirror.Summary { ran = true; return mirror.Summary{} }
	if err := syncCmd.RunE(syncCmd, nil); err != nil || ran {
		t.Fatalf("empty tree = %v, ran=%v", err, ran)
	}
}

func TestSyncPrinterWriteFailure(t *testing.T) {
	p := newSyncPrinter("json")
	old := os.Stdout
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = f // read-only: the write fails
	t.Cleanup(func() { os.Stdout = old; _ = f.Close() })
	if err := p.finish(mirror.Summary{}); err == nil {
		t.Fatal("expected a write to a read-only stdout to fail")
	}
	if err := writeNDJSON(f, 1); err == nil {
		t.Fatal("expected writeNDJSON to a read-only file to fail")
	}
	// And through the command, so the failure reaches the exit code rather
	// than being swallowed after a successful run.
	withSyncSeams(t)
	syncOutput = "json"
	if err := syncCmd.RunE(syncCmd, nil); err == nil {
		t.Fatal("expected the run to report the unwritable output")
	}
}
