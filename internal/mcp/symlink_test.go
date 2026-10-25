// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The content policy is checked on the requested name, so a link with an
// allowed name must not serve the refused file it points at. A clone can
// commit such links: readme.md -> .env, notes.md -> .git/config.
func TestFileResourceRefusesSymlinks(t *testing.T) {
	root := hardeningWorkspace(t)
	repo := filepath.Join(root, "Public", "go", "victim")
	writeRepoFile(t, root, ".env", "API_KEY=supersecret\n")
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "creds.txt"), []byte("OUTSIDE-ROOT-SECRET\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	links := map[string]string{
		"readme.md":   ".env",
		"notes.md":    ".git/config",
		"outside.md":  filepath.Join(outside, "creds.txt"),
		"docs/up.txt": "../.env",
	}
	for link, target := range links {
		full := filepath.Join(repo, link)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, full); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	s, err := NewServer(ServerOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	for link := range links {
		body, err := readFileResource(t, s, link)
		if err == nil {
			t.Errorf("%s: served through a symlink: %q", link, body)
			continue
		}
		for _, leaked := range []string{"supersecret", "OUTSIDE-ROOT-SECRET", "[remote"} {
			if strings.Contains(err.Error(), leaked) {
				t.Errorf("%s: refusal leaks content: %v", link, err)
			}
		}
	}
}

// A regular file is still served: the refusal is for links only.
func TestFileResourceStillServesRegularFiles(t *testing.T) {
	root := hardeningWorkspace(t)
	writeRepoFile(t, root, "docs/guide.md", "# guide\n")
	s, err := NewServer(ServerOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	body, err := readFileResource(t, s, "docs/guide.md")
	if err != nil || body != "# guide\n" {
		t.Fatalf("regular file: body=%q err=%v", body, err)
	}
}

// The legacy state sidecar sits in the working tree, so a clone can commit
// it as a symlink. It is read as a regular file or not at all.
func TestReadStateDoesNotFollowASymlinkedSidecar(t *testing.T) {
	outside := t.TempDir()
	target := filepath.Join(outside, "state.json")
	if err := os.WriteFile(target, []byte(`{"pushed_at":"2026-01-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := t.TempDir()
	if err := os.Symlink(target, filepath.Join(repo, legacyStateFileName)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, ok := readState(repo); ok {
		t.Fatal("readState followed a symlinked sidecar")
	}
	// A regular sidecar is still read.
	plain := t.TempDir()
	if err := os.WriteFile(filepath.Join(plain, legacyStateFileName), []byte(`{"pushed_at":"2026-01-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := readState(plain); !ok {
		t.Fatal("readState refused a regular sidecar")
	}
}
