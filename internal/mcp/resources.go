// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sebastienrousseau/corralctl/internal/sanitize"
)

// MIME types the corral resources advertise. Pinned constants keep the
// resource registration and the handlers in sync without typos drifting.
const (
	mimeJSON     = "application/json"
	mimeMarkdown = "text/markdown"
	mimePlain    = "text/plain"
)

// maxFileBytes caps the size of any single file the file-resource
// will return. A misconfigured agent that asks for a multi-GB log
// file shouldn't be able to OOM the host. 1 MiB matches the upper
// bound documented for VS Code MCP clients and is plenty for source.
const maxFileBytes = 1 << 20

// maxTreeEntries bounds a tree listing. A var, not a const, for the same
// reason as maxIndexRepos: the truncation branch is a real behaviour and
// tests need to reach it without materialising 2,000 files.
var maxTreeEntries = 2_000

var (
	marshalResource = json.MarshalIndent
	walkResource    = filepath.WalkDir
	openResource    = os.Open
	readResourceAll = io.ReadAll
)

// registerResources attaches the v0 resource set (one static index +
// three URI templates) to the underlying MCP server. URI scheme is
// `corral://` per the design doc; templated paths use RFC 6570
// expansion (handled by mcp-go via the github.com/yosida95/uritemplate
// dependency it already pulls in).
func (s *Server) registerResources() {
	s.mcp.AddResource(&mcp.Resource{
		URI:         "corral://workspace/index",
		Name:        "Workspace index",
		Description: "Full JSON index of every clone in the Corral workspace. Mirrors the corral_workspace_index tool output.",
		MIMEType:    mimeJSON,
	}, s.handleWorkspaceIndexResource)

	s.mcp.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "corral://repo/{owner}/{name}/state",
		Name:        "Repository sync state",
		Description: "Parsed sync sidecar for a single clone: last upstream pushed_at and last local sync timestamp.",
		MIMEType:    mimeJSON,
	}, s.handleRepoStateResource)

	s.mcp.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "corral://repo/{owner}/{name}/tree",
		Name:        "Repository top-level tree",
		Description: "Shallow (two levels) directory listing for a single clone. Use the file resource to read individual file contents.",
		MIMEType:    mimePlain,
	}, s.handleRepoTreeResource)

	// {+path} uses RFC 6570 reserved expansion so the segment matches "/".
	// With plain {path} (simple expansion) no file below the repository's top
	// level was reachable at all.
	s.mcp.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "corral://repo/{owner}/{name}/file/{+path}",
		Name:        "Repository file contents",
		Description: "Read a single file from a clone, bounded at 1 MiB. The path segment is relative to the repository root, may contain subdirectories, and is validated against the configured server root to prevent directory traversal. Only source, documentation and non-secret configuration files are served, by extension allowlist: " + allowedExtensionList() + ", plus conventional project files such as Makefile, Dockerfile, LICENSE and go.mod. Git internals, credential directories and credential files are refused.",
		MIMEType:    mimePlain,
	}, s.handleRepoFileResource)
}

// workspaceIndexResource is the only static resource. It mirrors the
// corral_workspace_index tool, but exposed as a resource so clients
// that prefer to subscribe (rather than call tools) get the same data.
func (s *Server) handleWorkspaceIndexResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	idx, err := s.scan()
	if err != nil {
		return nil, fmt.Errorf("scan workspace: %w", err)
	}
	// Mirror of the corral_workspace_index tool, so it gets the same
	// output-boundary redaction the tool path applies.
	redacted := Index{Root: idx.Root, Repos: RedactedEntries(idx.Repos), Truncated: idx.Truncated}
	b, err := marshalResource(redacted, "", "  ")
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: mimeJSON, Text: string(b)}}}, nil
}

// repoStateResource exposes the on-disk .corral-state.json sidecar for
// a single clone via a URI template. Returns 404-equivalent (error)
// when the repo or sidecar isn't found, so clients can distinguish
// "no such repo" from "no sync yet" by the error text.
func (s *Server) handleRepoStateResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	repo, err := s.resolveURIRepo(req.Params.URI)
	if err != nil {
		return nil, err
	}
	state, ok := readState(repo.Path)
	if !ok {
		return nil, fmt.Errorf("no .corral-state.json sidecar found in %s", repo.RelPath)
	}
	b, err := marshalResource(state, "", "  ")
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: mimeJSON, Text: string(b)}}}, nil
}

