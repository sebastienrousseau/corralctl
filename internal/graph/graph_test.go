// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package graph

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBuildGraphGoDependencies(t *testing.T) {
	tmp := t.TempDir()

	repoA := filepath.Join(tmp, "repoA")
	if err := os.MkdirAll(repoA, 0o750); err != nil {
		t.Fatal(err)
	}
	goModA := `module github.com/example/repoA

go 1.22

require (
	github.com/example/repoB v1.0.0
	golang.org/x/sync v0.1.0 // external
)
`
	if err := os.WriteFile(filepath.Join(repoA, "go.mod"), []byte(goModA), 0o600); err != nil {
		t.Fatal(err)
	}

	repoB := filepath.Join(tmp, "repoB")
	if err := os.MkdirAll(repoB, 0o750); err != nil {
		t.Fatal(err)
	}
	goModB := `module github.com/example/repoB

go 1.22
`
	if err := os.WriteFile(filepath.Join(repoB, "go.mod"), []byte(goModB), 0o600); err != nil {
		t.Fatal(err)
	}

	repos := []RepoInput{
		{Name: "repoA", Path: repoA, Language: "go", RemoteURL: "https://github.com/example/repoA.git"},
		{Name: "repoB", Path: repoB, Language: "go", RemoteURL: "git@github.com:example/repoB.git"},
	}

	g := BuildGraph(repos)

	if len(g.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(g.Nodes))
	}
	if len(g.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(g.Edges))
	}
	if g.Edges[0].From != "repoA" || g.Edges[0].To != "repoB" {
		t.Errorf("expected edge repoA -> repoB, got %+v", g.Edges[0])
	}
	if len(g.TopologicalOrder) != 2 {
		t.Fatalf("expected 2 in topological order, got %d", len(g.TopologicalOrder))
	}
	if g.TopologicalOrder[0] != "repoB" || g.TopologicalOrder[1] != "repoA" {
		t.Errorf("expected topological order [repoB, repoA], got %+v", g.TopologicalOrder)
	}
	if len(g.Dependents["repoB"]) != 1 || g.Dependents["repoB"][0] != "repoA" {
		t.Errorf("expected repoB dependents to contain repoA, got %+v", g.Dependents["repoB"])
	}
}

