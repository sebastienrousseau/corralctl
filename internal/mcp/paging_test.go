// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"encoding/json"
	"fmt"
	"testing"
)

func makeEntries(n int) []RepoEntry {
	out := make([]RepoEntry, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, RepoEntry{
			Name:       fmt.Sprintf("repo-%d", i),
			RelPath:    fmt.Sprintf("Public/go/repo-%d", i),
			Path:       fmt.Sprintf("/w/Public/go/repo-%d", i),
			RemoteURL:  fmt.Sprintf("https://github.com/o/repo-%d.git", i),
			Visibility: "Public",
			Language:   "go",
			State:      &StateRecord{LastSyncedAt: "2026-08-17T00:00:00Z"},
		})
	}
	return out
}

func TestPaginateWindows(t *testing.T) {
	entries := makeEntries(120)

	page, meta := paginate(entries, 50, 0)
	if len(page) != 50 || meta.Total != 120 || meta.Returned != 50 || meta.NextOffset != 50 {
		t.Fatalf("first page: len=%d meta=%+v", len(page), meta)
	}
	if page[0].Name != "repo-0" {
		t.Errorf("first page starts at %s", page[0].Name)
	}

	page, meta = paginate(entries, 50, 100)
	if len(page) != 20 || meta.NextOffset != 0 {
		t.Fatalf("last page: len=%d meta=%+v", len(page), meta)
	}
	if page[0].Name != "repo-100" {
		t.Errorf("last page starts at %s", page[0].Name)
	}
}

// Out-of-range and nonsensical inputs are clamped, not errors: a model that
// guesses an offset past the end should get an empty page and a clear total
// rather than a failure it has to recover from.
func TestPaginateClampsBadInput(t *testing.T) {
	entries := makeEntries(10)
	for _, tc := range []struct {
		name          string
		limit, offset int
		wantLen       int
	}{
		{"offset past end", 50, 999, 0},
		{"negative offset", 50, -5, 10},
		{"zero limit falls back to default", 0, 0, 10},
		{"negative limit falls back to default", -1, 0, 10},
		{"limit above the cap", 99999, 0, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, meta := paginate(entries, tc.limit, tc.offset)
			if len(page) != tc.wantLen {
				t.Errorf("len=%d, want %d (meta %+v)", len(page), tc.wantLen, meta)
			}
			if meta.Total != 10 {
				t.Errorf("total should always report the real match count, got %d", meta.Total)
			}
		})
	}
}

func TestPaginateRespectsMaxPageSize(t *testing.T) {
	page, _ := paginate(makeEntries(500), 100000, 0)
	if len(page) != maxPageSize {
		t.Errorf("page size %d, want it capped at %d", len(page), maxPageSize)
	}
}

// The concise projection is what keeps a first call inside a client's budget.
func TestProjectReposConciseIsMuchSmaller(t *testing.T) {
	entries := makeEntries(50)
	detailed, err := json.MarshalIndent(projectRepos(entries, formatDetailed), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	concise, err := json.MarshalIndent(projectRepos(entries, formatConcise), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if len(concise) >= len(detailed) {
		t.Fatalf("concise (%d) should be smaller than detailed (%d)", len(concise), len(detailed))
	}
	// Guard the actual win, not merely "smaller".
	if ratio := float64(len(detailed)) / float64(len(concise)); ratio < 2 {
		t.Errorf("concise is only %.1fx smaller; the projection is not earning its keep", ratio)
	}
	// Concise must still identify and locate a repository.
	if !contains(string(concise), "rel_path") || !contains(string(concise), "repo-0") {
		t.Error("concise projection must keep rel_path and name")
	}
	// And must not leak the fields it exists to drop.
	if contains(string(concise), "remote_url") || contains(string(concise), "last_synced") {
		t.Error("concise projection is still carrying detailed fields")
	}
}

// An unrecognised format must degrade to the cheap shape rather than erroring,
// so a model that invents a value does not get a failure it must recover from.
func TestProjectReposUnknownFormatIsConcise(t *testing.T) {
	// Asserted on the projection's content rather than its Go type. Both
	// shapes are now one struct — which is what lets a single output schema
	// describe either — so "is it concise" is a question about which fields
	// were filled, not about which type came back.
	out := projectRepos(makeEntries(2), "verbose-please")
	if len(out) != 2 {
		t.Fatalf("got %d entries, want 2", len(out))
	}
	for i, r := range out {
		if r.RelPath == "" || r.Name == "" {
			t.Errorf("entry %d lost its identity: %+v", i, r)
		}
		if r.Path != "" || r.RemoteURL != "" || r.State != nil {
			t.Errorf("entry %d carries detailed fields for an unknown format: %+v", i, r)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
