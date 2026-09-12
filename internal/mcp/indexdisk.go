// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"github.com/sebastienrousseau/corralctl/internal/search"
)

// Where the trigram index lives between runs.
//
// In memory, a complete index over this workspace was 181MB of heap that the
// runtime could never give back, and keeping it inside a budget meant dropping
// the postings that make an index selective. On disk and mapped, its size stops
// being a budget to police: the kernel keeps the pages a query touches and
// reclaims the rest, and a restart costs a walk rather than a rebuild.
//
// $XDG_CACHE_HOME/corral/index, matching where the symbol cache goes and for
// the same reason — this is derived data that can be rebuilt from the
// workspace at any moment, which is the distinction XDG draws between a cache
// and state.

// errIndexDisabled reports that persistence, and with it the index, is off.
var errIndexDisabled = errors.New("mcp: index cache is disabled")

// DefaultIndexCacheDir returns the platform-default location for the index.
func DefaultIndexCacheDir() string {
	if cache := os.Getenv("XDG_CACHE_HOME"); cache != "" {
		return filepath.Join(cache, "corral", "index")
	}
	home, err := auditUserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "corral", "index")
	}
	return filepath.Join(home, ".cache", "corral", "index")
}

// indexPathFor is where one repository's index file lives.
//
// Named by a hash of the absolute path rather than by the path itself: a
// repository path contains separators and characters a filename cannot hold,
// and the basename is kept only to make the directory readable by a human
// wondering what is in their cache.
func (s *Server) indexPathFor(repoPath string) string {
	sum := sha256.Sum256([]byte(repoPath))
	name := filepath.Base(repoPath) + "-" + hex.EncodeToString(sum[:8]) + ".idx"
	return filepath.Join(s.indexDir, name)
}

// loadOrBuildIndex returns a mapped index for one repository, building and
// writing it first if what is on disk is missing or stale.
//
// The fingerprint comes from a walk, which costs a directory traversal and no
// file reads. That is the whole reason the index is validated rather than
// rebuilt: a rebuild reads every byte of the repository, and a walk does not.
func (s *Server) loadOrBuildIndex(ctx context.Context, repo *RepoEntry) (*search.MappedIndex, error) {
	if s.indexDir == "" {
		return nil, errIndexDisabled
	}
	allowed := func(rel string) bool {
		_, ok := fileAllowed(rel, s.extraFileExts)
		return ok
	}

	fp, err := search.Fingerprint(ctx, repo.Path, allowed)
	if err != nil {
		return nil, err
	}

	path := s.indexPathFor(repo.Path)
	if mi, err := search.OpenIndex(path, fp); err == nil {
		return mi, nil
	}

	// Missing, stale, or unreadable: rebuild. Every failure above leads here,
	// because there is no case where serving a doubtful index beats spending
	// the time to make a sound one.
	ix, err := buildIndex(ctx, repo.Path, allowed)
	if err != nil {
		return nil, err
	}
	if err := search.WriteIndex(path, ix, fp); err != nil {
		// The index is good, it just could not be persisted — an unwritable
		// cache directory should make the next start slower, not fail the
		// search. Map nothing and let the caller use the in-memory form.
		return nil, err
	}
	return search.OpenIndex(path, fp)
}

// indexCacheDir resolves the configured index directory.
//
// "off" is honoured because a mapped index is a file on someone's disk, and a
// machine where that is unwelcome should be slower, not broken.
func indexCacheDir(dir string) string {
	if dir == "off" {
		return ""
	}
	if dir == "" {
		return DefaultIndexCacheDir()
	}
	return dir
}
