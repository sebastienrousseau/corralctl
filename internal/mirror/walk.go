// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mirror

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sebastienrousseau/corralctl/internal/git"
)

// Seams for the walk, so the error branches are reachable without a
// filesystem that produces them.
var (
	walkDir      = filepath.WalkDir
	isRepository = git.IsRepository
)

// Walk finds every repository under baseDir, in path order.
//
// A repository is a directory that holds a `.git` entry — a directory, or
// the file a worktree uses — and the walk does not descend into one, so a
// nested submodule is not counted twice. Unreadable directories are
// skipped: a stray directory the user cannot read must not stop the
// mirror of the hundreds they can.
//
// Two repositories with the same name, in any case, would converge on one
// destination and the second push would prune the first. Both are returned
// with Conflict set so the run reports them and pushes neither, rather
// than refusing the whole tree for one pair — a fork kept beside its
// original is ordinary, and the other two hundred repositories should not
// wait on it.
func Walk(baseDir string) ([]Repo, error) {
	var repos []Repo
	seen := map[string]int{}
	err := walkDir(baseDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if filepath.Clean(path) == filepath.Clean(baseDir) {
				return err
			}
			return nil //nolint:nilerr // deliberate: skip this entry, keep walking
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == ".git" {
			return fs.SkipDir
		}
		if path == baseDir || !isRepository(path) {
			return nil
		}
		name := filepath.Base(path)
		key := strings.ToLower(name)
		repo := Repo{Name: name, Path: path, Private: privateFromPath(baseDir, path)}
		if i, dup := seen[key]; dup {
			first := &repos[i]
			if first.Conflict == "" {
				first.Conflict = fmt.Sprintf("not mirrored: shares the name %q with %s; two repositories cannot land on one destination path", name, path)
			}
			repo.Conflict = fmt.Sprintf("not mirrored: shares the name %q with %s; two repositories cannot land on one destination path", name, first.Path)
		} else {
			seen[key] = len(repos)
		}
		repos = append(repos, repo)
		return fs.SkipDir
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Path < repos[j].Path })
	return repos, nil
}

// privateFromPath reads visibility from the directory segments between
// baseDir and the repository.
//
// corral's default layout is `{{.Collection}}/{{.Bucket}}/{{.Name}}`, with
// Collection one of Public, Private or Forks, and a custom layout may use
// `{{.Visibility}}`, which is lower-cased. So either spelling of "public"
// makes a repository public and either spelling of "private" makes it
// private — and private wins whenever both appear, because a tree that
// contradicts itself must not leak.
//
// Forks says nothing about visibility and takes the safe default. The
// repository's own directory name is never consulted: a repository called
// "public" is a name, not a classification. Anything unrecognised is
// private, because leaking is the failure a user cannot undo.
func privateFromPath(baseDir, repoPath string) bool {
	rel, err := filepath.Rel(baseDir, repoPath)
	if err != nil {
		return true
	}
	segments := strings.Split(filepath.ToSlash(rel), "/")
	public := false
	for _, seg := range segments[:len(segments)-1] {
		switch strings.ToLower(seg) {
		case "public":
			public = true
		case "private":
			return true
		}
	}
	return !public
}
