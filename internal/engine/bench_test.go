// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/github"
)

// BenchmarkEvaluateLayout measures the per-repository path computation. It
// runs once for every repository in a run, so its cost is multiplied by the
// size of the user's account.
func BenchmarkEvaluateLayout(b *testing.B) {
	tmpl, err := parseLayoutTemplate("{{.Collection}}/{{.Bucket}}/{{.Name}}")
	if err != nil {
		b.Fatal(err)
	}
	repo := github.Repo{
		Name: "corral", Owner: "acme", FullName: "acme/corral",
		Language: "Go", Visibility: "Public",
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := executeLayout(tmpl, repo, "acme"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkNormalizeLanguage measures the language-to-directory mapping,
// called at least twice per repository per run.
func BenchmarkNormalizeLanguage(b *testing.B) {
	languages := []string{"Go", "C++", "Jupyter Notebook", "Objective-C++", "", "Visual Basic .NET"}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		_ = normalizeLanguage(languages[i%len(languages)])
		i++
	}
}

// BenchmarkDiscoverExistingRepos measures the identity walk that runs once
// per invocation before any clone is scheduled, and which grows with the
// number of repositories already on disk.
func BenchmarkDiscoverExistingRepos(b *testing.B) {
	base := b.TempDir()
	for i := 0; i < 500; i++ {
		repo := filepath.Join(base, "Public", "go", fmt.Sprintf("repo-%04d", i))
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o750); err != nil {
			b.Fatal(err)
		}
		cfg := fmt.Sprintf("[remote \"origin\"]\n\turl = https://github.com/acme/repo-%04d.git\n", i)
		if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte(cfg), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	for b.Loop() {
		if got := discoverExistingRepos(base); len(got) == 0 {
			b.Fatal("discovery found nothing")
		}
	}
}
