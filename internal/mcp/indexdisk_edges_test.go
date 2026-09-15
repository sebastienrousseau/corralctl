// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/search"
)

// Where the index lives, and what happens when it cannot.
//
// A cache directory that cannot be written should make the next start slower,
// never fail a search — so each of these paths ends in a slower-but-correct
// answer rather than an error reaching the caller.

func TestDefaultIndexCacheDirFollowsXDG(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg-example")
	if got := DefaultIndexCacheDir(); got != filepath.Join("/tmp/xdg-example", "corral", "index") {
		t.Errorf("XDG_CACHE_HOME ignored: %s", got)
	}

	t.Setenv("XDG_CACHE_HOME", "")
	home := DefaultIndexCacheDir()
	if !strings.HasSuffix(home, filepath.Join(".cache", "corral", "index")) {
		t.Errorf("without XDG it should fall back under the home directory: %s", home)
	}

	// No home either: somewhere writable, rather than a path that is empty.
	stubSeam(t, &auditUserHomeDir, func() (string, error) { return "", errors.New("no home") })
	tmp := DefaultIndexCacheDir()
	if !strings.HasPrefix(tmp, os.TempDir()) {
		t.Errorf("with no home it should fall back to the temp dir: %s", tmp)
	}
}

func TestIndexCacheDirHonoursOff(t *testing.T) {
	if got := indexCacheDir("off"); got != "" {
		t.Errorf(`indexCacheDir("off") = %q, want "" so nothing is persisted`, got)
	}
	if got := indexCacheDir("/some/where"); got != "/some/where" {
		t.Errorf("an explicit directory was not honoured: %s", got)
	}
	if indexCacheDir("") == "" {
		t.Error("an empty setting should resolve to the default, not to disabled")
	}
}

// repoForIndexing builds a one-repository workspace and returns the server and
// the entry.
func repoForIndexing(t *testing.T, indexDir string) (*Server, *RepoEntry) {
	t.Helper()
	base := t.TempDir()
	repo := makeFakeRepo(t, base, "Public", "go", "alpha", "https://github.com/acme/alpha.git", "")
	writeIn(t, repo, "a.go", "package a\n\nfunc Findable() {}\n")
	srv, err := NewServer(ServerOptions{
		Root: base, Version: "test",
		SymbolCacheDir: t.TempDir(), IndexCacheDir: indexDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := srv.scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.Repos) != 1 {
		t.Fatalf("expected one repository, got %d", len(idx.Repos))
	}
	return srv, &idx.Repos[0]
}

func TestLoadOrBuildIndexDisabled(t *testing.T) {
	srv, repo := repoForIndexing(t, "off")
	if _, err := srv.loadOrBuildIndex(context.Background(), repo); !errors.Is(err, errIndexDisabled) {
		t.Errorf("err = %v, want the disabled marker", err)
	}
	// And indexFor must simply do nothing rather than fail.
	srv.indexFor(context.Background(), repo)
	if n, _ := srv.indexCache.stats(); n != 0 {
		t.Errorf("an index was cached with persistence off: %d entries", n)
	}
}

func TestLoadOrBuildIndexReportsFailures(t *testing.T) {
	boom := errors.New("boom")

	t.Run("fingerprint fails", func(t *testing.T) {
		srv, repo := repoForIndexing(t, t.TempDir())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := srv.loadOrBuildIndex(ctx, repo); err == nil {
			t.Error("a cancelled fingerprint should be reported")
		}
	})

	t.Run("build fails", func(t *testing.T) {
		srv, repo := repoForIndexing(t, t.TempDir())
		stubSeam(t, &buildIndex, func(context.Context, string, search.FileFilter) (*search.Index, error) {
			return nil, boom
		})
		if _, err := srv.loadOrBuildIndex(context.Background(), repo); !errors.Is(err, boom) {
			t.Errorf("err = %v, want the build failure", err)
		}
	})

	t.Run("cannot be persisted", func(t *testing.T) {
		// A directory that cannot be created: its parent is a file.
		blocker := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		srv, repo := repoForIndexing(t, filepath.Join(blocker, "index"))
		if _, err := srv.loadOrBuildIndex(context.Background(), repo); err == nil {
			t.Error("an unwritable cache directory should be reported to the caller")
		}
		// The search still has to work; it simply reads every file.
		srv.indexFor(context.Background(), repo)
		if n, _ := srv.indexCache.stats(); n != 0 {
			t.Errorf("nothing should be cached when it could not be written: %d", n)
		}
	})
}

func TestLoadOrBuildIndexReusesWhatIsOnDisk(t *testing.T) {
	dir := t.TempDir()
	srv, repo := repoForIndexing(t, dir)

	mi, err := srv.loadOrBuildIndex(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	_ = mi.Close()

	// Second time round the index is mapped rather than rebuilt, which is
	// what makes a restart cost a walk instead of a full read.
	built := 0
	stubSeam(t, &buildIndex, func(ctx context.Context, root string, f search.FileFilter) (*search.Index, error) {
		built++
		return search.BuildIndex(ctx, root, f)
	})
	mi2, err := srv.loadOrBuildIndex(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mi2.Close() }()
	if built != 0 {
		t.Errorf("the index was rebuilt %d time(s) despite being current on disk", built)
	}
}

func TestIndexPathForIsStableAndDistinct(t *testing.T) {
	srv, _ := repoForIndexing(t, t.TempDir())
	a := srv.indexPathFor("/one/repo")
	b := srv.indexPathFor("/another/repo")
	if a == b {
		t.Error("two repositories share an index file")
	}
	if a != srv.indexPathFor("/one/repo") {
		t.Error("the same repository produced two different paths")
	}
	if !strings.HasSuffix(a, ".idx") {
		t.Errorf("index path has no extension: %s", a)
	}
}
