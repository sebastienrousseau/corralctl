// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"context"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/sebastienrousseau/corralctl/internal/github"
)

const (
	finderNone   = 0
	finderGrey   = 1
	finderGreen  = 2
	finderPurple = 3
	finderBlue   = 4
	finderYellow = 5
	finderRed    = 6
	finderOrange = 7
)

var (
	readFinderTags   = platformReadFinderTags
	writeFinderTags  = platformWriteFinderTags
	finderNow        = time.Now
	inspectGitStatus = git.InspectRepoStatus
)

// ApplyCandidateFinderTags applies macOS Finder tags and color labels to a candidate
// repository, flagging whether it is a new/untracked repository to be committed as a new repo.
func ApplyCandidateFinderTags(path string, isNew bool, ecosystem string) error {
	existing, err := readFinderTags(path)
	if err != nil {
		return err
	}
	var managed []string
	if ecosystem != "" {
		managed = append(managed, finderTag("Ecosystem: "+ecosystem, 0))
	}
	if isNew {
		managed = append(managed, finderTag("New Repo", finderOrange))
	}
	return writeFinderTags(path, mergeFinderTags(existing, managed))
}

// RefreshRepoFinderTags audits local git status and re-applies managed Finder tags
// and folder colors for an existing repository on disk without running a network sync.
func RefreshRepoFinderTags(path string) error {
	repo := github.Repo{}
	if st, err := readCloneState(path); err == nil && !st.LastSyncedPushedAt.IsZero() {
		repo.PushedAt = st.LastSyncedPushedAt
	}
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	if len(parts) >= 3 {
		repo.Language = parts[len(parts)-2]
		repo.Visibility = parts[len(parts)-3]
	}
	result := RepoResult{Action: "SYNC", Message: "refreshed"}
	return applyFinderTags(path, repo, result)
}

func applyFinderTags(path string, repo github.Repo, result RepoResult) error {
	existing, err := readFinderTags(path)
	if err != nil {
		return err
	}
	return writeFinderTags(path, mergeFinderTags(existing, managedFinderTags(path, repo, result, finderNow())))
}

func managedFinderTags(path string, repo github.Repo, result RepoResult, now time.Time) []string {
	tags := []string{
		finderTag("GitHub", 0),
		finderTag("Visibility: "+canonicalVisibility(repo.Visibility), 0),
		finderTag("Collection: "+repositoryCollection(repo), 0),
		finderTag("Ecosystem: "+repositoryBucket(repo), 0),
	}
	owner := repo.Owner
	if owner == "" && strings.Contains(repo.FullName, "/") {
		owner = strings.SplitN(repo.FullName, "/", 2)[0]
	}
	if owner != "" {
		tags = append(tags, finderTag("Owner: "+owner, 0))
	}

	status, _ := inspectGitStatus(context.Background(), path)
	labelName, labelColor := determineOperationalTag(status, repo, result)
	tags = append(tags, finderTag(labelName, labelColor))

	if repo.Archived {
		tags = append(tags, finderTag("Archived", 0))
	}
	if repo.Fork {
		tags = append(tags, finderTag("Fork", 0))
	}
	if repo.IsTemplate {
		tags = append(tags, finderTag("Template", 0))
	}
	if repo.IsMirror {
		tags = append(tags, finderTag("Mirror", 0))
	}
	return tags
}

func determineOperationalTag(status git.RepoStatus, repo github.Repo, result RepoResult) (string, int) {
	switch {
	case result.Action == "ERROR" || strings.HasPrefix(result.Action, "FAIL") || status.HasConflict || status.IsDiverged:
		return "Needs Fix", finderRed
	case status.IsDirty || status.HasUnpushed || status.HasStash:
		return "In-Flight", finderGreen
	case status.HasWorktree:
		return "Worktree", finderPurple
	case repo.Archived:
		return "On Hold", finderYellow
	case repo.Fork || repo.IsTemplate || repo.IsMirror:
		return "Experiment", finderPurple
	case status.IsDormant:
		return "Dormant", finderGrey
	default:
		return "Pristine", finderBlue
	}
}

func extractFinderColor(tags []string) int {
	for _, tag := range tags {
		parts := strings.SplitN(tag, "\n", 2)
		if len(parts) == 2 {
			if c, err := strconv.Atoi(parts[1]); err == nil && c > 0 {
				return c
			}
		}
	}
	return 0
}

func mergeFinderTags(existing, managed []string) []string {
	merged := make([]string, 0, len(existing)+len(managed))
	seen := make(map[string]struct{}, len(existing)+len(managed))
	for _, tag := range existing {
		if isManagedFinderTag(tag) {
			continue
		}
		if _, ok := seen[tag]; !ok {
			seen[tag] = struct{}{}
			merged = append(merged, tag)
		}
	}
	sort.Strings(managed)
	for _, tag := range managed {
		if _, ok := seen[tag]; !ok {
			seen[tag] = struct{}{}
			merged = append(merged, tag)
		}
	}
	return merged
}

func isManagedFinderTag(tag string) bool {
	name := strings.SplitN(tag, "\n", 2)[0]
	switch name {
	case "Active", "In-Flight", "Pristine", "Dormant", "Worktree", "On Hold", "Needs Fix", "Experiment", "New Repo", "GitHub", "Fork", "Archived", "Template", "Mirror":
		return true
	}
	return strings.HasPrefix(name, "Visibility: ") || strings.HasPrefix(name, "Collection: ") || strings.HasPrefix(name, "Ecosystem: ") || strings.HasPrefix(name, "Owner: ")
}

func canonicalVisibility(visibility string) string {
	if strings.EqualFold(visibility, "private") {
		return "Private"
	}
	return "Public"
}

func finderTag(name string, color int) string {
	return name + "\n" + strconv.Itoa(color)
}
