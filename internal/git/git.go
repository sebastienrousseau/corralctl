// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package git provides helper functions to execute common Git commands
// by wrapping the system's git binary using os/exec.
package git

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/diag"
)

// TokenProvider, when set, returns a GitHub token used to authenticate HTTPS
// operations against github.com so private repositories can be cloned and
// pulled non-interactively. The token is supplied to git via GIT_CONFIG_*
// environment variables (an http extraheader scoped to https://github.com/), so
// it is never written to a repository's .git/config or exposed in the process
// argument list.
var TokenProvider func() string

var (
	runGitOutput       = gitOutput
	updateSubmodulesFn = updateSubmodules
	readFile           = os.ReadFile
)

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
	args := []string{"clone"}
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
	args := []string{"-C", targetDir, "submodule", "update", "--init", "--recursive"}
	// #nosec G204 -- fixed binary; controlled args.
	cmd := exec.CommandContext(ctx, gitBinary, args...)
	withGitEnv(cmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git submodule update failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// metadataTimeout bounds the quick, local metadata reads below.
//
// These used to run with no context and no deadline, so a stale NFS
// mount, a FUSE filesystem that stops responding, or a repository with a
// wedged index lock blocked them forever — and CurrentBranch sits on the
// sync decision path for every repository in a run. 30s is far beyond
// what `git rev-parse` needs locally while still bounding the hang.
const metadataTimeout = 30 * time.Second

// withMetadataTimeout derives a bounded context for a local metadata read,
// preserving cancellation from the caller's context.
func withMetadataTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, metadataTimeout)
}

