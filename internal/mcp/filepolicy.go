// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Content policy for the repository file resource.
//
// The policy is an allowlist with a denylist behind it, in that order of
// authority: a path must both be a recognised source/documentation/config
// shape AND not match a known credential store.
//
// It used to be a denylist alone. That inverted the burden onto the list
// author, who had to have thought of every credential filename in advance —
// and an audit found eight that had not been: .kube/config, kubeconfig,
// credentials.json, .pgpass, terraform.tfvars, .htpasswd, .yarnrc.yml and
// deploy.ppk were all served in full. On a surface a prompt-injected agent
// can reach, "everything except what I remembered to exclude" is the wrong
// default. An allowlist fails closed: an unrecognised extension is refused,
// and the cost of that is a clear error naming the flag that permits it.
//
// The denylist is kept, and still matters, because some credential files
// wear an allowed extension — credentials.json is .json, .yarnrc.yml is
// .yml — so extension alone cannot decide them.

// allowedFileExts are the extensions the file resource serves, matched
// case-insensitively against filepath.Ext. Source, documentation, markup
// and non-secret configuration. Anything absent is refused.
var allowedFileExts = map[string]struct{}{
	// Source
	".c": {}, ".cc": {}, ".clj": {}, ".cljs": {}, ".cpp": {}, ".cs": {},
	".cxx": {}, ".dart": {}, ".el": {}, ".elm": {}, ".erl": {}, ".ex": {},
	".exs": {}, ".fs": {}, ".fsx": {}, ".go": {}, ".groovy": {}, ".h": {},
	".hh": {}, ".hpp": {}, ".hrl": {}, ".hs": {}, ".java": {}, ".jl": {},
	".js": {}, ".jsx": {}, ".kt": {}, ".kts": {}, ".lisp": {}, ".lua": {},
	".m": {}, ".ml": {}, ".mli": {}, ".mm": {}, ".nim": {}, ".php": {},
	".pl": {}, ".pm": {}, ".purs": {}, ".py": {}, ".r": {}, ".rb": {},
	".rkt": {}, ".rs": {}, ".scala": {}, ".scm": {}, ".sql": {}, ".svelte": {},
	".swift": {}, ".ts": {}, ".tsx": {}, ".v": {}, ".vue": {}, ".zig": {},

	// Shell and build
	".awk": {}, ".bash": {}, ".bat": {}, ".cmake": {}, ".cmd": {},
	".fish": {}, ".gradle": {}, ".mk": {}, ".ps1": {}, ".sed": {},
	".sh": {}, ".zsh": {},

	// Schema and IDL
	".graphql": {}, ".gql": {}, ".proto": {}, ".thrift": {},

	// Markup and web
	".astro": {}, ".css": {}, ".htm": {}, ".html": {}, ".less": {},
	".sass": {}, ".scss": {}, ".styl": {}, ".svg": {}, ".xml": {},

	// Documentation
	".adoc": {}, ".asciidoc": {}, ".markdown": {}, ".md": {}, ".mdx": {},
	".org": {}, ".rst": {}, ".tex": {}, ".txt": {},

	// Data and configuration
	".cfg": {}, ".conf": {}, ".csv": {}, ".ini": {}, ".json": {},
	".jsonc": {}, ".properties": {}, ".sum": {}, ".toml": {}, ".tsv": {},
	".yaml": {}, ".yml": {},
}

// allowedFileNames are exact basenames served regardless of extension —
// the conventional extensionless files at a repository root, plus the
// dotfiles that describe a project rather than authenticate to anything.
var allowedFileNames = map[string]struct{}{
	".dockerignore": {}, ".editorconfig": {}, ".gitattributes": {},
	".gitignore": {}, ".gitmodules": {}, ".golangci.yml": {},
	".nvmrc": {}, ".prettierrc": {}, ".ruby-version": {}, ".tool-versions": {},
	"authors": {}, "brewfile": {}, "cargo.lock": {}, "changelog": {},
	"cmakelists.txt": {}, "codeowners": {}, "containerfile": {},
	"contributors": {}, "copying": {}, "dockerfile": {}, "gemfile": {},
	"go.mod": {}, "go.sum": {}, "jenkinsfile": {}, "justfile": {},
	"licence": {}, "license": {}, "makefile": {}, "notice": {},
	"procfile": {}, "rakefile": {}, "readme": {}, "todo": {},
	"vagrantfile": {}, "version": {},
}

// deniedAllowedExtNames are basenames that pass the extension allowlist but
// are credential stores anyway. Each wears a legitimate extension, which is
// exactly why the allowlist alone cannot refuse them.
// #nosec G101 -- these are filenames to refuse, not credentials
var deniedAllowedExtNames = map[string]string{
	"client_secret.json": "OAuth client secret",
	"credentials.json":   "service-account or OAuth credentials",
	"kubeconfig.yaml":    "kubeconfig holds cluster credentials",
	"kubeconfig.yml":     "kubeconfig holds cluster credentials",
	"secrets.json":       "named secrets file",
	".yarnrc.yml":        "yarnrc may hold registry tokens (npmAuthToken)",
}

// FileAllowed reports whether a repository-relative path is permitted by the
// default workspace content policy.
func FileAllowed(rel string) bool {
	_, ok := fileAllowed(rel, nil)
	return ok
}

