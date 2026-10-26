// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mirror

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sebastienrousseau/corralctl/internal/engine"
	"github.com/sebastienrousseau/corralctl/internal/git"
)

// Seams for the walk, so the error branches are reachable without a
// filesystem that produces them.
var (
	walkDir            = filepath.WalkDir
	isRepository       = git.IsRepository
	recordedVisibility = engine.RecordedVisibility
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
	w := &walker{base: baseDir, seen: map[string]int{}}
	if err := walkDir(baseDir, w.visit); err != nil {
		return nil, err
	}
	sort.Slice(w.repos, func(i, j int) bool { return w.repos[i].Path < w.repos[j].Path })
	return w.repos, nil
}

// walker is the state of one Walk: what has been found, and the index of
// the first repository seen under each case-folded name.
type walker struct {
	base  string
	repos []Repo
	seen  map[string]int
}

// visit is the WalkDirFunc. It stops at a repository, so a nested one is
// not counted twice.
func (w *walker) visit(path string, d fs.DirEntry, err error) error {
	if err != nil {
		if filepath.Clean(path) == filepath.Clean(w.base) {
			return err
		}
		return nil //nolint:nilerr // deliberate: skip this entry, keep walking
	}
	if !d.IsDir() {
		return nil
	}
	// A clone staged for deletion still holds .git; a crash between the
	// rename and the removal leaves it, and mirroring it would create a
	// destination repository under the staging name. doctor reports it.
	if d.Name() == ".git" || git.IsDeletionStaging(path) {
		return fs.SkipDir
	}
	if path == w.base || !isRepository(path) {
		return nil
	}
	w.add(path)
	return fs.SkipDir
}

// add records the repository at path, marking it and the first one of
// the same name in any case as conflicting.
func (w *walker) add(path string) {
	name := filepath.Base(path)
	key := strings.ToLower(name)
	private, known := visibility(w.base, path)
	repo := Repo{Name: name, Path: path, Private: private, VisibilityKnown: known}
	if i, dup := w.seen[key]; dup {
		first := &w.repos[i]
		if first.Conflict == "" {
			first.Conflict = fmt.Sprintf("not mirrored: shares the name %q with %s; two repositories cannot land on one destination path", name, path)
		}
		repo.Conflict = fmt.Sprintf("not mirrored: shares the name %q with %s; two repositories cannot land on one destination path", name, first.Path)
	} else {
		w.seen[key] = len(w.repos)
	}
	w.repos = append(w.repos, repo)
}

// visibility decides what a destination is created with. Public needs
// two agreeing witnesses: the source forge's own answer, recorded in the
// clone's sidecar by `corralctl clone`, and a directory that says public.
// Either one saying anything else makes it private, so a clone with no
// record is private wherever it sits, and nothing is public that the
// directory alone would not have made public. known reports whether a
// record was found.
func visibility(baseDir, repoPath string) (private, known bool) {
	recorded, known := recordedVisibility(repoPath)
	return !known || recorded || privateFromPath(baseDir, repoPath), known
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
