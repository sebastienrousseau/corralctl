// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/github"
	"github.com/sebastienrousseau/corralctl/internal/tui"
)

// TestRunEPassesForgeSelectionToFetch pins A-03: the forge travels with
// the run's options to the listing, rather than through package state
// that a concurrent run could overwrite.
func TestRunEPassesForgeSelectionToFetch(t *testing.T) {
	old := fetchRepos
	t.Cleanup(func() { fetchRepos = old })
	var gotName, gotURL string
	fetchRepos = func(_ context.Context, _ string, _ github.FetchOptions, name, url string) ([]github.Repo, error) {
		gotName, gotURL = name, url
		return nil, nil
	}
	opts := defaultRunOptions(t.TempDir())
	opts.Forge, opts.ForgeURL = "gitea", "https://git.example.com"
	opts.Output = OutputJSON
	if err := RunE(context.Background(), opts); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if gotName != "gitea" || gotURL != "https://git.example.com" {
		t.Errorf("fetch saw forge %q at %q, want gitea at https://git.example.com", gotName, gotURL)
	}
}

// TestOrphanReportUsesTheRunsForge: orphan detection reads the forge from
// the options it is given. It used to read package state that only RunE
// set, so any other caller compared a Codeberg tree against GitHub and
// found nothing.
func TestOrphanReportUsesTheRunsForge(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "Public", "other", "gone")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := "[remote \"origin\"]\n\turl = https://codeberg.org/acme/gone.git\n"
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := RunOptions{Owner: "acme", BaseDir: base, Orphans: true, Output: OutputJSON, Forge: "codeberg"}
	upstream := []github.Repo{{Name: "kept", FullName: "acme/kept"}}
	got := emitOrphanReport(opts, upstream, &runOutcome{}, nil)
	if len(got) != 1 || got[0] != dir {
		t.Errorf("orphans = %v, want [%s]", got, dir)
	}
}

// TestRunELeavesTheStandardLoggerAlone pins A-03: RunE redirected the
// process-wide standard logger to stderr on every run, overriding
// whatever an embedder had configured. The engine writes its own
// diagnostics through its own logger instead.
func TestRunELeavesTheStandardLoggerAlone(t *testing.T) {
	old := fetchRepos
	t.Cleanup(func() { fetchRepos = old })
	fetchRepos = func(context.Context, string, github.FetchOptions, string, string) ([]github.Repo, error) {
		return nil, nil
	}
	var embedder bytes.Buffer
	oldOut := log.Writer()
	log.SetOutput(&embedder)
	t.Cleanup(func() { log.SetOutput(oldOut) })

	if err := RunE(context.Background(), defaultRunOptions(t.TempDir())); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if log.Writer() != &embedder {
		t.Error("RunE replaced the standard logger's output")
	}
}

// TestRunELeavesTheBuildVersionAlone pins A-03: RunE wrote opts.Version
// into tui.Version, a package variable the build sets, so one run's
// version leaked into every later selector in the process. The run's
// version reaches the selector it starts as an argument instead.
func TestRunELeavesTheBuildVersionAlone(t *testing.T) {
	oldSelector, oldVersion := runSelector, tui.Version
	t.Cleanup(func() { runSelector, tui.Version = oldSelector, oldVersion })
	tui.Version = "build"
	var gotVersion string
	runSelector = func(_ context.Context, _, version string, _ github.FetchOptions, _ tui.FetchFunc) ([]github.Repo, bool, error) {
		gotVersion = version
		return nil, false, nil
	}
	opts := defaultRunOptions(t.TempDir())
	opts.Interactive, opts.Version = true, "9.9.9-embedder"
	if err := RunE(context.Background(), opts); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if tui.Version != "build" {
		t.Errorf("tui.Version = %q after RunE, want the build's %q", tui.Version, "build")
	}
	if gotVersion != "9.9.9-embedder" {
		t.Errorf("selector was given version %q, want the run's", gotVersion)
	}
}
