// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// buildSymbolWorkspace lays out n repositories that actually contain source,
// which the scan benchmarks' empty repositories do not.
//
// Symbol extraction is the most expensive thing this server does and its cost
// is driven by files, not repository directories, so a benchmark over empty
// repositories measures the walk and nothing else.
func buildSymbolWorkspace(tb testing.TB, repos, filesPerRepo int) string {
	tb.Helper()
	root := tb.TempDir()
	for i := 0; i < repos; i++ {
		repo := filepath.Join(root, "Public", "go", fmt.Sprintf("repo-%04d", i))
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o750); err != nil {
			tb.Fatal(err)
		}
		cfg := fmt.Sprintf("[remote \"origin\"]\n\turl = https://github.com/acme/repo-%04d.git\n", i)
		if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte(cfg), 0o600); err != nil {
			tb.Fatal(err)
		}
		for f := 0; f < filesPerRepo; f++ {
			var src string
			src += "package main\n\n"
			for d := 0; d < 10; d++ {
				src += fmt.Sprintf("type Type%d%d struct{ A int }\n", f, d)
				src += fmt.Sprintf("func Func%d%d() {}\n", f, d)
			}
			// One symbol every repository shares, so an unfiltered query
			// matches across the whole workspace — the shape that is slow.
			src += "func Shared() {}\n"
			name := filepath.Join(repo, fmt.Sprintf("file%02d.go", f))
			if err := os.WriteFile(name, []byte(src), 0o600); err != nil {
				tb.Fatal(err)
			}
		}
	}
	return root
}

// BenchmarkFindSymbolWarm measures the repeat call — the one an agent makes
// over and over in a session, and the one a cache exists to make cheap.
//
// The reported p95 for corral_find_symbol was 20.1s against a 234-repository
// workspace. The in-memory cache held 24 entries, so roughly 90% of the
// workspace missed on every call and was re-extracted; worse, each call evicted
// what the previous one had stored, so the cache never converged. Extraction
// walks and stats every source file before it can even consult the on-disk
// cache (the fingerprint is computed from that walk), so a miss is expensive
// whether or not the parse is avoided.
//
// Sized at 200 repositories to sit in the same range as the workspace that
// produced the report.
func BenchmarkFindSymbolWarm(b *testing.B) {
	root := buildSymbolWorkspace(b, 200, 4)
	srv, err := NewServer(ServerOptions{
		Root:           root,
		Version:        "bench",
		SymbolCacheDir: b.TempDir(),
	})
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	in := findSymbolInput{Name: "Shared", Limit: 10}

	// Warm both caches first: this measures the steady state, not the cold
	// start.
	if _, _, err := srv.handleFindSymbol(ctx, nil, in); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := srv.handleFindSymbol(ctx, nil, in); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFindSymbolCold measures the first call into a fresh server, with
// the on-disk cache already populated.
//
// This is what a client meets after a restart, and it is the half a bigger
// in-memory cache cannot help: every repository must be walked at least once.
func BenchmarkFindSymbolCold(b *testing.B) {
	root := buildSymbolWorkspace(b, 200, 4)
	diskDir := b.TempDir()
	ctx := context.Background()
	in := findSymbolInput{Name: "Shared", Limit: 10}

	// Populate the on-disk cache once, outside the measured loop.
	warm, err := NewServer(ServerOptions{Root: root, Version: "bench", SymbolCacheDir: diskDir})
	if err != nil {
		b.Fatal(err)
	}
	if _, _, err := warm.handleFindSymbol(ctx, nil, in); err != nil {
		b.Fatal(err)
	}

	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		srv, err := NewServer(ServerOptions{Root: root, Version: "bench", SymbolCacheDir: diskDir})
		if err != nil {
			b.Fatal(err)
		}
		b.StartTimer()
		if _, _, err := srv.handleFindSymbol(ctx, nil, in); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSearchCodeMiss measures the case that dominates a content search:
// repositories that contain no match and must be read in full to establish it.
//
// The query below matches nothing, so every repository is read and none of the
// early exits fire. That isolates the scan cost, which is the thing the
// reported 8.4s p95 is made of.
func BenchmarkSearchCodeMiss(b *testing.B) {
	root := buildSymbolWorkspace(b, 200, 4)
	srv, err := NewServer(ServerOptions{Root: root, Version: "bench", SymbolCacheDir: b.TempDir()})
	if err != nil {
		b.Fatal(err)
	}
	ctx := context.Background()
	in := searchCodeInput{Query: "zzz-no-such-token-zzz"}
	if _, _, err := srv.handleSearchCode(ctx, nil, in); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := srv.handleSearchCode(ctx, nil, in); err != nil {
			b.Fatal(err)
		}
	}
}
