// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestHandleGraphDependencies(t *testing.T) {
	base := t.TempDir()
	repoA := makeFakeRepo(t, base, "Public", "go", "repoA", "", "")
	_ = os.WriteFile(filepath.Join(repoA, "go.mod"), []byte("module repoA\nrequire repoB v1.0.0\n"), 0o600)

	repoB := makeFakeRepo(t, base, "Public", "go", "repoB", "", "")
	_ = os.WriteFile(filepath.Join(repoB, "go.mod"), []byte("module repoB\n"), 0o600)

	srv, err := NewServer(ServerOptions{Root: base})
	if err != nil {
		t.Fatal(err)
	}

	// 1. Whole workspace graph
	res, out, err := srv.handleGraphDependencies(context.Background(), nil, graphDependenciesInput{})
	if err != nil {
		t.Fatalf("handleGraphDependencies failed: %v", err)
	}
	if res.IsError {
		t.Fatalf("expected successful result, got error: %+v", res)
	}
	if len(out.Nodes) != 2 {
		t.Errorf("expected 2 nodes, got %d", len(out.Nodes))
	}
	if len(out.Edges) != 1 {
		t.Errorf("expected 1 edge, got %d", len(out.Edges))
	}

	// 2. Focused on target repo that exists
	resTarget, outTarget, err := srv.handleGraphDependencies(context.Background(), nil, graphDependenciesInput{Repo: "repoA"})
	if err != nil {
		t.Fatalf("handleGraphDependencies target failed: %v", err)
	}
	if resTarget.IsError {
		t.Fatalf("unexpected tool error: %+v", resTarget)
	}
	if outTarget.TargetRepo != "repoA" {
		t.Errorf("expected target repoA, got %q", outTarget.TargetRepo)
	}
	if len(outTarget.Dependencies) != 1 || outTarget.Dependencies[0] != "repoB" {
		t.Errorf("expected dependencies [repoB], got %+v", outTarget.Dependencies)
	}

	// 3. Focused on non-existent repo returns tool error
	resMissing, _, err := srv.handleGraphDependencies(context.Background(), nil, graphDependenciesInput{Repo: "missing-repo"})
	if err != nil {
		t.Fatalf("expected nil error on missing repo, got: %v", err)
	}
	if !resMissing.IsError {
		t.Errorf("expected tool error for missing repo")
	}

	// 4. Scan error path
	oldScan := scanWorkspace
	scanWorkspace = func(string) (*Index, error) {
		return nil, errors.New("simulated scan error")
	}
	defer func() { scanWorkspace = oldScan }()

	srv.invalidateScanCache()
	_, _, err = srv.handleGraphDependencies(context.Background(), nil, graphDependenciesInput{})
	if err == nil {
		t.Errorf("expected scan error to propagate, got nil")
	}
}
