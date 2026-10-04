// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package discover

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkShouldPrune measures the lookup cost for pruned directories.
// It is evaluated for every visited directory during discovery.
func BenchmarkShouldPrune(b *testing.B) {
	names := []string{"node_modules", "vendor", "target", ".venv", ".cache", "src", "cmd", "internal"}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		_ = shouldPrune(names[i%len(names)])
		i++
	}
}

// BenchmarkDetectLanguage measures heuristic language detection for a repository root.
func BenchmarkDetectLanguage(b *testing.B) {
	dir := b.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/test\n\ngo 1.24\n"), 0o600); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	for b.Loop() {
		_ = detectLanguage(dir)
	}
}

// BenchmarkInspectCandidate measures candidate inspection and git config resolution.
func BenchmarkInspectCandidate(b *testing.B) {
	dir := b.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o750); err != nil {
		b.Fatal(err)
	}
	cfg := "[remote \"origin\"]\n\turl = https://github.com/org/repo.git\n"
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(cfg), 0o600); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o600); err != nil {
		b.Fatal(err)
	}

	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		_, _ = inspectCandidate(ctx, dir)
	}
}

// BenchmarkDiscoverPruning measures discovery throughput when traversing directories
// containing deeply nested ignored boundary paths like node_modules and vendor.
func BenchmarkDiscoverPruning(b *testing.B) {
	base := b.TempDir()
	for i := 0; i < 20; i++ {
		repo := filepath.Join(base, fmt.Sprintf("repo-%02d", i))
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o750); err != nil {
			b.Fatal(err)
		}
		// Deep vendor tree that must be pruned immediately without descending
		vendor := filepath.Join(repo, "node_modules", "pkg-a", "node_modules", "pkg-b")
		if err := os.MkdirAll(vendor, 0o750); err != nil {
			b.Fatal(err)
		}
	}

	ctx := context.Background()
	opts := Options{BaseDir: base, MaxDepth: 8}
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		_, err := Discover(ctx, opts)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDiscoverLargeWorkspaces measures discovery performance across a broad workspace.
func BenchmarkDiscoverLargeWorkspaces(b *testing.B) {
	base := b.TempDir()
	for i := 0; i < 200; i++ {
		group := fmt.Sprintf("group-%02d", i%10)
		repo := filepath.Join(base, group, fmt.Sprintf("repo-%03d", i))
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o750); err != nil {
			b.Fatal(err)
		}
	}

	ctx := context.Background()
	opts := Options{BaseDir: base, MaxDepth: 4}
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		candidates, err := Discover(ctx, opts)
		if err != nil {
			b.Fatal(err)
		}
		if len(candidates) != 200 {
			b.Fatalf("expected 200 candidates, got %d", len(candidates))
		}
	}
}
