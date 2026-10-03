// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/sebastienrousseau/corralctl/internal/diag"
)

// watcherDebounceDelay is how long the workspace watcher waits after the last
// filesystem event before invalidating the workspace index cache. Debouncing
// collapses bursts from git operations (checkout, pull, branch creation) into
// a single invalidation.
const watcherDebounceDelay = 50 * time.Millisecond

// fsWatcher defines the subset of filesystem watching operations required by
// startWorkspaceWatcher, allowing test mocks to inject events and errors
// cleanly without background kernel thread conflicts.
type fsWatcher interface {
	Add(name string) error
	Close() error
	Events() <-chan fsnotify.Event
	Errors() <-chan error
}

type realWatcher struct {
	w *fsnotify.Watcher
}

func (r *realWatcher) Add(name string) error { return r.w.Add(name) }
func (r *realWatcher) Close() error { return r.w.Close() }
func (r *realWatcher) Events() <-chan fsnotify.Event { return r.w.Events }
func (r *realWatcher) Errors() <-chan error { return r.w.Errors }

var fsnotifyNewWatcher = fsnotify.NewWatcher

// newFSWatcher is a package seam for creating a filesystem watcher, allowing tests
// to inject mock watchers or simulate initialization failures.
var newFSWatcher = func() (fsWatcher, error) {
	w, err := fsnotifyNewWatcher()
	if err != nil {
		return nil, err
	}
	return &realWatcher{w: w}, nil
}

// startWorkspaceWatcher begins watching the workspace root and its structural
// directories in the background. When filesystem changes occur, it debounces
// the events and calls s.invalidateScanCache() so subsequent queries inspect
// the updated workspace state.
//
// Returns a channel that is closed when the background watcher has terminated.
func (s *Server) startWorkspaceWatcher(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})

	w, err := newFSWatcher()
	if err != nil {
		diag.Warnf("corral-mcp: workspace watcher disabled: %v", err)
		close(done)
		return done
	}

	// Register structural directories under Root.
	_ = s.registerWatchDirs(w, s.opts.Root)

	go func() {
		defer close(done)
		defer func() { _ = w.Close() }()

		var mu sync.Mutex
		var debounceTimer *time.Timer

		defer func() {
			mu.Lock()
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			mu.Unlock()
		}()

		eventsCh := w.Events()
		errorsCh := w.Errors()

		for {
			select {
			case <-ctx.Done():
				return

			case event, ok := <-eventsCh:
				if !ok {
					return
				}
				// If a new directory is created, watch it too.
				if event.Op&fsnotify.Create != 0 {
					if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
						_ = s.registerWatchDirs(w, event.Name)
					}
				}

				mu.Lock()
				if debounceTimer != nil {
					debounceTimer.Stop()
				}
				debounceTimer = time.AfterFunc(watcherDebounceDelay, func() {
					s.invalidateScanCache()
					diag.Debugf("corral-mcp: workspace cache invalidated by filesystem watcher")
				})
				mu.Unlock()

			case err, ok := <-errorsCh:
				if !ok {
					return
				}
				diag.Warnf("corral-mcp: workspace watcher error: %v", err)
			}
		}
	}()

	return done
}

// registerWatchDirs traverses the directory tree and adds watches on structural directories.
// Watching stops descending into .git internals beyond .git itself to avoid
// overwhelming the OS with watches on loose objects.
func (s *Server) registerWatchDirs(w fsWatcher, root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // Intentional: skip unreadable paths without aborting traversal
		}
		if !d.IsDir() {
			return nil
		}

		base := d.Name()
		// Do not watch hidden directories other than .git itself.
		if strings.HasPrefix(base, ".") && base != ".git" {
			return filepath.SkipDir
		}

		_ = w.Add(path)

		// Inside .git, we only watch the root of .git (to detect HEAD, refs, etc.)
		// and do not descend into objects/, logs/, or hooks/.
		if base == ".git" {
			return filepath.SkipDir
		}

		return nil
	})
}
