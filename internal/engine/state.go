// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/git"
)

// StateFileName is the basename of the per-clone sidecar file Corral writes
// *inside* the repository's Git directory. It records the most recent push
// timestamp the engine has observed for the upstream so subsequent runs can
// skip a no-op `git pull`.
//
// It lives in the Git directory rather than the working tree deliberately.
// Before v0.0.20 the sidecar sat at <repo>/.corral-state.json, which left an
// untracked file in every managed clone: `git status` reported every repo as
// dirty, and because both `corralctl prune` and the MCP delete tool refuse to
// touch a repo with local changes, neither could ever run. Anything under the
// Git directory is invisible to `git status` by construction, so no ignore-file
// management is required.
const StateFileName = "corral-state.json"

// LegacyStateFileName is the pre-v0.0.20 sidecar location, in the working tree.
// Still read so existing clones keep their smart-sync state across the upgrade,
// and removed on the next successful write.
const LegacyStateFileName = ".corral-state.json"

// statePath returns the current sidecar path for repoDir. It resolves the Git
// directory so worktrees (whose .git is a file, not a directory) land in the
// right place.
func statePath(repoDir string) (string, error) {
	gitDir, err := git.Dir(repoDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(gitDir, StateFileName), nil
}

// legacyStatePath returns the pre-v0.0.20 working-tree sidecar path.
func legacyStatePath(repoDir string) string {
	return filepath.Join(repoDir, LegacyStateFileName)
}

// cloneState is the JSON shape of <repo>/.corral-state.json. New fields must
// be added with omitempty so older sidecars continue to round-trip.
type cloneState struct {
	// LastSyncedPushedAt is the upstream PushedAt value at the time of the
	// last successful clone or sync.
	LastSyncedPushedAt time.Time `json:"last_synced_pushed_at"`
	// LastSyncedAt is the local wall-clock time of the last sync attempt
	// that touched the working tree (clone or successful pull). Used for
	// human display only — never for sync-skip decisions.
	LastSyncedAt time.Time `json:"last_synced_at"`
}

type stateTempFile interface {
	Write([]byte) (int, error)
	Close() error
	Name() string
}

var (
	createStateTemp = func(dir, pattern string) (stateTempFile, error) { return os.CreateTemp(dir, pattern) }
	renameStateFile = os.Rename
)

// readCloneState parses the sidecar for repoDir, preferring the current
// location inside the Git directory and falling back to the legacy working-tree
// path so clones written by an older corralctl keep their state. A missing file
// returns the zero value and a nil error so callers can treat "never synced"
// the same as "no state available". A malformed file surfaces as an error so
// the caller can decide whether to fall through to a full sync or abort.
func readCloneState(repoDir string) (cloneState, error) {
	// A repoDir that is not a repository has no state; treat it like a
	// missing sidecar rather than an error so callers fall through to a
	// full sync.
	path, err := statePath(repoDir)
	if err != nil {
		//nolint:nilerr // deliberate: "not a repository" is "no state", per the doc comment
		return cloneState{}, nil
	}
	s, err := readStateFile(path)
	if err == nil {
		return s, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return cloneState{}, err
	}
	// Fall back to the pre-v0.0.20 working-tree sidecar.
	s, err = readStateFile(legacyStatePath(repoDir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cloneState{}, nil
		}
		return cloneState{}, err
	}
	return s, nil
}

// readStateFile parses a single sidecar path. os.ErrNotExist is returned
// unwrapped so callers can distinguish "absent" from "corrupt".
func readStateFile(path string) (cloneState, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- path is derived from an engine-managed repository
	if err != nil {
		return cloneState{}, err
	}
	var s cloneState
	if err := json.Unmarshal(b, &s); err != nil {
		return cloneState{}, fmt.Errorf("malformed %s: %w", path, err)
	}
	return s, nil
}

// writeCloneState serialises s into the repository's Git directory atomically
// by writing to a sibling temp file and renaming it into place. A crash
// mid-write therefore leaves the previous valid state on disk rather than a
// half-written file that would fail to parse on the next run.
//
// On success any legacy working-tree sidecar is removed, so a clone created by
// an older corralctl stops showing up as untracked in `git status`.
func writeCloneState(repoDir string, s cloneState) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	finalPath, err := statePath(repoDir)
	if err != nil {
		return err
	}
	stateDir := filepath.Dir(finalPath)
	tmp, err := createStateTemp(stateDir, StateFileName+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup of the temp file if anything below fails before
	// the rename succeeds.
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := renameStateFile(tmpPath, finalPath); err != nil {
		return err
	}
	// Migration: the new sidecar is authoritative, so drop the working-tree
	// copy. Best-effort — a failure here leaves an untracked file behind but
	// must not fail the sync that already succeeded.
	_ = os.Remove(legacyStatePath(repoDir))
	return nil
}
