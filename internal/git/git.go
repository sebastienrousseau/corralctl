// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package git provides helper functions to execute common Git commands
// by wrapping the system's git binary using os/exec.
package git

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var (
	runGitOutput = gitOutput
	readFile     = os.ReadFile
)

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
	fullArgs := append(hardened("-C", targetDir), args...)
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
