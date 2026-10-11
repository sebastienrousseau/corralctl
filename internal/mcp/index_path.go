// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"fmt"
	"path/filepath"
	"strings"
)

var (
	absSafePath  = filepath.Abs
	relSafePath  = filepath.Rel
	evalSafePath = filepath.EvalSymlinks
)

// SafePath validates that path resolves to a file or directory beneath
// the index root, blocking directory-traversal attempts via the
// corral_get_file tool and the corral://repo/{org}/{name}/file/{path}
// resource. Returns the cleaned absolute path on success.
//
// Both the root and the candidate's existing ancestors are
// canonicalised via EvalSymlinks. This matters on macOS where /tmp is
// a symlink to /private/tmp: without canonicalising both sides of the
// rel-prefix check, every lookup spuriously "escapes" the root. When
// the candidate itself doesn't exist, the canonicalisation walks up
// to the deepest existing ancestor and reconstructs the path, so
// would-be lookups (e.g. for a file the caller is about to create)
// still get the same security checks as existing-file lookups.
func (i *Index) SafePath(path string) (string, error) {
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(i.Root, path)
	}
	rawAbs, err := absSafePath(candidate)
	if err != nil {
		return "", fmt.Errorf("resolving path: %w", err)
	}

	rootCanon := i.Root
	if r, err := evalSafePath(i.Root); err == nil {
		rootCanon = r
	}

	absCanon := canonicalizeExistingPrefix(rawAbs)

	rel, err := relSafePath(rootCanon, absCanon)
	// Compare path segments, not a raw string prefix: a repository legitimately
	// named "..foo" is inside the root and must not be rejected.
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes root %q", path, i.Root)
	}
	return absCanon, nil
}

// SafeMutationPath is SafePath with one extra restriction: the workspace root
// itself is never a valid target.
//
// SafePath accepts rel == "." because reading and listing the root is
// legitimate. A mutation is different — resolving to the root means
// corral_delete_repo would rm -rf the entire workspace, which is what happened
// when the root was itself a git repository and Scan collapsed to a single
// entry named after the root's basename.
func (i *Index) SafeMutationPath(path string) (string, error) {
	safe, err := i.SafePath(path)
	if err != nil {
		return "", err
	}
	rootCanon := i.Root
	if r, err := evalSafePath(i.Root); err == nil {
		rootCanon = r
	}
	rel, err := relSafePath(rootCanon, safe)
	if err != nil {
		return "", fmt.Errorf("resolving path: %w", err)
	}
	if rel == "." {
		return "", fmt.Errorf("refusing to mutate the workspace root %q itself", i.Root)
	}
	return safe, nil
}

// canonicalizeExistingPrefix returns abs with its longest existing
// prefix canonicalised via EvalSymlinks and the non-existing tail
// re-appended. If abs itself exists, EvalSymlinks handles it directly;
// otherwise we walk up looking for an existing ancestor whose
// canonical form we can use. Falls back to the raw path when no
// ancestor resolves (shouldn't happen on a normal POSIX root).
func canonicalizeExistingPrefix(abs string) string {
	if resolved, err := evalSafePath(abs); err == nil {
		return resolved
	}
	dir := abs
	var suffixParts []string
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs
		}
		if resolved, err := evalSafePath(parent); err == nil {
			out := resolved
			// Re-append the un-resolved children in original order.
			suffixParts = append([]string{filepath.Base(dir)}, suffixParts...)
			for _, p := range suffixParts {
				out = filepath.Join(out, p)
			}
			return out
		}
		suffixParts = append([]string{filepath.Base(dir)}, suffixParts...)
		dir = parent
	}
}
