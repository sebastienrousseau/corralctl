// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package git

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

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
// It first attempts a fast direct read of .git/HEAD to avoid subprocess
// execution overhead. If direct reading is not available, it falls back
// to git rev-parse --abbrev-ref HEAD.
func CurrentBranch(ctx context.Context, targetDir string) (string, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	if branch, ok := readDirectBranch(targetDir); ok {
		return branch, nil
	}
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

// readDirectBranch attempts to read the current branch directly from .git/HEAD.
func readDirectBranch(targetDir string) (string, bool) {
	gitDir, err := resolveGitDir(targetDir)
	if err != nil {
		return "", false
	}
	data, err := readFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(data))
	const refPrefix = "ref: refs/heads/"
	if strings.HasPrefix(s, refPrefix) {
		branch := strings.TrimSpace(strings.TrimPrefix(s, refPrefix))
		if branch != "" {
			return branch, true
		}
	}
	if s == "HEAD" || (len(s) == 40 && isHex(s)) || (len(s) == 64 && isHex(s)) {
		return "HEAD", true
	}
	return "", false
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

// HeadCommit retrieves the full commit hash of HEAD for the target repository.
// It uses fast direct reading of .git/HEAD, loose ref files, and .git/packed-refs
// to avoid subprocess invocation. If direct reading cannot resolve HEAD,
// it falls back to git rev-parse --verify HEAD.
func HeadCommit(ctx context.Context, targetDir string) (string, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	if commit, ok := readDirectHeadCommit(targetDir); ok {
		return commit, nil
	}
	ctx, cancel := withMetadataTimeout(ctx)
	defer cancel()
	// #nosec G204 -- fixed binary; targetDir is a local path.
	cmd := exec.CommandContext(ctx, gitBinary, "-C", targetDir, "rev-parse", "--verify", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// readDirectHeadCommit attempts to resolve the commit SHA of HEAD directly from
// repository metadata without spawning a git subprocess.
func readDirectHeadCommit(targetDir string) (string, bool) {
	gitDir, err := resolveGitDir(targetDir)
	if err != nil {
		return "", false
	}
	headData, err := readFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", false
	}
	s := strings.TrimSpace(string(headData))
	if (len(s) == 40 || len(s) == 64) && isHex(s) {
		return s, true
	}
	const refPrefix = "ref:"
	if !strings.HasPrefix(s, refPrefix) {
		return "", false
	}
	refName := strings.TrimSpace(strings.TrimPrefix(s, refPrefix))
	if refName == "" {
		return "", false
	}

	cleanRef := filepath.Clean(filepath.FromSlash(refName))
	if !strings.HasPrefix(cleanRef, "refs"+string(filepath.Separator)) {
		return "", false
	}

	// 1. Try loose ref file in <gitDir>/<refName>
	loosePath := filepath.Join(gitDir, cleanRef)
	if data, err := readFile(loosePath); err == nil {
		sha := strings.TrimSpace(string(data))
		if (len(sha) == 40 || len(sha) == 64) && isHex(sha) {
			return sha, true
		}
	}

	// 2. Try packed-refs in gitDir
	if sha, ok := readPackedRef(filepath.Join(gitDir, "packed-refs"), refName); ok {
		return sha, true
	}

	// 3. For linked worktrees, check commondir for packed-refs
	commondirPath := filepath.Join(gitDir, "commondir")
	if b, err := readFile(commondirPath); err == nil {
		common := strings.TrimSpace(string(b))
		if !filepath.IsAbs(common) {
			common = filepath.Join(gitDir, common)
		}
		if sha, ok := readPackedRef(filepath.Join(common, "packed-refs"), refName); ok {
			return sha, true
		}
	}

	return "", false
}

// readPackedRef scans a packed-refs file for the target reference name.
func readPackedRef(packedPath, targetRef string) (string, bool) {
	f, err := os.Open(packedPath) // #nosec G304 -- path resolved from repository gitDir
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line[0] == '#' || line[0] == '^' {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			sha, ref := fields[0], fields[1]
			if ref == targetRef && (len(sha) == 40 || len(sha) == 64) && isHex(sha) {
				return sha, true
			}
		}
	}
	return "", false
}

// directIsEmpty reports whether a repository has no commits without spawning git.
func directIsEmpty(targetDir string) (bool, bool) {
	gitDir, err := resolveGitDir(targetDir)
	if err != nil {
		return false, false
	}
	headData, err := readFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return false, false
	}
	s := strings.TrimSpace(string(headData))
	if (len(s) == 40 || len(s) == 64) && isHex(s) {
		return false, true
	}
	const refPrefix = "ref:"
	if !strings.HasPrefix(s, refPrefix) {
		return false, false
	}
	refName := strings.TrimSpace(strings.TrimPrefix(s, refPrefix))
	if refName == "" {
		return false, false
	}

	if _, ok := readDirectHeadCommit(targetDir); ok {
		return false, true
	}

	// If HEAD points to a ref, but no loose ref or packed-refs file exists:
	loosePath := filepath.Join(gitDir, filepath.Clean(filepath.FromSlash(refName)))
	_, looseErr := os.Stat(loosePath)
	packedPath := filepath.Join(gitDir, "packed-refs")
	_, packedErr := os.Stat(packedPath)

	if errors.Is(looseErr, os.ErrNotExist) && errors.Is(packedErr, os.ErrNotExist) {
		// Also check commondir for linked worktrees
		commondirPath := filepath.Join(gitDir, "commondir")
		if b, err := readFile(commondirPath); err == nil {
			common := strings.TrimSpace(string(b))
			if !filepath.IsAbs(common) {
				common = filepath.Join(gitDir, common)
			}
			if _, commonPackedErr := os.Stat(filepath.Join(common, "packed-refs")); !errors.Is(commonPackedErr, os.ErrNotExist) {
				return false, false
			}
		}
		return true, true // Unborn HEAD with no packed refs -> empty repo
	}

	return false, false
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
	if empty, ok := directIsEmpty(targetDir); ok {
		return empty
	}
	ctx, cancel := withMetadataTimeout(ctx)
	defer cancel()
	// #nosec G204 -- fixed binary; targetDir is a local path.
	cmd := exec.CommandContext(ctx, gitBinary, "-C", targetDir, "rev-parse", "--verify", "-q", "HEAD^{commit}")
	return cmd.Run() != nil
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
