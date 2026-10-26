// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/github"
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
