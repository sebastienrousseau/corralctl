// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/sebastienrousseau/corralctl/internal/diag"
	"github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/sebastienrousseau/corralctl/internal/sanitize"
)

// listReposInput is the argument set for corral_list_repos.
type listReposInput struct {
	Visibility     string `json:"visibility,omitempty" jsonschema:"Filter by visibility directory: 'Public' or 'Private'. Case-insensitive."`
	Language       string `json:"language,omitempty" jsonschema:"Filter by language directory (e.g. 'go', 'rust'). Case-insensitive."`
	NameContains   string `json:"name_contains,omitempty" jsonschema:"Substring match against the repository name. Case-insensitive."`
	SyncedOnly     bool   `json:"synced_only,omitempty" jsonschema:"When true return only repositories corral has previously synced."`
	Limit          int    `json:"limit,omitempty" jsonschema:"Maximum repositories to return. Default 50, maximum 200."`
	Offset         int    `json:"offset,omitempty" jsonschema:"Index to start from. Pass the previous response's next_offset."`
	ResponseFormat string `json:"response_format,omitempty" jsonschema:"'concise' (default) returns path name visibility and language only. 'detailed' adds origin URL and sync state."`
}

// queryInput is the single-field argument set shared by the lookup tools.
type queryInput struct {
	Query string `json:"query" jsonschema:"Repository identifier: bare name ('corral'), relative path ('Public/go/corral'), or any path suffix that uniquely identifies a repo."`
}

// pageInput is the argument set for corral_workspace_index.
type pageInput struct {
	Limit          int    `json:"limit,omitempty" jsonschema:"Maximum repositories to return. Default 50, maximum 200."`
	Offset         int    `json:"offset,omitempty" jsonschema:"Index to start from, for paging."`
	ResponseFormat string `json:"response_format,omitempty" jsonschema:"'concise' (default) or 'detailed'."`
}

// noInput is for tools that take no arguments.
type noInput struct{}

// cloneInput is the argument set for corral_clone_repo.
type cloneInput struct {
	URL      string `json:"url" jsonschema:"Git remote URL to clone."`
	Target   string `json:"target" jsonschema:"Destination path, relative to the workspace root or absolute within it."`
	Depth    int    `json:"depth,omitempty" jsonschema:"Shallow-clone depth. 0 (the default) clones full history."`
	Blobless bool   `json:"blobless,omitempty" jsonschema:"Use a partial clone with filter=blob:none."`
}

// createWorktreeInput is the argument set for corral_create_worktree.
type createWorktreeInput struct {
	Repo   string `json:"repo" jsonschema:"Repository name or relative path in workspace (required)."`
	Branch string `json:"branch" jsonschema:"New branch name for the isolated agent worktree (required)."`
}

// releaseWorktreeInput is the argument set for corral_release_worktree.
type releaseWorktreeInput struct {
	Repo   string `json:"repo" jsonschema:"Repository name or relative path in workspace (required)."`
	Branch string `json:"branch,omitempty" jsonschema:"Branch name of the worktree to remove."`
	Path   string `json:"path,omitempty" jsonschema:"Worktree path to remove if branch is not specified."`
	Force  bool   `json:"force,omitempty" jsonschema:"Force remove the worktree even if uncommitted changes exist."`
}

// readOnlyAnnotations is the annotation set every read tool carries.
func readOnlyAnnotations() *mcp.ToolAnnotations {
	t := true
	f := false
	return &mcp.ToolAnnotations{
		ReadOnlyHint:    t,
		DestructiveHint: &f,
		IdempotentHint:  t,
		OpenWorldHint:   &f,
	}
}

