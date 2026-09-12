// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import "github.com/sebastienrousseau/corralctl/internal/symbols"

// Tool output types.
//
// Every tool used to answer with `map[string]any` marshalled into a text
// block, and declared no outputSchema. That makes a result unverifiable: a
// client cannot tell a renamed field from a missing one, and nothing fails
// when a handler quietly stops emitting something. The MCP SDK derives an
// output schema from the handler's Out type and populates structuredContent
// from the returned value, so a concrete type per tool buys both at once.
//
// The types below are the single source of truth for a result. Handlers build
// one and pass it as BOTH the content payload and the structured output, so
// the text a model reads and the structure a client validates cannot drift
// apart — which they would if the map stayed and a struct were added beside it.
//
// Fields that are not always present are `omitempty` and therefore optional in
// the generated schema. Fields without it are required, which is the point:
// a result missing `returned` is a bug, and now it is a schema violation the
// SDK rejects before it reaches a client.

// RepoSummary is one repository in a tool result.
//
// It covers both projections. The concise form fills the first four fields;
// `detailed` adds the rest. One type rather than two keeps the schema honest
// about what a caller may receive, since response_format decides at runtime
// and a schema cannot: a client validating against a concise-only shape would
// reject a detailed response it asked for.
type RepoSummary struct {
	RelPath    string       `json:"rel_path"`
	Name       string       `json:"name"`
	Visibility string       `json:"visibility,omitempty"`
	Language   string       `json:"language,omitempty"`
	Path       string       `json:"path,omitempty"`
	RemoteURL  string       `json:"remote_url,omitempty"`
	State      *StateRecord `json:"state,omitempty"`
}

// summarizeConcise projects the cheap shape: no paths, no origin, no sync
// state.
func summarizeConcise(r RepoEntry) RepoSummary {
	r = r.Redacted()
	return RepoSummary{
		RelPath:    r.RelPath,
		Name:       r.Name,
		Visibility: r.Visibility,
		Language:   r.Language,
	}
}

// summarizeDetailed projects everything the index holds.
func summarizeDetailed(r RepoEntry) RepoSummary {
	r = r.Redacted()
	return RepoSummary{
		RelPath:    r.RelPath,
		Name:       r.Name,
		Visibility: r.Visibility,
		Language:   r.Language,
		Path:       r.Path,
		RemoteURL:  r.RemoteURL,
		State:      r.State,
	}
}

// PageOutput is the pagination envelope shared by the two listing tools.
type PageOutput struct {
	Root         string        `json:"root"`
	TotalMatched int           `json:"total_matched"`
	Returned     int           `json:"returned"`
	Repos        []RepoSummary `json:"repos"`
	NextOffset   int           `json:"next_offset,omitempty"`
	Note         string        `json:"note,omitempty"`
	// WorkspaceTruncated is set when the scan hit its cap, so a caller can
	// tell "this is the whole workspace" from "this is as much of it as was
	// scanned". It went unreported for several releases.
	WorkspaceTruncated     bool   `json:"workspace_truncated,omitempty"`
	WorkspaceTruncatedNote string `json:"workspace_truncated_note,omitempty"`
}

// RepoMetadataOutput is corral_get_repo_metadata.
type RepoMetadataOutput struct {
	Repo          RepoSummary `json:"repo"`
	CurrentBranch string      `json:"current_branch"`
}

// LanguageCount is one row of the language histogram.
type LanguageCount struct {
	Language string `json:"language"`
	Count    int    `json:"count"`
}

// StatusSummaryOutput is corral_status_summary.
type StatusSummaryOutput struct {
	Root         string          `json:"root"`
	Total        int             `json:"total"`
	Synced       int             `json:"synced"`
	ByVisibility map[string]int  `json:"by_visibility"`
	ByLanguage   []LanguageCount `json:"by_language"`
}

// SearchHit is one matching line.
type SearchHit struct {
	Repo   string `json:"repo"`
	File   string `json:"file"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
	Text   string `json:"text"`
}

// SearchCodeOutput is corral_search_code.
type SearchCodeOutput struct {
	Query                string      `json:"query"`
	RepositoriesSearched int         `json:"repositories_searched"`
	FilesSearched        int         `json:"files_searched"`
	Returned             int         `json:"returned"`
	Hits                 []SearchHit `json:"hits"`
	Regex                bool        `json:"regex,omitempty"`
	Truncated            bool        `json:"truncated,omitempty"`
	PartialRepositories  []string    `json:"partial_repositories,omitempty"`
	Note                 string      `json:"note,omitempty"`
}

// SymbolHit is one declaration.
type SymbolHit struct {
	Repo     string       `json:"repo"`
	Symbol   string       `json:"symbol"`
	Kind     symbols.Kind `json:"kind"`
	File     string       `json:"file"`
	Line     int          `json:"line"`
	Exported bool         `json:"exported"`
	Language string       `json:"language"`
	Test     bool         `json:"test,omitempty"`
}

// FindSymbolOutput is corral_find_symbol.
type FindSymbolOutput struct {
	Query                string      `json:"query"`
	RepositoriesSearch   int         `json:"repositories_search"`
	TotalMatched         int         `json:"total_matched"`
	Returned             int         `json:"returned"`
	Symbols              []SymbolHit `json:"symbols"`
	Note                 string      `json:"note,omitempty"`
	IndexesTruncated     []string    `json:"indexes_truncated,omitempty"`
	IndexesTruncatedNote string      `json:"indexes_truncated_note,omitempty"`
}

// DeclarationCounts is the per-kind histogram in a repository overview.
type DeclarationCounts struct {
	Func      int `json:"func"`
	Method    int `json:"method"`
	Type      int `json:"type"`
	Interface int `json:"interface"`
	Const     int `json:"const"`
	Var       int `json:"var"`
}

// RepoOverviewOutput is corral_repo_overview.
type RepoOverviewOutput struct {
	Repo              string            `json:"repo"`
	Path              string            `json:"path"`
	RemoteURL         string            `json:"remote_url"`
	Language          string            `json:"language"`
	Visibility        string            `json:"visibility"`
	Files             int               `json:"files"`
	Declarations      DeclarationCounts `json:"declarations"`
	ExportedTypes     []string          `json:"exported_types"`
	ExportedFunctions []string          `json:"exported_functions"`
	Truncated         bool              `json:"truncated,omitempty"`
	TruncatedNote     string            `json:"truncated_note,omitempty"`
	Note              string            `json:"note,omitempty"`
}

// MutationOutput is the result of a write tool.
//
// Sync names the repository it acted on; clone and delete name the target
// path. One type with both optional keeps the three write tools reporting the
// same shape rather than three near-identical ones.
type MutationOutput struct {
	Tool   string `json:"tool"`
	Result string `json:"result"`
	Repo   string `json:"repo,omitempty"`
	Target string `json:"target,omitempty"`
}

// emptyIfNil turns a nil slice into an empty one.
//
// A nil slice marshals to `null`, which does not satisfy a schema that says
// `type: array`. A repository with nothing exported is ordinary, so this is the
// normal path rather than an edge case.
func emptyIfNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
