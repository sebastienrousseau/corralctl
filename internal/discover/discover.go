// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package discover provides pruned filesystem traversal to detect unmanaged
// and untracked Git repositories.
package discover

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sebastienrousseau/corralctl/internal/git"
)

// Seams for filesystem and Git operations, allowing deterministic unit testing.
var (
	walkDir       = filepath.WalkDir
	statPath      = os.Stat
	isRepository  = git.IsRepository
	remoteOrigin  = git.RemoteOriginFromConfig
	currentBranch = git.CurrentBranch
	isEmpty       = git.IsEmpty
)

// Candidate represents a local Git repository discovered on the filesystem.
type Candidate struct {
	// Name is the base name of the repository folder.
	Name string `json:"name"`
	// Path is the absolute or resolved filesystem path of the repository.
	Path string `json:"path"`
	// HasRemote indicates whether an origin remote is configured.
	HasRemote bool `json:"has_remote"`
	// RemoteURL is the configured remote URL for origin, if present.
	RemoteURL string `json:"remote_url,omitempty"`
	// DefaultBranch is the currently checked-out or default branch.
	DefaultBranch string `json:"default_branch"`
	// IsEmpty indicates whether the repository has any commits.
	IsEmpty bool `json:"is_empty"`
	// DetectedLang is the primary language detected from build manifests.
	DetectedLang string `json:"detected_language"`
}

// Options configures the discovery crawler.
type Options struct {
	// BaseDir is the directory where discovery begins.
	BaseDir string
	// ExcludePaths holds directory prefixes to skip entirely during traversal.
	ExcludePaths []string
	// UntrackedOnly, when true, returns only repositories without an origin remote.
	UntrackedOnly bool
	// MaxDepth limits the directory traversal depth (0 means unbounded).
	MaxDepth int
}

// Discover traverses opts.BaseDir, returning all Git repositories matching options.
func Discover(ctx context.Context, opts Options) ([]Candidate, error) {
	if strings.TrimSpace(opts.BaseDir) == "" {
		return nil, errors.New("base directory cannot be empty")
	}
	cleanBase := filepath.Clean(opts.BaseDir)
	info, err := statPath(cleanBase)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("base path is not a directory")
	}

	var candidates []Candidate

	err = walkDir(cleanBase, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if filepath.Clean(path) == cleanBase {
				return walkErr
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if path == cleanBase {
			return nil
		}
		if !d.IsDir() {
			return nil
		}

		if shouldPrune(d.Name()) {
			return fs.SkipDir
		}

		if opts.MaxDepth > 0 {
			rel, relErr := filepath.Rel(cleanBase, path)
			if relErr == nil && strings.Count(filepath.ToSlash(rel), "/") >= opts.MaxDepth {
				return fs.SkipDir
			}
		}

		pathSlash := filepath.ToSlash(path)
		for _, excl := range opts.ExcludePaths {
			if strings.HasPrefix(pathSlash, filepath.ToSlash(filepath.Clean(excl))) {
				return fs.SkipDir
			}
		}

		if isRepository(path) {
			cand, candErr := inspectCandidate(ctx, path)
			if candErr == nil {
				if !opts.UntrackedOnly || !cand.HasRemote {
					candidates = append(candidates, cand)
				}
			}
			return fs.SkipDir
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Path < candidates[j].Path
	})

	return candidates, nil
}

func inspectCandidate(ctx context.Context, path string) (Candidate, error) {
	name := filepath.Base(path)
	cand := Candidate{
		Name:    name,
		Path:    path,
		IsEmpty: isEmpty(ctx, path),
	}

	remote, err := remoteOrigin(path)
	if err == nil && strings.TrimSpace(remote) != "" {
		cand.HasRemote = true
		cand.RemoteURL = strings.TrimSpace(remote)
	}

	branch, branchErr := currentBranch(ctx, path)
	if branchErr == nil && branch != "" {
		cand.DefaultBranch = branch
	} else {
		cand.DefaultBranch = "main"
	}

	cand.DetectedLang = detectLanguage(path)
	return cand, nil
}

func shouldPrune(name string) bool {
	switch name {
	case ".git", ".cache", ".next", ".venv", "DerivedData", "Pods",
		"build", "dist", "node_modules", "target", "vendor", "venv",
		".cargo", ".idea", ".vscode":
		return true
	default:
		return false
	}
}

func detectLanguage(path string) string {
	checks := []struct {
		file string
		lang string
	}{
		{"go.mod", "Go"},
		{"Cargo.toml", "Rust"},
		{"package.json", "TypeScript"},
		{"pyproject.toml", "Python"},
		{"requirements.txt", "Python"},
		{"pom.xml", "Java"},
		{"build.gradle", "Java"},
		{"Package.swift", "Swift"},
	}
	for _, c := range checks {
		if _, err := statPath(filepath.Join(path, c.file)); err == nil {
			return c.lang
		}
	}
	return "other"
}
