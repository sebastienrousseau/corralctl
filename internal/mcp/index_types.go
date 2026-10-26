// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"path/filepath"

	"github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/sebastienrousseau/corralctl/internal/sanitize"
)

// RepoEntry is one row in the workspace index. It captures the information
// agents most often want about a local clone without needing to spawn a
// `git` subprocess per repo.
type RepoEntry struct {
	// Name is the repository's basename (e.g. "corral").
	Name string `json:"name"`
	// Visibility is the visibility-directory the clone sits under
	// (typically "Public" or "Private"); empty when the layout does not
	// include a visibility segment.
	Visibility string `json:"visibility,omitempty"`
	// Language is the language-directory segment (lowercase, normalised
	// by corral on clone). Empty when not present in the layout.
	Language string `json:"language,omitempty"`
	// Path is the absolute on-disk path to the repository root.
	Path string `json:"path"`
	// RelPath is the path relative to the index root, joinable across
	// hosts (forward-slash separators).
	RelPath string `json:"rel_path"`
	// RemoteURL is the URL of the `origin` remote parsed from
	// .git/config; empty when unreadable.
	RemoteURL string `json:"remote_url,omitempty"`
	// State is the parsed contents of .corral-state.json when present.
	// nil when the sidecar is absent or unreadable.
	State *StateRecord `json:"state,omitempty"`

	// nameLower and relPathLower are Name and RelPath pre-lowercased for
	// Find, which is case-insensitive and runs over every entry.
	nameLower    string
	relPathLower string
}

// StateRecord mirrors the on-disk corral-state.json sidecar without
// importing internal/engine, which drives the terminal UI and would pull
// the Bubble Tea stack into the MCP server. import_boundary_test.go keeps
// that edge out.
type StateRecord struct {
	// LastSyncedPushedAt is the upstream pushed_at timestamp the engine
	// observed on the previous successful sync, formatted per RFC 3339.
	LastSyncedPushedAt string `json:"last_synced_pushed_at,omitempty"`
	// LastSyncedAt is when the engine last touched this clone, RFC 3339.
	LastSyncedAt string `json:"last_synced_at,omitempty"`
}

// Index is an in-memory snapshot of the workspace beneath a root
// directory. It is intentionally cheap to rebuild — every tool call
// triggers a fresh Scan — so the server stays correct as the user
// clones, syncs, and removes repos out-of-band without us having to
// implement filesystem watching.
type Index struct {
	// Root is the absolute path the index was built against.
	Root string
	// Repos is the discovered set of clones, sorted deterministically
	// by RelPath for stable agent output.
	Repos []RepoEntry
	// Truncated reports that the configured repository cap was reached.
	Truncated bool
}

// stateFileName mirrors engine.StateFileName without importing the
// engine package; kept in sync by hand because the value is part of
// corral's public on-disk contract.
const (
	stateFileName       = "corral-state.json"
	legacyStateFileName = ".corral-state.json"
)

// statePathsFor returns the sidecar paths to try for repoPath, current
// location first. A repoPath whose Git directory cannot be resolved yields
// only the legacy path, so a malformed clone degrades to "no state" rather
// than an error.
func statePathsFor(repoPath string) []string {
	legacy := filepath.Join(repoPath, legacyStateFileName)
	gitDir, err := git.Dir(repoPath)
	if err != nil {
		return []string{legacy}
	}
	return []string{filepath.Join(gitDir, stateFileName), legacy}
}

// Field bounds for untrusted values on their way to a model. Generous
// enough that no real repository is truncated — GitHub caps a repository
// name at 100 characters — and small enough that a hostile name cannot
// flood a context window.
const (
	maxEntryName   = 128
	maxEntryPath   = 1024
	maxEntryField  = 64
	maxEntryRemote = 512
)

// Redacted returns a copy of the entry with every attacker-controlled
// string bounded and stripped of characters that could hide or
// misrepresent it.
func (r RepoEntry) Redacted() RepoEntry {
	r.Name = sanitize.Untrusted(r.Name, maxEntryName)
	r.RelPath = sanitize.Untrusted(r.RelPath, maxEntryPath)
	r.Path = sanitize.Untrusted(r.Path, maxEntryPath)
	r.RemoteURL = sanitize.Untrusted(r.RemoteURL, maxEntryRemote)
	r.Visibility = sanitize.Untrusted(r.Visibility, maxEntryField)
	r.Language = sanitize.Untrusted(r.Language, maxEntryField)
	if r.State != nil {
		state := *r.State
		state.LastSyncedAt = sanitize.Untrusted(state.LastSyncedAt, maxEntryField)
		state.LastSyncedPushedAt = sanitize.Untrusted(state.LastSyncedPushedAt, maxEntryField)
		r.State = &state
	}
	return r
}

// RedactedEntries returns entries with Redacted applied to each. The
// index itself is never mutated: callers keep querying the exact values.
func RedactedEntries(entries []RepoEntry) []RepoEntry {
	if entries == nil {
		return nil
	}
	out := make([]RepoEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Redacted())
	}
	return out
}
