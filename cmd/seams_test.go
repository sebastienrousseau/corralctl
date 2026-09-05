// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"os"
	"reflect"
	"testing"

	"github.com/sebastienrousseau/corral/internal/engine"
	"github.com/sebastienrousseau/corral/internal/forge"
	gitutil "github.com/sebastienrousseau/corral/internal/git"
	"github.com/sebastienrousseau/corral/internal/mirror"
)

// TestCmdSeamsBindToRealImplementations pins the cmd-layer indirection seams to
// their production implementations.
//
// Mutation testing found that setting engineRun to a no-op left the whole suite
// green at 100% coverage: the CLI would parse flags perfectly and then never
// clone anything. Same for localStateCheck, which is prune's only guard against
// deleting a repository holding unpublished work.
func TestCmdSeamsBindToRealImplementations(t *testing.T) {
	cases := []struct {
		name string
		got  any
		want any
	}{
		{"osExit", osExit, os.Exit},
		{"engineRun", engineRun, engine.Run},
		{"preflightRunner", preflightRunner, runPreflight},
		{"opsFetchRepos", opsFetchRepos, fetchReposForOps},
		{"localStateCheck", localStateCheck, gitutil.HasUnpublishedWork},
		{"removeAll", removeAll, os.RemoveAll},
		{"userHomeDir", userHomeDir, os.UserHomeDir},
		{"syncWalk", syncWalk, mirror.Walk},
		{"syncRun", syncRun, mirror.Run},
		{"syncToken", syncToken, engine.ForgeToken},
		{"asTarget", asTarget, forge.AsTarget},
	}
	for _, tc := range cases {
		if tc.got == nil || tc.want == nil {
			t.Errorf("%s: seam or reference is nil", tc.name)
			continue
		}
		if reflect.ValueOf(tc.got).Pointer() != reflect.ValueOf(tc.want).Pointer() {
			t.Errorf("%s is not bound to its production implementation "+
				"(a stub leaked out of a test, or the default was changed)", tc.name)
		}
	}
}
