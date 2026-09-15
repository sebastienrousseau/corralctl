// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package git

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"unicode"
)

// The push side, for `corralctl sync`.
//
// Everything else in this package pulls. These two functions push, and
// they are the only ones that do, so the security model's claim that
// corral never forces a branch can be checked by reading one file.

// mirrorRefspecs is the whole of what a mirror pushes, in one round trip.
//
// Branches are pushed without force: a non-fast-forward is refused and
// surfaced, which is the safety net that stops a stale local clobbering
// the remote. Tags carry the `+` that forces them, because git refuses to
// move an existing tag by default and a tag re-pointed at origin — the
// canonical source — would otherwise fail forever with "already exists".
//
// `--prune` applies per refspec, so a remote branch or tag with no local
// counterpart is deleted. That is what "mirror" means.
var mirrorRefspecs = []string{
	"refs/heads/*:refs/heads/*",
	"+refs/tags/*:refs/tags/*",
}

// PushCredential is an HTTP basic-auth pair git presents when pushing to
// one origin over HTTPS. It reaches git as a scoped http.extraheader in
// the environment, never as part of the URL and never in .git/config.
type PushCredential struct {
	// Username and Secret form the basic-auth pair.
	Username string
	Secret   string
}

// EnsureRemote points the named remote at url, adding it when absent and
// updating it when it exists with a different URL, so the local state
// converges on every run.
func EnsureRemote(ctx context.Context, targetDir, name, url string) error {
	if err := validateRemoteName(name); err != nil {
		return err
	}
	current, err := runGitOutput(ctx, targetDir, "config", "--get", "remote."+name+".url")
	if err == nil {
		if strings.TrimSpace(current) == url {
			return nil
		}
		return runMirror(ctx, targetDir, nil, "remote", "set-url", "--", name, url)
	}
	// `git config --get` exits 1 when the key is absent. Anything else is
	// a real failure and must not be papered over with `remote add`.
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		return err
	}
	return runMirror(ctx, targetDir, nil, "remote", "add", "--", name, url)
}

// PushMirror brings the named remote's branches and tags to parity with
// the local repository in a single push:
//
//	git push --prune --no-verify <remote> refs/heads/*:refs/heads/* +refs/tags/*:refs/tags/*
//
// Branches are never forced, tags are, both namespaces are pruned, and a
// local pre-push hook is skipped so a hook that cannot pass unattended
// cannot block the mirror. pushURL is the remote's URL, used only to
// scope the credential: git is told to send it to that origin and no
// other, so a credential for one forge cannot reach a second.
func PushMirror(ctx context.Context, targetDir, remote, pushURL string, cred *PushCredential) error {
	if err := validateRemoteName(remote); err != nil {
		return err
	}
	env, err := pushAuthEnv(pushURL, cred)
	if err != nil {
		return err
	}
	args := append([]string{"push", "--prune", "--no-verify", remote}, mirrorRefspecs...)
	return runMirror(ctx, targetDir, env, args...)
}

// pushAuthEnv renders a credential as the GIT_CONFIG_* environment that
// injects an Authorization header scoped to pushURL's origin. A nil
// credential, or a non-HTTPS URL, yields nothing: SSH authenticates
// itself.
func pushAuthEnv(pushURL string, cred *PushCredential) ([]string, error) {
	if cred == nil || !strings.HasPrefix(pushURL, "https://") {
		return nil, nil
	}
	u, err := url.Parse(pushURL)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("cannot scope a credential to %q", pushURL)
	}
	origin := "https://" + u.Host + "/"
	pair := base64.StdEncoding.EncodeToString([]byte(cred.Username + ":" + cred.Secret))
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http." + origin + ".extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic " + pair,
	}, nil
}

// validateRemoteName refuses a remote name git would misread as an option
// or a path, or that could not be a config key.
func validateRemoteName(name string) error {
	if name == "" || strings.HasPrefix(name, "-") || strings.ContainsAny(name, "./\\\r\n\x00") {
		return fmt.Errorf("invalid remote name %q", name)
	}
	for _, r := range name {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return fmt.Errorf("invalid remote name %q", name)
		}
	}
	return nil
}

// runMirror runs one git command in targetDir with the non-interactive
// environment plus extra, and reports failure with the command's stderr.
//
// It deliberately does not go through withGitEnv: that attaches the
// GitHub token to every invocation, and a push to another forge should
// carry exactly one credential, the one meant for it.
func runMirror(ctx context.Context, targetDir string, extra []string, args ...string) error {
	fullArgs := append([]string{"-C", targetDir}, args...)
	cmd := exec.CommandContext(ctx, gitBinary, fullArgs...) // #nosec G204 -- fixed git binary and structured arguments
	cmd.Env = append(append(os.Environ(), nonInteractiveEnv()...), extra...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
