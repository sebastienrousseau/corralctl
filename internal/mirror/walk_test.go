// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mirror

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestWalkFindsRepositoriesInOrder(t *testing.T) {
	base := t.TempDir()
	private := filepath.Join(base, "Private", "Go", "zeta")
	public := filepath.Join(base, "Public", "Go", "alpha")
	worktree := filepath.Join(base, "Public", "Rust", "beta")
	fork := filepath.Join(base, "Forks", "Go", "gamma")
	for _, dir := range []string{private, public, worktree, fork} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{private, public, fork} {
		if err := os.Mkdir(filepath.Join(dir, ".git"), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: elsewhere"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A repository inside a repository is not visited.
	if err := os.MkdirAll(filepath.Join(public, "vendor", "nested", ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	got, err := Walk(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 || got[0].Name != "gamma" || got[1].Name != "zeta" || got[2].Name != "alpha" || got[3].Name != "beta" {
		t.Fatalf("repositories = %+v", got)
	}
	if !got[0].Private || !got[1].Private || got[2].Private || got[3].Private {
		t.Fatalf("visibility = %+v", got)
	}
}

func TestWalkMarksNameCollisions(t *testing.T) {
	base := t.TempDir()
	for _, p := range []string{
		filepath.Join(base, "Public", "Go", "Repo", ".git"),
		filepath.Join(base, "Private", "Rust", "repo", ".git"),
		filepath.Join(base, "Forks", "Rust", "REPO", ".git"),
		filepath.Join(base, "Public", "Go", "other", ".git"),
	} {
		if err := os.MkdirAll(p, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Walk(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 4 {
		t.Fatalf("repositories = %+v", got)
	}
	conflicts := 0
	for _, r := range got {
		switch {
		case r.Name == "other" && r.Conflict != "":
			t.Fatalf("a unique name was marked: %+v", r)
		case r.Name != "other" && r.Conflict == "":
			t.Fatalf("a colliding name was not marked: %+v", r)
		case r.Conflict != "":
			conflicts++
		}
	}
	if conflicts != 3 {
		t.Fatalf("conflicts = %d, want all three spellings", conflicts)
	}
}

func TestWalkErrors(t *testing.T) {
	if _, err := Walk(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected a missing base to fail")
	}
	old := walkDir
	t.Cleanup(func() { walkDir = old })
	walkDir = func(root string, fn fs.WalkDirFunc) error {
		if err := fn(root, entry{name: "root", dir: true}, nil); err != nil {
			return err
		}
		if err := fn(filepath.Join(root, "denied"), nil, errors.New("denied")); err != nil {
			return err
		}
		if err := fn(filepath.Join(root, "file"), entry{name: "file"}, nil); err != nil {
			return err
		}
		if got := fn(filepath.Join(root, ".git"), entry{name: ".git", dir: true}, nil); !errors.Is(got, fs.SkipDir) {
			t.Fatalf(".git callback = %v", got)
		}
		return fn(filepath.Join(root, "plain"), entry{name: "plain", dir: true}, nil)
	}
	if got, err := Walk("root"); err != nil || len(got) != 0 {
		t.Fatalf("injected walk = %+v, %v", got, err)
	}
}

func TestPrivateFromPath(t *testing.T) {
	cases := map[string]bool{
		filepath.Join("Public", "Go", "repo"):      false,
		filepath.Join("Private", "Go", "repo"):     true,
		filepath.Join("Forks", "Go", "repo"):       true,
		filepath.Join("owner", "public", "repo"):   false,
		filepath.Join("owner", "private", "repo"):  true,
		filepath.Join("Public", "Private", "repo"): true,
		filepath.Join("private", "Public", "repo"): true,
		filepath.Join("other", "repo"):             true,
		filepath.Join("Private", "Go", "Public"):   true,
		"public":                                   true,
		filepath.Join("PUBLIC", "repo"):            false,
		filepath.Join("Public-stuff", "repo"):      true,
	}
	for rel, want := range cases {
		if got := privateFromPath("base", filepath.Join("base", rel)); got != want {
			t.Errorf("privateFromPath(%q) = %v, want %v", rel, got, want)
		}
	}
	if !privateFromPath("base", string(filepath.Separator)+"elsewhere") {
		t.Fatal("an unrelatable path must be private")
	}
}

type entry struct {
	name string
	dir  bool
}

func (e entry) Name() string               { return e.name }
func (e entry) IsDir() bool                { return e.dir }
func (e entry) Type() fs.FileMode          { return 0 }
func (e entry) Info() (fs.FileInfo, error) { return nil, errors.New("unused") }