// CurrentBranch retrieves the name of the currently checked-out branch.
func CurrentBranch(ctx context.Context, targetDir string) (string, error) {
	ctx, cancel := withMetadataTimeout(ctx)
	defer cancel()
	// #nosec G204 -- fixed "git" binary; targetDir is a local path, not shell input.
	cmd := exec.CommandContext(ctx, gitBinary, "-C", targetDir, "rev-parse", "--abbrev-ref", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// IsEmpty reports whether the repo at targetDir has no commits.
//
// This is the local mirror of "an empty GitHub repository" — one that
// was created upstream but never pushed to. Its .git/refs/heads is
// empty and HEAD is unborn, so `git pull` fails with
// "no such ref was fetched". Detecting the state locally lets corral
// treat it as SKIP-with-reason instead of surfacing that git error to
// the user.
//
// `git rev-parse --verify HEAD^{commit} -q` returns 0 exactly when
// HEAD resolves to a commit. On any failure — unborn HEAD (empty
// repo), corrupted refs, or the target not being a git repo at all —
// this returns true. Callers should have already established that
// targetDir *is* a git repo (via a .git-directory check) before
// calling; the "not a git repo" case is defence-in-depth.
func IsEmpty(ctx context.Context, targetDir string) bool {
	ctx, cancel := withMetadataTimeout(ctx)
	defer cancel()
	// #nosec G204 -- fixed binary; targetDir is a local path.
	cmd := exec.CommandContext(ctx, gitBinary, "-C", targetDir, "rev-parse", "--verify", "-q", "HEAD^{commit}")
	return cmd.Run() != nil
}

// IsRepository reports whether targetDir is a Git repository. It accepts both
// the usual .git directory and the .git indirection file used by worktrees.
func IsRepository(targetDir string) bool {
	_, err := os.Stat(filepath.Join(targetDir, ".git"))
	return err == nil
}

// CanonicalRemote normalizes common HTTPS, SSH, and scp-like Git remote URLs
// into a host/path identity suitable for equality checks.
func CanonicalRemote(raw string) string {
	raw = strings.TrimSpace(strings.TrimSuffix(raw, "/"))
	if raw == "" {
		return ""
	}
	if parsed, err := url.Parse(raw); err == nil && parsed.Scheme != "" {
		host := strings.ToLower(parsed.Hostname())
		path := strings.Trim(strings.TrimSuffix(parsed.Path, ".git"), "/")
		if host != "" && path != "" {
			return strings.ToLower(host + "/" + path)
		}
	}
	// scp-style SSH: git@github.com:owner/repo.git
	if colon := strings.Index(raw, ":"); colon >= 0 {
		hostPart := raw[:colon]
		if at := strings.LastIndex(hostPart, "@"); at >= 0 {
			hostPart = hostPart[at+1:]
		}
		path := strings.Trim(strings.TrimSuffix(raw[colon+1:], ".git"), "/")
		if hostPart != "" && path != "" {
			return strings.ToLower(hostPart + "/" + path)
		}
	}
	return strings.ToLower(strings.TrimSuffix(raw, ".git"))
}

// HasUnpublishedWork reports whether deleting targetDir could discard Git
// objects or state that are not represented by its remotes. It checks commits
// reachable from every local branch, working-tree changes, stashes, and
// local-only or divergent tags.
// Verification errors fail closed and are returned in the detail string.
func HasUnpublishedWork(ctx context.Context, targetDir string) (bool, string) {
	if dirty, detail := HasLocalChanges(ctx, targetDir); dirty {
		return true, detail
	}
	// The specific checks run before the catch-all count so the refusal names
	// something the user can act on ("stash entries are present") rather than
	// an opaque commit tally. rev-list --all would otherwise absorb them, since
	// refs/stash lives under refs/.
	if _, err := runGitOutput(ctx, targetDir, "rev-parse", "--verify", "--quiet", "refs/stash"); err == nil {
		return true, "stash entries are present"
	} else if exitErr, ok := err.(*exec.ExitError); !ok || exitErr.ExitCode() != 1 {
		return true, "unable to verify stash state: " + err.Error()
	}

	// Submodules keep their own object stores. A submodule whose gitlink is
	// committed leaves the parent clean, so the parent's rev-list never sees
	// the submodule's unpushed commits — and deleting the parent takes them.
	if unpublished, detail := submodulesHaveUnpublishedWork(ctx, targetDir); unpublished {
		return true, detail
	}

	// --all rather than --branches. --branches covers refs/heads/** only, so
	// commits made in detached HEAD were reachable from HEAD and from no
	// branch: the count came back 0 and the delete proceeded, destroying work
	// that existed nowhere else. git's --all means every ref under refs/ plus
	// HEAD.
	branchOut, err := runGitOutput(ctx, targetDir, "rev-list", "--count", "--all", "--not", "--remotes")
	if err != nil {
		return true, "unable to verify local branches: " + err.Error()
	}
	branchCount := strings.TrimSpace(branchOut)
	if branchCount != "" && branchCount != "0" {
		return true, branchCount + " commits reachable only from local refs or HEAD"
	}

	localTags, err := refMap(ctx, targetDir, "show-ref", "--tags")
	if err != nil {
		return true, "unable to inspect local tags: " + err.Error()
	}
	if len(localTags) == 0 {
		return false, ""
	}
	remoteTags, err := refMap(ctx, targetDir, "ls-remote", "--tags", "--refs", "origin")
	if err != nil {
		return true, "unable to verify remote tags: " + err.Error()
	}
	for ref, hash := range localTags {
		if remoteTags[ref] != hash {
			return true, "local-only or divergent tag " + strings.TrimPrefix(ref, "refs/tags/")
		}
	}
	return false, ""
}

// HasLocalChanges reports tracked, staged, or untracked working-tree changes.
// Git errors are treated as unsafe so destructive callers fail closed.
func HasLocalChanges(ctx context.Context, targetDir string) (bool, string) {
	out, err := runGitOutput(ctx, targetDir, "status", "--porcelain")
	if err != nil {
		return true, fmt.Sprintf("cannot inspect working tree: %v", err)
	}
	if strings.TrimSpace(out) != "" {
		return true, "working tree has local changes"
	}
	return false, ""
}

// HasIgnoredContent reports gitignored files present in the working tree.
//
// `git status --porcelain` excludes ignored files by design, so a clone can be
// reported clean while holding the content that is least recoverable: local
// .env files, SQLite databases, build caches with credentials in them. Deleting
// on the strength of "clean" therefore destroyed exactly the files no remote
// has a copy of. Callers about to remove a clone consult this too.
func HasIgnoredContent(ctx context.Context, targetDir string) (bool, string) {
	out, err := runGitOutput(ctx, targetDir, "status", "--porcelain", "--ignored=matching")
	if err != nil {
		return true, fmt.Sprintf("cannot inspect ignored files: %v", err)
	}
	var ignored []string
	for _, line := range strings.Split(out, "\n") {
		// Porcelain v1 marks ignored entries with "!!".
		if strings.HasPrefix(line, "!! ") {
			ignored = append(ignored, strings.TrimSpace(strings.TrimPrefix(line, "!!")))
		}
	}
	if len(ignored) == 0 {
		return false, ""
	}
	sample := ignored
	const maxSample = 3
	if len(sample) > maxSample {
		sample = sample[:maxSample]
	}
	detail := fmt.Sprintf("%d gitignored path(s) present, e.g. %s",
		len(ignored), strings.Join(sample, ", "))
	return true, detail
}

// submodulesHaveUnpublishedWork checks each initialised submodule for commits
// that exist nowhere but locally. A repository without submodules returns
// false with no error.
func submodulesHaveUnpublishedWork(ctx context.Context, targetDir string) (bool, string) {
	if _, err := os.Stat(filepath.Join(targetDir, ".gitmodules")); err != nil {
		return false, "" // no submodules; nothing to check
	}
	// --quiet keeps the "Entering ..." lines out of the output so anything
	// printed is a path we care about.
	out, err := runGitOutput(ctx, targetDir, "submodule", "--quiet", "foreach", "--recursive",
		`c=$(git rev-list --count --all --not --remotes 2>/dev/null || echo unknown); `+
			`if [ "$c" != "0" ]; then echo "$displaypath:$c"; fi`)
	if err != nil {
		return true, "unable to verify submodules: " + err.Error()
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		return true, "submodule has unpublished commits: " + line
	}
	return false, ""
}

func gitOutput(ctx context.Context, targetDir string, args ...string) (string, error) {
	fullArgs := append([]string{"-C", targetDir}, args...)
	cmd := exec.CommandContext(ctx, gitBinary, fullArgs...) // #nosec G204 -- fixed git binary and structured arguments
	withGitEnv(cmd)
	out, err := cmd.Output()
	return string(out), err
}

func refMap(ctx context.Context, targetDir string, args ...string) (map[string]string, error) {
	out, err := runGitOutput(ctx, targetDir, args...)
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 && len(out) == 0 {
			return map[string]string{}, nil
		}
		return nil, err
	}
	refs := make(map[string]string)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			refs[fields[1]] = fields[0]
		}
	}
	return refs, nil
}

