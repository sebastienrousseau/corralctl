// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sebastienrousseau/corralctl/internal/sanitize"
	"github.com/sebastienrousseau/corralctl/internal/symbols"
)

// symbolsFor extracts one repository's symbols through two caches.
func (s *Server) symbolsFor(ctx context.Context, repo *RepoEntry) (*symbols.Result, error) {
	if cached, ok := s.symbolCache.get(repo.Path); ok {
		return cached, nil
	}
	res, err := extractSymbols(ctx, repo.Path, s.symbolDisk)
	if err != nil {
		return nil, err
	}
	s.symbolCache.put(repo.Path, res)
	return res, nil
}

// repoFanOut bounds how many repositories are extracted at once.
var repoFanOut = func() int {
	return clampFanOut(runtime.GOMAXPROCS(0) * 2)
}

// maxRepoFanOut caps the fan-out regardless of core count.
const maxRepoFanOut = 16

// clampFanOut holds a worker count inside [1, maxRepoFanOut].
func clampFanOut(n int) int {
	if n < 1 {
		return 1
	}
	if n > maxRepoFanOut {
		return maxRepoFanOut
	}
	return n
}

// extractSymbols is indirected so tests can stub the expensive parse.
var extractSymbols = symbols.ExtractRepoCached

// findSymbolInput is the argument set for corral_find_symbol.
type findSymbolInput struct {
	Name         string `json:"name" jsonschema:"Symbol to find. Matched exactly and case-insensitively; a method also matches its 'Receiver.Name' form."`
	Kind         string `json:"kind,omitempty" jsonschema:"Restrict to one kind: func, method, type, interface, const or var."`
	Repo         string `json:"repo,omitempty" jsonschema:"Restrict to one repository, by the same identifier corral_find_repo accepts. Omit to search every clone in the workspace."`
	Substring    bool   `json:"substring,omitempty" jsonschema:"Match any symbol whose name contains the query, instead of matching it exactly."`
	ExportedOnly bool   `json:"exported_only,omitempty" jsonschema:"Return only symbols visible outside their package."`
	IncludeTests bool   `json:"include_tests,omitempty" jsonschema:"Include declarations from test files. Excluded by default because they usually outnumber everything else."`
	Limit        int    `json:"limit,omitempty" jsonschema:"Maximum symbols to return. Default 50, maximum 200."`
}

// repoOverviewInput is the argument set for corral_repo_overview.
type repoOverviewInput struct {
	Query string `json:"query" jsonschema:"Repository identifier: bare name, relative path, or any unique path suffix."`
}

// registerSymbolTools attaches the symbol tools. They are read-only and
// local, like the rest of the read set.
func (s *Server) registerSymbolTools() {
	langs := strings.Join(symbols.Languages(), ", ")

	addTool(s, &mcp.Tool{
		Name:        "corral_find_symbol",
		Title:       "Find where a symbol is defined",
		Annotations: readOnlyAnnotations(),
		Description: "Find where a function, method, type, interface, constant or variable is defined, across every repository in the Corral workspace — not just one. This is the tool to reach for when you know a name but not which repository it lives in. Returns file and line, not source: read the file at the location it gives you. Indexed languages: " + langs + ". Test declarations are excluded unless include_tests is set. Results are paginated and the response reports whether any repository's index was truncated.",
	}, s.handleFindSymbol)

	addTool(s, &mcp.Tool{
		Name:        "corral_repo_overview",
		Title:       "Summarise one repository",
		Annotations: readOnlyAnnotations(),
		Description: "Orient in a single repository in one call: its location and origin, how many source files it has, its declaration counts by kind, and its most significant exported types and functions. Cheaper and far smaller than listing the tree and reading files. Use it before corral_find_symbol when you do not yet know what a repository contains.",
	}, s.handleRepoOverview)
}

