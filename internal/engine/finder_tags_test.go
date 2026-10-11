// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/sebastienrousseau/corralctl/internal/github"
)

func TestManagedFinderTagsLifecycle(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	oldInspect := inspectGitStatus
	t.Cleanup(func() { inspectGitStatus = oldInspect })

	tests := []struct {
		name       string
		repo       github.Repo
		result     RepoResult
		mockStatus git.RepoStatus
		want       string
	}{
		{name: "error", result: RepoResult{Action: "ERROR"}, want: "Needs Fix\n6"},
		{name: "failure prefix", result: RepoResult{Action: "FAIL-PULL"}, want: "Needs Fix\n6"},
		{name: "conflict", mockStatus: git.RepoStatus{HasConflict: true}, want: "Needs Fix\n6"},
		{name: "diverged", mockStatus: git.RepoStatus{IsDiverged: true}, want: "Needs Fix\n6"},
		{name: "dirty", mockStatus: git.RepoStatus{IsDirty: true}, want: "In-Flight\n2"},
		{name: "unpushed", mockStatus: git.RepoStatus{HasUnpushed: true}, want: "In-Flight\n2"},
		{name: "stash", mockStatus: git.RepoStatus{HasStash: true}, want: "In-Flight\n2"},
		{name: "worktree", mockStatus: git.RepoStatus{HasWorktree: true}, want: "Worktree\n3"},
		{name: "archived", repo: github.Repo{Archived: true}, want: "On Hold\n5"},
		{name: "fork", repo: github.Repo{Fork: true}, want: "Experiment\n3"},
		{name: "template", repo: github.Repo{IsTemplate: true}, want: "Experiment\n3"},
		{name: "mirror", repo: github.Repo{IsMirror: true}, want: "Experiment\n3"},
		{name: "dormant", mockStatus: git.RepoStatus{IsDormant: true}, want: "Dormant\n1"},
		{name: "pristine", want: "Pristine\n4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inspectGitStatus = func(context.Context, string) (git.RepoStatus, error) {
				return tt.mockStatus, nil
			}
			tt.repo.Name = "repo"
			tt.repo.Language = "Go"
			tt.repo.Visibility = "Public"
			tags := managedFinderTags("repo", tt.repo, tt.result, now)
			if tt.want != "" && !containsString(tags, tt.want) {
				t.Fatalf("tags %v do not contain %q", tags, tt.want)
			}
		})
	}
}

func TestManagedFinderTagsMetadata(t *testing.T) {
	repo := github.Repo{
		Name: "site.github.io", FullName: "acme/site.github.io", Visibility: "Private",
		Language: "JavaScript", Fork: true, Archived: true, IsTemplate: true, IsMirror: true,
	}
	tags := managedFinderTags("repo", repo, RepoResult{}, time.Now())
	for _, want := range []string{"GitHub\n0", "Visibility: Private\n0", "Collection: Forks\n0", "Ecosystem: Web\n0", "Owner: acme\n0", "Archived\n0", "Fork\n0", "Template\n0", "Mirror\n0"} {
		if !containsString(tags, want) {
			t.Errorf("tags %v do not contain %q", tags, want)
		}
	}
}

func TestExtractFinderColor(t *testing.T) {
	if extractFinderColor([]string{"GitHub\n0", "Visibility: Public\n0"}) != 0 {
		t.Errorf("expected 0 for non-colored tags")
	}
	if extractFinderColor([]string{"GitHub\n0", "In-Flight\n2"}) != 2 {
		t.Errorf("expected 2 for In-Flight tag")
	}
	if extractFinderColor([]string{"invalid-tag"}) != 0 {
		t.Errorf("expected 0 for malformed tag")
	}
}

func TestRefreshRepoFinderTags(t *testing.T) {
	oldRead, oldWrite, oldInspect := readFinderTags, writeFinderTags, inspectGitStatus
	t.Cleanup(func() { readFinderTags, writeFinderTags, inspectGitStatus = oldRead, oldWrite, oldInspect })

	inspectGitStatus = func(context.Context, string) (git.RepoStatus, error) {
		return git.RepoStatus{}, nil
	}

	dir := t.TempDir()
	repoPath := filepath.Join(dir, "Public", "Go", "myrepo")
	if err := os.MkdirAll(filepath.Join(repoPath, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := writeCloneState(repoPath, cloneState{LastSyncedPushedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	readFinderTags = func(string) ([]string, error) { return nil, nil }
	var writtenTags []string
	writeFinderTags = func(_ string, tags []string) error {
		writtenTags = tags
		return nil
	}

	if err := RefreshRepoFinderTags(repoPath); err != nil {
		t.Fatalf("RefreshRepoFinderTags failed: %v", err)
	}
	if !containsString(writtenTags, "Ecosystem: Go\n0") {
		t.Errorf("expected Ecosystem: Go in written tags, got %v", writtenTags)
	}
	if !containsString(writtenTags, "Pristine\n4") {
		t.Errorf("expected Pristine\n4 in written tags, got %v", writtenTags)
	}
}

func TestMergeFinderTagsPreservesPersonalTags(t *testing.T) {
	existing := []string{"Client\n4", "In-Flight\n2", "Ecosystem: Rust\n0", "Client\n4"}
	managed := []string{"In-Flight\n2", "GitHub\n0"}
	want := []string{"Client\n4", "GitHub\n0", "In-Flight\n2"}
	if got := mergeFinderTags(existing, managed); !reflect.DeepEqual(got, want) {
		t.Fatalf("mergeFinderTags() = %v, want %v", got, want)
	}
	for _, tag := range []string{"In-Flight\n2", "Pristine\n4", "Dormant\n1", "Worktree\n3", "On Hold\n5", "Needs Fix\n6", "Experiment\n3", "Fork\n0", "Archived\n0", "Template\n0", "Mirror\n0", "Visibility: Public\n0", "Collection: Public\n0", "Owner: acme\n0"} {
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

func TestApplyCandidateFinderTags(t *testing.T) {
	oldRead, oldWrite := readFinderTags, writeFinderTags
	t.Cleanup(func() { readFinderTags, writeFinderTags = oldRead, oldWrite })

	// Read error
	readFinderTags = func(string) ([]string, error) { return nil, errors.New("read error") }
	if err := ApplyCandidateFinderTags("repo", true, "Go"); err == nil {
		t.Fatal("expected read error")
	}

	// Success with tags
	var writtenTags []string
	readFinderTags = func(string) ([]string, error) { return []string{"Client\n4"}, nil }
	writeFinderTags = func(path string, tags []string) error {
		writtenTags = tags
		return nil
	}

	if err := ApplyCandidateFinderTags("repo", true, "Go"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !containsString(writtenTags, "New Repo\n7") {
		t.Errorf("written tags %v do not contain New Repo", writtenTags)
	}
	if !containsString(writtenTags, "Ecosystem: Go\n0") {
		t.Errorf("written tags %v do not contain Ecosystem: Go", writtenTags)
	}

	// Success with isNew false and empty ecosystem
	if err := ApplyCandidateFinderTags("repo", false, ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
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
