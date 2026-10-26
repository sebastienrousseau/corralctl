// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"text/template"
	"unicode"
	"unicode/utf8"

	"github.com/sebastienrousseau/corralctl/internal/github"
)

const (
	defaultLayout          = "{{.Collection}}/{{.Bucket}}/{{.Name}}"
	searchLayout           = "{{.Owner}}/{{.Collection}}/{{.Bucket}}/{{.Name}}"
	maxLayoutTemplateBytes = 4096
)

func usesClassicLayout(opts RunOptions) bool {
	return (opts.Layout == "" && !isSearchOwner(opts.Owner)) || opts.Layout == defaultLayout
}

func effectiveLayout(opts RunOptions, repo github.Repo) string {
	if opts.Layout != "" {
		return opts.Layout
	}
	if isSearchOwner(opts.Owner) {
		return searchLayout
	}
	return defaultLayout
}

func isSearchOwner(owner string) bool {
	return strings.HasPrefix(owner, "topic:") || strings.HasPrefix(owner, "language:")
}

func normalizeLanguage(lang string) string {
	if lang == "" {
		return "other"
	}
	l := lang
	switch l {
	case "C#":
		l = "CSharp"
	case "C++":
		l = "Cpp"
	}
	l = strings.ReplaceAll(l, " ", "_")
	l = strings.ReplaceAll(l, "/", "_")
	return strings.ToLower(l)
}

func repositoryBucket(repo github.Repo) string {
	if strings.HasSuffix(strings.ToLower(repo.Name), ".github.io") {
		return "Web"
	}
	return canonicalLanguage(repo.Language)
}

func canonicalLanguage(lang string) string {
	normalized := normalizeLanguage(lang)
	canonical := map[string]string{
		"c": "C", "cpp": "Cpp", "csharp": "CSharp", "css": "CSS",
		"dockerfile": "Docker", "go": "Go", "html": "HTML",
		"javascript": "JavaScript", "jupyter_notebook": "Python", "lua": "Lua",
		"objective-c": "Objective-C", "objective-c++": "Objective-Cpp", "other": "Other",
		"php": "PHP", "python": "Python", "ruby": "Ruby", "rust": "Rust",
		"scss": "SCSS", "shell": "Shell", "solidity": "Solidity", "stylus": "Stylus",
		"swift": "Swift", "tex": "TeX", "typescript": "TypeScript",
	}
	if bucket, ok := canonical[normalized]; ok {
		return bucket
	}
	parts := strings.FieldsFunc(normalized, func(r rune) bool { return r == '_' || r == '-' })
	for i, part := range parts {
		parts[i] = strings.ToUpper(part[:1]) + part[1:]
	}
	return strings.Join(parts, "")
}

func repositoryCollection(repo github.Repo) string {
	if repo.Fork {
		return "Forks"
	}
	if strings.EqualFold(repo.Visibility, "private") {
		return "Private"
	}
	return "Public"
}

// ResolveLayoutPath constructs the canonical destination directory path for an adopted
// or relocated repository according to the collection, language ecosystem, and repository name.
func ResolveLayoutPath(baseDir, collection, language, repoName string) string {
	col := canonicalCollectionName(collection)
	if col == "" {
		trimmed := strings.TrimSpace(collection)
		if trimmed == "" {
			col = "Public"
		} else {
			r, size := utf8.DecodeRuneInString(trimmed)
			col = string(unicode.ToUpper(r)) + strings.ToLower(trimmed[size:])
		}
	}
	cleanName := filepath.Base(filepath.Clean(repoName))
	if cleanName == "." || cleanName == "/" || cleanName == ".." {
		cleanName = "unnamed-repo"
	}
	bucket := canonicalLanguage(language)
	target := filepath.Join(baseDir, col, bucket, cleanName)
	rel, err := filepath.Rel(baseDir, target)
	if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
		return filepath.Join(baseDir, "Public", bucket, cleanName)
	}
	return target
}

// collections are the top-level directories of the default layout, in
// the order a migration creates them.
var collections = []string{"Public", "Private", "Work", "Forks"}

// Collections returns the top-level directories of the default layout:
// Public, Private, Work and Forks. The returned slice is a copy, so a
// caller may modify it.
func Collections() []string {
	return append([]string(nil), collections...)
}

func canonicalCollectionName(name string) string {
	for _, collection := range collections {
		if strings.EqualFold(name, collection) {
			return collection
		}
	}
	return ""
}

func evaluateLayout(layoutTpl string, repo github.Repo, owner string) (string, error) {
	if layoutTpl == "" {
		layoutTpl = defaultLayout
	}
	tmpl, err := parseLayoutTemplate(layoutTpl)
	if err != nil {
		return "", err
	}
	return executeLayout(tmpl, repo, owner)
}

// ParseLayoutTemplate validates a --layout template without rendering it, so
// the CLI can reject a malformed template during flag validation instead of
// after a full paginated GitHub fetch has already happened.
func ParseLayoutTemplate(layout string) (*template.Template, error) {
	return parseLayoutTemplate(layout)
}

func parseLayoutTemplate(layout string) (*template.Template, error) {
	if len(layout) > maxLayoutTemplateBytes {
		return nil, fmt.Errorf("layout template exceeds %d bytes", maxLayoutTemplateBytes)
	}
	return template.New("layout").Parse(layout)
}

func executeLayout(tmpl *template.Template, repo github.Repo, owner string) (string, error) {
	var buf bytes.Buffer
	data := struct {
		Visibility    string
		Language      string
		Collection    string
		Bucket        string
		Name          string
		Owner         string
		FullName      string
		ID            int64
		DefaultBranch string
	}{
		Visibility:    strings.ToLower(repo.Visibility),
		Language:      normalizeLanguage(repo.Language),
		Collection:    repositoryCollection(repo),
		Bucket:        repositoryBucket(repo),
		Name:          repo.Name,
		Owner:         firstNonEmpty(repo.Owner, owner),
		FullName:      repo.FullName,
		ID:            repo.ID,
		DefaultBranch: repo.DefaultBranch,
	}
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", err
	}
	cleanPath := filepath.Clean(buf.String())
	if strings.HasPrefix(cleanPath, "..") || cleanPath == "." || filepath.IsAbs(cleanPath) || strings.HasPrefix(buf.String(), "/") || strings.HasPrefix(buf.String(), "\\") {
		return "", fmt.Errorf("layout escapes base directory: %s", cleanPath)
	}
	return filepath.ToSlash(cleanPath), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