// handleFindSymbol resolves a symbol across one repository or all of them.
func (s *Server) handleFindSymbol(ctx context.Context, _ *mcp.CallToolRequest, in findSymbolInput) (*mcp.CallToolResult, FindSymbolOutput, error) {
	s.noteSymbolQuery()
	if strings.TrimSpace(in.Name) == "" {
		return nil, FindSymbolOutput{}, fmt.Errorf("name must not be empty")
	}

	query := symbols.Query{
		Name:         in.Name,
		Substring:    in.Substring,
		ExportedOnly: in.ExportedOnly,
		IncludeTests: in.IncludeTests,
	}
	if in.Kind != "" {
		kind, err := symbols.ParseKind(in.Kind)
		if err != nil {
			return nil, FindSymbolOutput{}, err
		}
		query.Kind = kind
	}

	idx, err := s.scan()
	if err != nil {
		return nil, FindSymbolOutput{}, fmt.Errorf("scan workspace: %v", err)
	}

	targets := idx.Repos
	if in.Repo != "" {
		match, err := idx.Find(in.Repo)
		if err != nil {
			return nil, FindSymbolOutput{}, err
		}
		targets = []RepoEntry{*match}
	}

	var (
		mu        sync.Mutex
		hits      []SymbolHit
		truncated []string
		scanned   int
		next      atomic.Int64
		wg        sync.WaitGroup
	)
	workers := repoFanOut()
	if workers > len(targets) {
		workers = len(targets)
	}

	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1)) - 1
				if i >= len(targets) || ctx.Err() != nil {
					return
				}
				repo := &targets[i]
				res, err := s.symbolsFor(ctx, repo)
				if err != nil {
					continue
				}
				red := repo.Redacted()

				var local []SymbolHit
				for _, sym := range res.Symbols {
					if !query.Match(sym) {
						continue
					}
					local = append(local, SymbolHit{
						Repo:     red.RelPath,
						Symbol:   sanitize.Untrusted(sym.Qualified(), maxEntryName),
						Kind:     sym.Kind,
						File:     sanitize.Untrusted(sym.File, maxEntryPath),
						Line:     sym.Line,
						Exported: sym.Exported,
						Language: sym.Language,
						Test:     sym.Test,
					})
				}

				mu.Lock()
				scanned++
				if res.Truncated {
					truncated = append(truncated, red.RelPath)
				}
				hits = append(hits, local...)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if err := ctx.Err(); err != nil {
		//nolint:nilerr // deliberate: cancellation is a tool result, not an error
		return toolError("cancelled after %d repositories", scanned), FindSymbolOutput{
			Query:              in.Name,
			RepositoriesSearch: scanned,
			Symbols:            []SymbolHit{},
			Note:               "Cancelled before every repository was searched; the counts are partial.",
		}, nil
	}
	sort.Strings(truncated)

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Exported != hits[j].Exported {
			return hits[i].Exported
		}
		if hits[i].Repo != hits[j].Repo {
			return hits[i].Repo < hits[j].Repo
		}
		if hits[i].File != hits[j].File {
			return hits[i].File < hits[j].File
		}
		return hits[i].Line < hits[j].Line
	})

	limit := in.Limit
	if limit <= 0 {
		limit = defaultPageSize
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	total := len(hits)
	if len(hits) > limit {
		hits = hits[:limit]
	}

	if hits == nil {
		hits = []SymbolHit{}
	}

	body := FindSymbolOutput{
		Query:              in.Name,
		RepositoriesSearch: scanned,
		TotalMatched:       total,
		Returned:           len(hits),
		Symbols:            hits,
	}
	if total == 0 {
		body.Note = "No match. Symbols come from " + strings.Join(symbols.Languages(), ", ") +
			" sources only; try substring:true, or include_tests:true if it is declared in a test."
	}
	if total > len(hits) {
		body.Note = "More matches than the limit; narrow with kind, repo or exported_only."
	}
	if len(truncated) > 0 {
		body.IndexesTruncated = truncated
		body.IndexesTruncatedNote = "These repositories exceeded the per-repository file or symbol cap; their results are incomplete."
	}
	return jsonResult(body), body, nil
}

// handleRepoOverview summarises one repository in a single call.
func (s *Server) handleRepoOverview(ctx context.Context, _ *mcp.CallToolRequest, in repoOverviewInput) (*mcp.CallToolResult, RepoOverviewOutput, error) {
	s.noteSymbolQuery()
	idx, err := s.scan()
	if err != nil {
		return nil, RepoOverviewOutput{}, fmt.Errorf("scan workspace: %v", err)
	}
	match, err := idx.Find(in.Query)
	if err != nil {
		return nil, RepoOverviewOutput{}, err
	}
	res, err := s.symbolsFor(ctx, match)
	if err != nil {
		return nil, RepoOverviewOutput{}, fmt.Errorf("index %s: %v", match.Redacted().RelPath, err)
	}

	byKind := map[symbols.Kind]int{}
	var exportedTypes, exportedFuncs []string
	for _, sym := range res.Symbols {
		if sym.Test {
			continue
		}
		byKind[sym.Kind]++
		if !sym.Exported {
			continue
		}
		name := sanitize.Untrusted(sym.Qualified(), maxEntryName)
		switch sym.Kind {
		case symbols.KindType, symbols.KindInterface:
			exportedTypes = append(exportedTypes, name)
		case symbols.KindFunc:
			exportedFuncs = append(exportedFuncs, name)
		}
	}
	sort.Strings(exportedTypes)
	sort.Strings(exportedFuncs)

	const maxListed = 25
	red := match.Redacted()
	body := RepoOverviewOutput{
		Repo:       red.RelPath,
		Path:       red.Path,
		RemoteURL:  red.RemoteURL,
		Language:   red.Language,
		Visibility: red.Visibility,
		Files:      res.Files,
		Declarations: DeclarationCounts{
			Func: byKind[symbols.KindFunc], Method: byKind[symbols.KindMethod],
			Type: byKind[symbols.KindType], Interface: byKind[symbols.KindInterface],
			Const: byKind[symbols.KindConst], Var: byKind[symbols.KindVar],
		},
		ExportedTypes:     emptyIfNil(capList(exportedTypes, maxListed)),
		ExportedFunctions: emptyIfNil(capList(exportedFuncs, maxListed)),
	}
	if res.Truncated {
		body.Truncated = true
		body.TruncatedNote = "This repository exceeded the per-repository file or symbol cap; the counts are a lower bound."
	}
	if len(exportedTypes) > maxListed || len(exportedFuncs) > maxListed {
		body.Note = fmt.Sprintf("Lists are capped at %d; use corral_find_symbol for the rest.", maxListed)
	}
	return jsonResult(body), body, nil
}

// capList truncates a list to at most n entries.
func capList(xs []string, n int) []string {
	if len(xs) > n {
		return xs[:n]
	}
	return xs
}
