// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"strings"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/github"
)

// FuzzNormalizeLanguage checks that normalizeLanguage never panics and always
// returns a non-empty, lowercase directory name free of path separators and
// spaces, for any language string the GitHub API might return.
func FuzzNormalizeLanguage(f *testing.F) {
	for _, seed := range []string{
		"", "Go", "C#", "C++", "Jupyter Notebook", "C/C++",
		"Objective-C++", "F*", "Visual Basic .NET", "  ", "/",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, lang string) {
		got := normalizeLanguage(lang)
		if got == "" {
			t.Fatalf("normalizeLanguage(%q) returned an empty directory name", lang)
		}
		if got != strings.ToLower(got) {
			t.Fatalf("normalizeLanguage(%q) = %q is not lowercase", lang, got)
		}
		if strings.ContainsAny(got, " /") {
			t.Fatalf("normalizeLanguage(%q) = %q contains a space or path separator", lang, got)
		}
	})
}

// FuzzEvaluateLayout checks that evaluateLayout handles arbitrary layout templates,
// repository visibilities, languages, and owner names without panicking.
func FuzzEvaluateLayout(f *testing.F) {
	f.Add("", "Public", "Go", "repo", "owner")
	f.Add("{{.Visibility}}/{{.Language}}/{{.Name}}", "Public", "Go", "repo", "owner")
	f.Add("{{.Owner}}/{{.Name}}", "Public", "Go", "repo", "owner")
	f.Add("../{{.Name}}", "Public", "Go", "repo", "owner")

	f.Fuzz(func(t *testing.T, layout, visibility, language, repoName, owner string) {
		if len(layout) > maxLayoutTemplateBytes || len(visibility) > 32 || len(language) > 128 || len(repoName) > 256 || len(owner) > 256 {
			t.Skip()
		}
		repo := github.Repo{
			Visibility: visibility,
			Language:   language,
			Name:       repoName,
		}
		_, _ = evaluateLayout(layout, repo, owner)
	})
}
