// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/search"
)

// mappedFor builds and maps an index for a throwaway repository.
func mappedFor(t *testing.T, dir string) *search.MappedIndex {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		body := "package p\n\nfunc Distinctive" + string(rune('A'+i)) + "() {}\n"
		if err := os.WriteFile(filepath.Join(root, string(rune('a'+i))+".go"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	allowed := func(string) bool { return true }
	ix, err := search.BuildIndex(context.Background(), root, allowed)
	if err != nil {
		t.Fatal(err)
	}
	fp, err := search.Fingerprint(context.Background(), root, allowed)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "x.idx")
	if err := search.WriteIndex(path, ix, fp); err != nil {
		t.Fatal(err)
	}
	mi, err := search.OpenIndex(path, fp)
	if err != nil {
		t.Fatal(err)
	}
	return mi
}

// TestIndexCacheHoldsMappingWhileInUse is the one that matters.
//
// An index's slices point into its mapping. Unmapping it while a query is
// reading does not produce a wrong answer — it produces a read of memory the
// process no longer owns. Eviction and revalidation both unmap, and both can
// happen mid-search, so the reference count is the only thing standing between
// this cache and a crash under exactly the load it exists to serve.
//
// Run this with -race.
func TestIndexCacheHoldsMappingWhileInUse(t *testing.T) {
	dir := t.TempDir()
	c := newIndexCache()
	c.put("repo", mappedFor(t, dir))

	ix, release, ok := c.acquire("repo")
	if !ok {
		t.Fatal("a freshly stored index was not available")
	}

	// Evict while the reference is held. The mapping must survive.
	c.mu.Lock()
	e := c.entries["repo"]
	c.dropLocked("repo", e)
	c.mu.Unlock()

	if e.mi == nil {
		t.Fatal("the mapping was released while a query held it")
	}
	m, err := search.Compile(search.Query{Pattern: "DistinctiveA", MaxHits: 5})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ix.Candidates(m); !ok {
		t.Error("the held index stopped answering after eviction")
	}

	release()
	c.mu.Lock()
	stillMapped := e.mi != nil
	c.mu.Unlock()
	if stillMapped {
		t.Error("the mapping outlived its last reference")
	}
}

// TestIndexCacheConcurrentUse drives acquire, release and replacement together.
//
// The failure this is looking for is a mapping released while another
// goroutine reads it, which -race and the runtime will report as a crash
// rather than as a wrong answer.
func TestIndexCacheConcurrentUse(t *testing.T) {
	dir := t.TempDir()
	c := newIndexCache()
	c.put("repo", mappedFor(t, dir))
	t.Cleanup(c.closeAll)

	m, err := search.Compile(search.Query{Pattern: "DistinctiveA", MaxHits: 5})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				ix, release, ok := c.acquire("repo")
				if !ok {
					continue
				}
				_, _ = ix.Candidates(m)
				release()
			}
		}()
	}
	// Meanwhile, keep replacing the entry, which unmaps the previous one.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 10; j++ {
			c.put("repo", mappedFor(t, t.TempDir()))
		}
	}()
	wg.Wait()
}

// TestIndexCacheDeclinesWhenFull checks a full cache does not evict a live
// entry, which would unmap something a query could be holding.
func TestIndexCacheDeclinesWhenFull(t *testing.T) {
	c := newIndexCache()
	// Fill the table with entries that hold no mapping, so this stays cheap.
	c.mu.Lock()
	for i := 0; i < maxIndexEntries; i++ {
		c.entries[string(rune(i))] = &indexEntry{refs: 1}
	}
	c.mu.Unlock()

	mi := mappedFor(t, t.TempDir())
	c.put("new", mi)

	c.mu.Lock()
	_, present := c.entries["new"]
	n := len(c.entries)
	c.mu.Unlock()
	if present {
		t.Error("a full cache admitted an entry, which means it evicted a live one")
	}
	if n != maxIndexEntries {
		t.Errorf("cache holds %d entries, want %d", n, maxIndexEntries)
	}
}
