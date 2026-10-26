// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/sebastienrousseau/corralctl/internal/diag"
	"github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/sebastienrousseau/corralctl/internal/search"
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

func (r *realWatcher) Add(name string) error         { return r.w.Add(name) }
func (r *realWatcher) Close() error                  { return r.w.Close() }
func (r *realWatcher) Events() <-chan fsnotify.Event { return r.w.Events }
func (r *realWatcher) Errors() <-chan error          { return r.w.Errors }

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

// watcherWarm is the warm pass the watcher starts after a change. A seam so
// tests can observe or hold it. The watcher runs it on a goroutine it tracks
// (see workspaceWatcher.flush); this function itself is synchronous.
var watcherWarm = func(s *Server, ctx context.Context) {
	s.warmSymbols(ctx)
}

// What the watcher watches, and why so little.
//
// The watcher exists to drop the cached workspace index (see Server.scan)
// sooner than its five-second TTL when a repository is cloned, moved,
// removed or re-pointed. That index is built from the layout directories
// down to maxIndexDepth and from each clone's .git directory; it never
// reads a clone's working tree. So those are the only directories watched.
//
// It used to watch every directory under the root, node_modules and target
// trees included. fsnotify's kqueue backend (macOS, the BSDs) holds one
// descriptor per watched directory and per entry in it, so a real workspace
// took more than ten thousand descriptors, and on a process limited to 256
// the workspace scan itself then failed to open the root and reported zero
// repositories. Watching a clone's contents bought nothing in return: the
// trigram and symbol caches revalidate by fingerprint on their own TTLs, and
// a warm pass skips any entry that is still fresh.
//
// The registration is also bounded by a descriptor budget (watchBudget), and
// a failure to add a watch is reported, not discarded. A directory left
// unwatched is not wrong, only slower to notice: the scan cache expires on
// its own within scanTTL.

// watchReport is what one registration pass did.
type watchReport struct {
	// watched is how many directories have a watch.
	watched int
	// cost is the descriptors those watches are estimated to hold.
	cost int
	// skipped is how many directories were left unwatched for the budget.
	skipped int
	// err is the first failure to add a watch. Registration stops there.
	err error
}

// degraded reports that some directory the scan reads is not watched.
func (r watchReport) degraded() bool { return r.err != nil || r.skipped > 0 }

// watchRegistrar adds watches for the directories the workspace scan reads,
// within a descriptor budget, for the lifetime of one watcher.
type watchRegistrar struct {
	ctx    context.Context
	w      fsWatcher
	root   string
	budget int
	rep    watchReport
	warned bool
}

// newWatchRegistrar prepares a registrar for the server's root, which
// NewServer has already required to be absolute.
func (s *Server) newWatchRegistrar(ctx context.Context, w fsWatcher) *watchRegistrar {
	return &watchRegistrar{ctx: ctx, w: w, root: filepath.Clean(s.opts.Root), budget: watchBudget()}
}

// registerWatchDirs watches dir and whatever beneath it the scan reads, and
// reports what it did.
func (s *Server) registerWatchDirs(ctx context.Context, w fsWatcher, dir string) watchReport {
	r := s.newWatchRegistrar(ctx, w)
	r.add(dir)
	return r.rep
}

// skipWatchDir reports a directory name the watcher never descends into:
// hidden directories, and the dependency and build trees the content search
// also skips.
func skipWatchDir(name string) bool {
	return strings.HasPrefix(name, ".") || search.IgnoredDir(name)
}

// add registers path, which is the root or a directory created under it.
func (r *watchRegistrar) add(path string) {
	depth, ok := r.depthOf(path)
	switch {
	case !ok:
		return
	case depth == 0:
		r.walk(path, 0)
	case filepath.Base(path) == ".git":
		// A clone being created: its parent just became a repository.
		r.watchGitDir(filepath.Dir(path))
	case skipWatchDir(filepath.Base(path)), r.insideClone(path):
		return
	default:
		r.visit(path, depth)
	}
}

