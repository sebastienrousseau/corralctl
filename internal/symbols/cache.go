// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package symbols

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"time"
)

// Persisting the symbol index.
//
// Extraction is the expensive thing this server does. Measured against a
// real 187-repository workspace: a workspace scan answers in 10–60 ms,
// while the first cross-repository symbol lookup takes 6.9 seconds — and
// because the in-memory cache holds only a couple of dozen repositories,
// most of that is paid again on the next query, and all of it again after
// a restart. A client that launches the server per session pays it every
// session.
//
// # Why a file per repository, and not a database
//
// The obvious answer is SQLite, and it is the wrong one here. corral holds
// few direct dependencies and a hand-maintained SBOM that CI checks;
// the pure-Go SQLite driver is an order of magnitude larger than corral
// itself, and the fast one is CGO, which ADR-0006 already rules out. None
// of what a database offers is needed: there are no joins, no
// transactions, no concurrent writers to reconcile, and no queries beyond
// "give me this repository's symbols".
//
// What is needed is that a stale entry can never be served, and that one
// corrupt file cannot break the index. A file per repository gives both:
// invalidation is per repository, and a file that fails to parse is a
// cache miss rather than a failure.
//
// Entries are gzipped. Uncompressed, a real 187-repository workspace
// produced 53 MB of cache — defensible but rude for something that lives
// in a user's home directory, and the content is JSON with the same dozen
// field names repeated a million times, which is close to the ideal case
// for a compressor. Decompression is far cheaper than the extraction it
// replaces.
//
// # Why the fingerprint is trustworthy
//
// A cache hit still walks the repository. The walk is the cheap half of
// extraction and it already stats every file, so the count, the byte
// total and the newest modification time come for free — and every edit
// changes at least one of them. What this cannot detect is an edit that
// leaves the file's size and mtime identical, which requires deliberately
// restoring both; a developer's editor does not do that, and the cost of
// being wrong is a stale line number rather than a wrong file.

// Fingerprint identifies a repository's source content cheaply enough to
// compute on every call.
type Fingerprint struct {
	// Files is how many source files the walk found.
	Files int `json:"files"`
	// Bytes is their total size.
	Bytes int64 `json:"bytes"`
	// ModUnixNano is the newest modification time among them.
	ModUnixNano int64 `json:"mod_unix_nano"`
}

// Cache stores extraction results between calls.
//
// An interface so the walk does not care whether the store is on disk, in
// memory, or absent — and so a test can drive a cache that fails without
// arranging a broken filesystem.
type Cache interface {
	// Get returns a cached result when one was stored for this repository
	// against exactly this fingerprint.
	Get(repo string, fp Fingerprint) (*Result, bool)
	// Put stores a result. Failures are the implementation's to absorb: a
	// cache that cannot be written is a slow index, not a broken one.
	Put(repo string, fp Fingerprint, res *Result)
}

// cacheSchema is bumped whenever the stored shape changes.
//
// An entry written by an older version is discarded rather than migrated.
// The alternative — a migration path for a cache that can always be
// rebuilt in seconds — is machinery that would be wrong far more often
// than it was exercised.
const cacheSchema = 1

// cacheEntry is one repository's stored extraction.
type cacheEntry struct {
	Schema      int         `json:"schema"`
	Repo        string      `json:"repo"`
	Fingerprint Fingerprint `json:"fingerprint"`
	Written     time.Time   `json:"written"`
	Files       int         `json:"files"`
	Truncated   bool        `json:"truncated"`
	Symbols     []Symbol    `json:"symbols"`
}

// DiskCache stores one JSON file per repository under a directory.
type DiskCache struct {
	dir string
	// maxEntries bounds the directory. Exceeding it prunes the oldest,
	// because an abandoned workspace should not keep its symbols forever.
	maxEntries int
}

// DefaultMaxCacheEntries bounds a DiskCache's directory.
//
// Generous, because an entry is small and a large workspace is exactly the
// case this exists for; bounded, because a cache that only grows is a bug
// somebody finds years later.
const DefaultMaxCacheEntries = 2000

// NewDiskCache returns a cache rooted at dir, creating it if needed.
//
// A directory that cannot be created is not an error the caller has to
// handle: the cache degrades to storing nothing, which is exactly the
// behaviour before it existed.
func NewDiskCache(dir string) *DiskCache {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil
	}
	return &DiskCache{dir: dir, maxEntries: DefaultMaxCacheEntries}
}

