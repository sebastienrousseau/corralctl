// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"fmt"
	"strconv"
	"sync"

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
func (s *Server) handleSearchCode(ctx context.Context, _ *mcp.CallToolRequest, in searchCodeInput) (*mcp.CallToolResult, SearchCodeOutput, error) {
	s.noteSymbolQuery()
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
		return nil, SearchCodeOutput{}, err
	}

	idx, err := s.scan()
	if err != nil {
		return nil, SearchCodeOutput{}, fmt.Errorf("scan workspace: %v", err)
	}

	targets := idx.Repos
	if in.Repo != "" {
		match, findErr := idx.Find(in.Repo)
		if findErr != nil {
			return nil, SearchCodeOutput{}, findErr
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
			return nil, SearchCodeOutput{}, fmt.Errorf("no repositories with language %q; call corral_status_summary for the languages present", in.Language)
		}
		targets = filtered
	}

	var (
		hits            []SearchHit
		partial         []string
		scanned         int
		repoLimitHit    bool
		indexedRepos    int
		filesSearched   int
		stoppedEarly    bool
		remainingBudget = limit
	)

	// Repositories are searched in bounded parallel batches, in order.
	//
	// This loop used to be sequential while the symbol tools fanned out to
	// sixteen workers, so a content search read one repository at a time —
	// 11-17s on a 234-repository workspace, almost all of it waiting on the
	// filesystem. The cost is dominated by repositories that contain no match
	// at all, which cannot be skipped without an index and can be overlapped.
	//
	// Batching in order rather than racing every repository is what keeps the
	// answer deterministic. Hits are merged in repository order and the limit
	// is applied to that merged list, so two identical queries return the same
	// results regardless of which worker happened to finish first — the
	// property the symbol tools sort to recover after their fan-out.
	//
	// The early exit survives: the budget is checked between batches, so a
	// query whose answer fills up stops after at most one extra batch rather
	// than reading the rest of the workspace. Any hits that batch collected
	// beyond the limit are discarded in order, which is the same set the
	// sequential loop would have produced.
	batch := repoFanOut()
	if batch > len(targets) {
		batch = len(targets)
	}

	type repoResult struct {
		res     *search.Result
		indexed bool
		err     error
	}

	for start := 0; start < len(targets); start += batch {
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

		stop := start + batch
		if stop > len(targets) {
			stop = len(targets)
		}
		results := make([]repoResult, stop-start)
		var wg sync.WaitGroup
		for j := start; j < stop; j++ {
			wg.Add(1)
			go func(slot int, repo *RepoEntry) {
				defer wg.Done()
				res, indexed, err := s.searchOneRepo(ctx, repo, matcher)
				results[slot] = repoResult{res: res, indexed: indexed, err: err}
			}(j-start, &targets[j])
		}
		wg.Wait()

		for j := start; j < stop; j++ {
			r := results[j-start]
			if r.err != nil {
				// One unreadable repository must not fail the whole search.
				continue
			}
			if scanned >= maxSearchRepos {
				repoLimitHit = true
				break
			}
			repo := &targets[j]
			scanned++
			if r.indexed {
				indexedRepos++
			}
			filesSearched += r.res.Files
			if r.res.Truncated {
				partial = append(partial, repo.Redacted().RelPath)
			}
			red := repo.Redacted()
			for _, h := range r.res.Hits {
				if remainingBudget <= 0 {
					stoppedEarly = true
					break
				}
				hits = append(hits, SearchHit{
					Repo:   red.RelPath,
					File:   sanitize.Untrusted(h.File, maxEntryPath),
					Line:   h.Line,
					Column: h.Column,
					// The matching line is source written by whoever owns
					// the repository. It reaches a model's context verbatim
					// otherwise, which is exactly the runtime half of the
					// trust gap the server instructions describe.
					Text: sanitize.Untrusted(h.Text, maxHitText),
				})
				remainingBudget--
			}
		}
	}

	// A nil slice marshals to `null`, and the output schema says `array`.
	// "No matches" is the most common answer this tool gives, so the empty
	// case is the one that must be right: a client reading hits.length should
	// see 0, not trip over a null.
	if hits == nil {
		hits = []SearchHit{}
	}

	body := SearchCodeOutput{
		Query:                sanitize.Untrusted(in.Query, maxEntryName),
		RepositoriesSearched: scanned,
		FilesSearched:        filesSearched,
		Returned:             len(hits),
		Hits:                 hits,
		Regex:                in.Regex,
		IndexedRepositories:  indexedRepos,
	}

	switch {
	case repoLimitHit:
		body.Truncated = true
		body.Note = "Stopped after " + strconv.Itoa(maxSearchRepos) +
			" repositories. Narrow the search with repo, language or path_glob."
	case stoppedEarly:
		body.Truncated = true
		body.Note = "Stopped at the result limit; more matches exist. " +
			"Raise max_results, or narrow with repo, language or path_glob."
	case len(partial) > 0:
		body.Truncated = true
		body.PartialRepositories = capList(partial, 10)
		body.Note = "Some repositories hit a file or size bound, so their results are incomplete."
	}

	if len(hits) == 0 && !repoLimitHit && !stoppedEarly {
		body.Note = "No match in " + strconv.Itoa(filesSearched) + " files across " +
			strconv.Itoa(scanned) + " repositories. Only files the file resource would serve are searched, " +
			"and test files are excluded unless include_tests is set."
	}

	return jsonResult(body), body, nil
}

// searchRepo is the seam tests replace to drive failure paths that a real
// filesystem will not produce on demand.
var searchRepo = search.SearchRepo
