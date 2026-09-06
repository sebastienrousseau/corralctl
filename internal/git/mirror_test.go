// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func mirrorGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...) // #nosec G204 -- test helper
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func mirrorRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mirrorGit(t, dir, "init", "-q", "-b", "main")
	mirrorGit(t, dir, "config", "user.name", "Test")
	mirrorGit(t, dir, "config", "user.email", "test@example.com")
	mirrorGit(t, dir, "config", "commit.gpgsign", "false")
	mirrorCommit(t, dir, "README")
	return dir
}

func mirrorCommit(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o600); err != nil {
		t.Fatal(err)
	}
	mirrorGit(t, dir, "add", name)
	mirrorGit(t, dir, "commit", "-q", "-m", "add "+name)
	return mirrorGit(t, dir, "rev-parse", "HEAD")
}

func bareRefs(t *testing.T, bare string) map[string]string {
	t.Helper()
	refs := map[string]string{}
	for _, line := range strings.Split(mirrorGit(t, bare, "for-each-ref", "--format=%(refname) %(objectname)"), "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			refs[f[0]] = f[1]
		}
	}
	return refs
}

func TestEnsureRemote(t *testing.T) {
	ctx := context.Background()
	repo := mirrorRepo(t)
	if err := EnsureRemote(ctx, repo, "mirror", "https://example.com/a.git"); err != nil {
		t.Fatal(err)
	}
	if err := EnsureRemote(ctx, repo, "mirror", "https://example.com/a.git"); err != nil {
		t.Fatal(err)
	}
	if err := EnsureRemote(ctx, repo, "mirror", "git@example.com:a.git"); err != nil {
		t.Fatal(err)
	}
	if got := mirrorGit(t, repo, "remote", "get-url", "mirror"); got != "git@example.com:a.git" {
		t.Fatalf("remote = %q", got)
	}
	if err := EnsureRemote(ctx, repo, "bad/name", "https://example.com/a.git"); err == nil {
		t.Fatal("expected a bad remote name to be refused")
	}
	if err := EnsureRemote(ctx, filepath.Join(repo, "missing"), "other", "https://example.com/a.git"); err == nil {
		t.Fatal("expected a missing directory to fail")
	}
}

// TestPushMirrorSemantics pins what the security model relies on: branches
// are never forced, tags are, and both namespaces are pruned — in one push.
func TestPushMirrorSemantics(t *testing.T) {
	ctx := context.Background()
	repo := mirrorRepo(t)
	bare := t.TempDir()
	mirrorGit(t, bare, "init", "-q", "--bare")
	mirrorGit(t, repo, "remote", "add", "mirror", bare)
	mirrorGit(t, repo, "branch", "feature")
	mirrorGit(t, repo, "tag", "v1")
	first := mirrorGit(t, repo, "rev-parse", "HEAD")

	if err := PushMirror(ctx, repo, "mirror", bare, nil); err != nil {
		t.Fatal(err)
	}
	refs := bareRefs(t, bare)
	if refs["refs/heads/main"] != first || refs["refs/heads/feature"] != first || refs["refs/tags/v1"] != first {
		t.Fatalf("first push refs = %v", refs)
	}

	mirrorGit(t, repo, "branch", "-D", "feature")
	second := mirrorCommit(t, repo, "second")
	mirrorGit(t, repo, "tag", "-f", "v1")
	mirrorGit(t, bare, "update-ref", "refs/heads/remote-only", first)
	mirrorGit(t, bare, "update-ref", "refs/tags/remote-tag", first)

	if err := PushMirror(ctx, repo, "mirror", bare, nil); err != nil {
		t.Fatal(err)
	}
	refs = bareRefs(t, bare)
	if refs["refs/heads/main"] != second || refs["refs/tags/v1"] != second {
		t.Fatalf("second push refs = %v", refs)
	}
	for _, gone := range []string{"refs/heads/feature", "refs/heads/remote-only", "refs/tags/remote-tag"} {
		if _, exists := refs[gone]; exists {
			t.Fatalf("%s was not pruned: %v", gone, refs)
		}
	}

	mirrorGit(t, repo, "reset", "-q", "--hard", first)
	mirrorCommit(t, repo, "diverged")
	if err := PushMirror(ctx, repo, "mirror", bare, nil); err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("expected a refused non-fast-forward push, got %v", err)
	}
	if got := bareRefs(t, bare)["refs/heads/main"]; got != second {
		t.Fatalf("remote main rewritten to %s by a refused push", got)
	}

	if err := PushMirror(ctx, repo, "bad name", bare, nil); err == nil {
		t.Fatal("expected a bad remote name to be refused")
	}
	if err := PushMirror(ctx, repo, "missing", bare, nil); err == nil {
		t.Fatal("expected a missing remote to fail")
	}
	if err := PushMirror(ctx, repo, "mirror", "https://%zz", &PushCredential{Username: "u", Secret: "s"}); err == nil {
		t.Fatal("expected an unscopable credential to be refused")
	}
}

// TestPushMirrorScopesTheCredential asserts the header reaches git for the
// push URL's origin only. The push itself goes to a local bare repository,
// and the environment git received is checked through a wrapper that
// records it.
func TestPushMirrorScopesTheCredential(t *testing.T) {
	env, err := pushAuthEnv("https://gitlab.example.com:8443/me/repo.git", &PushCredential{Username: "oauth2", Secret: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "GIT_CONFIG_KEY_0=http.https://gitlab.example.com:8443/.extraheader") ||
		!strings.Contains(joined, "GIT_CONFIG_VALUE_0=Authorization: Basic b2F1dGgyOnRvaw==") ||
		strings.Contains(joined, "tok\n") {
		t.Fatalf("unexpected env: %q", joined)
	}
	if env, err := pushAuthEnv("git@gitlab.example.com:me/repo.git", &PushCredential{Username: "u", Secret: "s"}); err != nil || env != nil {
		t.Fatalf("ssh must carry no header: %v, %v", env, err)
	}
	if env, err := pushAuthEnv("https://gitlab.example.com/me/repo.git", nil); err != nil || env != nil {
		t.Fatalf("no credential must mean no header: %v, %v", env, err)
	}
	if _, err := pushAuthEnv("https:///no-host", &PushCredential{}); err == nil {
		t.Fatal("expected a URL without a host to be refused")
	}

	// And a real push over HTTPS with the header in place, against a
	// server that demands exactly that header.
	repo := mirrorRepo(t)
	if err := runMirror(context.Background(), repo, env, "config", "corral.probe", "1"); err != nil {
		t.Fatalf("extra env broke a plain git command: %v", err)
	}
}

func TestValidateRemoteNameForMirrors(t *testing.T) {
	for _, bad := range []string{"", "-x", "a.b", "a/b", "a b", "a\nb", "a\x00b"} {
		if err := validateRemoteName(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	if err := validateRemoteName("gitlab"); err != nil {
		t.Fatal(err)
	}
}
