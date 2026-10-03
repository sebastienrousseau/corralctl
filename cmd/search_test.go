// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	corralmcp "github.com/sebastienrousseau/corralctl/internal/mcp"
	"github.com/sebastienrousseau/corralctl/internal/search"
)

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() {
		os.Stderr = orig
	}()

	outC := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		_ = r.Close()
		outC <- buf.String()
	}()

	fn()
	_ = w.Close()
	return <-outC
}

func resetSearchFlags() {
	searchRepoFlag = ""
	searchLangFlag = ""
	searchRegex = false
	searchCaseSensitive = false
	searchIncludeTests = false
	searchMax = 50
	searchPathGlob = ""
	searchOutput = "text"
	searchJSON = false
}

func TestSearchCommandPreRunE(t *testing.T) {
	origOutput, origMax := searchOutput, searchMax
	t.Cleanup(func() {
		searchOutput, searchMax = origOutput, origMax
	})

	searchOutput = "text"
	searchMax = 50
	if err := searchCmd.PreRunE(searchCmd, nil); err != nil {
		t.Fatalf("unexpected error for text: %v", err)
	}

	searchOutput = "json"
	if err := searchCmd.PreRunE(searchCmd, nil); err != nil {
		t.Fatalf("unexpected error for json: %v", err)
	}

	searchOutput = ""
	if err := searchCmd.PreRunE(searchCmd, nil); err != nil {
		t.Fatalf("unexpected error for empty: %v", err)
	}

	searchOutput = "yaml"
	if err := searchCmd.PreRunE(searchCmd, nil); err == nil {
		t.Fatal("expected error for yaml, got nil")
	}

	searchOutput = "text"
	searchMax = 0
	if err := searchCmd.PreRunE(searchCmd, nil); err == nil {
		t.Fatal("expected error for max 0, got nil")
	}

	searchMax = -5
	if err := searchCmd.PreRunE(searchCmd, nil); err == nil {
		t.Fatal("expected error for max -5, got nil")
	}
}

func TestSearchCommandScanError(t *testing.T) {
	origScan := searchScan
	t.Cleanup(func() { searchScan = origScan })

	resetSearchFlags()
	searchScan = func(string) (*corralmcp.Index, error) {
		return nil, errors.New("simulated scan error")
	}

	err := searchCmd.RunE(searchCmd, []string{"pattern"})
	if err == nil || !strings.Contains(err.Error(), "simulated scan error") {
		t.Fatalf("expected simulated scan error, got %v", err)
	}
}

