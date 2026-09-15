// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/github"
)

func TestManagedFinderTagsLifecycle(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		repo   github.Repo
		result RepoResult
		want   string
	}{
		{name: "error", result: RepoResult{Action: "ERROR"}, want: "Needs Fix\n6"},
		{name: "failure prefix", result: RepoResult{Action: "FAIL-PULL"}, want: "Needs Fix\n6"},
		{name: "archived", repo: github.Repo{Archived: true}, want: "On Hold\n5"},
		{name: "fork", repo: github.Repo{Fork: true}, want: "Experiment\n3"},
		{name: "template", repo: github.Repo{IsTemplate: true}, want: "Experiment\n3"},
		{name: "mirror", repo: github.Repo{IsMirror: true}, want: "Experiment\n3"},
		{name: "working branch", result: RepoResult{Message: "on branch feature"}, want: "Active\n2"},
		{name: "recent", repo: github.Repo{PushedAt: now.Add(-24 * time.Hour)}, want: "Active\n2"},
		{name: "stable", repo: github.Repo{PushedAt: now.AddDate(-1, 0, 0)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.repo.Name = "repo"
			tt.repo.Language = "Go"
			tt.repo.Visibility = "Public"
			tags := managedFinderTags(tt.repo, tt.result, now)
			if tt.want != "" && !containsString(tags, tt.want) {
				t.Fatalf("tags %v do not contain %q", tags, tt.want)
			}
			if tt.want == "" && containsLifecycleTag(tags) {
				t.Fatalf("stable repository received lifecycle tag: %v", tags)
			}
		})
	}
}

func TestManagedFinderTagsMetadata(t *testing.T) {
	repo := github.Repo{
		Name: "site.github.io", FullName: "acme/site.github.io", Visibility: "Private",
		Language: "JavaScript", Fork: true, Archived: true, IsTemplate: true, IsMirror: true,
	}
	tags := managedFinderTags(repo, RepoResult{}, time.Now())
	for _, want := range []string{"GitHub\n0", "Visibility: Private\n0", "Collection: Forks\n0", "Ecosystem: Web\n0", "Owner: acme\n0", "Archived\n0", "Fork\n0", "Template\n0", "Mirror\n0"} {
		if !containsString(tags, want) {
			t.Errorf("tags %v do not contain %q", tags, want)
		}
	}
}

func TestMergeFinderTagsPreservesPersonalTags(t *testing.T) {
	existing := []string{"Client\n4", "Active\n2", "Ecosystem: Rust\n0", "Client\n4"}
	managed := []string{"Active\n2", "GitHub\n0"}
	want := []string{"Client\n4", "Active\n2", "GitHub\n0"}
	if got := mergeFinderTags(existing, managed); !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeFinderTags() = %v, want %v", got, want)
	}
	for _, tag := range []string{"On Hold\n5", "Needs Fix\n6", "Experiment\n3", "Fork\n0", "Archived\n0", "Template\n0", "Mirror\n0", "Visibility: Public\n0", "Collection: Public\n0", "Owner: acme\n0"} {
		if !isManagedFinderTag(tag) {
			t.Errorf("expected %q to be managed", tag)
		}
	}
	if isManagedFinderTag("Client\n4") {
		t.Fatal("personal tag was treated as managed")
	}
}

func TestCanonicalVisibility(t *testing.T) {
	if canonicalVisibility("PRIVATE") != "Private" || canonicalVisibility("public") != "Public" {
		t.Fatal("visibility was not canonicalized")
	}
}

func TestApplyFinderTags(t *testing.T) {
	oldRead, oldWrite, oldNow := readFinderTags, writeFinderTags, finderNow
	t.Cleanup(func() { readFinderTags, writeFinderTags, finderNow = oldRead, oldWrite, oldNow })
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	finderNow = func() time.Time { return now }

	readFinderTags = func(string) ([]string, error) { return nil, errors.New("read") }
	if err := applyFinderTags("repo", github.Repo{}, RepoResult{}); err == nil {
		t.Fatal("expected read error")
	}

	readFinderTags = func(string) ([]string, error) { return []string{"Client\n4"}, nil }
	writeFinderTags = func(path string, tags []string) error {
		if path != "repo" || !containsString(tags, "Client\n4") {
			t.Fatalf("unexpected write: %q %v", path, tags)
		}
		return errors.New("write")
	}
	if err := applyFinderTags("repo", github.Repo{Name: "repo", Language: "Go"}, RepoResult{}); err == nil {
		t.Fatal("expected write error")
	}
}

func TestApplyJobFinderTags(t *testing.T) {
	oldApply := applyTags
	t.Cleanup(func() { applyTags = oldApply })
	calls := 0
	applyTags = func(path string, _ github.Repo, _ RepoResult) error {
		calls++
		if path == "error-path" {
			return errors.New("tag")
		}
		return nil
	}
	job := Job{Existing: "existing", Repo: github.Repo{Name: "repo"}}

	applyJobFinderTags(RunOptions{}, job, RepoResult{Target: "target"})
	applyJobFinderTags(RunOptions{FinderTags: true, DryRun: true}, job, RepoResult{Target: "target"})
	applyJobFinderTags(RunOptions{FinderTags: true}, Job{}, RepoResult{Action: "ERROR", Target: "target"})
	applyJobFinderTags(RunOptions{FinderTags: true}, job, RepoResult{Target: "target"})
	applyJobFinderTags(RunOptions{FinderTags: true}, job, RepoResult{Action: "ERROR", Target: "target"})
	job.Existing = "error-path"
	applyJobFinderTags(RunOptions{FinderTags: true}, job, RepoResult{Action: "FAIL-PULL", Target: "target"})
	if calls != 3 {
		t.Fatalf("applyTags calls = %d, want 3", calls)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsLifecycleTag(tags []string) bool {
	for _, tag := range tags {
		switch tag {
		case "Active\n2", "On Hold\n5", "Needs Fix\n6", "Experiment\n3":
			return true
		}
	}
	return false
}
