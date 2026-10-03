// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/graph"
	corralmcp "github.com/sebastienrousseau/corralctl/internal/mcp"
)

func TestGraphCommandPreRunE(t *testing.T) {
	origOutput := graphOutput
	t.Cleanup(func() { graphOutput = origOutput })

	graphOutput = "text"
	if err := graphCmd.PreRunE(graphCmd, nil); err != nil {
		t.Fatalf("unexpected error for text: %v", err)
	}

	graphOutput = "json"
	if err := graphCmd.PreRunE(graphCmd, nil); err != nil {
		t.Fatalf("unexpected error for json: %v", err)
	}

	graphOutput = ""
	if err := graphCmd.PreRunE(graphCmd, nil); err != nil {
		t.Fatalf("unexpected error for empty: %v", err)
	}

	graphOutput = "mermaid"
	if err := graphCmd.PreRunE(graphCmd, nil); err != nil {
		t.Fatalf("unexpected error for mermaid: %v", err)
	}

	graphOutput = "dot"
	if err := graphCmd.PreRunE(graphCmd, nil); err != nil {
		t.Fatalf("unexpected error for dot: %v", err)
	}

	graphOutput = "yaml"
	if err := graphCmd.PreRunE(graphCmd, nil); err == nil {
		t.Fatal("expected error for yaml, got nil")
	}
}

func TestGraphCommandScanFailure(t *testing.T) {
	origScan := graphScan
	t.Cleanup(func() { graphScan = origScan })

	graphScan = func(string) (*corralmcp.Index, error) {
		return nil, errors.New("simulated scan error")
	}

	err := graphCmd.RunE(graphCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "simulated scan error") {
		t.Fatalf("expected simulated scan error, got %v", err)
	}
}

