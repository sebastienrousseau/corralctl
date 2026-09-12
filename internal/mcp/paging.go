// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import "strings"

// Response budgeting.
//
// Tool results were unbounded: corral_list_repos and corral_workspace_index
// returned every match, indented, at roughly 526 bytes per repository. A
// 500-repository workspace produced ~66,000 tokens against a client budget of
// 25,000, so the response was over the limit from about 190 repositories — and
// the workspace this was measured on has 192.
//
// Two levers, both here: a page window, and a projection that drops the fields
// an orientation pass does not need.
const (
	// defaultPageSize keeps a first call comfortably inside a client's budget
	// while still being enough to answer "what have I got".
	defaultPageSize = 50
	// maxPageSize bounds what a caller can ask for in one response.
	maxPageSize = 200

	formatConcise  = "concise"
	formatDetailed = "detailed"
)

// pageMeta describes the window returned to the caller.
type pageMeta struct {
	// Total is how many entries matched before paging.
	Total int
	// Returned is how many are in this response.
	Returned int
	// NextOffset is the offset to pass for the next page, or 0 when this is
	// the last one.
	NextOffset int
}

// paginate returns the requested window of entries plus the metadata a caller
// needs to continue. Out-of-range and negative inputs are clamped rather than
// erroring: a model that guesses an offset past the end should get an empty
// page and a clear total, not a failure it has to recover from.
func paginate(entries []RepoEntry, limit, offset int) ([]RepoEntry, pageMeta) {
	total := len(entries)
	if limit <= 0 {
		limit = defaultPageSize
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	if offset < 0 {
		offset = 0
	}
	if offset >= total {
		return nil, pageMeta{Total: total}
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page := entries[offset:end]
	meta := pageMeta{Total: total, Returned: len(page)}
	if end < total {
		meta.NextOffset = end
	}
	return page, meta
}

// conciseRepo is the orientation-pass projection: enough to identify and locate
// a repository, without the origin URL and sync timestamps that dominate the
// payload. Roughly a tenth the size of the full entry.
type conciseRepo struct {
	RelPath    string `json:"rel_path"`
	Name       string `json:"name"`
	Visibility string `json:"visibility,omitempty"`
	Language   string `json:"language,omitempty"`
}

// projectRepos renders entries in the requested shape. Any value other than
// "detailed" is treated as concise, so a model that invents a format name gets
// the cheap response rather than an error.
// Both shapes are redacted: every string below is chosen by whoever owns
// the repository, and this is the boundary where it stops being data on
// disk and becomes text in a model's context.
func projectRepos(entries []RepoEntry, format string) []RepoSummary {
	out := make([]RepoSummary, 0, len(entries))
	if format == formatDetailed {
		for _, r := range entries {
			out = append(out, summarizeDetailed(r))
		}
		return out
	}
	for _, r := range entries {
		out = append(out, summarizeConcise(r))
	}
	return out
}

// lowerTrim normalises a filter value for case-insensitive comparison.
func lowerTrim(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// containsFold reports whether haystack contains needle, case-insensitively.
// needle is expected to be pre-lowered by the caller.
func containsFold(haystack, needle string) bool {
	return strings.Contains(strings.ToLower(haystack), needle)
}
