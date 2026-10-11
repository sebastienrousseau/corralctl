// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/diag"
	"github.com/sebastienrousseau/corralctl/internal/git"
)

var (
	readStateFile    = os.ReadFile
	renameSyncFile   = os.Rename
	marshalSyncState = json.MarshalIndent
)

type syncTempFile interface {
	Write([]byte) (int, error)
	Close() error
	Name() string
}

var createSyncTemp = func(dir, pattern string) (syncTempFile, error) {
	return os.CreateTemp(dir, pattern)
}

// readState parses the .corral-state.json sidecar. A missing file is
// not an error — it is the expected state for any clone made before
// the smart-sync feature shipped or for clones managed outside corral.
// A file that is present but malformed IS an error, and gets logged to
// stderr (never stdout, which is reserved for the JSON-RPC protocol
// stream) so operators can trace bad sidecars without breaking the
// tool call itself.
func readState(repoPath string) (*StateRecord, bool) {
	for _, path := range statePathsFor(repoPath) {
		b, err := readStateFile(path) // #nosec G304 -- repoPath comes from the root-confined index
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				diag.Debugf("corral-mcp: read state %s: %v", path, err)
			}
			continue
		}
		var s StateRecord
		if err := json.Unmarshal(b, &s); err != nil {
			diag.Debugf("corral-mcp: parse state %s: %v", path, err)
			continue
		}
		return &s, true
	}
	return nil, false
}

// markStateSynced records a successful MCP-triggered pull while preserving the
// last upstream pushed_at value observed by the GitHub-backed sync engine.
func markStateSynced(repoPath string) error {
	state, ok := readState(repoPath)
	if !ok {
		state = &StateRecord{}
	}
	state.LastSyncedAt = time.Now().UTC().Format(time.RFC3339Nano)
	b, err := marshalSyncState(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal sync state: %w", err)
	}
	gitDir, err := git.Dir(repoPath)
	if err != nil {
		return fmt.Errorf("resolve git dir: %w", err)
	}
	tmp, err := createSyncTemp(gitDir, stateFileName+".*.tmp")
	if err != nil {
		return fmt.Errorf("create sync state: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write sync state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close sync state: %w", err)
	}
	if err := renameSyncFile(tmpPath, filepath.Join(gitDir, stateFileName)); err != nil {
		return fmt.Errorf("replace sync state: %w", err)
	}
	// Migration: drop the pre-v0.0.20 working-tree sidecar now that the
	// authoritative copy lives in the Git directory. Best-effort.
	_ = os.Remove(filepath.Join(repoPath, legacyStateFileName))
	return nil
}
