// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mirror

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/git"
)

// gitRepoWithOrigin makes a repository whose origin is url, with extra
// `git config` key/value pairs applied.
func gitRepoWithOrigin(t *testing.T, url string, config ...[2]string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) // #nosec G204 -- fixed fixture arguments
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("remote", "add", "origin", url)
	for _, kv := range config {
		run("config", kv[0], kv[1])
	}
	return dir
}

// guardSkips runs a mirror of the repository at path into github.com and
// reports whether the origin guard stopped it.
func guardSkips(t *testing.T, path string) bool {
	t.Helper()
	gh := &fakeTarget{login: "me", remote: okRemote()}
	report, results := collect()
	Run(context.Background(), Options{
		Repos:        []Repo{{Name: "repo", Path: path, Private: true}},
		Destinations: []Destination{dest("github", "github.com", gh)},
		Timeout:      time.Second, DryRun: true, Report: report,
	})
	r, _ := find(*results, "repo", "github")
	return r.Action == ActionSkip
}

// The guard exists so a clone is never pushed with --prune onto its own
// origin. An origin spelled through an insteadOf rewrite or GitHub's
// port-443 SSH host is still github.com, and must still be refused.
func TestOriginGuardSeesThroughGitAndForgeAliases(t *testing.T) {
	withSeams(t)
	originURL, resolveOrigin = git.RemoteOriginFromConfig, resolvedOrigin

	cases := map[string]string{
		"insteadOf":      gitRepoWithOrigin(t, "work:me/repo.git", [2]string{"url.git@github.com:.insteadOf", "work:"}),
		"ssh.github.com": gitRepoWithOrigin(t, "ssh://git@ssh.github.com:443/me/repo.git"),
		"plain":          gitRepoWithOrigin(t, "git@github.com:me/repo.git"),
	}
	for name, path := range cases {
		if !guardSkips(t, path) {
			t.Errorf("%s: origin is github.com but the guard let the mirror through", name)
		}
	}
	if guardSkips(t, gitRepoWithOrigin(t, "https://gitlab.com/me/repo.git")) {
		t.Error("a gitlab.com origin must still mirror to github.com")
	}
}

// An ssh config alias (`Host github-work`, `HostName github.com`) reaches
// github.com, so the guard asks ssh what a host name resolves to.
func TestOriginGuardSeesThroughSSHConfigAliases(t *testing.T) {
	withSeams(t)
	originURL = func(string) (string, error) { return "github-work:me/repo.git", nil }
	sshHostname = func(_ context.Context, host string) string {
		if host == "github-work" {
			return "github.com"
		}
		return host
	}
	if !guardSkips(t, t.TempDir()) {
		t.Error("an ssh alias for github.com must not mirror to github.com")
	}
	originURL = func(string) (string, error) { return "https://github-work/me/repo.git", nil }
	if guardSkips(t, t.TempDir()) {
		t.Error("an https origin is not resolved through the ssh config")
	}
	originURL = func(string) (string, error) { return "/srv/git/repo.git", nil }
	if guardSkips(t, t.TempDir()) {
		t.Error("a local-path origin has no host and must still mirror")
	}
}

// fakeSSH installs a stand-in ssh that prints out for `ssh -G`.
func fakeSSH(t *testing.T, out string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in ssh is a shell script")
	}
	path := filepath.Join(t.TempDir(), "ssh")
	script := "#!/bin/sh\ncat <<'OUT'\n" + out + "\nOUT\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil { // #nosec G306 -- the stand-in ssh must be executable
		t.Fatal(err)
	}
	old := sshBinary
	t.Cleanup(func() { sshBinary = old })
	sshBinary = path
}

func TestSSHConfigHostname(t *testing.T) {
	ctx := context.Background()
	fakeSSH(t, "user git\nhostname GitHub.com\nport 22")
	if got := sshConfigHostname(ctx, "github-work"); got != "github.com" {
		t.Errorf("alias = %q, want github.com", got)
	}
	if got := sshConfigHostname(ctx, "-oProxyCommand=x"); got != "-oProxyCommand=x" {
		t.Errorf("an option-shaped host reached ssh: %q", got)
	}
	fakeSSH(t, "user git")
	if got := sshConfigHostname(ctx, "plain"); got != "plain" {
		t.Errorf("no hostname line = %q, want the host itself", got)
	}
	sshBinary = filepath.Join(t.TempDir(), "missing")
	if got := sshConfigHostname(ctx, "plain"); got != "plain" {
		t.Errorf("ssh failure = %q, want the host itself", got)
	}
}

func TestIsSSHRemote(t *testing.T) {
	for remote, want := range map[string]bool{
		"git@github.com:me/repo.git":       true,
		"ssh://git@github.com/me/repo.git": true,
		"git+ssh://github.com/me/repo.git": true,
		"https://github.com/me/repo.git":   false,
		"file:///srv/me/repo.git":          false,
	} {
		if got := isSSHRemote(remote); got != want {
			t.Errorf("isSSHRemote(%q) = %v, want %v", remote, got, want)
		}
	}
}

// GitLab's and Bitbucket's port-443 hosts fold to the forge, and a
// destination given in capitals still matches.
func TestOriginOnFoldsAliasesAndCase(t *testing.T) {
	hosts := []string{canonicalHost("altssh.gitlab.com"), canonicalHost("altssh.bitbucket.org")}
	for _, dest := range []string{"GitLab.com", "bitbucket.org"} {
		if !originOn(hosts, dest) {
			t.Errorf("%s: alias not folded", dest)
		}
	}
	if originOn(hosts, "github.com") {
		t.Error("github.com is not among the hosts")
	}
}
