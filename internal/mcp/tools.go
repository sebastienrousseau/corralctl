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
	"github.com/sebastienrousseau/corralctl/internal/sanitize"
)

// Read-only tool set.
//
// Input schemas are derived from the Go structs below by the SDK, rather than
// declared with chained builders and then re-read with GetString/GetBool at the
// top of every handler. That removes the two places those could disagree: a
// parameter present in the schema but never read, and a parameter read but
// never declared. The `jsonschema` tags carry the descriptions the model sees.
//
// Annotations are set explicitly on every tool. They are not optional in
// practice: an omitted destructiveHint serialises as true, so leaving them off
// marked every read tool destructive and made corral_delete_repo's annotation
// indistinguishable from a directory listing's.

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
}

func (s *Server) handleListRepos(ctx context.Context, _ *mcp.CallToolRequest, in listReposInput) (*mcp.CallToolResult, any, error) {
	idx, err := s.scan()
	if err != nil {
		return toolError("scan workspace: %v", err), nil, nil
	}
	out := filterRepos(idx.Repos, in)
	page, meta := paginate(out, in.Limit, in.Offset)
	body := map[string]any{
		"root":          idx.Root,
		"total_matched": meta.Total,
		"returned":      meta.Returned,
		"repos":         projectRepos(page, in.ResponseFormat),
	}
	if meta.NextOffset > 0 {
		body["next_offset"] = meta.NextOffset
		body["note"] = "More results are available. Pass next_offset as 'offset', or narrow the filters."
	}
	if idx.Truncated {
		body["workspace_truncated"] = true
	}
	return jsonResult(body), nil, nil
}

func (s *Server) handleFindRepo(ctx context.Context, _ *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, any, error) {
	idx, err := s.scan()
	if err != nil {
		return toolError("scan workspace: %v", err), nil, nil
	}
	match, err := idx.Find(in.Query)
	if err != nil {
		return toolError("%v", err), nil, nil
	}
	return jsonResult(match.Redacted()), nil, nil
}

func (s *Server) handleRepoMetadata(ctx context.Context, _ *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, any, error) {
	idx, err := s.scan()
	if err != nil {
		return toolError("scan workspace: %v", err), nil, nil
	}
	match, err := idx.Find(in.Query)
	if err != nil {
		return toolError("%v", err), nil, nil
	}
	// match.Path (not the redacted copy) is what git is run against; only
	// the reported value is sanitised. The branch name is attacker-
	// controlled too — a branch may be named anything a ref allows.
	branch := sanitize.Untrusted(currentBranch(ctx, match.Path), maxEntryField)
	return jsonResult(map[string]any{
		"repo":           match.Redacted(),
		"current_branch": branch,
	}), nil, nil
}

func (s *Server) handleStatusSummary(ctx context.Context, _ *mcp.CallToolRequest, _ noInput) (*mcp.CallToolResult, any, error) {
	idx, err := s.scan()
	if err != nil {
		return toolError("scan workspace: %v", err), nil, nil
	}
	byVis := map[string]int{}
	byLang := map[string]int{}
	synced := 0
	for _, r := range idx.Repos {
		// Both are directory names under the workspace root, so both are
		// chosen by whoever created the directory.
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
	return jsonResult(map[string]any{
		"root":          idx.Root,
		"total":         len(idx.Repos),
		"synced":        synced,
		"by_visibility": byVis,
		"by_language":   sortedLangCounts(byLang),
	}), nil, nil
}

func (s *Server) handleWorkspaceIndex(ctx context.Context, _ *mcp.CallToolRequest, in pageInput) (*mcp.CallToolResult, any, error) {
	idx, err := s.scan()
	if err != nil {
		return toolError("scan workspace: %v", err), nil, nil
	}
	page, meta := paginate(idx.Repos, in.Limit, in.Offset)
	body := map[string]any{
		"root":          idx.Root,
		"total_matched": meta.Total,
		"returned":      meta.Returned,
		"repos":         projectRepos(page, in.ResponseFormat),
	}
	if meta.NextOffset > 0 {
		body["next_offset"] = meta.NextOffset
		body["note"] = "More results are available. Pass next_offset as 'offset', or use corral_list_repos with filters."
	}
	if idx.Truncated {
		// Set when a workspace exceeds the scan cap. Previously never surfaced
		// in any payload, so an over-cap workspace looked complete to callers.
		body["workspace_truncated"] = true
		body["workspace_truncated_note"] = "The workspace exceeded the scan cap; some repositories are missing from this index."
	}
	return jsonResult(body), nil, nil
}

// filterRepos applies the corral_list_repos filters. Split out so the handler
// stays about protocol shape and this stays about selection.
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

var _ = fmt.Sprintf // retained for handlers that format errors

// cloneInput is the argument set for corral_clone_repo.
type cloneInput struct {
	URL      string `json:"url" jsonschema:"Git remote URL to clone."`
	Target   string `json:"target" jsonschema:"Destination path, relative to the workspace root or absolute within it."`
	Depth    int    `json:"depth,omitempty" jsonschema:"Shallow-clone depth. 0 (the default) clones full history."`
	Blobless bool   `json:"blobless,omitempty" jsonschema:"Use a partial clone with filter=blob:none."`
}

func sortedLangCounts(m map[string]int) []map[string]any {
	out := make([]map[string]any, 0, len(m))
	for k, v := range m {
		out = append(out, map[string]any{"language": k, "count": v})
	}
	sort.Slice(out, func(i, j int) bool {
		ci, cj := out[i]["count"].(int), out[j]["count"].(int)
		if ci != cj {
			return ci > cj
		}
		return out[i]["language"].(string) < out[j]["language"].(string)
	})
	return out
}

// currentBranch shells out to git rev-parse to resolve HEAD's branch.
// Indirected through a package var so tests can stub without spawning
// a real subprocess. On error the caller gets an empty string (the
// tool result still succeeds with "current_branch": "") but the error
// is logged to stderr so operators can debug detached-HEAD,
// permission, and corrupt-git-tree cases that used to be silent.
// stderr is the only safe channel — stdout carries the JSON-RPC
// protocol stream and must not be polluted.
var currentBranch = func(ctx context.Context, repoPath string) string {
	cmd := exec.CommandContext(ctx, "git", "-C", repoPath, "rev-parse", "--abbrev-ref", "HEAD") // #nosec G204 -- fixed executable and root-confined path
	out, err := cmd.Output()
	if err != nil {
		// Include stderr from the failed process so operators can tell
		// "not a git repo" apart from "detached HEAD" apart from
		// "permission denied" apart from "corrupted refs".
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
	return strings.TrimSpace(string(out))
}

// addTool registers a tool and records its name.
//
// A thin wrapper over mcp.AddTool so the server knows its own surface.
// Without it, "which tools does this server expose" has no answer in code,
// and the only place the list exists is prose — which is exactly how
// `corralctl mcp --help` came to claim five tools while seven were
// registered, and later to omit two more entirely.
//
// ToolNames is what the help-text gate in cmd compares against, so a tool
// added here and not documented fails a test rather than shipping.
func addTool[In, Out any](s *Server, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	mcp.AddTool(s.mcp, t, h)
	s.toolNames = append(s.toolNames, t.Name)
}

// ToolNames returns every tool this server registered, in registration
// order.
//
// Which tools those are depends on the options the server was built with:
// a read-only server reports the read set, and the write tools appear only
// when they were unlocked.
func (s *Server) ToolNames() []string {
	out := make([]string, len(s.toolNames))
	copy(out, s.toolNames)
	return out
}
