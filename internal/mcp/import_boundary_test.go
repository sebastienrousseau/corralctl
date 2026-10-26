// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/sebastienrousseau/corralctl"

// forbiddenForMCP lists what the MCP server must never link, directly or
// through another package of this module. internal/engine drives the
// terminal UI, and internal/tui pulls in the Bubble Tea stack; neither has
// any business in a stdio JSON-RPC server.
var forbiddenForMCP = []string{
	modulePath + "/internal/engine",
	modulePath + "/internal/tui",
	"github.com/charmbracelet/",
}

// TestMCPDoesNotImportTheTerminalUI walks the import graph of internal/mcp
// through this module's own packages and fails if any of them reaches a
// forbidden import. Test files are skipped (they never ship); every other
// file is parsed whatever its build tags, which over-approximates the
// graph and so cannot miss an edge.
func TestMCPDoesNotImportTheTerminalUI(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	queue := []string{modulePath + "/internal/mcp"}
	for len(queue) > 0 {
		pkg := queue[0]
		queue = queue[1:]
		if seen[pkg] {
			continue
		}
		seen[pkg] = true
		for _, imp := range packageImports(t, root, pkg) {
			if bad := forbiddenPrefix(imp); bad != "" {
				t.Errorf("%s imports %s (forbidden: %s)", pkg, imp, bad)
			}
			if strings.HasPrefix(imp, modulePath+"/") {
				queue = append(queue, imp)
			}
		}
	}
	if !seen[modulePath+"/internal/git"] {
		t.Fatal("walk never reached internal/git; the graph walk is broken, not clean")
	}
}

// forbiddenPrefix returns the forbiddenForMCP entry imp matches, or "".
func forbiddenPrefix(imp string) string {
	for _, f := range forbiddenForMCP {
		if imp == f || strings.HasPrefix(imp, strings.TrimSuffix(f, "/")+"/") {
			return f
		}
	}
	return ""
}

// packageImports returns the imports of every non-test Go file in pkg.
func packageImports(t *testing.T, root, pkg string) []string {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(pkg, modulePath+"/")))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	var out []string
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		for _, spec := range f.Imports {
			p, _ := strconv.Unquote(spec.Path.Value)
			out = append(out, p)
		}
	}
	return out
}

func TestForbiddenPrefixMatchesWholeSegments(t *testing.T) {
	cases := map[string]string{
		modulePath + "/internal/tui":        modulePath + "/internal/tui",
		modulePath + "/internal/tuile":      "",
		modulePath + "/internal/engine/sub": modulePath + "/internal/engine",
		"github.com/charmbracelet/lipgloss": "github.com/charmbracelet/",
		modulePath + "/internal/git":        "",
	}
	for imp, want := range cases {
		if got := forbiddenPrefix(imp); got != want {
			t.Errorf("forbiddenPrefix(%q) = %q, want %q", imp, got, want)
		}
	}
}
