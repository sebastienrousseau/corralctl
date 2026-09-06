// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import "testing"

// TestRootCommandExposesTheRealTree guards the accessor scripts/gen_docs.go
// walks to render manpages and completions. If it ever returned a detached
// or partial command, the generated pages would document a CLI that does
// not exist — and nothing else in the suite would notice, because the
// generator runs at build time rather than test time.
func TestRootCommandExposesTheRealTree(t *testing.T) {
	root := RootCommand()
	if root == nil {
		t.Fatal("RootCommand returned nil")
	}
	if root != rootCmd {
		t.Fatal("RootCommand returned a different command than the one Execute runs")
	}
	if root.Name() != "corralctl" {
		t.Fatalf("root command name = %q, want %q", root.Name(), "corralctl")
	}

	// Every subcommand a manpage is generated for must be reachable.
	want := map[string]bool{
		"clone": false, "sync": false, "mcp": false, "exec": false, "status": false,
		"plan": false, "prune": false, "profile": false, "config": false,
	}
	for _, sub := range root.Commands() {
		if _, ok := want[sub.Name()]; ok {
			want[sub.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("subcommand %q is not reachable from RootCommand", name)
		}
	}

	// The generator renders a NAME line from Short; an empty one produces a
	// malformed page that groff would reject in CI.
	if root.Short == "" {
		t.Error("root command has no Short description; the manpage NAME section would be empty")
	}
	for _, sub := range root.Commands() {
		if sub.IsAvailableCommand() && sub.Name() != "help" && sub.Short == "" {
			t.Errorf("subcommand %q has no Short description", sub.Name())
		}
	}
}
