// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/sebastienrousseau/corralctl/internal/diag"
)

// recordingWatcher is a mockWatcher that remembers what was added and can
// fail or block an Add on demand.
type recordingWatcher struct {
	*mockWatcher
	mu      sync.Mutex
	added   []string
	addErr  error
	blockOn chan struct{}
}

func newRecordingWatcher() *recordingWatcher {
	return &recordingWatcher{mockWatcher: newMockWatcher()}
}

func (r *recordingWatcher) Add(name string) error {
	if r.blockOn != nil {
		<-r.blockOn
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.addErr != nil {
		return r.addErr
	}
	r.added = append(r.added, name)
	return nil
}

func (r *recordingWatcher) addedRel(t *testing.T, base string) []string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.added))
	for _, p := range r.added {
		rel, err := filepath.Rel(base, p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, filepath.ToSlash(rel))
	}
	sort.Strings(out)
	return out
}

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o750); err != nil {
			t.Fatal(err)
		}
	}
}

// P-01: the watcher descended into every directory of every clone, so a
// workspace with node_modules or target trees held one descriptor per
// directory (and, on kqueue, per file) and ran the process out of them.
// Only what the workspace scan reads is watched now: the layout directories
// and each clone's .git directory.
func TestRegisterWatchDirsWatchesOnlyLayoutAndGitDirs(t *testing.T) {
	base := t.TempDir()
	repo := makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	mkdirs(t,
		filepath.Join(repo, "node_modules", "left-pad", "lib"),
		filepath.Join(repo, "target", "debug"),
		filepath.Join(repo, "vendor", "x"),
		filepath.Join(repo, "src", "deep"),
		filepath.Join(base, ".cache", "sub"),
		filepath.Join(base, "Public", "node_modules", "pkg"),
	)
	srv, err := NewServer(ServerOptions{Root: base})
	if err != nil {
		t.Fatal(err)
	}
	w := newRecordingWatcher()
	srv.registerWatchDirs(context.Background(), w, base)

	got := w.addedRel(t, base)
	want := []string{".", "Public", "Public/go", "Public/go/alpha/.git"}
	if len(got) != len(want) {
		t.Fatalf("watched %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("watched %v, want %v", got, want)
		}
	}
}

// P-01: a failed Add was discarded, so the process kept trying and the
// operator never learned the watcher was not working.
func TestRegisterWatchDirsReportsAddFailure(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	srv, err := NewServer(ServerOptions{Root: base})
	if err != nil {
		t.Fatal(err)
	}
	w := newRecordingWatcher()
	w.addErr = syscall.EMFILE
	rep := srv.registerWatchDirs(context.Background(), w, base)
	if rep.err == nil || !errors.Is(rep.err, syscall.EMFILE) {
		t.Fatalf("report error = %v, want EMFILE", rep.err)
	}
	if rep.watched != 0 {
		t.Fatalf("watched = %d after the first Add failed, want 0", rep.watched)
	}
	if !rep.degraded() {
		t.Fatal("a failed registration must report the watcher as degraded")
	}
}

// P-01: the walk ran before the stdio transport started, so a large
// workspace delayed the MCP handshake by the length of the walk.
func TestStartWorkspaceWatcherDoesNotBlockOnRegistration(t *testing.T) {
	base := t.TempDir()
	srv, err := NewServer(ServerOptions{Root: base})
	if err != nil {
		t.Fatal(err)
	}
	w := newRecordingWatcher()
	w.blockOn = make(chan struct{})
	old := newFSWatcher
	newFSWatcher = func() (fsWatcher, error) { return w, nil }
	defer func() { newFSWatcher = old }()

	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan (<-chan struct{}), 1)
	go func() { returned <- srv.startWorkspaceWatcher(ctx) }()

	var done <-chan struct{}
	select {
	case done = <-returned:
	case <-time.After(2 * time.Second):
		close(w.blockOn)
		t.Fatal("startWorkspaceWatcher blocked on directory registration")
	}
	cancel()
	close(w.blockOn)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not stop after cancel")
	}
}

