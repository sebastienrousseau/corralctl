// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sebastienrousseau/corralctl/internal/sanitize"
	"github.com/sebastienrousseau/corralctl/internal/search"
)

// Content search across the workspace.
//
// corral_find_symbol answers "where is this declared". This answers "where
// is this written", which is the question asked more often — who calls it,
// what reads this environment variable, which repository holds the string
// in the error message somebody pasted into a ticket.
//
// The reason it belongs here rather than in the agent's shell is the same
// reason the symbol index does: an agent can run grep, but only against a
// directory it already knows about. Corral knows about all of them, and
// knows which of their files are safe to read.
//
// # The file policy is not optional here
//
// Every candidate file goes through the same allowlist that decides what
// the file resource will serve. Without that, search would be a way to
// read a refused file one line at a time — ask for `AWS_SECRET`, get the
// line it is on. The check is applied inside the walk rather than to the
// results, so a denied file is never opened at all.

// maxSearchRepos bounds how many repositories one query will read.
//
// Unlike a symbol lookup, which consults a cache that survives between
// calls, every search reads files from disk. On a thousand-repository
// workspace an unbounded query is minutes of I/O for an answer the agent
// will have given up waiting for, so the bound is low and the response
// says plainly that it was reached.
var maxSearchRepos = 200

// maxSearchHits bounds the response.
const maxSearchHits = 200

// maxHitText bounds one reported line after sanitising. Shorter than the
// search package's own bound because this is the value that reaches a
// model's context, and a hit is a pointer to a line, not the line's
// content.
const maxHitText = 300

type searchCodeInput struct {
	Query         string `json:"query" jsonschema:"Text to find. A literal substring unless regex is set."`
	Regex         bool   `json:"regex,omitempty" jsonschema:"Treat query as an RE2 regular expression rather than literal text."`
	CaseSensitive bool   `json:"case_sensitive,omitempty" jsonschema:"Match case exactly. Default is case-insensitive."`
	Repo          string `json:"repo,omitempty" jsonschema:"Limit the search to one repository: bare name, relative path, or any unique path suffix."`
	PathGlob      string `json:"path_glob,omitempty" jsonschema:"Limit to files whose path or filename matches this glob, e.g. \"*.go\" or \"internal/*/*.ts\"."`
	Language      string `json:"language,omitempty" jsonschema:"Limit to repositories of this language, as reported by corral_list_repos."`
	IncludeTests  bool   `json:"include_tests,omitempty" jsonschema:"Include matches in test files. Excluded by default."`
	MaxResults    int    `json:"max_results,omitempty" jsonschema:"Maximum hits to return (default 50, maximum 200)."`
}

// registerSearchTool attaches corral_search_code. Read-only and local,
// like the rest of the read set.
func (s *Server) registerSearchTool() {
	addTool(s, &mcp.Tool{
		Name:        "corral_search_code",
		Title:       "Search file contents across every repository",
		Annotations: readOnlyAnnotations(),
		Description: "Search the contents of source and documentation files across every repository in the Corral workspace at once. Use this for where something is *used* — call sites, configuration keys, error strings — and corral_find_symbol for where something is *declared*. Returns file, line and the matching line's text, not whole files: read the file at the location it gives you. Only files the file resource would serve are searched, so credential files never match. Test files are excluded unless include_tests is set. Narrow with repo, language or path_glob on a large workspace; the response reports whether any bound was reached. When a name is spelled differently per language — MaxAttempts in Go, MAX_ATTEMPTS in Python, maxAttempts in TypeScript — a literal search finds only one of them; use regex for those, e.g. \"max_?attempts\".",
	}, s.handleSearchCode)
}

