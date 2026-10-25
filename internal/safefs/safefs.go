// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package safefs opens files inside a directory tree without following
// symbolic links.
//
// A clone can commit a symlink to any path on disk. Code that walks a
// repository and then reads what it found must not let such a link
// redirect the read: not out of the workspace (a link to ~/.aws/credentials),
// and not onto a refused file inside it (readme.md pointing at .env). The
// rule here is the simplest one that holds: below the given root, every
// component of the path must be a real directory or file, the last one must
// be a regular file, and the file opened must be the one that was checked.
//
// The root itself may sit behind a symlink (/tmp on macOS, a workspace
// moved with a link); only the part of the path below it is checked.
package safefs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrSymlink reports a symbolic link in the path below the root.
var ErrSymlink = errors.New("path contains a symbolic link")

// ErrNotRegular reports that the path is not a regular file.
var ErrNotRegular = errors.New("not a regular file")

// ErrOutsideRoot reports a relative path that leaves the root.
var ErrOutsideRoot = errors.New("path leaves the root")

// ErrChanged reports that the file opened is not the file that was checked:
// the path was replaced between the check and the open.
var ErrChanged = errors.New("file changed while being opened")

// Seams over the filesystem calls, so tests can reach every branch,
// including the replace-between-check-and-open race.
var (
	lstat    = os.Lstat
	openFile = os.Open
	fstat    = func(f *os.File) (fs.FileInfo, error) { return f.Stat() }
)

// OpenRegular opens root/rel for reading. It refuses the path if rel leaves
// root, if any component below root is a symbolic link, if the last
// component is not a regular file, or if the file opened is not the one
// that was checked. rel uses either separator.
func OpenRegular(root, rel string) (*os.File, fs.FileInfo, error) {
	full, checked, err := checkPath(root, rel)
	if err != nil {
		return nil, nil, err
	}
	f, err := openFile(full) // #nosec G304 -- every component below root was checked by checkPath
	if err != nil {
		return nil, nil, err
	}
	opened, err := fstat(f)
	if err != nil {
		_ = f.Close()
		return nil, nil, err
	}
	if !os.SameFile(checked, opened) {
		_ = f.Close()
		return nil, nil, fmt.Errorf("%s: %w", rel, ErrChanged)
	}
	return f, opened, nil
}

// ReadFile reads root/rel with OpenRegular, refusing a file larger than
// limit bytes.
func ReadFile(root, rel string, limit int64) ([]byte, error) {
	f, info, err := OpenRegular(root, rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	if info.Size() > limit {
		return nil, fmt.Errorf("%s: %d bytes is over the %d-byte limit", rel, info.Size(), limit)
	}
	return io.ReadAll(io.LimitReader(f, limit))
}

// escapesRoot reports whether a cleaned relative path names the root
// itself or anything not below it. On Windows a path can be rooted
// without being absolute (`\etc`, `/etc`) or carry a drive (`C:x`); both
// are refused rather than joined under the root.
func escapesRoot(clean string) bool {
	sep := string(filepath.Separator)
	return clean == "." || clean == ".." ||
		strings.HasPrefix(clean, ".."+sep) ||
		filepath.IsAbs(clean) ||
		filepath.VolumeName(clean) != "" ||
		strings.HasPrefix(clean, sep)
}

// checkPath walks rel below root with lstat and returns the full path and
// the info of its last component.
func checkPath(root, rel string) (string, fs.FileInfo, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if escapesRoot(clean) {
		return "", nil, fmt.Errorf("%s: %w", rel, ErrOutsideRoot)
	}
	cur := root
	var info fs.FileInfo
	for _, part := range strings.Split(clean, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		i, err := lstat(cur)
		if err != nil {
			return "", nil, err
		}
		if i.Mode()&fs.ModeSymlink != 0 {
			return "", nil, fmt.Errorf("%s: %w", rel, ErrSymlink)
		}
		info = i
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%s: %w", rel, ErrNotRegular)
	}
	return cur, info, nil
}