// P-01: a root the walk could not read produced an empty index and no
// error, which a search reported as "No match ... across 0 repositories".
func TestScanUnreadableRootIsAnError(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	old := walkIndex
	walkIndex = func(root string, fn fs.WalkDirFunc) error {
		info, err := os.Stat(root)
		if err != nil {
			t.Fatal(err)
		}
		d := fs.FileInfoToDirEntry(info)
		if err := fn(root, d, nil); err != nil {
			return err
		}
		return fn(root, d, &fs.PathError{Op: "open", Path: root, Err: syscall.EMFILE})
	}
	defer func() { walkIndex = old }()

	idx, err := Scan(base)
	if err == nil {
		t.Fatalf("Scan returned %d repositories and no error for an unreadable root", len(idx.Repos))
	}
	if !errors.Is(err, syscall.EMFILE) {
		t.Fatalf("Scan error = %v, want it to wrap EMFILE", err)
	}
}

// P-01: running out of descriptors part-way through the walk dropped every
// clone beneath the directory it happened on. That is the process failing,
// not one unreadable directory, so the scan fails rather than answering
// with part of the workspace.
func TestScanDescriptorExhaustionIsAnError(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	makeFakeRepo(t, base, "Private", "go", "beta", "", "")
	blocked := filepath.Join(base, "Private")
	old := walkIndex
	defer func() { walkIndex = old }()

	walkIndex = walkFailingAt(blocked, syscall.EMFILE)
	idx, err := Scan(base)
	if !errors.Is(err, syscall.EMFILE) {
		t.Fatalf("Scan = %v, %v; want an EMFILE error", idx, err)
	}

	// A directory that is merely unreadable is still tolerated.
	walkIndex = walkFailingAt(blocked, fs.ErrPermission)
	idx, err = Scan(base)
	if err != nil || len(idx.Repos) != 1 {
		t.Fatalf("Scan with a permission-denied directory = %v, %v; want 1 repository", idx, err)
	}
}

// walkFailingAt is filepath.WalkDir, except that reading the directory at
// blocked fails with cause, the way WalkDir reports it: a second call for
// the same directory carrying the error.
func walkFailingAt(blocked string, cause error) func(string, fs.WalkDirFunc) error {
	return func(root string, fn fs.WalkDirFunc) error {
		return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if path != blocked || err != nil {
				return fn(path, d, err)
			}
			if r := fn(path, d, nil); r != nil {
				return r
			}
			return fn(path, d, &fs.PathError{Op: "open", Path: path, Err: cause})
		})
	}
}

func TestWatchBudgetFromLimit(t *testing.T) {
	if got := budgetFromLimit(256); got != 128 {
		t.Fatalf("budgetFromLimit(256) = %d, want 128", got)
	}
	if got := budgetFromLimit(1 << 40); got != maxWatchBudget {
		t.Fatalf("budgetFromLimit(huge) = %d, want the cap", got)
	}
	if got := defaultWatchBudget(); got <= 0 {
		t.Fatalf("defaultWatchBudget() = %d on this machine", got)
	}
}

func TestRegisterWatchDirsStaysWithinBudget(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	makeFakeRepo(t, base, "Public", "go", "beta", "", "")
	srv, err := NewServer(ServerOptions{Root: base})
	if err != nil {
		t.Fatal(err)
	}
	old := watchBudget
	watchBudget = func() int { return 3 }
	defer func() { watchBudget = old }()

	logs := captureDiag(t, diag.LevelWarn)
	w := newRecordingWatcher()
	r := srv.newWatchRegistrar(context.Background(), w)
	r.add(base)
	if r.rep.cost > 3 || r.rep.skipped == 0 || !r.rep.degraded() {
		t.Fatalf("report = %+v, want cost within 3 and something skipped", r.rep)
	}
	r.warnIfDegraded()
	r.warnIfDegraded()
	if n := strings.Count(logs.String(), "budget"); n != 1 {
		t.Fatalf("budget warning logged %d times, want once:\n%s", n, logs)
	}
}

func TestRegisterWatchDirsWarnsOnAddFailure(t *testing.T) {
	base := t.TempDir()
	srv, err := NewServer(ServerOptions{Root: base})
	if err != nil {
		t.Fatal(err)
	}
	logs := captureDiag(t, diag.LevelWarn)
	w := newRecordingWatcher()
	w.addErr = syscall.EMFILE
	r := srv.newWatchRegistrar(context.Background(), w)
	r.add(base)
	r.warnIfDegraded()
	if !strings.Contains(logs.String(), "stopped adding watches") {
		t.Fatalf("no warning for a failed Add:\n%s", logs)
	}

	// A healthy registration says nothing.
	logs.Reset()
	r = srv.newWatchRegistrar(context.Background(), newRecordingWatcher())
	r.add(base)
	r.warnIfDegraded()
	if logs.Len() != 0 {
		t.Fatalf("unexpected warning: %s", logs)
	}
}

