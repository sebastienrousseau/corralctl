// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mirror

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestWalkDoesNotTrustAPublicDirectory is the regression test for S-07: a
// clone under Public/ with no recorded upstream visibility was walked as
// public, and sync would create it public on every destination. A
// directory name is a guess, so an unrecorded repository is private.
func TestWalkDoesNotTrustAPublicDirectory(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "Public", "Go", "secret")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	got, err := Walk(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Private {
		t.Fatalf("an unrecorded repository under Public/ must be private: %+v", got)
	}
}

// stubRecorded replaces the sidecar lookup: a repository named in
// recorded has that visibility (true for private) on record, any other
// has none.
func stubRecorded(t *testing.T, recorded map[string]bool) {
	t.Helper()
	old := recordedVisibility
	t.Cleanup(func() { recordedVisibility = old })
	recordedVisibility = func(path string) (bool, bool) {
		private, ok := recorded[filepath.Base(path)]
		if !ok {
			return true, false
		}
		return private, true
	}
}

// TestWalkTakesVisibilityFromTheRecord: public needs both the forge's word
// and a directory that says public; anything less is private.
func TestWalkTakesVisibilityFromTheRecord(t *testing.T) {
	base := t.TempDir()
	cases := []struct {
		rel     string
		private bool
		known   bool
	}{
		{filepath.Join("Forks", "Go", "fork"), true, true},         // recorded public, directory not public
		{filepath.Join("Private", "Go", "filed"), true, true},      // recorded public, directory vetoes
		{filepath.Join("Public", "Go", "madeprivate"), true, true}, // recorded private under Public/
		{filepath.Join("Public", "Go", "open"), false, true},       // recorded public under Public/
		{filepath.Join("Public", "Go", "unknown"), true, false},    // nothing recorded
	}
	for _, c := range cases {
		if err := os.MkdirAll(filepath.Join(base, c.rel, ".git"), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	stubRecorded(t, map[string]bool{"fork": false, "filed": false, "madeprivate": true, "open": false})
	got, err := Walk(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(cases) {
		t.Fatalf("repositories = %+v", got)
	}
	for i, c := range cases {
		if got[i].Private != c.private || got[i].VisibilityKnown != c.known {
			t.Errorf("%s: private=%t known=%t, want private=%t known=%t", c.rel, got[i].Private, got[i].VisibilityKnown, c.private, c.known)
		}
	}
}

// TestWrongVisibilityExplainsAnUnrecordedClone: a clone that is private
// only because nothing was recorded is refused against an existing
// public destination, and the refusal says how to clear it. A recorded
// one gets the plain refusal.
func TestWrongVisibilityExplainsAnUnrecordedClone(t *testing.T) {
	withSeams(t)
	public := okRemote()
	public.Private = false
	for _, known := range []bool{false, true} {
		report, results := collect()
		Run(context.Background(), Options{
			Repos:        []Repo{{Name: "a", Path: "/a", Private: true, VisibilityKnown: known}},
			Destinations: []Destination{dest("gitlab", "gitlab.example.com", &fakeTarget{login: "me", remote: public})},
			Timeout:      time.Second,
			Report:       report,
		})
		r, _ := find(*results, "a", "gitlab")
		hinted := strings.Contains(r.Message, "run `corralctl clone`")
		if r.Action != ActionError || !strings.Contains(r.Message, "wrong visibility") || hinted == known {
			t.Errorf("known=%t: %+v", known, r)
		}
	}
}

// TestDryRunNamesTheVisibility: the preview says which visibility a
// destination would be created with, so a dry run shows S-07's decision.
func TestDryRunNamesTheVisibility(t *testing.T) {
	withSeams(t)
	report, results := collect()
	Run(context.Background(), Options{
		Repos:        []Repo{{Name: "a", Path: "/a", Private: true}, {Name: "b", Path: "/b"}},
		Destinations: []Destination{dest("gitlab", "gitlab.example.com", &fakeTarget{login: "me"})},
		Timeout:      time.Second,
		DryRun:       true,
		Report:       report,
	})
	a, _ := find(*results, "a", "gitlab")
	b, _ := find(*results, "b", "gitlab")
	if !strings.Contains(a.Message, "me/a (private)") || !strings.Contains(b.Message, "me/b (public)") {
		t.Fatalf("dry run = %+v / %+v", a, b)
	}
}
