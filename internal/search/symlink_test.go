// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package search

import (
	"os"
	"path/filepath"
	"testing"
)

// symlinkOrSkip creates a symlink, or skips where the platform refuses.
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

// A clone can commit a symlink to any path on disk. Searching must never
// read through it: the target can be a credential file outside the root.
func TestSearchDoesNotFollowSymlinkOutOfRoot(t *testing.T) {
	outside := t.TempDir()
	write(t, outside, "creds.txt", "aws_secret_access_key = OUTSIDE-ROOT-SECRET\n")
	root := t.TempDir()
	write(t, root, "README.md", "# demo\n")
	symlinkOrSkip(t, filepath.Join(outside, "creds.txt"), filepath.Join(root, "leak.md"))
	symlinkOrSkip(t, outside, filepath.Join(root, "linked"))

	res := run(t, root, Query{Pattern: "OUTSIDE-ROOT-SECRET"}, nil)
	if len(res.Hits) != 0 {
		t.Fatalf("search read through a symlink:%s", files(res))
	}
}

// A symlink inside the repository is skipped too: following it would let a
// link named like an allowed file stand in for a refused one.
func TestSearchDoesNotFollowSymlinkWithinRoot(t *testing.T) {
	root := t.TempDir()
	write(t, root, "secret.env", "TOKEN=INSIDE-SECRET\n")
	symlinkOrSkip(t, "secret.env", filepath.Join(root, "notes.md"))

	allowMarkdown := func(rel string) bool { return filepath.Ext(rel) == ".md" }
	res := run(t, root, Query{Pattern: "INSIDE-SECRET"}, allowMarkdown)
	if len(res.Hits) != 0 {
		t.Fatalf("a link named notes.md served secret.env:%s", files(res))
	}
}

// The read itself refuses a path that became a symlink after discovery.
func TestSearchFileRefusesASymlink(t *testing.T) {
	outside := t.TempDir()
	write(t, outside, "creds.txt", "OUTSIDE-ROOT-SECRET\n")
	root := t.TempDir()
	symlinkOrSkip(t, filepath.Join(outside, "creds.txt"), filepath.Join(root, "swapped.md"))

	m, err := Compile(Query{Pattern: "OUTSIDE"})
	if err != nil {
		t.Fatal(err)
	}
	if hits, _ := searchFile(root, "swapped.md", m, newFileBuffer()); len(hits) != 0 {
		t.Fatalf("searchFile read through a symlink: %+v", hits)
	}
	if _, err := readFileBounded(root, "swapped.md", nil); err == nil {
		t.Fatal("readFileBounded read through a symlink")
	}
}

// A file that opens cleanly and then fails to read yields nothing, and one
// bad file does not fail the search. Directories used to reach this path;
// safefs now refuses them before the read.
func TestSearchFileSurvivesAReadFailure(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.md", "needle\n")
	old := openSearchFile
	t.Cleanup(func() { openSearchFile = old })
	openSearchFile = func(root, rel string) (*os.File, error) {
		f, err := old(root, rel)
		if err == nil {
			_ = f.Close()
		}
		return f, err
	}
	m, err := Compile(Query{Pattern: "needle", MaxHits: 10})
	if err != nil {
		t.Fatal(err)
	}
	if hits, _ := searchFile(root, "a.md", m, newFileBuffer()); len(hits) != 0 {
		t.Errorf("a failed read produced hits: %v", hits)
	}
	if _, err := readFileBounded(root, "a.md", nil); err == nil {
		t.Error("readFileBounded hid a read failure")
	}
}
