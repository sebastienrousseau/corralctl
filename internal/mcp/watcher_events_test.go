// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// P-02: every event invalidated the whole workspace index, including the
// server's own git commands rewriting .git/index.
func TestWatcherIgnoresIrrelevantGitWrites(t *testing.T) {
	base := t.TempDir()
	repo := makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	srv, err := NewServer(ServerOptions{Root: base})
	if err != nil {
		t.Fatal(err)
	}
	mock := newMockWatcher()
	old := newFSWatcher
	newFSWatcher = func() (fsWatcher, error) { return mock, nil }
	defer func() { newFSWatcher = old }()

	if _, err := srv.scan(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := srv.startWorkspaceWatcher(ctx)
	defer func() { cancel(); <-done }()

	for _, name := range []string{"index", "index.lock", "FETCH_HEAD", "ORIG_HEAD"} {
		mock.events <- fsnotify.Event{Name: filepath.Join(repo, ".git", name), Op: fsnotify.Write}
	}
	time.Sleep(4 * watcherDebounceDelay)
	srv.scanMu.Lock()
	cached := srv.scanIndex != nil
	srv.scanMu.Unlock()
	if !cached {
		t.Fatal("a write to .git/index invalidated the workspace index")
	}

	mock.events <- fsnotify.Event{Name: filepath.Join(repo, ".git", "config"), Op: fsnotify.Write}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		srv.scanMu.Lock()
		cached = srv.scanIndex != nil
		srv.scanMu.Unlock()
		if !cached {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("a write to .git/config did not invalidate the workspace index")
}

func TestWatcherEventRelevance(t *testing.T) {
	base := t.TempDir()
	repo := makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	srv, err := NewServer(ServerOptions{Root: base})
	if err != nil {
		t.Fatal(err)
	}
	ww := &workspaceWatcher{s: srv, reg: srv.newWatchRegistrar(context.Background(), newMockWatcher())}
	cases := []struct {
		name string
		op   fsnotify.Op
		want bool
	}{
		{filepath.Join(base, "Public", "go", "beta"), fsnotify.Create, true},
		{filepath.Join(base, "Public", "go"), fsnotify.Chmod, false},
		{filepath.Join(repo, ".git"), fsnotify.Remove, true},
		{filepath.Join(repo, ".git", "HEAD"), fsnotify.Write, true},
		{filepath.Join(repo, ".git", stateFileName), fsnotify.Create, true},
		{filepath.Join(repo, ".git", "packed-refs"), fsnotify.Write, false},
		{filepath.Join(repo, "README.md"), fsnotify.Write, false},
	}
	for _, c := range cases {
		if got := ww.relevant(fsnotify.Event{Name: c.name, Op: c.op}); got != c.want {
			t.Errorf("relevant(%s %s) = %v, want %v", c.op, c.name, got, c.want)
		}
	}
}
