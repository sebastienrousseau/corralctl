// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/sebastienrousseau/corralctl/internal/github"
)

// TestRecordedVisibility: only an explicit record is known; a missing,
// empty, unrecognised or unreadable one reads as private and unknown.
func TestRecordedVisibility(t *testing.T) {
	cases := map[string]struct{ private, known bool }{
		"public":  {false, true},
		"private": {true, true},
		"":        {true, false},
		"secret":  {true, false},
	}
	for value, want := range cases {
		dir := newStateRepo(t)
		if err := writeCloneState(dir, cloneState{Visibility: value}); err != nil {
			t.Fatal(err)
		}
		if private, known := RecordedVisibility(dir); private != want.private || known != want.known {
			t.Errorf("%q: private=%t known=%t", value, private, known)
		}
	}
	dir := newStateRepo(t)
	path, err := statePath(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if private, known := RecordedVisibility(dir); !private || known {
		t.Errorf("malformed: private=%t known=%t", private, known)
	}
}

// TestRecordVisibilityKeepsTheOtherFields: refreshing the visibility on a
// skipped clone must not reset the smart-sync timestamp.
func TestRecordVisibilityKeepsTheOtherFields(t *testing.T) {
	dir := newStateRepo(t)
	pushed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := writeCloneState(dir, cloneState{LastSyncedPushedAt: pushed, Visibility: visibilityPublic}); err != nil {
		t.Fatal(err)
	}
	recordVisibility(dir, github.Repo{Visibility: "Private"})
	got, err := readCloneState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Visibility != visibilityPrivate || !got.LastSyncedPushedAt.Equal(pushed) {
		t.Fatalf("state = %+v", got)
	}
	// Unchanged and unrecognised values leave the file alone.
	recordVisibility(dir, github.Repo{Visibility: "Private"})
	recordVisibility(dir, github.Repo{Visibility: ""})
	if got, _ := readCloneState(dir); got.Visibility != visibilityPrivate {
		t.Fatalf("state = %+v", got)
	}
	// A clone whose sidecar cannot be written keeps going.
	recordVisibility("/no/such/path/anywhere", github.Repo{Visibility: "Public"})
}

// TestProcessRepoRecordsVisibilityOnEveryPass: an existing clone gets its
// listed visibility recorded even when nothing is pulled, because a
// visibility change upstream does not move pushed_at. A dry run writes
// nothing.
func TestProcessRepoRecordsVisibilityOnEveryPass(t *testing.T) {
	dir := newStateRepo(t)
	cmd := exec.Command("git", "remote", "add", "origin", "https://github.com/acme/repo.git")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	job := Job{Target: dir, Repo: github.Repo{
		Name: "repo", Owner: "acme", Visibility: "Private",
		CloneURL: "https://github.com/acme/repo.git",
	}}
	dry := processRepo(context.Background(), "acme", "https", false, true, git.CloneOptions{}, SyncOptions{}, job)
	if _, known := RecordedVisibility(dir); known || dry.Action != "SKIP" {
		t.Fatalf("dry run recorded a visibility: %+v", dry)
	}
	result := processRepo(context.Background(), "acme", "https", false, false, git.CloneOptions{}, SyncOptions{}, job)
	if result.Action != "SKIP" {
		t.Fatalf("result = %+v", result)
	}
	if private, known := RecordedVisibility(dir); !private || !known {
		t.Fatalf("private=%t known=%t", private, known)
	}
}

// TestStampCloneStateRecordsVisibility: a clone or pull records the
// visibility with the timestamp.
func TestStampCloneStateRecordsVisibility(t *testing.T) {
	dir := newStateRepo(t)
	stampCloneState(dir, github.Repo{Visibility: "Public"})
	if private, known := RecordedVisibility(dir); private || !known {
		t.Fatalf("private=%t known=%t", private, known)
	}
}
