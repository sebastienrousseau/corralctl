// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"os"
	"reflect"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/git"
)

// TestMCPSeamsBindToRealImplementations pins the MCP mutation seams.
//
// hasUnpushedCommits, hasDirtyWorkingTree and hasIgnoredContent are the three
// guards standing between corral_delete_repo and unrecoverable data loss.
// Mutation testing showed that stubbing hasUnpushedCommits to "false" left the
// suite green at 100% coverage — the delete tool would have lost its unpushed
// check with no test noticing.
func TestMCPSeamsBindToRealImplementations(t *testing.T) {
	cases := []struct {
		name string
		got  any
		want any
	}{
		{"gitPull", gitPull, git.Pull},
		{"gitClone", gitClone, git.Clone},
		{"hasUnpushedCommits", hasUnpushedCommits, git.HasUnpublishedWork},
		{"hasDirtyWorkingTree", hasDirtyWorkingTree, git.HasLocalChanges},
		{"hasIgnoredContent", hasIgnoredContent, git.HasIgnoredContent},
		{"statMutation", statMutation, os.Stat},
		{"mkdirMutation", mkdirMutation, os.MkdirAll},
		{"removeMutation", removeMutation, os.RemoveAll},
		{"markSynced", markSynced, markStateSynced},
		{"openResource", openResource, os.Open},
	}
	for _, tc := range cases {
		if reflect.ValueOf(tc.got).Pointer() != reflect.ValueOf(tc.want).Pointer() {
			t.Errorf("%s is not bound to its production implementation "+
				"(a stub leaked out of a test, or the default was changed)", tc.name)
		}
	}
}
