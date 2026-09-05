// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/sebastienrousseau/corralctl/internal/tui"
)

// TestSeamsBindToRealImplementations asserts that every indirection seam in
// this package points at the production function it is supposed to point at.
//
// Why this exists: the package is at 100% statement coverage, and every test
// replaces these vars with a stub. Nothing asserted what they point to by
// default, so mutation testing showed that assigning any of them a no-op —
// gitClone, gitPull, applyTags, fetchRepos, gitIsEmpty — left the entire suite
// green at 100%. Coverage measured that the lines ran; it could not measure
// that they did anything.
//
// Comparing func values requires reflect: Go does not define == on funcs.
func TestSeamsBindToRealImplementations(t *testing.T) {
	cases := []struct {
		name string
		got  any
		want any
	}{
		{"fetchRepos", fetchRepos, fetchFromForge},
		{"osExit", osExit, os.Exit},
		{"gitPull", gitPull, git.Pull},
		{"gitClone", gitClone, git.Clone},
		{"gitCurrentBranch", gitCurrentBranch, git.CurrentBranch},
		{"gitIsEmpty", gitIsEmpty, git.IsEmpty},
		{"gitRemoteOrigin", gitRemoteOrigin, git.RemoteOriginFromConfig},
		{"runSelector", runSelector, tui.RunSelector},
		{"walkDir", walkDir, filepath.WalkDir},
		{"readDir", readDir, os.ReadDir},
		{"statPath", statPath, os.Stat},
		{"sameFile", sameFile, os.SameFile},
		{"mkdirAll", mkdirAll, os.MkdirAll},
		{"renamePath", renamePath, os.Rename},
		{"applyTags", applyTags, applyFinderTags},
	}
	for _, tc := range cases {
		gotPtr := reflect.ValueOf(tc.got).Pointer()
		wantPtr := reflect.ValueOf(tc.want).Pointer()
		if gotPtr != wantPtr {
			t.Errorf("%s is not bound to its production implementation "+
				"(a stub leaked out of a test, or the default was changed)", tc.name)
		}
	}
}

// TestStateSeamsBindToRealImplementations covers the sidecar IO seams.
func TestStateSeamsBindToRealImplementations(t *testing.T) {
	if reflect.ValueOf(renameStateFile).Pointer() != reflect.ValueOf(os.Rename).Pointer() {
		t.Error("renameStateFile is not bound to os.Rename")
	}
	// createStateTemp wraps os.CreateTemp to satisfy the stateTempFile
	// interface, so assert behaviour rather than identity.
	f, err := createStateTemp(t.TempDir(), "seam.*.tmp")
	if err != nil {
		t.Fatalf("createStateTemp must really create a file: %v", err)
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(name); err != nil {
		t.Errorf("createStateTemp did not create %s: %v", name, err)
	}
	_ = os.Remove(name)
}
