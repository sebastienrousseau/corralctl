// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sebastienrousseau/corralctl/internal/discover"
	"github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/sebastienrousseau/corralctl/internal/graph"
)

// graphDependenciesInput is the argument set for corral_graph_dependencies.
type graphDependenciesInput struct {
	Repo string `json:"repo,omitempty" jsonschema:"Optional repository identifier to focus on. If provided, returns the direct dependencies and dependents for this repository."`
}

// GraphDependenciesOutput is the structured content returned by corral_graph_dependencies.
type GraphDependenciesOutput struct {
	Nodes            []graph.Node        `json:"nodes"`
	Edges            []graph.Edge        `json:"edges"`
	TopologicalOrder []string            `json:"topological_order,omitempty"`
	Cycles           [][]string          `json:"cycles,omitempty"`
	Dependents       map[string][]string `json:"dependents,omitempty"`
	TargetRepo       string              `json:"target_repo,omitempty"`
	Dependencies     []string            `json:"dependencies,omitempty"`
}

func (s *Server) handleGraphDependencies(ctx context.Context, _ *mcp.CallToolRequest, in graphDependenciesInput) (*mcp.CallToolResult, GraphDependenciesOutput, error) {
	idx, err := s.scan()
	if err != nil {
		return nil, GraphDependenciesOutput{}, fmt.Errorf("scan workspace: %v", err)
	}

	repos := make([]graph.RepoInput, 0, len(idx.Repos))
	for _, r := range idx.Repos {
		repos = append(repos, graph.RepoInput{
			Name:      r.Name,
			Path:      r.Path,
			Language:  r.Language,
			RemoteURL: r.RemoteURL,
		})
	}

	g := graph.BuildGraph(repos)

	if in.Repo != "" {
		match, err := idx.Find(in.Repo)
		if err != nil {
			return toolError("repo %q: %v", in.Repo, err), GraphDependenciesOutput{}, nil
		}
		repoName := match.Name
		var node *graph.Node
		for i := range g.Nodes {
			if g.Nodes[i].Name == repoName {
				node = &g.Nodes[i]
				break
			}
		}
		res := GraphDependenciesOutput{
			Nodes:        []graph.Node{},
			Edges:        []graph.Edge{},
			TargetRepo:   repoName,
			Dependencies: []string{},
			Dependents:   g.Dependents,
		}
		if node != nil {
			res.Dependencies = node.Dependencies
		}
		return jsonResult(res), res, nil
	}

	res := GraphDependenciesOutput{
		Nodes:            g.Nodes,
		Edges:            g.Edges,
		TopologicalOrder: g.TopologicalOrder,
		Cycles:           g.Cycles,
		Dependents:       g.Dependents,
	}
	return jsonResult(res), res, nil
}

// doctorInput is the argument set for corral_doctor.
type doctorInput struct {
	CheckCaches bool `json:"check_caches,omitempty" jsonschema:"When true, perform deeper inspection of cache and index states."`
}

// DoctorOutput describes the overall workspace health, uncommitted changes, and worktrees.
type DoctorOutput struct {
	BaseDir       string          `json:"base_dir"`
	TotalRepos    int             `json:"total_repos"`
	ByVisibility  []LanguageCount `json:"by_visibility"`
	ByLanguage    []LanguageCount `json:"by_language"`
	DirtyRepos    []string        `json:"dirty_repos"`
	UnpushedRepos []string        `json:"unpushed_repos"`
	Worktrees     []string        `json:"worktrees"`
	Healthy       bool            `json:"healthy"`
	IssuesCount   int             `json:"issues_count"`
}

var (
	doctorHasLocalChanges    = git.HasLocalChanges
	doctorHasUnpublishedWork = git.HasUnpublishedWork
	doctorListWorktrees      = git.ListWorktrees
	discoverReposFunc        = discover.Discover
)

func (s *Server) handleDoctor(ctx context.Context, _ *mcp.CallToolRequest, in doctorInput) (*mcp.CallToolResult, DoctorOutput, error) {
	idx, err := s.scan()
	if err != nil {
		return nil, DoctorOutput{}, fmt.Errorf("scan workspace: %v", err)
	}

	byVis := make(map[string]int)
	byLang := make(map[string]int)
	dirty := []string{}
	unpushed := []string{}
	worktrees := []string{}

	for _, repo := range idx.Repos {
		if repo.Visibility != "" {
			byVis[repo.Visibility]++
		}
		if repo.Language != "" {
			byLang[repo.Language]++
		}
		if hasLocal, _ := doctorHasLocalChanges(ctx, repo.Path); hasLocal {
			dirty = append(dirty, repo.Name)
		}
		if unpublished, _ := doctorHasUnpublishedWork(ctx, repo.Path); unpublished {
			unpushed = append(unpushed, repo.Name)
		}
		if wts, err := doctorListWorktrees(ctx, repo.Path); err == nil {
			for _, wt := range wts {
				if wt.Path != repo.Path && !wt.Bare {
					worktrees = append(worktrees, fmt.Sprintf("%s:%s", repo.Name, wt.Path))
				}
			}
		}
	}

	issues := len(dirty) + len(unpushed)
	out := DoctorOutput{
		BaseDir:       idx.Root,
		TotalRepos:    len(idx.Repos),
		ByVisibility:  sortedLangCounts(byVis),
		ByLanguage:    sortedLangCounts(byLang),
		DirtyRepos:    dirty,
		UnpushedRepos: unpushed,
		Worktrees:     worktrees,
		Healthy:       issues == 0,
		IssuesCount:   issues,
	}
	return jsonResult(out), out, nil
}

// discoverReposInput is the argument set for corral_discover_repos.
type discoverReposInput struct {
	BaseDir       string `json:"base_dir,omitempty" jsonschema:"Directory where discovery begins. Defaults to workspace root."`
	UntrackedOnly bool   `json:"untracked_only,omitempty" jsonschema:"When true, returns only repositories without an origin remote."`
	MaxDepth      int    `json:"max_depth,omitempty" jsonschema:"Maximum directory traversal depth. 0 for unlimited."`
}

// DiscoverReposOutput describes the results returned by corral_discover_repos.
type DiscoverReposOutput struct {
	// BaseDir is the directory path where discovery was run.
	BaseDir string `json:"base_dir"`
	// Count is the number of candidate repositories discovered.
	Count int `json:"count"`
	// Candidates contains the list of discovered repository details.
	Candidates []discover.Candidate `json:"candidates"`
}

func (s *Server) handleDiscoverRepos(ctx context.Context, _ *mcp.CallToolRequest, input discoverReposInput) (*mcp.CallToolResult, DiscoverReposOutput, error) {
	idx, err := s.scan()
	if err != nil {
		return nil, DiscoverReposOutput{}, fmt.Errorf("scan workspace: %v", err)
	}
	base := idx.Root
	if input.BaseDir != "" {
		resolved, err := idx.SafePath(input.BaseDir)
		if err != nil {
			return nil, DiscoverReposOutput{}, fmt.Errorf("invalid base_dir: %w", err)
		}
		base = resolved
	}
	candidates, err := discoverReposFunc(ctx, discover.Options{
		BaseDir:       base,
		UntrackedOnly: input.UntrackedOnly,
		MaxDepth:      input.MaxDepth,
	})
	if err != nil {
		return nil, DiscoverReposOutput{}, fmt.Errorf("discovery failed: %w", err)
	}
	body := DiscoverReposOutput{
		BaseDir:    base,
		Count:      len(candidates),
		Candidates: candidates,
	}
	return jsonResult(body), body, nil
}