// RemoteOrigin retrieves the remote origin URL of the target directory by
// invoking `git remote get-url origin`. Prefer RemoteOriginFromConfig on hot
// paths (e.g. orphan detection over hundreds of clones) to avoid the
// per-call cost of spawning a subprocess.
func RemoteOrigin(ctx context.Context, targetDir string) (string, error) {
	ctx, cancel := withMetadataTimeout(ctx)
	defer cancel()
	// #nosec G204 -- fixed "git" binary; targetDir is a local path, not shell input.
	cmd := exec.CommandContext(ctx, gitBinary, "-C", targetDir, "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// RemoteOriginFromConfig parses the `url =` entry under [remote "origin"]
// directly from <targetDir>/.git/config, avoiding the ~5-15ms per-call cost of
// spawning `git remote get-url origin`. Returns the wrapped os.ErrNotExist
// when the config file is absent, and a clear error when the section or key
// is missing. Tolerates blank lines, `#` / `;` comments, indented entries,
// and CRLF line endings.
func RemoteOriginFromConfig(targetDir string) (string, error) {
	gitDir, err := resolveGitDir(targetDir)
	if err != nil {
		return "", err
	}
	configPath := filepath.Join(gitDir, "config")
	if _, err := os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		// Linked worktrees keep config in the common repository directory.
		// #nosec G304 -- gitDir is resolved from the repository's .git metadata.
		if b, readErr := readFile(filepath.Join(gitDir, "commondir")); readErr == nil {
			common := strings.TrimSpace(string(b))
			if !filepath.IsAbs(common) {
				common = filepath.Join(gitDir, common)
			}
			configPath = filepath.Join(filepath.Clean(common), "config")
		}
	}
	f, err := os.Open(configPath) // #nosec G304 -- path is resolved from targetDir's .git metadata
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	var inOrigin bool
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			// A new section ends the previous one. The origin section header
			// can appear as `[remote "origin"]` or with extra whitespace.
			inOrigin = strings.EqualFold(strings.Join(strings.Fields(line), " "), `[remote "origin"]`)
			continue
		}
		if !inOrigin {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k), "url") {
			value := strings.Trim(strings.TrimSpace(v), `"`)
			return strings.ReplaceAll(value, `\\`, `\`), nil
		}
	}
	if err := s.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("origin url not found in %s", configPath)
}

// Dir resolves targetDir to its Git metadata directory, transparently
// following the `gitdir: ...` pointer that worktrees store in a regular .git
// file. Callers that need to keep a file out of the working tree (and so out
// of `git status`) should place it here rather than beside .git.
func Dir(targetDir string) (string, error) {
	return resolveGitDir(targetDir)
}

// resolveGitDir resolves targetDir/.git to the actual Git metadata directory.
// Worktrees store a `gitdir: ...` pointer in a regular .git file.
func resolveGitDir(targetDir string) (string, error) {
	dotGit := filepath.Join(targetDir, ".git")
	info, err := os.Stat(dotGit)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return dotGit, nil
	}
	b, err := readFile(dotGit) // #nosec G304 -- dotGit is scoped to targetDir
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(string(b))
	const prefix = "gitdir:"
	if !strings.HasPrefix(strings.ToLower(line), prefix) {
		return "", fmt.Errorf("invalid gitdir file %s", dotGit)
	}
	dir := strings.TrimSpace(line[len(prefix):])
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(targetDir, dir)
	}
	return filepath.Clean(dir), nil
}