// fileAllowed reports whether rel's shape is servable, and when it is not,
// an error naming the flag that would permit it. rel is repository-relative
// and already lexically cleaned by the caller.
//
// extra carries the extensions added via --allow-file-ext, normalised to a
// leading dot and lowercase by normalizeExtraExts.
func fileAllowed(rel string, extra map[string]struct{}) (string, bool) {
	base := strings.ToLower(filepath.Base(rel))
	if base == "" || base == "." {
		return "not a file", false
	}
	if _, ok := allowedFileNames[base]; ok {
		return "", true
	}
	ext := strings.ToLower(filepath.Ext(base))
	// filepath.Ext(".gitignore") is ".gitignore" — the whole name — because
	// the final dot is at index 0. Such a name is only servable via
	// allowedFileNames above, so treat it as having no extension here rather
	// than letting a dotfile masquerade as an extension match.
	if ext == base {
		return fmt.Sprintf("%q is not a recognised source or documentation file; "+
			"pass --allow-file-ext to permit additional extensions", base), false
	}
	if _, ok := allowedFileExts[ext]; ok {
		return "", true
	}
	if _, ok := extra[ext]; ok {
		return "", true
	}
	if ext == "" {
		return fmt.Sprintf("%q has no extension and is not a recognised project file; "+
			"pass --allow-file-ext to permit additional extensions", base), false
	}
	return fmt.Sprintf("%s files are not served; pass --allow-file-ext %s to permit them", ext, ext), false
}

// normalizeExtraExts turns user-supplied extensions into the lookup form
// fileAllowed expects: lowercased, dot-prefixed, blanks dropped. Accepts
// both "go" and ".go" because a user will type either.
func normalizeExtraExts(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(values))
	for _, v := range values {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			continue
		}
		if !strings.HasPrefix(v, ".") {
			v = "." + v
		}
		if v == "." {
			continue
		}
		out[v] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// allowedExtensionList renders the served extensions for the resource
// description, so the model is told what it may read instead of finding
// out one refusal at a time.
func allowedExtensionList() string {
	exts := make([]string, 0, len(allowedFileExts))
	for ext := range allowedFileExts {
		exts = append(exts, ext)
	}
	sort.Strings(exts)
	return strings.Join(exts, " ")
}

// allowedCloneSchemes are the transports corral_clone_repo will hand to
// git. The agent chooses the URL, so the set it may choose from is
// corral's to decide rather than the local git binary's.
//
// git already refuses ext:: by default — protocol.ext.allow is "never"
// unless a user sets otherwise, and a probe against git 2.55 confirmed
// `fatal: transport 'ext' not allowed`. That default is the only thing
// standing between an agent-supplied URL and arbitrary command
// execution, and it lives in configuration corral does not own, on a
// machine corral does not control. A user who enables ext:// for a
// legitimate reason should not silently lose the guarantee.
//
// file:// is excluded for a different reason: it is not dangerous, but a
// clone of a local path is not what "clone a repository into the
// workspace" means, and allowing it lets an agent copy any local
// repository into the indexed tree.
var allowedCloneSchemes = map[string]struct{}{
	"https": {},
	"ssh":   {},
	"git":   {},
}

// validateCloneURL reports why raw may not be cloned, if it may not.
// Accepts both scheme://host/path and the scp-style user@host:path form
// that git and every forge's copy button emit.
func validateCloneURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Errorf("clone url must not be empty")
	}
	// A leading "-" would be read as an option by anything that loses the
	// "--" terminator downstream.
	if strings.HasPrefix(trimmed, "-") {
		return fmt.Errorf("clone url must not begin with %q", "-")
	}
	// Remote-helper syntax is "transport::address", not "transport://" —
	// the dangerous one is `ext::sh -c …`, which runs a command. Checking
	// only for "://" missed it entirely: it fell through to the scp-style
	// branch and was refused for the wrong reason, which would have been a
	// confusing error and a fragile guarantee. None of the permitted
	// transports use this form, so reject the whole shape.
	if helper, _, found := strings.Cut(trimmed, "::"); found && isTransportToken(helper) {
		return fmt.Errorf("clone url transport %q is not permitted; allowed schemes are git, https, ssh", helper)
	}
	if i := strings.Index(trimmed, "://"); i >= 0 {
		scheme := strings.ToLower(trimmed[:i])
		if _, ok := allowedCloneSchemes[scheme]; !ok {
			return fmt.Errorf("clone url scheme %q is not permitted; allowed schemes are git, https, ssh", scheme)
		}
		return nil
	}
	// scp-style: user@host:path — no scheme, and git treats it as ssh.
	if at := strings.Index(trimmed, "@"); at > 0 {
		if colon := strings.Index(trimmed[at:], ":"); colon > 0 {
			return nil
		}
	}
	return fmt.Errorf("clone url %q has no scheme; use https://, ssh:// or the git@host:owner/repo form", trimmed)
}

// isTransportToken reports whether s looks like a git transport name — the
// left-hand side of "transport::address". Anything with a slash, a space or
// an "@" is a path or an scp-style target that merely happens to contain a
// double colon, not a remote-helper invocation.
func isTransportToken(s string) bool {
	if s == "" || strings.ContainsAny(s, "/@ \t") {
		return false
	}
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case i > 0 && (r >= '0' && r <= '9' || r == '+' || r == '.' || r == '-'):
		default:
			return false
		}
	}
	return true
}