// repoTreeResource returns a top-level file/directory listing for one
// clone, scoped to two-deep entries (the agent's first orientation pass
// rarely needs more than that, and a deep listing of a large repo would
// blow the response budget). Bigger walks go through follow-up tool
// calls in later phases.
func (s *Server) handleRepoTreeResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	repo, err := s.resolveURIRepo(req.Params.URI)
	if err != nil {
		return nil, err
	}
	var lines []string
	err = walkResource(repo.Path, func(path string, d os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(lines) >= maxTreeEntries {
			return filepath.SkipAll
		}
		// One unreadable entry should not empty the whole tree listing.
		if walkErr != nil {
			return nil //nolint:nilerr // deliberate: skip this entry, keep walking
		}
		rel, _ := filepath.Rel(repo.Path, path)
		if rel == "." {
			return nil
		}
		depth := strings.Count(rel, string(filepath.Separator))
		if depth >= 2 {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Hide the .git internals — the agent doesn't need them and
		// they overwhelm the listing.
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		suffix := ""
		if d.IsDir() {
			suffix = "/"
		}
		// Filenames inside a cloned repository are entirely the remote's
		// choice, so the listing is an untrusted channel just as the
		// repository name is.
		lines = append(lines, sanitize.Untrusted(filepath.ToSlash(rel)+suffix, maxEntryPath))
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(lines) >= maxTreeEntries {
		lines = append(lines, fmt.Sprintf("[corral-mcp: tree truncated at %d entries]", maxTreeEntries))
	}
	return &mcp.ReadResourceResult{
		Contents: []*mcp.ResourceContents{{
			URI:      req.Params.URI,
			MIMEType: mimePlain,
			Text:     strings.Join(lines, "\n"),
		}},
	}, nil
}

// repoFileResource reads one file inside a clone. This is the highest-
// security-impact resource in v0: a path-traversal bug here would let
// an agent escape the workspace root. The handler validates the
// resolved path is still under the configured Root via Index.SafePath
// before opening the file, and bounds the read at maxFileBytes.
func (s *Server) handleRepoFileResource(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
	repo, err := s.resolveURIRepo(req.Params.URI)
	if err != nil {
		return nil, err
	}
	path, err := extractFilePath(req.Params.URI)
	if err != nil {
		return nil, err
	}
	// Content policy runs before path resolution: the sandbox stops an
	// agent leaving the repository, but .git/config is *inside* it and
	// contains the credentials of anyone who has cloned over HTTPS with a
	// token in the URL. The tree listing already hides .git; the file
	// reader did not, so fixing the {+path} routing above without this
	// would have turned an unreachable resource into a token leak.
	//
	// Denylist first, then allowlist, because the denylist produces the
	// more specific message: "git credential store" tells the caller why
	// this particular file is refused, where the allowlist can only say
	// the extension is not served.
	if reason, blocked := blockedRepoFile(path); blocked {
		return nil, fmt.Errorf("refusing to read %s: %s", path, reason)
	}
	if reason, ok := fileAllowed(path, s.extraFileExts); !ok {
		return nil, fmt.Errorf("refusing to read %s: %s", path, reason)
	}

	// Scope validation to the selected repository, not merely the wider
	// workspace. Otherwise ../ traversal could read a sibling clone.
	idx := &Index{Root: repo.Path}
	candidate := filepath.Join(repo.Path, path)
	safe, err := idx.SafePath(candidate)
	if err != nil {
		return nil, err
	}
	f, err := openResource(safe) // #nosec G304 -- SafePath enforces the workspace sandbox
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer func() { _ = f.Close() }()

	limited := io.LimitReader(f, maxFileBytes+1)
	body, err := readResourceAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	truncated := false
	if int64(len(body)) > maxFileBytes {
		body = body[:maxFileBytes]
		truncated = true
	}
	mime := guessMIME(safe)
	text := string(body)
	if truncated {
		text += "\n\n[corral-mcp: truncated at 1 MiB]\n"
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, MIMEType: mime, Text: text}}}, nil
}

