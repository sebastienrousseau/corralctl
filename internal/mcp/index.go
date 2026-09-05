// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package mcp implements the corral Model Context Protocol server: a
// stdio-based JSON-RPC server that exposes the local Corral-organised
// workspace (cloned repositories under ~/Code) to AI coding agents via
// the read-only tools and resources defined in this package.
//
// The server is a wedge for the "local index for AI" positioning
// described in the v0.0.8 design doc: GitHub's own MCP server already
// covers the remote API surface with 50+ tools, so corral-mcp focuses
// on the dimension only it can serve — a developer's already-cloned
// local mirror, organised by visibility and language, queryable
// without a network round-trip.
package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/diag"
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
	//
	// Lowercasing inside the loop allocated once per repository per call —
	// 999 allocations to answer one lookup on a 1,000-repository workspace,
	// on a path three of the eight tools reach. Computing them during the
	// scan that already touches every repository makes the query allocate
	// nothing.
	//
	// Unexported, so encoding/json ignores them and the wire shape is
	// unchanged; they copy by value with the struct, so Redacted() keeps
	// working without knowing about them.
	nameLower    string
	relPathLower string
}

// StateRecord mirrors the on-disk .corral-state.json sidecar without
// importing internal/engine (which would create a dependency cycle —
// internal/engine already imports internal/git, and corral-mcp will need
// to import internal/engine in later phases for sync operations).
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
//
// Since v0.0.20 the sidecar lives inside the repository's Git directory so it
// no longer shows up in `git status`. legacyStateFileName is the pre-v0.0.20
// working-tree location, still read so this server reports accurate state for
// clones an older corralctl wrote.
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

// maxIndexDepth bounds the walk so a misconfigured root (e.g. $HOME)
// cannot blow up scan time or memory on a deeply nested filesystem.
// Three levels covers the documented Visibility/Language/Repo layout
// plus a generous one-level slack for custom Layouts.
const maxIndexDepth = 4

// maxIndexRepos bounds memory and response size for accidentally broad roots.
var maxIndexRepos = 10_000

