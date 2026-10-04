// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type mockWatcher struct {
	events       chan fsnotify.Event
	errors       chan error
	mu           sync.Mutex
	eventsClosed bool
	errorsClosed bool
}

func newMockWatcher() *mockWatcher {
	return &mockWatcher{
		events: make(chan fsnotify.Event, 10),
		errors: make(chan error, 10),
	}
}

func (m *mockWatcher) Add(string) error { return nil }

func (m *mockWatcher) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.eventsClosed {
		m.eventsClosed = true
		close(m.events)
	}
	if !m.errorsClosed {
		m.errorsClosed = true
		close(m.errors)
	}
	return nil
}

func (m *mockWatcher) closeEvents() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.eventsClosed {
		m.eventsClosed = true
		close(m.events)
	}
}

func (m *mockWatcher) closeErrors() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.errorsClosed {
		m.errorsClosed = true
		close(m.errors)
	}
}

func (m *mockWatcher) Events() <-chan fsnotify.Event { return m.events }
func (m *mockWatcher) Errors() <-chan error          { return m.errors }

func TestWorkspaceWatcherInvalidatesScanCache(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")

	srv, err := NewServer(ServerOptions{
		Root:           base,
		WatchWorkspace: true,
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	// Warm the scan cache
	idx, err := srv.scan()
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	if len(idx.Repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(idx.Repos))
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := srv.startWorkspaceWatcher(ctx)

	// Touch a file in alpha to trigger an event
	touchFile := filepath.Join(base, "Public", "go", "alpha", "touched.txt")
	if err := os.WriteFile(touchFile, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Wait for debounce timer to fire and invalidate cache
	deadline := time.Now().Add(2 * time.Second)
	invalidated := false
	for time.Now().Before(deadline) {
		srv.scanMu.Lock()
		exp := srv.scanExpires
		srv.scanMu.Unlock()
		if exp.IsZero() {
			invalidated = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	if !invalidated {
		t.Fatal("expected watcher to invalidate scan cache on file creation")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not stop on ctx cancel")
	}
}

func TestWorkspaceWatcherRegistersNewDirectories(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")

	srv, err := NewServer(ServerOptions{
		Root:           base,
		WatchWorkspace: true,
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := srv.startWorkspaceWatcher(ctx)

	// Create a new directory and repo
	newRepo := filepath.Join(base, "Private", "python", "beta")
	if err := os.MkdirAll(newRepo, 0o750); err != nil {
		t.Fatal(err)
	}

	// Wait for directory watch registration
	time.Sleep(100 * time.Millisecond)

	// Touch file inside new directory
	if err := os.WriteFile(filepath.Join(newRepo, "main.py"), []byte("print(1)"), 0o600); err != nil {
		t.Fatal(err)
	}

	time.Sleep(100 * time.Millisecond)
	cancel()
	<-done
}

func TestWorkspaceWatcherInitFailureGraceful(t *testing.T) {
	base := t.TempDir()
	srv, err := NewServer(ServerOptions{Root: base})
	if err != nil {
		t.Fatal(err)
	}

	old := fsnotifyNewWatcher
	fsnotifyNewWatcher = func() (*fsnotify.Watcher, error) {
		return nil, errors.New("cannot create inotify watcher")
	}
	defer func() { fsnotifyNewWatcher = old }()

	done := srv.startWorkspaceWatcher(context.Background())
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("expected done channel to close immediately on watcher init failure")
	}
}

func TestWorkspaceWatcherRegisterWatchDirsEdges(t *testing.T) {
	base := t.TempDir()
	srv, _ := NewServer(ServerOptions{Root: base})

	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()

	rw := &realWatcher{w: w}

	// Create hidden dir (.cache), .git dir with internal objects, and regular file
	if err := os.MkdirAll(filepath.Join(base, ".cache", "subdir"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "repo", ".git", "objects"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "file.txt"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := srv.registerWatchDirs(rw, base); err != nil {
		t.Fatalf("registerWatchDirs failed: %v", err)
	}
}

func TestWorkspaceWatcherDebounceAndErrors(t *testing.T) {
	base := t.TempDir()
	srv, _ := NewServer(ServerOptions{Root: base})

	mock := newMockWatcher()
	old := newFSWatcher
	newFSWatcher = func() (fsWatcher, error) {
		return mock, nil
	}
	defer func() { newFSWatcher = old }()

	ctx, cancel := context.WithCancel(context.Background())
	done := srv.startWorkspaceWatcher(ctx)

	// Simulate burst of events to exercise debounce reset
	file := filepath.Join(base, "sample.txt")
	mock.events <- fsnotify.Event{Name: file, Op: fsnotify.Write}
	mock.events <- fsnotify.Event{Name: file, Op: fsnotify.Write}

	// Send an error to exercise error branch
	mock.errors <- errors.New("simulated error")

	// Wait for debounce timer
	time.Sleep(100 * time.Millisecond)

	cancel()
	<-done
}

func TestWorkspaceWatcherEventsClosed(t *testing.T) {
	base := t.TempDir()
	srv, _ := NewServer(ServerOptions{Root: base})

	mock := newMockWatcher()
	old := newFSWatcher
	newFSWatcher = func() (fsWatcher, error) {
		return mock, nil
	}
	defer func() { newFSWatcher = old }()

	done := srv.startWorkspaceWatcher(context.Background())
	mock.closeEvents()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected watcher loop to exit when events channel is closed")
	}
}

func TestWorkspaceWatcherErrorsClosed(t *testing.T) {
	base := t.TempDir()
	srv, _ := NewServer(ServerOptions{Root: base})

	mock := newMockWatcher()
	old := newFSWatcher
	newFSWatcher = func() (fsWatcher, error) {
		return mock, nil
	}
	defer func() { newFSWatcher = old }()

	done := srv.startWorkspaceWatcher(context.Background())
	mock.closeErrors()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("expected watcher loop to exit when errors channel is closed")
	}
}

func TestWorkspaceWatcherUnreadablePath(t *testing.T) {
	base := t.TempDir()
	srv, _ := NewServer(ServerOptions{Root: base})

	mock := newMockWatcher()
	if err := srv.registerWatchDirs(mock, filepath.Join(base, "missing-subdir")); err != nil {
		t.Fatalf("expected nil on missing root, got %v", err)
	}
}

func TestWorkspaceWatcherServeStdioAndHTTP(t *testing.T) {
	base := t.TempDir()
	srv, err := NewServer(ServerOptions{
		Root:           base,
		WatchWorkspace: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Test ServeStdio with WatchWorkspace
	oldServeStdio := serveStdio
	serveStdio = func(*mcp.Server) error { return nil }
	defer func() { serveStdio = oldServeStdio }()

	if err := srv.ServeStdio(); err != nil {
		t.Fatalf("ServeStdio failed: %v", err)
	}

	// Test ServeHTTP with WatchWorkspace
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listenAddr := "127.0.0.1:0"
	served := make(chan error, 1)
	go func() { served <- srv.ServeHTTP(ctx, listenAddr) }()

	// Give listener a moment to start
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("ServeHTTP error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ServeHTTP did not return on cancel")
	}
}

func TestWorkspaceWatcherAutoWarmsSymbolsOnActiveQuery(t *testing.T) {
	base := t.TempDir()
	srv, err := NewServer(ServerOptions{
		Root:           base,
		WatchWorkspace: true,
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	mock := newMockWatcher()
	oldWatcher := newFSWatcher
	newFSWatcher = func() (fsWatcher, error) { return mock, nil }
	defer func() { newFSWatcher = oldWatcher }()

	warmCalled := make(chan struct{}, 1)
	oldWarm := warmSymbolsAsync
	warmSymbolsAsync = func(s *Server, ctx context.Context) {
		warmCalled <- struct{}{}
	}
	defer func() { warmSymbolsAsync = oldWarm }()

	// Mark query active
	srv.noteSymbolQuery()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.startWorkspaceWatcher(ctx)

	// Send an event
	mock.events <- fsnotify.Event{
		Name: filepath.Join(base, "file.go"),
		Op:   fsnotify.Write,
	}

	select {
	case <-warmCalled:
		// Success!
	case <-time.After(2 * time.Second):
		t.Fatal("expected warmSymbolsAsync to be called on file change with active query")
	}
}

func TestWorkspaceWatcherSkipsWarmingWhenIdle(t *testing.T) {
	base := t.TempDir()
	srv, err := NewServer(ServerOptions{
		Root:           base,
		WatchWorkspace: true,
	})
	if err != nil {
		t.Fatalf("NewServer failed: %v", err)
	}

	mock := newMockWatcher()
	oldWatcher := newFSWatcher
	newFSWatcher = func() (fsWatcher, error) { return mock, nil }
	defer func() { newFSWatcher = oldWatcher }()

	warmCalled := make(chan struct{}, 1)
	oldWarm := warmSymbolsAsync
	warmSymbolsAsync = func(s *Server, ctx context.Context) {
		warmCalled <- struct{}{}
	}
	defer func() { warmSymbolsAsync = oldWarm }()

	// Symbol queries are idle (never called)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.startWorkspaceWatcher(ctx)

	mock.events <- fsnotify.Event{
		Name: filepath.Join(base, "file.go"),
		Op:   fsnotify.Write,
	}

	// Wait past debounce delay
	time.Sleep(100 * time.Millisecond)
	select {
	case <-warmCalled:
		t.Fatal("warmSymbolsAsync should not be called when idle")
	default:
		// Success!
	}
}

func TestWarmSymbolsAsyncDefaultExecution(t *testing.T) {
	base := t.TempDir()
	srv, err := NewServer(ServerOptions{Root: base})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	warmSymbolsAsync(srv, ctx)
}