// registerTools attaches the read-only tool set.
func (s *Server) registerTools() {
	addTool(s, &mcp.Tool{
		Name:        "corral_list_repos",
		Title:       "List local repository clones",
		Annotations: readOnlyAnnotations(),
		Description: "List local clones in the Corral-organised workspace, optionally filtered by visibility (Public/Private), language, repository-name substring, or whether a sync sidecar is present. Results are paginated: the response carries total_matched, returned and next_offset; pass next_offset back as 'offset' to continue. Use response_format 'concise' (the default) unless you specifically need origin URLs and sync timestamps — 'detailed' is roughly ten times larger per repository.",
	}, s.handleListRepos)

	addTool(s, &mcp.Tool{
		Name:        "corral_find_repo",
		Title:       "Find one repository by name",
		Annotations: readOnlyAnnotations(),
		Description: "Resolve a fuzzy repository name (bare name, relative path, or path suffix) to a single local clone in the Corral workspace. Returns the matched repository, or an error listing all candidate paths when the query is ambiguous.",
	}, s.handleFindRepo)

	addTool(s, &mcp.Tool{
		Name:        "corral_get_repo_metadata",
		Title:       "Get full metadata for one repository",
		Annotations: readOnlyAnnotations(),
		Description: "Return full metadata for a single local clone: repository entry, current branch, and parsed sync state. The branch lookup spawns one git subprocess per call; prefer corral_list_repos for bulk queries.",
	}, s.handleRepoMetadata)

	addTool(s, &mcp.Tool{
		Name:        "corral_status_summary",
		Title:       "Summarise the workspace",
		Annotations: readOnlyAnnotations(),
		Description: "High-level workspace summary: total repository count and breakdowns by visibility and language. Cheap to compute; the right opening call for orienting in an unfamiliar workspace.",
	}, s.handleStatusSummary)

	addTool(s, &mcp.Tool{
		Name:        "corral_workspace_index",
		Title:       "Page through every repository",
		Annotations: readOnlyAnnotations(),
		Description: "Return a whole-workspace summary plus a bounded page of repositories. Prefer corral_list_repos when you can filter — this returns everything and is the most expensive read here. Paginated: pass next_offset back as 'offset'. Defaults to the concise projection; 'detailed' adds origin URLs and sync state.",
	}, s.handleWorkspaceIndex)

	addTool(s, &mcp.Tool{
		Name:        "corral_graph_dependencies",
		Title:       "Analyze cross-repository dependencies",
		Annotations: readOnlyAnnotations(),
		Description: "Analyze package dependencies across all repositories in the Corral workspace (Go modules, Rust crates, Node/TypeScript packages, Python projects). Returns the directed dependency graph, topological build and test execution order, detected cycles, and reverse dependent lookups.",
	}, s.handleGraphDependencies)

	addTool(s, &mcp.Tool{
		Name:        "corral_doctor",
		Title:       "Audit workspace health and git status",
		Annotations: readOnlyAnnotations(),
		Description: "Audit local repository clones in the Corral workspace for uncommitted changes, unpushed commits, detached heads, and linked worktrees. Returns workspace health summary and counts.",
	}, s.handleDoctor)

	addTool(s, &mcp.Tool{
		Name:        "corral_discover_repos",
		Title:       "Discover unmanaged and untracked repositories",
		Annotations: readOnlyAnnotations(),
		Description: "Scan the filesystem under base_dir (defaults to workspace root) to find local Git repositories that are untracked or unmanaged by Corral. Returns candidate paths, detected primary languages, and remote status with aggressive boundary pruning.",
	}, s.handleDiscoverRepos)
}

func (s *Server) handleListRepos(ctx context.Context, _ *mcp.CallToolRequest, in listReposInput) (*mcp.CallToolResult, PageOutput, error) {
	idx, err := s.scan()
	if err != nil {
		return nil, PageOutput{}, fmt.Errorf("scan workspace: %v", err)
	}
	out := filterRepos(idx.Repos, in)
	page, meta := paginate(out, in.Limit, in.Offset)
	body := PageOutput{
		Root:         idx.Root,
		TotalMatched: meta.Total,
		Returned:     meta.Returned,
		Repos:        projectRepos(page, in.ResponseFormat),
	}
	if meta.NextOffset > 0 {
		body.NextOffset = meta.NextOffset
		body.Note = "More results are available. Pass next_offset as 'offset', or narrow the filters."
	}
	if idx.Truncated {
		body.WorkspaceTruncated = true
	}
	return jsonResult(body), body, nil
}

func (s *Server) handleFindRepo(ctx context.Context, _ *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, RepoSummary, error) {
	idx, err := s.scan()
	if err != nil {
		return nil, RepoSummary{}, fmt.Errorf("scan workspace: %v", err)
	}
	match, err := idx.Find(in.Query)
	if err != nil {
		return nil, RepoSummary{}, err
	}
	out := summarizeDetailed(*match)
	return jsonResult(out), out, nil
}