func TestSearchCommandEmptyWorkspace(t *testing.T) {
	root := t.TempDir()
	origScan := searchScan
	t.Cleanup(func() { searchScan = origScan })

	resetSearchFlags()
	searchScan = func(string) (*corralmcp.Index, error) {
		return &corralmcp.Index{Root: root, Repos: []corralmcp.RepoEntry{}}, nil
	}

	// Text mode
	out := captureStdout(t, func() {
		if err := searchCmd.RunE(searchCmd, []string{"pattern", root}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "No repositories found to search.") {
		t.Fatalf("expected empty repositories message, got %q", out)
	}

	// JSON mode
	searchJSON = true
	out = captureStdout(t, func() {
		if err := searchCmd.RunE(searchCmd, []string{"pattern", root}); err != nil {
			t.Fatal(err)
		}
	})
	var report SearchResultReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}
	if len(report.Hits) != 0 || report.TotalHits != 0 {
		t.Fatalf("expected 0 hits, got %+v", report)
	}
}

func setupSearchWorkspace(t *testing.T) (string, *corralmcp.Index) {
	t.Helper()
	root := t.TempDir()

	repoADir := filepath.Join(root, "repo-a")
	if err := os.MkdirAll(repoADir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoADir, "main.go"), []byte("package main\n\nconst TargetConstant = \"Hello Corral\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoADir, "main_test.go"), []byte("package main\n\nfunc TestTarget() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	repoBDir := filepath.Join(root, "repo-b")
	if err := os.MkdirAll(repoBDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoBDir, "app.go"), []byte("package app\n\nvar TargetConfig = \"Corral App\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	repoCDir := filepath.Join(root, "repo-c")
	if err := os.MkdirAll(repoCDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoCDir, "lib.rs"), []byte("pub const TARGET_RUST: &str = \"Corral Rust\";\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	idx := &corralmcp.Index{
		Root: root,
		Repos: []corralmcp.RepoEntry{
			{Name: "repo-a", RelPath: "repo-a", Path: repoADir, Language: "Go"},
			{Name: "repo-b", RelPath: "repo-b", Path: repoBDir, Language: "Go"},
			{Name: "repo-c", RelPath: "repo-c", Path: repoCDir, Language: "Rust"},
		},
	}
	return root, idx
}

func TestSearchCommandBasicAndFormats(t *testing.T) {
	root, idx := setupSearchWorkspace(t)
	origScan := searchScan
	t.Cleanup(func() { searchScan = origScan })

	resetSearchFlags()
	searchScan = func(string) (*corralmcp.Index, error) { return idx, nil }

	// 1. Text mode match
	out := captureStdout(t, func() {
		if err := searchCmd.RunE(searchCmd, []string{"TargetConstant", root}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "repo-a/main.go:3:") || !strings.Contains(out, "TargetConstant") {
		t.Fatalf("unexpected text output: %s", out)
	}

	// 2. JSON mode via --output json
	resetSearchFlags()
	searchOutput = "json"
	out = captureStdout(t, func() {
		if err := searchCmd.RunE(searchCmd, []string{"TargetConstant", root}); err != nil {
			t.Fatal(err)
		}
	})
	var report SearchResultReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("failed to decode JSON: %v\nOutput: %s", err, out)
	}
	if report.TotalHits != 1 || len(report.Hits) != 1 || report.Hits[0].Repo != "repo-a" {
		t.Fatalf("unexpected JSON report: %+v", report)
	}

	// 3. No matches found
	resetSearchFlags()
	out = captureStdout(t, func() {
		if err := searchCmd.RunE(searchCmd, []string{"NonExistentKeyword", root}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "No matches found.") {
		t.Fatalf("expected No matches found., got: %s", out)
	}
}

func TestSearchCommandFilters(t *testing.T) {
	root, idx := setupSearchWorkspace(t)
	origScan := searchScan
	t.Cleanup(func() { searchScan = origScan })

	resetSearchFlags()
	searchScan = func(string) (*corralmcp.Index, error) { return idx, nil }

	// 1. Filter by repo
	resetSearchFlags()
	searchRepoFlag = "repo-b"
	out := captureStdout(t, func() {
		if err := searchCmd.RunE(searchCmd, []string{"Corral", root}); err != nil {
			t.Fatal(err)
		}
	})
	if strings.Contains(out, "repo-a") || !strings.Contains(out, "repo-b/app.go:") {
		t.Fatalf("expected only repo-b matches, got: %s", out)
	}

	// 2. Filter by nonexistent repo
	resetSearchFlags()
	searchRepoFlag = "ghost-repo"
	if err := searchCmd.RunE(searchCmd, []string{"Corral", root}); err == nil {
		t.Fatal("expected error for nonexistent repo, got nil")
	}

	// 3. Filter by language
	resetSearchFlags()
	searchLangFlag = "rust"
	out = captureStdout(t, func() {
		if err := searchCmd.RunE(searchCmd, []string{"Corral", root}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "repo-c/lib.rs:") || strings.Contains(out, "repo-a") {
		t.Fatalf("expected only repo-c match for rust, got: %s", out)
	}

	// 4. Filter by nonexistent language
	resetSearchFlags()
	searchLangFlag = "python"
	if err := searchCmd.RunE(searchCmd, []string{"Corral", root}); err == nil {
		t.Fatal("expected error for nonexistent language, got nil")
	}
}

func TestSearchCommandFlags(t *testing.T) {
	root, idx := setupSearchWorkspace(t)
	origScan := searchScan
	t.Cleanup(func() { searchScan = origScan })

	resetSearchFlags()
	searchScan = func(string) (*corralmcp.Index, error) { return idx, nil }

	// 1. Regex search
	resetSearchFlags()
	searchRegex = true
	out := captureStdout(t, func() {
		if err := searchCmd.RunE(searchCmd, []string{"Target.*Constant", root}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "repo-a/main.go:") {
		t.Fatalf("expected regex match, got: %s", out)
	}

	// 2. Invalid regex pattern
	resetSearchFlags()
	searchRegex = true
	if err := searchCmd.RunE(searchCmd, []string{"[unclosed", root}); err == nil {
		t.Fatal("expected compile error for unclosed regex, got nil")
	}

	// 3. Case sensitive
	resetSearchFlags()
	searchCaseSensitive = true
	out = captureStdout(t, func() {
		if err := searchCmd.RunE(searchCmd, []string{"TARGET_RUST", root}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "repo-c/lib.rs:") {
		t.Fatalf("expected case-sensitive match, got: %s", out)
	}

	// Case sensitive mismatch
	out = captureStdout(t, func() {
		if err := searchCmd.RunE(searchCmd, []string{"target_rust", root}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "No matches found.") {
		t.Fatalf("expected no match for mismatched case, got: %s", out)
	}

	// 4. Test files inclusion
	resetSearchFlags()
	searchIncludeTests = false
	out = captureStdout(t, func() {
		if err := searchCmd.RunE(searchCmd, []string{"TestTarget", root}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "No matches found.") {
		t.Fatalf("expected test file excluded by default, got: %s", out)
	}

	searchIncludeTests = true
	out = captureStdout(t, func() {
		if err := searchCmd.RunE(searchCmd, []string{"TestTarget", root}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "repo-a/main_test.go:") {
		t.Fatalf("expected test file included with --tests, got: %s", out)
	}

	// 5. Path glob
	resetSearchFlags()
	searchPathGlob = "*.rs"
	out = captureStdout(t, func() {
		if err := searchCmd.RunE(searchCmd, []string{"Corral", root}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "repo-c/lib.rs:") || strings.Contains(out, "repo-a") {
		t.Fatalf("expected only *.rs match, got: %s", out)
	}
}

func TestSearchCommandTruncationAndErrorSkip(t *testing.T) {
	root, idx := setupSearchWorkspace(t)
	origScan, origRepoOp := searchScan, searchRepoOp
	t.Cleanup(func() {
		searchScan, searchRepoOp = origScan, origRepoOp
	})

	resetSearchFlags()
	searchScan = func(string) (*corralmcp.Index, error) { return idx, nil }

	// 1. Truncation when hits >= max in text mode
	resetSearchFlags()
	searchMax = 1
	var stderr string
	out := captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			if err := searchCmd.RunE(searchCmd, []string{"Corral", root}); err != nil {
				t.Fatal(err)
			}
		})
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 hit line due to max=1, got %d: %q", len(lines), out)
	}
	if !strings.Contains(stderr, "results truncated at 1 matches") {
		t.Fatalf("expected truncation stderr warning, got: %s", stderr)
	}

	// 2. Truncation in JSON mode
	resetSearchFlags()
	searchMax = 1
	searchJSON = true
	out = captureStdout(t, func() {
		if err := searchCmd.RunE(searchCmd, []string{"Corral", root}); err != nil {
			t.Fatal(err)
		}
	})
	var report SearchResultReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}
	if !report.Truncated || report.TotalHits != 1 {
		t.Fatalf("expected truncated=true and total_hits=1, got: %+v", report)
	}

	// 3. Truncated result from underlying searchRepoOp
	resetSearchFlags()
	searchRepoOp = func(ctx context.Context, path string, m *search.Matcher, allowed search.FileFilter) (*search.Result, error) {
		return &search.Result{
			Hits: []search.Hit{
				{File: "a.go", Line: 1, Column: 1, Text: "sample"},
			},
			Files:     1,
			Truncated: true,
		}, nil
	}
	searchJSON = true
	searchMax = 50
	out = captureStdout(t, func() {
		if err := searchCmd.RunE(searchCmd, []string{"sample", root}); err != nil {
			t.Fatal(err)
		}
	})
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("failed to decode JSON: %v", err)
	}
	if !report.Truncated {
		t.Fatalf("expected truncated=true, got: %+v", report)
	}

	// 4. searchRepoOp returns error (should skip repo and continue)
	resetSearchFlags()
	searchRepoOp = func(ctx context.Context, path string, m *search.Matcher, allowed search.FileFilter) (*search.Result, error) {
		if strings.Contains(path, "repo-a") {
			return nil, errors.New("simulated repo unreadable")
		}
		return &search.Result{
			Hits: []search.Hit{
				{File: "app.go", Line: 1, Column: 1, Text: "sample"},
			},
			Files: 1,
		}, nil
	}
	out = captureStdout(t, func() {
		if err := searchCmd.RunE(searchCmd, []string{"sample", root}); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "repo-b/app.go:") {
		t.Fatalf("expected repo-b hit after skipping repo-a error, got: %s", out)
	}
}