func TestBuildGraphPolyglotManifests(t *testing.T) {
	tmp := t.TempDir()

	// Rust repo
	rustDir := filepath.Join(tmp, "rust-crate")
	if err := os.MkdirAll(rustDir, 0o750); err != nil {
		t.Fatal(err)
	}
	cargoToml := `
# A comment
[package]
name = "rust-crate"
version = "0.1.0"

[dependencies]
node-pkg = { path = "../node-pkg" }
`
	if err := os.WriteFile(filepath.Join(rustDir, "Cargo.toml"), []byte(cargoToml), 0o600); err != nil {
		t.Fatal(err)
	}

	// Node repo
	nodeDir := filepath.Join(tmp, "node-pkg")
	if err := os.MkdirAll(nodeDir, 0o750); err != nil {
		t.Fatal(err)
	}
	packageJSON := `{
  "name": "node-pkg",
  "version": "1.0.0",
  "dependencies": {
    "py-service": "^1.0.0"
  },
  "devDependencies": {
    "typescript": "^5.0.0"
  }
}`
	if err := os.WriteFile(filepath.Join(nodeDir, "package.json"), []byte(packageJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	// Python repo
	pyDir := filepath.Join(tmp, "py-service")
	if err := os.MkdirAll(pyDir, 0o750); err != nil {
		t.Fatal(err)
	}
	pyproject := `
[project]
name = "py-service"
version = "0.1.0"

[tool.poetry.dependencies]
python = "^3.11"
`
	if err := os.WriteFile(filepath.Join(pyDir, "pyproject.toml"), []byte(pyproject), 0o600); err != nil {
		t.Fatal(err)
	}

	// Unknown language / empty repo
	otherDir := filepath.Join(tmp, "other")
	if err := os.MkdirAll(otherDir, 0o750); err != nil {
		t.Fatal(err)
	}

	repos := []RepoInput{
		{Name: "rust-crate", Path: rustDir, Language: "rust"},
		{Name: "node-pkg", Path: nodeDir, Language: "typescript"},
		{Name: "py-service", Path: pyDir, Language: "python"},
		{Name: "other", Path: otherDir, Language: "markdown"},
	}

	g := BuildGraph(repos)

	if len(g.Nodes) != 4 {
		t.Fatalf("expected 4 nodes, got %d", len(g.Nodes))
	}
	if len(g.Edges) != 2 {
		t.Fatalf("expected 2 edges, got %d (%+v)", len(g.Edges), g.Edges)
	}
	// rust-crate -> node-pkg -> py-service
	// Build order: py-service, node-pkg, rust-crate (and other)
	foundPyFirst := false
	for _, name := range g.TopologicalOrder {
		if name == "py-service" {
			foundPyFirst = true
			break
		}
		if name == "rust-crate" {
			t.Errorf("rust-crate appeared before py-service in build order")
		}
	}
	if !foundPyFirst {
		t.Errorf("py-service missing from topological order")
	}
}

func TestBuildGraphCycleDetection(t *testing.T) {
	tmp := t.TempDir()

	repoA := filepath.Join(tmp, "cycleA")
	_ = os.MkdirAll(repoA, 0o750)
	_ = os.WriteFile(filepath.Join(repoA, "go.mod"), []byte("module cycleA\nrequire cycleB v1.0.0\n"), 0o600)

	repoB := filepath.Join(tmp, "cycleB")
	_ = os.MkdirAll(repoB, 0o750)
	_ = os.WriteFile(filepath.Join(repoB, "go.mod"), []byte("module cycleB\nrequire cycleA v1.0.0\n"), 0o600)

	repos := []RepoInput{
		{Name: "cycleA", Path: repoA, Language: "go"},
		{Name: "cycleB", Path: repoB, Language: "go"},
	}

	g := BuildGraph(repos)

	if len(g.Cycles) == 0 {
		t.Fatalf("expected cycles to be detected")
	}
	if len(g.Cycles[0]) != 2 {
		t.Errorf("expected 2 nodes in cycle, got %+v", g.Cycles[0])
	}
}

func TestManifestParsingEdges(t *testing.T) {
	tmp := t.TempDir()

	// Missing files should return empty gracefully
	name, deps := parseGoMod(filepath.Join(tmp, "missing.mod"))
	if name != "" || len(deps) != 0 {
		t.Errorf("expected empty for missing go.mod")
	}

	name, deps = parsePackageJSON(filepath.Join(tmp, "missing.json"))
	if name != "" || len(deps) != 0 {
		t.Errorf("expected empty for missing package.json")
	}

	name, deps = parseCargoTOML(filepath.Join(tmp, "missing.toml"))
	if name != "" || len(deps) != 0 {
		t.Errorf("expected empty for missing Cargo.toml")
	}

	name, deps = parsePyprojectTOML(filepath.Join(tmp, "missing.toml"))
	if name != "" || len(deps) != 0 {
		t.Errorf("expected empty for missing pyproject.toml")
	}

	// Invalid JSON
	badJSON := filepath.Join(tmp, "bad.json")
	_ = os.WriteFile(badJSON, []byte("{not json"), 0o600)
	name, deps = parsePackageJSON(badJSON)
	if name != "" || len(deps) != 0 {
		t.Errorf("expected empty for invalid package.json")
	}

	// Single line require in go.mod
	singleLineGoMod := filepath.Join(tmp, "single.mod")
	_ = os.WriteFile(singleLineGoMod, []byte("module mymod\nrequire othermod v1.0.0\n"), 0o600)
	name, deps = parseGoMod(singleLineGoMod)
	if name != "mymod" || len(deps) != 1 || deps[0] != "othermod" {
		t.Errorf("single line go.mod parse failed: name=%q, deps=%+v", name, deps)
	}

	// Test resolveDep branches: direct name match, base name match, and poetry non-python dep
	poetryDir := filepath.Join(tmp, "poetry-app")
	_ = os.MkdirAll(poetryDir, 0o750)
	_ = os.WriteFile(filepath.Join(poetryDir, "pyproject.toml"), []byte(`
[tool.poetry]
name = "poetry-app"

[tool.poetry.dependencies]
python = "^3.11"
lib-a = "^1.0"
lib-b = "^2.0"
`), 0o600)

	libADir := filepath.Join(tmp, "lib-a")
	_ = os.MkdirAll(libADir, 0o750)
	libBDir := filepath.Join(tmp, "lib-b")
	_ = os.MkdirAll(libBDir, 0o750)

	g := BuildGraph([]RepoInput{
		{Name: "poetry-app", Path: poetryDir, Language: "python"},
		{Name: "lib-a", Path: libADir, Language: "unknown"},
		{Name: "lib-b", Path: libBDir, Language: "unknown"},
	})

	if len(g.Edges) != 2 {
		t.Errorf("expected 2 edges for multiple deps from same repo, got %d", len(g.Edges))
	}

	// Test filepath.Base fallback matching in resolveDep
	baseMatch := resolveDep("github.com/org/custom-lib", map[string]string{}, map[string]string{"custom-lib": "repo-custom"})
	if baseMatch != "repo-custom" {
		t.Errorf("expected base match 'repo-custom', got %q", baseMatch)
	}
}