var (
	absIndex         = filepath.Abs
	statIndex        = os.Stat
	walkIndex        = filepath.WalkDir
	relIndex         = filepath.Rel
	readStateFile    = os.ReadFile
	absSafePath      = filepath.Abs
	relSafePath      = filepath.Rel
	evalSafePath     = filepath.EvalSymlinks
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

// Scan walks root looking for directories that contain a .git child
// and returns an Index. The walk stops descending into a directory
// once it has been identified as a repository root (no recursing into
// nested submodules or vendor trees) and respects maxIndexDepth.
//
// Per-entry errors are tolerated: a single unreadable directory does
// not abort the whole scan. The function only returns an error when
// the root itself is unreadable, so a caller can distinguish a
// misconfigured root from an empty workspace.
func Scan(root string) (*Index, error) {
	absRoot, err := absIndex(root)
	if err != nil {
		return nil, fmt.Errorf("resolving root: %w", err)
	}
	info, err := statIndex(absRoot)
	if err != nil {
		return nil, fmt.Errorf("stat root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("root %q is not a directory", absRoot)
	}

	idx := &Index{Root: absRoot}

	// Discovery and enrichment run as a pipeline, not as two passes.
	//
	// The walk hands each repository to a pool the moment it finds one, so
	// a worker opens that repository's .git/config while its directory is
	// still hot from the walk that just stat'd it. Collecting every path
	// first and enriching afterwards threw that locality away: it was
	// measurably slower on a small workspace than doing the work inline,
	// even with the fan-out disabled.
	//
	// Workers append in whatever order they finish. That is safe because
	// the sort below — which this function has always done — is what
	// establishes determinism, not the order of arrival.
	work := make(chan string, scanQueueDepth)
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		maxPool = scanWorkers()
		pool    int
	)
	spawn := func() {
		pool++
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Accumulate locally and merge once, so the lock is taken per
			// worker rather than per repository.
			var local []RepoEntry
			for path := range work {
				local = append(local, buildEntry(absRoot, path))
			}
			if len(local) == 0 {
				return
			}
			mu.Lock()
			idx.Repos = append(idx.Repos, local...)
			mu.Unlock()
		}()
	}
	// A small pool up front, grown under backpressure.
	//
	// Neither extreme is right. Starting at the full pool makes a
	// ten-repository workspace pay for two dozen goroutines and their
	// result slices, which showed up as +14% allocations. Starting at one
	// leaves that same workspace enriching serially, which cost 7% in
	// wall time — the walk is fast enough that even ten repositories
	// benefit from overlap. Seeding a handful captures the overlap
	// immediately and lets the queue ask for the rest.
	for i := 0; i < initialScanWorkers && i < maxPool; i++ {
		spawn()
	}

	found := 0

	// Per-entry errors inside the walk are swallowed (logged or ignored)
	// because the walk is best-effort discovery, not a transactional
	// scan: surfacing every unreadable directory would force the agent
	// into noise.
	_ = walkIndex(absRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if found >= maxIndexRepos {
			idx.Truncated = true
			return fs.SkipAll
		}
		if walkErr != nil {
			// Unreadable: skip its subtree without aborting the scan.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		rel, err := relIndex(absRoot, path)
		if err != nil {
			return fs.SkipDir
		}
		depth := 0
		if rel != "." {
			depth = strings.Count(rel, string(filepath.Separator)) + 1
		}
		if depth > maxIndexDepth {
			return fs.SkipDir
		}
		// The workspace root is a container, never a repository entry, even
		// when it happens to be under version control itself. Without this the
		// first callback matched the root, appended it as the sole entry and
		// SkipDir aborted the entire walk — so `corralctl mcp --root ~/Code`
		// with dotfiles or a monorepo at that path reported exactly one repo
		// and `corral_delete_repo` could resolve the workspace root itself.
		// findLocalRepos and the engine's discovery walk already do this.
		if path == absRoot {
			return nil
		}
		// Accept both regular clones and linked worktrees (.git is a file).
		if git.IsRepository(path) {
			found++
			// Grow the pool only when the queue is genuinely backed up,
			// which is the signal that enrichment is behind the walk.
			select {
			case work <- path:
			default:
				if pool < maxPool {
					spawn()
				}
				work <- path
			}
			return fs.SkipDir
		}
		return nil
	})

	close(work)
	wg.Wait()

	sort.Slice(idx.Repos, func(i, j int) bool {
		return idx.Repos[i].RelPath < idx.Repos[j].RelPath
	})
	return idx, nil
}

// initialScanWorkers is the pool the walk starts with, before any
// backpressure. Enough to overlap enrichment with the walk on a small
// workspace without allocating a slice per idle worker.
const initialScanWorkers = 4

// scanQueueDepth buffers the walk's handoff to the pool.
//
// Deliberately shallow. The queue filling is the signal that enrichment has
// fallen behind the walk, and it is what grows the pool — so a deep buffer
// would absorb the backpressure and leave a single worker draining a large
// workspace on its own.
const scanQueueDepth = 16

// maxScanWorkers caps the pool regardless of core count, so a machine with
// many cores does not open an unreasonable number of files at once.
const maxScanWorkers = 32

// scanWorkers bounds how many repositories are enriched at once.
//
// Deliberately a multiple of GOMAXPROCS rather than equal to it. A profile
// of the original serial scan put 97% of its samples in syscalls and
// essentially none in user code: every worker spends its time waiting on
// the filesystem, not computing, so oversubscribing the cores is what
// actually overlaps the waiting.
//
// A var so a test can pin it.
var scanWorkers = func() int {
	return clampWorkers(runtime.GOMAXPROCS(0) * 4)
}

// clampWorkers bounds a proposed worker count to [1, maxScanWorkers].
//
// Split out from scanWorkers because the bounds are otherwise untestable:
// whether either applies depends on the host's core count, so on any given
// machine one or both branches can never be taken.
func clampWorkers(n int) int {
	if n > maxScanWorkers {
		return maxScanWorkers
	}
	if n < 1 {
		return 1
	}
	return n
}