func TestRegisterWatchDirsToleratesVanishedDirectory(t *testing.T) {
	base := t.TempDir()
	srv, err := NewServer(ServerOptions{Root: base})
	if err != nil {
		t.Fatal(err)
	}
	w := newRecordingWatcher()
	w.addErr = fs.ErrNotExist
	if rep := srv.registerWatchDirs(context.Background(), w, base); rep.err != nil || rep.watched != 0 {
		t.Fatalf("report = %+v, want a vanished directory skipped quietly", rep)
	}
}

func TestWatchRegistrarAddPaths(t *testing.T) {
	base := t.TempDir()
	makeFakeRepo(t, base, "Public", "go", "alpha", "", "")
	fresh := filepath.Join(base, "Public", "go", "fresh")
	worktree := filepath.Join(base, "Public", "go", "wt")
	mkdirs(t,
		filepath.Join(fresh, ".git"),
		filepath.Join(base, "Public", "go", "alpha", "src"),
		filepath.Join(base, "node_modules", "x"),
		filepath.Join(base, "Public", ".hidden"),
		filepath.Join(base, "a", "b", "c", "d", "e", "f"),
		worktree,
	)
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(ServerOptions{Root: base})
	if err != nil {
		t.Fatal(err)
	}
	w := newRecordingWatcher()
	r := srv.newWatchRegistrar(context.Background(), w)
	for _, p := range []string{
		"relative/path",
		filepath.Dir(base),
		filepath.Join(base, "node_modules", "x"),
		filepath.Join(base, "Public", ".hidden"),
		filepath.Join(base, "Public", "go", "alpha", "src"),
		filepath.Join(base, "a", "b", "c", "d", "e"),
		filepath.Join(base, "a", "b", "c", "d", "e", "f"),
		worktree,
		filepath.Join(fresh, ".git"),
	} {
		r.add(p)
	}
	got := w.addedRel(t, base)
	if len(got) != 1 || got[0] != "Public/go/fresh/.git" {
		t.Fatalf("watched %v, want only the new clone's .git", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r = srv.newWatchRegistrar(ctx, w)
	r.add(filepath.Join(fresh, ".git"))
	if r.rep.watched != 0 {
		t.Fatal("a stopped registrar added a watch")
	}
}

func TestWorkspaceWatcherWatchesCreatedDirectory(t *testing.T) {
	base := t.TempDir()
	srv, err := NewServer(ServerOptions{Root: base})
	if err != nil {
		t.Fatal(err)
	}
	w := newRecordingWatcher()
	old := newFSWatcher
	newFSWatcher = func() (fsWatcher, error) { return w, nil }
	defer func() { newFSWatcher = old }()

	ctx, cancel := context.WithCancel(context.Background())
	done := srv.startWorkspaceWatcher(ctx)
	defer func() { cancel(); <-done }()

	// Let the initial registration finish first, or it may find the
	// directory itself and the event below would prove nothing.
	waitWatched(t, w, base, ".")
	created := filepath.Join(base, "Public")
	mkdirs(t, created)
	file := filepath.Join(base, "notes.txt")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	w.events <- fsnotify.Event{Name: file, Op: fsnotify.Create}
	w.events <- fsnotify.Event{Name: created, Op: fsnotify.Create}
	waitWatched(t, w, base, "Public")
}

// waitWatched waits until rel (relative to base) has been added to w.
func waitWatched(t *testing.T, w *recordingWatcher, base, rel string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range w.addedRel(t, base) {
			if p == rel {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("%s was not watched; watched %v", rel, w.addedRel(t, base))
}

// The debounce callback must never block: after the loop has exited nothing
// drains fire, and a pending signal is as good as a second one.
func TestWatcherDebounceNeverBlocks(t *testing.T) {
	ww := &workspaceWatcher{fire: make(chan struct{}, 1)}
	ww.fire <- struct{}{}
	ww.arm()
	ww.arm()
	time.Sleep(4 * watcherDebounceDelay)
	ww.stopTimer()
	if len(ww.fire) != 1 {
		t.Fatalf("fire holds %d signals, want 1", len(ww.fire))
	}
}
