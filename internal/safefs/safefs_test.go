// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package safefs

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func TestOpenRegularReadsARegularFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "docs", "a.md"), "hello")
	f, info, err := OpenRegular(root, "docs/a.md")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if info.Size() != 5 {
		t.Fatalf("size = %d", info.Size())
	}
}

// The root itself may be reached through a link; only what is below it is
// checked.
func TestOpenRegularAllowsALinkedRoot(t *testing.T) {
	real := t.TempDir()
	writeFile(t, filepath.Join(real, "a.md"), "hello")
	linked := filepath.Join(t.TempDir(), "root")
	symlink(t, real, linked)
	f, _, err := OpenRegular(linked, "a.md")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

func TestOpenRegularRefuses(t *testing.T) {
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret"), "x")
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "real", "a.md"), "x")
	symlink(t, filepath.Join(outside, "secret"), filepath.Join(root, "file-link.md"))
	symlink(t, outside, filepath.Join(root, "dir-link"))
	symlink(t, "real/a.md", filepath.Join(root, "inside-link.md"))

	cases := map[string]error{
		"file-link.md":    ErrSymlink,
		"dir-link/secret": ErrSymlink,
		"inside-link.md":  ErrSymlink,
		"real":            ErrNotRegular,
		".":               ErrOutsideRoot,
		"..":              ErrOutsideRoot,
		"../escape":       ErrOutsideRoot,
		"real/../../x":    ErrOutsideRoot,
		"/etc/passwd":     ErrOutsideRoot,
	}
	if runtime.GOOS == "windows" {
		cases[`\etc\passwd`] = ErrOutsideRoot
		cases[`C:x`] = ErrOutsideRoot
		cases[`C:\Windows\win.ini`] = ErrOutsideRoot
		cases[`\\server\share\x`] = ErrOutsideRoot
	}
	for rel, want := range cases {
		if _, _, err := OpenRegular(root, rel); !errors.Is(err, want) {
			t.Errorf("%s: err = %v, want %v", rel, err, want)
		}
	}
	if _, _, err := OpenRegular(root, "missing.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing: err = %v", err)
	}
}

func TestOpenRegularSurfacesOpenAndStatFailures(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.md"), "x")
	writeFile(t, filepath.Join(root, "b.md"), "y")
	oldOpen, oldStat := openFile, fstat
	t.Cleanup(func() { openFile, fstat = oldOpen, oldStat })

	boom := errors.New("boom")
	openFile = func(string) (*os.File, error) { return nil, boom }
	if _, _, err := OpenRegular(root, "a.md"); !errors.Is(err, boom) {
		t.Errorf("open failure: %v", err)
	}

	openFile = oldOpen
	fstat = func(*os.File) (fs.FileInfo, error) { return nil, boom }
	if _, _, err := OpenRegular(root, "a.md"); !errors.Is(err, boom) {
		t.Errorf("stat failure: %v", err)
	}

	// The path is swapped for another file between the check and the open.
	fstat = oldStat
	openFile = func(string) (*os.File, error) { return os.Open(filepath.Join(root, "b.md")) } // #nosec G304 -- test file in t.TempDir()
	if _, _, err := OpenRegular(root, "a.md"); !errors.Is(err, ErrChanged) {
		t.Errorf("swap: err = %v, want ErrChanged", err)
	}
}

func TestReadFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.md"), "hello")
	got, err := ReadFile(root, "a.md", 64)
	if err != nil || string(got) != "hello" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := ReadFile(root, "a.md", 2); err == nil {
		t.Fatal("over-limit file was read")
	}
	if _, err := ReadFile(root, "missing.md", 64); err == nil {
		t.Fatal("missing file was read")
	}
}