// pathFor maps a repository path to its cache file.
//
// Hashed rather than escaped: a repository path can be arbitrarily long,
// contain separators, and differ only in case on a case-insensitive
// filesystem. A hash sidesteps all three, and the repository path is
// stored inside the file so an entry is still identifiable by hand.
func (c *DiskCache) pathFor(repo string) string {
	sum := sha256.Sum256([]byte(repo))
	return filepath.Join(c.dir, hex.EncodeToString(sum[:])+".json")
}

// Get implements Cache.
func (c *DiskCache) Get(repo string, fp Fingerprint) (*Result, bool) {
	if c == nil {
		return nil, false
	}
	f, err := os.Open(c.pathFor(repo)) // #nosec G304 -- the name is a hash this package computed
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()

	var e cacheEntry
	// A corrupt or truncated entry is a miss, not a failure — it will be
	// overwritten by the extraction that follows — so every error from
	// here on returns the same way.
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, false
	}
	defer func() { _ = zr.Close() }()
	if err := json.NewDecoder(zr).Decode(&e); err != nil {
		return nil, false
	}
	// Every one of these must match. The schema because the shape may have
	// changed; the repository because a hash collision, however unlikely,
	// must not serve one repository's symbols for another; the fingerprint
	// because that is the whole point.
	if e.Schema != cacheSchema || e.Repo != repo || e.Fingerprint != fp {
		return nil, false
	}
	return &Result{Symbols: e.Symbols, Files: e.Files, Truncated: e.Truncated}, true
}

// Put implements Cache.
//
// Written to a temporary file and renamed, so a reader never sees a
// half-written entry and a crash mid-write leaves the previous entry
// intact rather than a truncated one.
func (c *DiskCache) Put(repo string, fp Fingerprint, res *Result) {
	if c == nil || res == nil {
		return
	}
	b, err := compressEntry(cacheEntry{
		Schema:      cacheSchema,
		Repo:        repo,
		Fingerprint: fp,
		Written:     time.Now().UTC(),
		Files:       res.Files,
		Truncated:   res.Truncated,
		Symbols:     res.Symbols,
	})
	if err == nil {
		err = writeCacheEntry(c.dir, c.pathFor(repo), b)
	}
	if err != nil {
		return
	}
	c.prune()
}

// compressEntry marshals and gzips one entry.
//
// Neither step can fail for this struct — it holds only strings, ints,
// bools and a time, and the writer is a bytes.Buffer — so the errors are
// joined into one arm rather than three that pretend to be independently
// reachable.
func compressEntry(e cacheEntry) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	encErr := json.NewEncoder(zw).Encode(e)
	closeErr := zw.Close()
	// Returned rather than branched on: neither can fail here, so a branch
	// would be a line no test could ever reach, and the caller already
	// checks the error before using the bytes.
	return buf.Bytes(), errors.Join(encErr, closeErr)
}

// writeCacheEntry writes b to a temporary file in dir and renames it over
// final, so a reader never sees a half-written entry and a crash mid-write
// leaves the previous entry intact rather than a truncated one.
//
// Indirected because a test cannot make a real disk fail on demand, and a
// cache that swallows a write failure silently is exactly what needs
// asserting.
var writeCacheEntry = func(dir, final string, b []byte) error {
	// Unique by process and, within a process, by counter — so two
	// goroutines caching the same repository cannot truncate each other's
	// temporary file and publish half of it.
	name := final + fmt.Sprintf(".tmp-%d-%d", os.Getpid(), tmpSeq.Add(1))
	if err := os.WriteFile(name, b, 0o600); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, final); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// tmpSeq makes temporary names unique within a process.
var tmpSeq atomic.Uint64

// prune keeps the directory under maxEntries by removing the oldest files.
//
// Oldest by modification time, which for this directory is the time the
// entry was last written — so a repository nobody has looked at since the
// cache filled up is the first to go, and one in daily use is not.
func (c *DiskCache) prune() {
	entries, err := os.ReadDir(c.dir)
	if err != nil || len(entries) <= c.maxEntries {
		return
	}
	type aged struct {
		name string
		mod  time.Time
	}
	files := make([]aged, 0, len(entries))
	for _, e := range entries {
		// An entry whose metadata cannot be read has almost certainly been
		// removed already; a zero time sorts it first, which is the right
		// disposition either way.
		var mod time.Time
		if info, err := e.Info(); err == nil {
			mod = info.ModTime()
		}
		files = append(files, aged{name: e.Name(), mod: mod})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for i := 0; i < len(files)-c.maxEntries; i++ {
		_ = os.Remove(filepath.Join(c.dir, files[i].name))
	}
}