func TestGraphCommandEmptyWorkspace(t *testing.T) {
	root := t.TempDir()
	origScan, origRepo, origJSON, origOutput, origMermaid, origDOT := graphScan, graphRepo, graphJSON, graphOutput, graphMermaid, graphDOT
	t.Cleanup(func() {
		graphScan, graphRepo, graphJSON, graphOutput, graphMermaid, graphDOT = origScan, origRepo, origJSON, origOutput, origMermaid, origDOT
	})

	graphScan = func(string) (*corralmcp.Index, error) {
		return &corralmcp.Index{Root: root, Repos: []corralmcp.RepoEntry{}}, nil
	}
	graphRepo = ""
	graphJSON = false
	graphMermaid = false
	graphDOT = false
	graphOutput = "text"

	// Text mode
	out := captureStdout(t, func() {
		if err := graphCmd.RunE(graphCmd, []string{root}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "No repositories found in workspace.") {
		t.Fatalf("expected empty workspace message, got %q", out)
	}

	// JSON mode
	graphJSON = true
	out = captureStdout(t, func() {
		if err := graphCmd.RunE(graphCmd, []string{root}); err != nil {
			t.Fatal(err)
		}
	})
	var report WorkspaceGraphReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("failed to parse JSON report: %v\nOutput: %s", err, out)
	}
	if len(report.Nodes) != 0 {
		t.Fatalf("expected 0 nodes, got %d", len(report.Nodes))
	}

	// Mermaid mode
	graphJSON = false
	graphMermaid = true
	out = captureStdout(t, func() {
		if err := graphCmd.RunE(graphCmd, []string{root}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "flowchart TD") {
		t.Fatalf("expected flowchart TD, got %q", out)
	}

	// DOT mode
	graphMermaid = false
	graphDOT = true
	out = captureStdout(t, func() {
		if err := graphCmd.RunE(graphCmd, []string{root}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "digraph G") {
		t.Fatalf("expected digraph G, got %q", out)
	}
}

func setupGraphWorkspace(t *testing.T) (string, *corralmcp.Index) {
	t.Helper()
	root := t.TempDir()

	// Repo A (Go) depends on Repo B
	repoADir := filepath.Join(root, "Public", "Go", "repo-a")
	if err := os.MkdirAll(repoADir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoADir, "go.mod"), []byte("module github.com/acme/repo-a\n\nrequire github.com/acme/repo-b v1.0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Repo B (Go)
	repoBDir := filepath.Join(root, "Public", "Go", "repo-b")
	if err := os.MkdirAll(repoBDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoBDir, "go.mod"), []byte("module github.com/acme/repo-b\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Repo C (Standalone Rust)
	repoCDir := filepath.Join(root, "Public", "Rust", "repo-c")
	if err := os.MkdirAll(repoCDir, 0o750); err != nil {
		t.Fatal(err)
	}

	repos := []corralmcp.RepoEntry{
		{
			Name:      "repo-a",
			RelPath:   "Public/Go/repo-a",
			Path:      repoADir,
			Language:  "Go",
			RemoteURL: "https://github.com/acme/repo-a.git",
		},
		{
			Name:      "repo-b",
			RelPath:   "Public/Go/repo-b",
			Path:      repoBDir,
			Language:  "Go",
			RemoteURL: "https://github.com/acme/repo-b.git",
		},
		{
			Name:      "repo-c",
			RelPath:   "Public/Rust",
			Path:      repoCDir,
			Language:  "Rust",
			RemoteURL: "https://github.com/acme/repo-c.git",
		},
	}

	idx := &corralmcp.Index{
		Root:  root,
		Repos: repos,
	}
	return root, idx
}

func TestGraphCommandWorkspaceView(t *testing.T) {
	root, idx := setupGraphWorkspace(t)
	origScan, origRepo, origJSON, origOutput, origMermaid, origDOT := graphScan, graphRepo, graphJSON, graphOutput, graphMermaid, graphDOT
	t.Cleanup(func() {
		graphScan, graphRepo, graphJSON, graphOutput, graphMermaid, graphDOT = origScan, origRepo, origJSON, origOutput, origMermaid, origDOT
	})

	graphScan = func(string) (*corralmcp.Index, error) { return idx, nil }
	graphRepo = ""
	graphJSON = false
	graphMermaid = false
	graphDOT = false
	graphOutput = "text"

	// 1. Text mode
	out := captureStdout(t, func() {
		if err := graphCmd.RunE(graphCmd, []string{root}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "Repositories (3):") {
		t.Fatalf("expected repository header, got: %s", out)
	}
	if !strings.Contains(out, "repo-a") || !strings.Contains(out, "repo-b") {
		t.Fatalf("expected repo-a and repo-b in output, got: %s", out)
	}
	if !strings.Contains(out, "Topological Build Order") {
		t.Fatalf("expected build order, got: %s", out)
	}

	// 2. JSON mode via --output json
	graphOutput = "json"
	out = captureStdout(t, func() {
		if err := graphCmd.RunE(graphCmd, nil); err != nil {
			t.Fatal(err)
		}
	})
	var report WorkspaceGraphReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("failed to decode JSON report: %v\nOutput: %s", err, out)
	}
	if len(report.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(report.Nodes))
	}
	if len(report.Edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(report.Edges))
	}

	// 3. Mermaid mode via --output mermaid
	graphOutput = "mermaid"
	out = captureStdout(t, func() {
		if err := graphCmd.RunE(graphCmd, nil); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "flowchart TD") || !strings.Contains(out, "repo_a") {
		t.Fatalf("expected mermaid flowchart, got: %s", out)
	}

	// 4. Mermaid mode via --mermaid flag
	graphOutput = ""
	graphMermaid = true
	out = captureStdout(t, func() {
		if err := graphCmd.RunE(graphCmd, nil); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "flowchart TD") || !strings.Contains(out, "repo_a") {
		t.Fatalf("expected mermaid flowchart via flag, got: %s", out)
	}
	graphMermaid = false

	// 5. DOT mode via --output dot
	graphOutput = "dot"
	out = captureStdout(t, func() {
		if err := graphCmd.RunE(graphCmd, nil); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "digraph G") || !strings.Contains(out, `"repo-a" -> "repo-b"`) {
		t.Fatalf("expected dot graph, got: %s", out)
	}

	// 6. DOT mode via --dot flag
	graphOutput = ""
	graphDOT = true
	out = captureStdout(t, func() {
		if err := graphCmd.RunE(graphCmd, nil); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "digraph G") || !strings.Contains(out, `"repo-a" -> "repo-b"`) {
		t.Fatalf("expected dot graph via flag, got: %s", out)
	}
	graphDOT = false
}

func TestGraphCommandRepoFilter(t *testing.T) {
	root, idx := setupGraphWorkspace(t)
	origScan, origRepo, origJSON, origOutput, origMermaid, origDOT := graphScan, graphRepo, graphJSON, graphOutput, graphMermaid, graphDOT
	t.Cleanup(func() {
		graphScan, graphRepo, graphJSON, graphOutput, graphMermaid, graphDOT = origScan, origRepo, origJSON, origOutput, origMermaid, origDOT
	})

	graphScan = func(string) (*corralmcp.Index, error) { return idx, nil }
	graphMermaid = false
	graphDOT = false

	// 1. Filter to repo-a (has dependency on repo-b, no dependents)
	graphRepo = "repo-a"
	graphJSON = false
	graphOutput = "text"
	out := captureStdout(t, func() {
		if err := graphCmd.RunE(graphCmd, []string{root}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "Repository:   repo-a") || !strings.Contains(out, "Dependencies (1):") {
		t.Fatalf("unexpected repo-a output: %s", out)
	}
	if !strings.Contains(out, "Dependents:   none") {
		t.Fatalf("expected Dependents: none for repo-a, got: %s", out)
	}

	// 2. Filter to repo-b (has no dependencies, has dependent repo-a)
	graphRepo = "repo-b"
	out = captureStdout(t, func() {
		if err := graphCmd.RunE(graphCmd, []string{root}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "Repository:   repo-b") || !strings.Contains(out, "Dependencies: none") {
		t.Fatalf("unexpected repo-b output: %s", out)
	}
	if !strings.Contains(out, "Dependents (1):") {
		t.Fatalf("expected Dependents (1): for repo-b, got: %s", out)
	}

	// 3. Filter to repo-a in JSON mode via --output json
	graphRepo = "repo-a"
	graphJSON = false
	graphOutput = "json"
	out = captureStdout(t, func() {
		if err := graphCmd.RunE(graphCmd, []string{root}); err != nil {
			t.Fatal(err)
		}
	})
	var detail RepoGraphDetail
	if err := json.Unmarshal([]byte(out), &detail); err != nil {
		t.Fatalf("failed to decode detail JSON: %v\nOutput: %s", err, out)
	}
	if detail.TargetRepo != "repo-a" || len(detail.Dependencies) != 1 || detail.Dependencies[0] != "repo-b" {
		t.Fatalf("unexpected detail: %+v", detail)
	}

	// 4. Filter to repo-a in Mermaid mode via --mermaid flag
	graphRepo = "repo-a"
	graphJSON = false
	graphMermaid = true
	graphOutput = ""
	out = captureStdout(t, func() {
		if err := graphCmd.RunE(graphCmd, []string{root}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "flowchart TD") || !strings.Contains(out, "repo_a") {
		t.Fatalf("expected targeted mermaid, got: %s", out)
	}
	graphMermaid = false

	// 5. Filter to repo-a in Mermaid mode via --output mermaid
	graphOutput = "mermaid"
	out = captureStdout(t, func() {
		if err := graphCmd.RunE(graphCmd, []string{root}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "flowchart TD") || !strings.Contains(out, "repo_a") {
		t.Fatalf("expected targeted mermaid via --output, got: %s", out)
	}

	// 6. Filter to repo-a in DOT mode via --dot flag
	graphOutput = ""
	graphDOT = true
	out = captureStdout(t, func() {
		if err := graphCmd.RunE(graphCmd, []string{root}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "digraph G") || !strings.Contains(out, `"repo-a" [style=filled`) {
		t.Fatalf("expected targeted dot, got: %s", out)
	}
	graphDOT = false

	// 7. Filter to repo-a in DOT mode via --output dot
	graphOutput = "dot"
	out = captureStdout(t, func() {
		if err := graphCmd.RunE(graphCmd, []string{root}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "digraph G") || !strings.Contains(out, `"repo-a" [style=filled`) {
		t.Fatalf("expected targeted dot via --output, got: %s", out)
	}
	graphOutput = ""

	// 8. Non-existent repo returns error
	graphRepo = "non-existent"
	if err := graphCmd.RunE(graphCmd, []string{root}); err == nil {
		t.Fatal("expected error for non-existent repo, got nil")
	}
}

func TestGraphCommandCycleAndMissingNode(t *testing.T) {
	root := t.TempDir()
	origScan, origRepo, origJSON, origOutput, origMermaid, origDOT := graphScan, graphRepo, graphJSON, graphOutput, graphMermaid, graphDOT
	t.Cleanup(func() {
		graphScan, graphRepo, graphJSON, graphOutput, graphMermaid, graphDOT = origScan, origRepo, origJSON, origOutput, origMermaid, origDOT
	})

	graphRepo = ""
	graphJSON = false
	graphMermaid = false
	graphDOT = false
	graphOutput = "text"

	// Workspace with circular dependency
	repoXDir := filepath.Join(root, "repo-x")
	repoYDir := filepath.Join(root, "repo-y")
	_ = os.MkdirAll(repoXDir, 0o750)
	_ = os.MkdirAll(repoYDir, 0o750)
	_ = os.WriteFile(filepath.Join(repoXDir, "package.json"), []byte(`{"name":"x","dependencies":{"y":"*"}}`), 0o600)
	_ = os.WriteFile(filepath.Join(repoYDir, "package.json"), []byte(`{"name":"y","dependencies":{"x":"*"}}`), 0o600)

	idx := &corralmcp.Index{
		Root: root,
		Repos: []corralmcp.RepoEntry{
			{Name: "repo-x", RelPath: "repo-x", Path: repoXDir, Language: "JavaScript"},
			{Name: "repo-y", RelPath: "repo-y", Path: repoYDir, Language: "JavaScript"},
		},
	}
	graphScan = func(string) (*corralmcp.Index, error) { return idx, nil }
	graphRepo = ""
	graphJSON = false
	graphOutput = "text"

	out := captureStdout(t, func() {
		if err := graphCmd.RunE(graphCmd, []string{root}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "Cycles Detected") {
		t.Fatalf("expected cycle warning, got: %s", out)
	}

	// Test missing node error path:
	// If idx.Find returns a match whose name does not appear in g.Nodes
	weirdIdx := &corralmcp.Index{
		Root: root,
		Repos: []corralmcp.RepoEntry{
			{Name: "ghost", RelPath: "ghost", Path: "/nonexistent"},
		},
	}
	g := &graph.Graph{Nodes: []graph.Node{}}
	if err := runGraphWith(weirdIdx, "ghost", "text", g); err == nil {
		t.Fatal("expected error when node is missing from graph")
	}

	// Test nil dependencies in node
	nilDepsIdx := &corralmcp.Index{
		Root: root,
		Repos: []corralmcp.RepoEntry{
			{Name: "nildeps", RelPath: "nildeps", Path: "/nildeps"},
		},
	}
	gNil := &graph.Graph{
		Nodes: []graph.Node{{Name: "nildeps", Path: "/nildeps", Dependencies: nil}},
	}
	if err := runGraphWith(nilDepsIdx, "nildeps", "json", gNil); err != nil {
		t.Fatalf("unexpected error with nil dependencies: %v", err)
	}
}
