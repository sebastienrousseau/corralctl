// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/github"
)

const (
	finderGreen  = 2
	finderPurple = 3
	finderYellow = 5
	finderRed    = 6
)

var (
	readFinderTags  = platformReadFinderTags
	writeFinderTags = platformWriteFinderTags
	finderNow       = time.Now
)

func applyFinderTags(path string, repo github.Repo, result RepoResult) error {
	existing, err := readFinderTags(path)
	if err != nil {
		return err
	}
	return writeFinderTags(path, mergeFinderTags(existing, managedFinderTags(repo, result, finderNow())))
}

func managedFinderTags(repo github.Repo, result RepoResult, now time.Time) []string {
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

	switch {
	case result.Action == "ERROR" || strings.HasPrefix(result.Action, "FAIL"):
		tags = append(tags, finderTag("Needs Fix", finderRed))
	case repo.Archived:
		tags = append(tags, finderTag("On Hold", finderYellow), finderTag("Archived", 0))
	case repo.Fork || repo.IsTemplate || repo.IsMirror:
		tags = append(tags, finderTag("Experiment", finderPurple))
	case strings.HasPrefix(result.Message, "on branch ") || (!repo.PushedAt.IsZero() && repo.PushedAt.After(now.AddDate(0, 0, -7))):
		tags = append(tags, finderTag("Active", finderGreen))
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
	case "Active", "On Hold", "Needs Fix", "Experiment", "GitHub", "Fork", "Archived", "Template", "Mirror":
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