// depthOf is how far below the root path is, and whether it is somewhere the
// scan would look at all: inside the root, at most one level below
// maxIndexDepth (a .git directory), and not inside a skipped directory.
func (r *watchRegistrar) depthOf(path string) (int, bool) {
	rel, err := filepath.Rel(r.root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return 0, false
	}
	if rel == "." {
		return 0, true
	}
	parts := strings.Split(rel, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		if skipWatchDir(part) {
			return 0, false
		}
	}
	return len(parts), len(parts) <= maxIndexDepth+1
}

// insideClone reports that path is in a clone's working tree, which the
// scan never reads. That happens when a directory was watched as part of the
// layout and then became a clone: git clone creates the directory first.
func (r *watchRegistrar) insideClone(path string) bool {
	parent := filepath.Dir(path)
	return parent != r.root && git.IsRepository(parent)
}

// visit registers one directory below the root: a clone's .git directory if
// it is one, otherwise the directory and its own subdirectories.
func (r *watchRegistrar) visit(path string, depth int) {
	if depth > maxIndexDepth {
		return
	}
	if git.IsRepository(path) {
		r.watchGitDir(path)
		return
	}
	r.walk(path, depth)
}

// walk watches a layout directory and visits its subdirectories.
func (r *watchRegistrar) walk(dir string, depth int) {
	if r.stopped() {
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		// Unreadable: the scan cannot see beneath it either.
		return
	}
	r.watch(dir, len(entries))
	for _, e := range entries {
		if e.IsDir() && !skipWatchDir(e.Name()) {
			r.visit(filepath.Join(dir, e.Name()), depth+1)
		}
	}
}

// watchGitDir watches a clone's .git directory, which is where its remote
// and sidecar state live. A .git file (a linked worktree) has nothing of its
// own to watch; its appearance and removal show in the parent's watch.
func (r *watchRegistrar) watchGitDir(repo string) {
	if r.stopped() {
		return
	}
	gitDir := filepath.Join(repo, ".git")
	entries, err := os.ReadDir(gitDir)
	if err != nil {
		return
	}
	r.watch(gitDir, len(entries))
}

// watch adds one watch if the budget allows. The cost is the directory plus
// its entries, which is what kqueue holds open; inotify holds less, so the
// estimate errs on the safe side there.
func (r *watchRegistrar) watch(dir string, entries int) {
	cost := entries + 1
	if r.rep.cost+cost > r.budget {
		r.rep.skipped++
		return
	}
	if err := r.w.Add(dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return
		}
		r.rep.err = fmt.Errorf("watch %s: %w", dir, err)
		return
	}
	r.rep.cost += cost
	r.rep.watched++
}

// stopped reports that registration should not continue: the watcher is
// shutting down, or adding a watch has already failed and every further
// attempt would fail the same way.
func (r *watchRegistrar) stopped() bool {
	return r.rep.err != nil || r.ctx.Err() != nil
}

// warnIfDegraded says once, on stderr, that part of the workspace is not
// watched and what that means for freshness.
func (r *watchRegistrar) warnIfDegraded() {
	if r.warned || !r.rep.degraded() {
		return
	}
	r.warned = true
	if r.rep.err != nil {
		diag.Warnf("corral-mcp: workspace watcher stopped adding watches after %d directories: %v; "+
			"changes elsewhere are seen when the workspace cache expires (%s)", r.rep.watched, r.rep.err, scanTTL)
		return
	}
	diag.Warnf("corral-mcp: workspace watcher reached its budget of %d descriptors; %d directories are not watched "+
		"and their changes are seen when the workspace cache expires (%s)", r.budget, r.rep.skipped, scanTTL)
}

// startWorkspaceWatcher begins watching the workspace in the background (see
// above for what is watched). When a watched directory changes, it debounces
// the events and calls s.invalidateScanCache() so subsequent queries inspect
// the updated workspace state.
//
// Registration happens on the watcher's goroutine, not the caller's: the
// stdio transport starts only after this returns, and a walk of a large
// workspace must not hold up the MCP handshake.
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

	ww := &workspaceWatcher{
		s:    s,
		w:    w,
		reg:  s.newWatchRegistrar(ctx, w),
		fire: make(chan struct{}, 1),
	}
	go func() {
		defer close(done)
		defer func() { _ = w.Close() }()
		ww.run(ctx)
	}()
	return done
}