// blockedRepoFileNames are basenames never served by the file resource. Each
// is a well-known credential or key store that routinely sits inside a working
// tree, so a prompt-injected agent reading one clone could otherwise exfiltrate
// secrets belonging to every other clone in the workspace.
// #nosec G101 -- these are filenames to refuse, not credentials
var blockedRepoFileNames = map[string]string{
	".env":                 "environment files commonly hold credentials",
	".envrc":               "environment files commonly hold credentials",
	".netrc":               "netrc holds host credentials",
	"_netrc":               "netrc holds host credentials",
	".npmrc":               "npmrc may hold registry tokens",
	".pypirc":              "pypirc may hold registry tokens",
	".git-credentials":     "git credential store",
	"credentials":          "credential store",
	"id_rsa":               "private key",
	"id_dsa":               "private key",
	"id_ecdsa":             "private key",
	"id_ed25519":           "private key",
	".dockercfg":           "docker registry credentials",
	"terraform.tfstate":    "terraform state may hold secrets",
	"secrets.yml":          "named secrets file",
	"secrets.yaml":         "named secrets file",
	"service-account.json": "service-account key",
	// Found served by an audit of the pre-allowlist policy. Most are now
	// also refused by the extension allowlist, but they are named here so
	// the caller gets "kubeconfig holds cluster credentials" rather than
	// "no extension and not a recognised project file" — and so the policy
	// still refuses them if their extension is ever allowlisted.
	".htpasswd":         "htpasswd holds password hashes",
	".pgpass":           "pgpass holds database credentials",
	".terraformrc":      "terraform CLI config may hold tokens",
	"kubeconfig":        "kubeconfig holds cluster credentials",
	".dockerconfigjson": "docker registry credentials",
}

// blockedRepoFileSuffixes are extensions never served, matched case-insensitively.
var blockedRepoFileSuffixes = map[string]string{
	".pem":      "private key or certificate",
	".key":      "private key",
	".p12":      "keystore",
	".pfx":      "keystore",
	".jks":      "keystore",
	".keystore": "keystore",
	".kdbx":     "password database",
	".ppk":      "PuTTY private key",
	".asc":      "PGP key or signature",
	".gpg":      "PGP encrypted data",
	".tfvars":   "terraform variables commonly hold secrets",
}

// blockedRepoFile reports whether the file resource must refuse rel, and why.
//
// rel is repository-relative and already lexically cleaned by the caller's
// filepath.Join. The check is deliberately conservative and name-based rather
// than content-based: refusing a handful of well-known names costs an agent
// almost nothing, while a single leaked token is unrecoverable.
func blockedRepoFile(rel string) (string, bool) {
	clean := filepath.ToSlash(filepath.Clean("/" + rel))
	clean = strings.TrimPrefix(clean, "/")
	if clean == "" || clean == "." {
		return "not a file", true
	}

	// Anything inside the git directory: config carries the remote URL and any
	// token embedded in it, and the object store is not useful to an agent.
	for _, seg := range strings.Split(clean, "/") {
		if strings.EqualFold(seg, ".git") {
			return "git internals are not readable", true
		}
		if strings.EqualFold(seg, ".ssh") || strings.EqualFold(seg, ".aws") ||
			strings.EqualFold(seg, ".gnupg") || strings.EqualFold(seg, ".kube") ||
			strings.EqualFold(seg, ".gcloud") || strings.EqualFold(seg, ".docker") {
			return "credential directory", true
		}
	}

	base := strings.ToLower(filepath.Base(clean))
	if reason, ok := blockedRepoFileNames[base]; ok {
		return reason, true
	}
	// Credential stores wearing an extension the allowlist serves, so the
	// allowlist alone cannot refuse them.
	if reason, ok := deniedAllowedExtNames[base]; ok {
		return reason, true
	}
	// .env.local, .env.production and friends.
	if strings.HasPrefix(base, ".env.") {
		return "environment files commonly hold credentials", true
	}
	for suffix, reason := range blockedRepoFileSuffixes {
		if strings.HasSuffix(base, suffix) {
			return reason, true
		}
	}
	return "", false
}

