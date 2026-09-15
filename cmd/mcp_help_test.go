// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/mcp"
)

// The MCP help text is checked against the server's own registered tool
// set rather than against a list maintained here.
//
// A hand-kept list is only as good as the discipline of updating it, and
// this text has drifted twice: it said "five" while seven were registered,
// and the two symbol tools were missing entirely. A list in the test would
// have missed the third drift too — corral_search_code was added and every
// test still passed, because the list did not know about it.
//
// Asking a real server what it registered removes the second place to
// forget. Adding a tool now fails this test until the help text mentions
// it.
func registeredTools(t *testing.T, mutations bool) []string {
	t.Helper()
	srv, err := mcp.NewServer(mcp.ServerOptions{
		Root:                       t.TempDir(),
		Version:                    "test",
		EnableMutations:            mutations,
		EnableDestructiveMutations: mutations,
		AuditLogPath:               filepath.Join(t.TempDir(), "audit.log"),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return srv.ToolNames()
}

// readOnlyToolNames is what a default server exposes.
func readOnlyToolNames(t *testing.T) []string {
	t.Helper()
	return registeredTools(t, false)
}

// mutationToolNames is what --enable-mutations and
// --enable-destructive-mutations add on top.
func mutationToolNames(t *testing.T) []string {
	t.Helper()
	read := map[string]bool{}
	for _, n := range readOnlyToolNames(t) {
		read[n] = true
	}
	var extra []string
	for _, n := range registeredTools(t, true) {
		if !read[n] {
			extra = append(extra, n)
		}
	}
	return extra
}

func TestMCPHelpListsEveryTool(t *testing.T) {
	help := mcpCmd.Long

	all := append(readOnlyToolNames(t), mutationToolNames(t)...)
	if len(all) == 0 {
		t.Fatal("the server registered no tools at all")
	}
	for _, name := range all {
		if !strings.Contains(help, name) {
			t.Errorf("`corralctl mcp --help` does not mention %s", name)
		}
	}
}

// TestMCPHelpCountIsAccurate pins the numeral, which is the part that
// actually went wrong: the list can be right while the sentence above it
// says something else.
func TestMCPHelpCountIsAccurate(t *testing.T) {
	help := mcpCmd.Long

	spelled := map[int]string{
		4: "four", 5: "five", 6: "six", 7: "seven",
		8: "eight", 9: "nine", 10: "ten", 11: "eleven", 12: "twelve",
	}
	n := len(readOnlyToolNames(t))
	want, ok := spelled[n]
	if !ok {
		t.Fatalf("no spelling for %d tools; extend the table", n)
	}
	claim := fmt.Sprintf("%s read-only tools", want)
	if !strings.Contains(help, claim) {
		t.Errorf("help should say %q for %d read-only tools", claim, n)
	}

	// Any other spelled number in front of "read-only tools" is a stale
	// claim that happens to sit beside a correct list.
	for count, word := range spelled {
		if count == n {
			continue
		}
		if strings.Contains(help, word+" read-only tools") {
			t.Errorf("help still claims %q read-only tools", word)
		}
	}
}

// TestMCPHelpNamesTheDifferentiator: the cross-repository lookup is the
// capability a single-repository code index cannot offer, and the help is
// where someone finds that out.
func TestMCPHelpNamesTheDifferentiator(t *testing.T) {
	help := mcpCmd.Long
	for _, want := range []string{"corral_find_symbol", "across"} {
		if !strings.Contains(help, want) {
			t.Errorf("help should explain the cross-repository lookup; missing %q", want)
		}
	}
}