// workspaceWatcher is the state of one running watcher. Everything but the
// debounce timer's callback runs on the watcher's own goroutine.
type workspaceWatcher struct {
	s   *Server
	w   fsWatcher
	reg *watchRegistrar
	// fire is signalled by the debounce timer. Buffered so the callback
	// never blocks, including after the loop has exited.
	fire  chan struct{}
	timer *time.Timer
	// warms tracks the warm passes this watcher started. run does not
	// return until they have, so the watcher's done channel closing means
	// nothing it started can still be reading a mapped index: serve
	// releases every mapping right after.
	warms sync.WaitGroup
}

// run registers the watches, then handles events until ctx ends or the
// watcher closes.
func (ww *workspaceWatcher) run(ctx context.Context) {
	// Deferred first so it runs last. Add is only ever called from this
	// goroutine (in flush), so it cannot race this Wait.
	defer ww.warms.Wait()
	defer ww.stopTimer()

	ww.reg.add(ww.reg.root)
	ww.reg.warnIfDegraded()
	diag.Debugf("corral-mcp: workspace watcher watching %d directories (~%d descriptors)",
		ww.reg.rep.watched, ww.reg.rep.cost)

	eventsCh := ww.w.Events()
	errorsCh := ww.w.Errors()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-eventsCh:
			if !ok {
				return
			}
			ww.handleEvent(event)
		case err, ok := <-errorsCh:
			if !ok {
				return
			}
			diag.Warnf("corral-mcp: workspace watcher error: %v", err)
		case <-ww.fire:
			ww.flush(ctx)
		}
	}
}

// handleEvent watches a directory that was just created and, if the event
// can change the workspace index, (re)arms the debounce timer.
func (ww *workspaceWatcher) handleEvent(event fsnotify.Event) {
	if event.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
			ww.reg.add(event.Name)
			ww.reg.warnIfDegraded()
		}
	}
	if !ww.relevant(event) {
		return
	}
	ww.arm()
}

// relevant reports whether an event can change what the workspace index
// holds. Everything else would drop the index (and possibly start a warm
// pass) for nothing: git rewrites .git/index on a plain `git status`, which
// the server itself runs, and a fetch rewrites FETCH_HEAD.
func (ww *workspaceWatcher) relevant(event fsnotify.Event) bool {
	name := filepath.Base(event.Name)
	switch {
	case event.Op == fsnotify.Chmod:
		// Permission and timestamp changes; fsnotify itself advises
		// ignoring them.
		return false
	case name == ".git":
		// A clone appearing or disappearing.
		return true
	case filepath.Base(filepath.Dir(event.Name)) == ".git":
		return gitFileRelevant(name)
	default:
		return !ww.reg.insideClone(event.Name)
	}
}

// gitFileRelevant reports the entries of a .git directory the index reads
// (the remote in config, the sidecar state) or that mark a branch switch.
func gitFileRelevant(name string) bool {
	switch name {
	case "config", "HEAD", stateFileName:
		return true
	}
	return false
}

// arm restarts the debounce window.
func (ww *workspaceWatcher) arm() {
	if ww.timer != nil {
		ww.timer.Stop()
	}
	ww.timer = time.AfterFunc(watcherDebounceDelay, func() {
		select {
		case ww.fire <- struct{}{}:
		default:
		}
	})
}

// stopTimer cancels a pending debounce.
func (ww *workspaceWatcher) stopTimer() {
	if ww.timer != nil {
		ww.timer.Stop()
	}
}

// flush is what a settled burst of events does: drop the workspace index
// and, if someone is using the symbol tools, warm the caches again.
func (ww *workspaceWatcher) flush(ctx context.Context) {
	ww.s.invalidateScanCache()
	diag.Debugf("corral-mcp: workspace cache invalidated by filesystem watcher")
	if ww.s.symbolQueryIdle() {
		return
	}
	// Off the loop, so events keep draining during a pass that can take
	// half a minute, but tracked, so stopping the watcher waits for it. A
	// pass already running (from here or the ticker warmer) makes this one
	// return at once: warmSymbols is single-flight.
	ww.warms.Add(1)
	go func() {
		defer ww.warms.Done()
		watcherWarm(ww.s, ctx)
	}()
}