// resolveURIRepo parses owner+name out of a corral:// URI and returns
// the matching RepoEntry. Returns an error when the URI is malformed
// or the repo isn't in the index — both are surfaced to the agent.
//
// URIs are expected to look like:
//
//	corral://repo/{owner}/{name}/state
//	corral://repo/{owner}/{name}/tree
//	corral://repo/{owner}/{name}/file/{path}
//
// url.Parse treats "repo" as the Host and the rest as Path, so this
// concatenates them via path.Join semantics to avoid the double-slash
// pitfall that broke the first naive implementation.
func (s *Server) resolveURIRepo(uri string) (*RepoEntry, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("invalid uri: %w", err)
	}
	if u.Scheme != "corral" {
		return nil, fmt.Errorf("unsupported scheme %q (want corral)", u.Scheme)
	}
	combined := strings.Trim(u.Host, "/") + "/" + strings.TrimPrefix(u.Path, "/")
	combined = strings.Trim(combined, "/")
	parts := strings.Split(combined, "/")
	if len(parts) > 0 && parts[0] == "repo" {
		parts = parts[1:]
	}
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("uri %q is missing owner/name segments", uri)
	}
	owner, name := parts[0], parts[1]

	idx, err := s.scan()
	if err != nil {
		return nil, err
	}
	for i := range idx.Repos {
		r := &idx.Repos[i]
		if r.Name != name {
			continue
		}
		if strings.EqualFold(r.Visibility, owner) ||
			strings.EqualFold(firstSegment(r.RelPath), owner) ||
			ownerMatchesURL(r.RemoteURL, owner) {
			return r, nil
		}
	}
	return nil, fmt.Errorf("no repository %s/%s in workspace", owner, name)
}

// ownerMatchesURL reports whether owner equals ANY namespace segment
// preceding the repository name in remoteURL. This matters for
// GitLab-style / Gitea-style / self-hosted layouts with nested groups
// where an origin URL like https://git.example.com/parent/team/repo.git
// should match agent queries against both "parent" and "team". For
// standard GitHub URLs (https://github.com/owner/repo) the namespace
// list is a single element and behaviour is unchanged.
func ownerMatchesURL(remoteURL, owner string) bool {
	if remoteURL == "" || owner == "" {
		return false
	}
	for _, seg := range parseOwnerFromURL(remoteURL) {
		if strings.EqualFold(seg, owner) {
			return true
		}
	}
	return false
}

// parseOwnerFromURL returns every namespace segment that precedes the
// final repository segment in remoteURL, in order (root-most first).
// Empty slice when the URL can't be parsed. Handles both the HTTPS
// scheme://host/A/B/…/repo form and the SSH user@host:A/B/…/repo form.
// Returned as []string (rather than the previous single-segment form)
// so callers can match against deep hierarchies without losing the
// intermediate names.
func parseOwnerFromURL(remoteURL string) []string {
	if remoteURL == "" {
		return nil
	}
	remoteURL = strings.TrimSuffix(remoteURL, ".git")

	// HTTPS style: https://host/A/B/.../repo
	if strings.Contains(remoteURL, "://") {
		if u, err := url.Parse(remoteURL); err == nil {
			parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
			if len(parts) >= 2 {
				return parts[:len(parts)-1]
			}
		}
	}

	// SSH style: user@host:A/B/.../repo — everything after the first ':'
	// is the path.
	if idx := strings.Index(remoteURL, ":"); idx >= 0 {
		parts := strings.Split(remoteURL[idx+1:], "/")
		if len(parts) >= 2 {
			return parts[:len(parts)-1]
		}
	}
	return nil
}

// extractFilePath pulls the {path} portion out of a
// corral://repo/{owner}/{name}/file/{path} URI. mcp-go's URI template
// matcher doesn't expose captured groups to the handler, so we re-parse.
func extractFilePath(uri string) (string, error) {
	const marker = "/file/"
	idx := strings.Index(uri, marker)
	if idx < 0 {
		return "", fmt.Errorf("uri %q is not a file resource", uri)
	}
	raw := uri[idx+len(marker):]
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", fmt.Errorf("decoding path segment: %w", err)
	}
	if decoded == "" {
		return "", fmt.Errorf("file resource requires a non-empty path")
	}
	return decoded, nil
}

// firstSegment returns the first path component, used as a fallback
// match when a custom layout doesn't carry an explicit Visibility.
func firstSegment(rel string) string {
	if i := strings.Index(rel, "/"); i >= 0 {
		return rel[:i]
	}
	return rel
}

// guessMIME picks a content type from the file extension. Mostly to
// help clients render code with syntax highlighting; not security-
// relevant. Defaults to text/plain for anything unrecognised.
func guessMIME(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown":
		return mimeMarkdown
	case ".json":
		return mimeJSON
	default:
		return mimePlain
	}
}