// handleSearchCode runs one content search.
func (s *Server) handleSearchCode(ctx context.Context, _ *mcp.CallToolRequest, in searchCodeInput) (*mcp.CallToolResult, any, error) {
	limit := in.MaxResults
	switch {
	case limit <= 0:
		limit = 50
	case limit > maxSearchHits:
		limit = maxSearchHits
	}

	matcher, err := search.Compile(search.Query{
		Pattern:       in.Query,
		Regex:         in.Regex,
		CaseSensitive: in.CaseSensitive,
		PathGlob:      in.PathGlob,
		IncludeTests:  in.IncludeTests,
		MaxHits:       limit,
	})
	if err != nil {
		// A pattern that cannot be compiled is an error, never an empty
		// result: "no matches" is a conclusion an agent will act on, and
		// it is the wrong one.
		return toolError("%v", err), nil, nil
	}

	idx, err := s.scan()
	if err != nil {
		return toolError("scan workspace: %v", err), nil, nil
	}

	targets := idx.Repos
	if in.Repo != "" {
		match, findErr := idx.Find(in.Repo)
		if findErr != nil {
			return toolError("%v", findErr), nil, nil
		}
		targets = []RepoEntry{*match}
	} else if in.Language != "" {
		want := lowerTrim(in.Language)
		var filtered []RepoEntry
		for _, r := range targets {
			if lowerTrim(r.Language) == want {
				filtered = append(filtered, r)
			}
		}
		if len(filtered) == 0 {
			return toolError("no repositories with language %q; call corral_status_summary for the languages present", in.Language), nil, nil
		}
		targets = filtered
	}

	// The file policy the file resource enforces, applied to every
	// candidate before it is opened.
	allowed := func(rel string) bool {
		_, ok := fileAllowed(rel, s.extraFileExts)
		return ok
	}

	type hit struct {
		Repo   string `json:"repo"`
		File   string `json:"file"`
		Line   int    `json:"line"`
		Column int    `json:"column"`
		Text   string `json:"text"`
	}

	var (
		hits            []hit
		partial         []string
		scanned         int
		repoLimitHit    bool
		filesSearched   int
		stoppedEarly    bool
		remainingBudget = limit
	)

	for i := range targets {
		if err := ctx.Err(); err != nil {
			stoppedEarly = true
			break
		}
		if scanned >= maxSearchRepos {
			repoLimitHit = true
			break
		}
		if remainingBudget <= 0 {
			// The answer is already full. Stopping here rather than
			// reading every remaining repository is the difference
			// between a fast common case and a slow one.
			stoppedEarly = true
			break
		}
		repo := &targets[i]

		res, searchErr := searchRepo(ctx, repo.Path, matcher, allowed)
		if searchErr != nil {
			// One unreadable repository must not fail the whole search.
			continue
		}
		scanned++
		filesSearched += res.Files
		if res.Truncated {
			partial = append(partial, repo.Redacted().RelPath)
		}
		red := repo.Redacted()
		for _, h := range res.Hits {
			if remainingBudget <= 0 {
				stoppedEarly = true
				break
			}
			hits = append(hits, hit{
				Repo:   red.RelPath,
				File:   sanitize.Untrusted(h.File, maxEntryPath),
				Line:   h.Line,
				Column: h.Column,
				// The matching line is source written by whoever owns the
				// repository. It reaches a model's context verbatim
				// otherwise, which is exactly the runtime half of the
				// trust gap the server instructions describe.
				Text: sanitize.Untrusted(h.Text, maxHitText),
			})
			remainingBudget--
		}
	}

	body := map[string]any{
		"query":                 sanitize.Untrusted(in.Query, maxEntryName),
		"repositories_searched": scanned,
		"files_searched":        filesSearched,
		"returned":              len(hits),
		"hits":                  hits,
	}
	if in.Regex {
		body["regex"] = true
	}

	switch {
	case repoLimitHit:
		body["truncated"] = true
		body["note"] = "Stopped after " + strconv.Itoa(maxSearchRepos) +
			" repositories. Narrow the search with repo, language or path_glob."
	case stoppedEarly:
		body["truncated"] = true
		body["note"] = "Stopped at the result limit; more matches exist. " +
			"Raise max_results, or narrow with repo, language or path_glob."
	case len(partial) > 0:
		body["truncated"] = true
		body["partial_repositories"] = capList(partial, 10)
		body["note"] = "Some repositories hit a file or size bound, so their results are incomplete."
	}

	if len(hits) == 0 && !repoLimitHit && !stoppedEarly {
		body["note"] = "No match in " + strconv.Itoa(filesSearched) + " files across " +
			strconv.Itoa(scanned) + " repositories. Only files the file resource would serve are searched, " +
			"and test files are excluded unless include_tests is set."
	}

	return jsonResult(body), nil, nil
}

// searchRepo is the seam tests replace to drive failure paths that a real
// filesystem will not produce on demand.
var searchRepo = search.SearchRepo
