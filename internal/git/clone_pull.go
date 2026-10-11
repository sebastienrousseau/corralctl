// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package git

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sebastienrousseau/corralctl/internal/diag"
)

// TokenProvider, when set, returns a GitHub token used to authenticate HTTPS
// operations against github.com so private repositories can be cloned and
// pulled non-interactively. The token is supplied to git via GIT_CONFIG_*
// environment variables (an http extraheader scoped to https://github.com/), so
// it is never written to a repository's .git/config or exposed in the process
// argument list.
var TokenProvider func() string

var updateSubmodulesFn = updateSubmodules

// authEnv returns the environment variables that inject an Authorization header
// for github.com HTTPS requests, or nil when no token is available. The header
// is scoped to https://github.com/, so it is harmless for SSH remotes.
func authEnv() []string {
	if TokenProvider == nil {
		return nil
	}
	tok := TokenProvider()
	if tok == "" {
		return nil
	}
	cred := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + tok))
	return []string{
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=http.https://github.com/.extraheader",
		"GIT_CONFIG_VALUE_0=Authorization: Basic " + cred,
	}
}

// nonInteractiveEnv returns the environment variables that force git into
// strict non-interactive mode. They are applied unconditionally to every git
// invocation so unattended runs (cron, CI) never hang on a missing credential,
// askpass helper, or GPG pinentry.
func nonInteractiveEnv() []string {
	return []string{
		"GIT_TERMINAL_PROMPT=0", // disable interactive username/password prompts
		"GIT_ASKPASS=/bin/true", // suppress any askpass helper (GUI or CLI)
		"SSH_ASKPASS=/bin/true", // suppress SSH passphrase pinentry
		"GCM_INTERACTIVE=Never", // Git Credential Manager on macOS/Windows
	}
}

// withGitEnv attaches the credentials header (when available) plus the
// non-interactive env vars to cmd, replacing any prior cmd.Env. It always sets
// cmd.Env so the non-interactive guards apply even on anonymous clones.
func withGitEnv(cmd *exec.Cmd) {
	env := append(os.Environ(), nonInteractiveEnv()...)
	if auth := authEnv(); auth != nil {
		env = append(env, auth...)
	}
	cmd.Env = env
}

// CloneOptions configures optional clone-time performance and layout flags.
type CloneOptions struct {
	// RecurseSubmodules, when true, clones submodules recursively by adding
	// the --recurse-submodules flag.
	RecurseSubmodules bool
	// SingleBranch, when true, clones only the history of the default branch
	// by adding the --single-branch flag.
	SingleBranch bool
	// Blobless, when true, performs a blobless partial clone by adding the
	// --filter=blob:none flag, deferring blob downloads until needed.
	Blobless bool
	// Depth, when greater than zero, creates a shallow clone truncated to the
	// given number of commits by adding the --depth flag.
	Depth int
}

// Clone executes a git clone command for the given URL into the target directory.
func Clone(ctx context.Context, url, targetDir string, opts CloneOptions) error {
	args := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "protocol.ext.allow=never",
		"clone",
	}
	if opts.RecurseSubmodules {
		args = append(args, "--recurse-submodules")
	}
	if opts.SingleBranch {
		args = append(args, "--single-branch")
	}
	if opts.Blobless {
		args = append(args, "--filter=blob:none")
	}
	if opts.Depth > 0 {
		args = append(args, "--depth", strconv.Itoa(opts.Depth))
	}
	// The "--" terminator prevents a URL or path beginning with "-" from being
	// interpreted as a git option.
	args = append(args, "--", url, targetDir)
	// #nosec G204 -- the executable is the fixed "git" binary and all arguments
	// are constructed internally from controlled options, not shell input.
	cmd := exec.CommandContext(ctx, gitBinary, args...)
	withGitEnv(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Do not include args here: MCP callers may supply clone URLs with
		// embedded credentials, and errors are persisted to the audit log.
		return fmt.Errorf("git clone failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// PullOptions configures a `git pull` invocation.
type PullOptions struct {
	// RecurseSubmodules, when true, also updates submodules after the pull.
	// When IgnoreSubmoduleFailures is set, the submodule update runs as a
	// separate step so its failure does not abort the parent pull.
	RecurseSubmodules bool
	// IgnoreSubmoduleFailures, when true, logs (but does not propagate)
	// errors from the post-pull submodule update step. Useful when a
	// submodule has been deleted upstream or access has been revoked but
	// the parent repository's history should still update.
	IgnoreSubmoduleFailures bool
}

// Pull executes a `git pull --rebase --autostash` in the target directory.
// Signature verification (merge.verifySignatures / rebase.verifySignatures)
// and commit signing (commit.gpgsign) are explicitly disabled for this
// invocation so an unattended sync never aborts on unsigned commits or
// blocks on a GPG/SSH passphrase prompt for users who sign commits globally.
//
// When opts.RecurseSubmodules is true:
//   - if opts.IgnoreSubmoduleFailures is false, --recurse-submodules is
//     appended to the pull so failures abort the whole operation
//     (existing pre-v0.0.7 behaviour);
//   - if opts.IgnoreSubmoduleFailures is true, the pull runs without
//     --recurse-submodules and submodule updates are attempted in a
//     separate `git submodule update --init --recursive` step whose
//     error is logged but not returned.
func Pull(ctx context.Context, targetDir string, opts PullOptions) error {
	args := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "protocol.ext.allow=never",
		"-c", "merge.verifySignatures=false",
		"-c", "rebase.verifySignatures=false",
		// Rebase replays commits, which respects the global commit.gpgsign
		// setting. Disabling it here prevents an unattended sync from blocking
		// on a GPG/SSH passphrase prompt for users who sign commits globally.
		"-c", "commit.gpgsign=false",
		"-c", "gpg.format=openpgp",
		"-C", targetDir, "pull", "--rebase", "--autostash",
	}
	if opts.RecurseSubmodules && !opts.IgnoreSubmoduleFailures {
		args = append(args, "--recurse-submodules")
	}
	// #nosec G204 -- the executable is the fixed "git" binary and all arguments
	// are constructed internally from controlled options, not shell input.
	cmd := exec.CommandContext(ctx, gitBinary, args...)
	withGitEnv(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// The operation, not the argv. Joining every argument put the
		// whole invocation into the message — eight internal -c flags and
		// an absolute path — which is noise in a terminal and worse in an
		// MCP tool result, where it reaches a model's context in place of
		// the actual git error.
		return fmt.Errorf("git pull failed in %s: %w: %s",
			filepath.Base(targetDir), err, strings.TrimSpace(string(out)))
	}

	if opts.RecurseSubmodules && opts.IgnoreSubmoduleFailures {
		if sErr := updateSubmodulesFn(ctx, targetDir); sErr != nil {
			// Best-effort: log and swallow, matching the documented contract.
			diag.Warnf("submodule update failed in %s: %v (continuing)", targetDir, sErr)
		}
	}
	return nil
}

// updateSubmodules runs `git submodule update --init --recursive` in
// targetDir as a separate subprocess. Exposed indirectly via Pull's
// IgnoreSubmoduleFailures branch.
func updateSubmodules(ctx context.Context, targetDir string) error {
	args := []string{
		"-c", "core.hooksPath=/dev/null",
		"-c", "protocol.ext.allow=never",
		"-C", targetDir, "submodule", "update", "--init", "--recursive",
	}
	// #nosec G204 -- fixed binary; controlled args.
	cmd := exec.CommandContext(ctx, gitBinary, args...)
	withGitEnv(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git submodule update failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