func (s *Server) handleRepoMetadata(ctx context.Context, _ *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, RepoMetadataOutput, error) {
	idx, err := s.scan()
	if err != nil {
		return nil, RepoMetadataOutput{}, fmt.Errorf("scan workspace: %v", err)
	}
	match, err := idx.Find(in.Query)
	if err != nil {
		return nil, RepoMetadataOutput{}, err
	}
	branch := sanitize.Untrusted(currentBranch(ctx, match.Path), maxEntryField)
	out := RepoMetadataOutput{
		Repo:          summarizeDetailed(*match),
		CurrentBranch: branch,
	}
	return jsonResult(out), out, nil
}

func (s *Server) handleStatusSummary(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, StatusSummaryOutput, error) {
	idx, err := s.scan()
	if err != nil {
		return nil, StatusSummaryOutput{}, fmt.Errorf("scan workspace: %v", err)
	}
	byVis := map[string]int{}
	byLang := map[string]int{}
	synced := 0
	for _, r := range idx.Repos {
		r = r.Redacted()
		if r.Visibility != "" {
			byVis[r.Visibility]++
		}
		if r.Language != "" {
			byLang[r.Language]++
		}
		if r.State != nil && r.State.LastSyncedAt != "" {
			synced++
		}
	}
	out := StatusSummaryOutput{
		Root:         idx.Root,
		Total:        len(idx.Repos),
		Synced:       synced,
		ByVisibility: byVis,
		ByLanguage:   sortedLangCounts(byLang),
	}
	return jsonResult(out), out, nil
}

func (s *Server) handleWorkspaceIndex(ctx context.Context, _ *mcp.CallToolRequest, in pageInput) (*mcp.CallToolResult, PageOutput, error) {
	idx, err := s.scan()
	if err != nil {
		return nil, PageOutput{}, fmt.Errorf("scan workspace: %v", err)
	}
	page, meta := paginate(idx.Repos, in.Limit, in.Offset)
	body := PageOutput{
		Root:         idx.Root,
		TotalMatched: meta.Total,
		Returned:     meta.Returned,
		Repos:        projectRepos(page, in.ResponseFormat),
	}
	if meta.NextOffset > 0 {
		body.NextOffset = meta.NextOffset
		body.Note = "More results are available. Pass next_offset as 'offset', or use corral_list_repos with filters."
	}
	if idx.Truncated {
		body.WorkspaceTruncated = true
		body.WorkspaceTruncatedNote = "The workspace exceeded the scan cap; some repositories are missing from this index."
	}
	return jsonResult(body), body, nil
}

func filterRepos(repos []RepoEntry, in listReposInput) []RepoEntry {
	visibility := lowerTrim(in.Visibility)
	language := lowerTrim(in.Language)
	nameSubstr := lowerTrim(in.NameContains)

	var out []RepoEntry
	for _, r := range repos {
		if visibility != "" && lowerTrim(r.Visibility) != visibility {
			continue
		}
		if language != "" && lowerTrim(r.Language) != language {
			continue
		}
		if nameSubstr != "" && !containsFold(r.Name, nameSubstr) {
			continue
		}
		if in.SyncedOnly && (r.State == nil || r.State.LastSyncedAt == "") {
			continue
		}
		out = append(out, r)
	}
	return out
}

func sortedLangCounts(m map[string]int) []LanguageCount {
	out := make([]LanguageCount, 0, len(m))
	for k, v := range m {
		out = append(out, LanguageCount{Language: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Language < out[j].Language
	})
	return out
}

var currentBranch = func(ctx context.Context, repoPath string) string {
	branch, err := git.CurrentBranch(ctx, repoPath)
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		if stderr != "" {
			diag.Warnf("corral-mcp: git rev-parse in %s failed: %v (%s)", repoPath, err, stderr)
		} else {
			diag.Warnf("corral-mcp: git rev-parse in %s failed: %v", repoPath, err)
		}
		return ""
	}
	return branch
}

func addTool[In, Out any](s *Server, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	mcp.AddTool(s.mcp, t, h)
	s.toolNames = append(s.toolNames, t.Name)
}

// ToolNames returns every tool this server registered, in registration
// order.
func (s *Server) ToolNames() []string {
	out := make([]string, len(s.toolNames))
	copy(out, s.toolNames)
	return out
}