// buildEntry constructs a RepoEntry from a discovered repository path,
// extracting Visibility/Language from the leading path segments under
// the index root and best-effort enriching with remote URL + sidecar
// state.
func buildEntry(root, repoPath string) RepoEntry {
	rel, _ := filepath.Rel(root, repoPath)
	rel = filepath.ToSlash(rel)

	name := filepath.Base(repoPath)
	entry := RepoEntry{
		Name:         name,
		Path:         repoPath,
		RelPath:      rel,
		nameLower:    strings.ToLower(name),
		relPathLower: strings.ToLower(rel),
	}

	// Map leading segments to Visibility / Language. Layouts that don't
	// follow the default Visibility/Language/Repo schema simply leave
	// the corresponding fields empty.
	parts := strings.Split(rel, "/")
	if len(parts) >= 3 {
		entry.Visibility = parts[0]
		entry.Language = parts[1]
	} else if len(parts) == 2 {
		entry.Language = parts[0]
	}

	if url, err := git.RemoteOriginFromConfig(repoPath); err == nil {
		entry.RemoteURL = url
	}
	if state, ok := readState(repoPath); ok {
		entry.State = state
	}
	return entry
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

// Find returns the entry whose Name, RelPath, or RemoteURL repo segment
// equals or has the supplied query as a suffix. It is the primitive
// behind the corral_find_repo tool. Returns ErrRepoNotFound when no
// candidate matches and ErrAmbiguous when multiple do — the caller
// should surface both for the agent to disambiguate.
func (i *Index) Find(query string) (*RepoEntry, error) {
	if query == "" {
		return nil, ErrRepoNotFound
	}
	q := strings.ToLower(strings.TrimSpace(query))
	// The suffix form is built once rather than per entry.
	suffix := "/" + q
	var matches []*RepoEntry
	for idx := range i.Repos {
		r := &i.Repos[idx]
		// Compare against the precomputed keys. An entry built by hand
		// rather than by buildEntry has empty keys, so fall back to
		// EqualFold for it: a caller constructing RepoEntry literals — the
		// tests do — must still be findable.
		name, rel := r.nameLower, r.relPathLower
		if name == "" && rel == "" {
			if strings.EqualFold(r.Name, q) ||
				strings.EqualFold(r.RelPath, q) ||
				strings.HasSuffix(strings.ToLower(r.RelPath), suffix) {
				matches = append(matches, r)
			}
			continue
		}
		if name == q || rel == q || strings.HasSuffix(rel, suffix) {
			matches = append(matches, r)
		}
	}
	switch len(matches) {
	case 0:
		return nil, ErrRepoNotFound
	case 1:
		return matches[0], nil
	default:
		names := make([]string, 0, len(matches))
		for _, m := range matches {
			// This error text reaches the model, so the paths in it are
			// an output boundary like any other.
			names = append(names, sanitize.Untrusted(m.RelPath, maxEntryPath))
		}
		return nil, fmt.Errorf("%w: %s", ErrAmbiguous, strings.Join(names, ", "))
	}
}

// ErrRepoNotFound is returned by Index.Find when no entry matches.
var ErrRepoNotFound = errors.New("no repository matches the query")

// ErrAmbiguous is returned by Index.Find when more than one entry
// matches and the caller must disambiguate.
var ErrAmbiguous = errors.New("multiple repositories match the query")

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
//
// Applied on the way out, never at construction: Path is what
// SafeMutationPath resolves and what git is handed, so the stored value
// must stay byte-exact. Sanitising in buildEntry would have made the
// index disagree with the filesystem — a worse bug than the one it fixes.
//
// Every field here is chosen by someone else. A repository's directory
// name is its owner's, and `corralctl topic:…` clones repositories the
// user never named; RemoteURL is read from .git/config; Visibility and
// Language are path segments under the workspace root.
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
