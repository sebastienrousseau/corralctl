// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package discover

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type mockDirEntry struct {
	name  string
	isDir bool
}

func (m mockDirEntry) Name() string               { return m.name }
func (m mockDirEntry) IsDir() bool                { return m.isDir }
func (m mockDirEntry) Type() fs.FileMode          { return 0 }
func (m mockDirEntry) Info() (fs.FileInfo, error) { return nil, nil }

func TestDiscoverValidationErrors(t *testing.T) {
	ctx := context.Background()

	// Empty base dir
	if _, err := Discover(ctx, Options{BaseDir: ""}); err == nil {
		t.Fatal("expected error for empty base dir")
	}

	// Stat failure on base dir
	origStat := statPath
	defer func() { statPath = origStat }()
	statPath = func(name string) (os.FileInfo, error) {
		return nil, errors.New("disk failure")
	}
	if _, err := Discover(ctx, Options{BaseDir: "/mock"}); err == nil {
		t.Fatal("expected error on stat failure")
	}

	// Not a directory
	tmpFile := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(tmpFile, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	statPath = origStat
	if _, err := Discover(ctx, Options{BaseDir: tmpFile}); err == nil {
		t.Fatal("expected error for non-directory base path")
	}
}

func TestDiscoverWalkAndPrune(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()

	// Create repository 1 (untracked, Go)
	repo1 := filepath.Join(base, "repo1")
	if err := os.MkdirAll(filepath.Join(repo1, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo1, "go.mod"), []byte("module test"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Create repository 2 (has remote, Rust)
	repo2 := filepath.Join(base, "repo2")
	if err := os.MkdirAll(filepath.Join(repo2, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo2, "Cargo.toml"), []byte("[package]"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Create ignored directory (node_modules) with pseudo repo inside
	ignoredRepo := filepath.Join(base, "node_modules", "some-pkg")
	if err := os.MkdirAll(filepath.Join(ignoredRepo, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}

	// Create a plain file at root
	if err := os.WriteFile(filepath.Join(base, "rootfile.txt"), []byte("plain"), 0o600); err != nil {
		t.Fatal(err)
	}

	origRemote := remoteOrigin
	origBranch := currentBranch
	origEmpty := isEmpty
	defer func() {
		remoteOrigin = origRemote
		currentBranch = origBranch
		isEmpty = origEmpty
	}()

	remoteOrigin = func(dir string) (string, error) {
		if strings.Contains(dir, "repo2") {
			return "git@github.com:owner/repo2.git", nil
		}
		return "", errors.New("no remote")
	}
	currentBranch = func(ctx context.Context, dir string) (string, error) {
		if strings.Contains(dir, "repo1") {
			return "feature", nil
		}
		return "", errors.New("detached")
	}
	isEmpty = func(ctx context.Context, dir string) bool {
		return strings.Contains(dir, "repo1")
	}

	// Discover all repos
	all, err := Discover(ctx, Options{BaseDir: base, UntrackedOnly: false})
	if err != nil {
		t.Fatalf("unexpected discover error: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 repositories, got %d", len(all))
	}
	if all[0].Name != "repo1" || all[0].DetectedLang != "Go" || all[0].DefaultBranch != "feature" || !all[0].IsEmpty || all[0].HasRemote {
		t.Fatalf("unexpected repo1 metadata: %+v", all[0])
	}
	if all[1].Name != "repo2" || all[1].DetectedLang != "Rust" || all[1].DefaultBranch != "main" || all[1].IsEmpty || !all[1].HasRemote {
		t.Fatalf("unexpected repo2 metadata: %+v", all[1])
	}

	// Discover untracked only
	untracked, err := Discover(ctx, Options{BaseDir: base, UntrackedOnly: true})
	if err != nil {
		t.Fatalf("unexpected discover error: %v", err)
	}
	if len(untracked) != 1 || untracked[0].Name != "repo1" {
		t.Fatalf("expected 1 untracked repository, got %d", len(untracked))
	}
}

func TestDiscoverDepthAndExclusions(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()

	deepDir := filepath.Join(base, "level1", "level2", "deeprepo")
	if err := os.MkdirAll(filepath.Join(deepDir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	exclDir := filepath.Join(base, "excluded", "exclrepo")
	if err := os.MkdirAll(filepath.Join(exclDir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}

	// MaxDepth limits recursion
	res, err := Discover(ctx, Options{
		BaseDir:  base,
		MaxDepth: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res {
		if c.Name == "deeprepo" {
			t.Fatal("expected deeprepo to be skipped by MaxDepth")
		}
	}

	// ExcludePaths filters out matching prefix
	resExcl, err := Discover(ctx, Options{
		BaseDir:      base,
		ExcludePaths: []string{filepath.Join(base, "excluded")},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range resExcl {
		if c.Name == "exclrepo" {
			t.Fatal("expected exclrepo to be excluded")
		}
	}
}

func TestDiscoverErrorsAndCancellation(t *testing.T) {
	base := t.TempDir()
	origWalk := walkDir
	defer func() { walkDir = origWalk }()

	// Cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Discover(ctx, Options{BaseDir: base})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}

	// WalkDir root error
	walkDir = func(root string, fn fs.WalkDirFunc) error {
		return fn(root, mockDirEntry{name: filepath.Base(root), isDir: true}, errors.New("root permission denied"))
	}
	if _, err := Discover(context.Background(), Options{BaseDir: base}); err == nil {
		t.Fatal("expected walk error on root")
	}

	// WalkDir nested error is skipped
	walkDir = func(root string, fn fs.WalkDirFunc) error {
		_ = fn(filepath.Join(root, "nested"), mockDirEntry{name: "nested", isDir: true}, errors.New("skip me"))
		return nil
	}
	res, err := Discover(context.Background(), Options{BaseDir: base})
	if err != nil || len(res) != 0 {
		t.Fatalf("expected nil error and 0 repos, got err: %v, len: %d", err, len(res))
	}
}

func TestDetectLanguages(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		file string
		lang string
	}{
		{"package.json", "TypeScript"},
		{"pyproject.toml", "Python"},
		{"requirements.txt", "Python"},
		{"pom.xml", "Java"},
		{"build.gradle", "Java"},
		{"Package.swift", "Swift"},
	}

	for _, tt := range tests {
		sub := filepath.Join(dir, tt.lang+"-"+tt.file)
		if err := os.MkdirAll(sub, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, tt.file), []byte("content"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := detectLanguage(sub); got != tt.lang {
			t.Errorf("for %s expected %s, got %s", tt.file, tt.lang, got)
		}
	}

	emptyDir := filepath.Join(dir, "empty")
	if err := os.MkdirAll(emptyDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if got := detectLanguage(emptyDir); got != "other" {
		t.Errorf("expected other, got %s", got)
	}
}
