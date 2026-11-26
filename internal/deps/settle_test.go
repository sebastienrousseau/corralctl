// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package deps

import (
	"strings"
	"testing"
)

var (
	dependabotCommit = Commit{SHA: "d1d1d1d1d1", Subject: "chore(deps): bump x from 1.0.0 to 1.0.1", Author: "dependabot[bot]", Parents: 1}
	foldMerge        = Commit{SHA: "f2f2f2f2f2", Subject: "Merge #5 into feat/v0.0.58", Author: "web-flow", Parents: 2}
	branchUpdate     = Commit{SHA: "b3b3b3b3b3", Subject: "Merge branch 'main' into feat/v0.0.58", Author: "sebastienrousseau", Parents: 2}
	releasePrep      = Commit{SHA: "r4r4r4r4r4", Subject: "chore(release): v0.0.58", Author: "sebastienrousseau", Parents: 1}
)

func TestMergeReadyAcceptsHygieneWithItsReleasePreparation(t *testing.T) {
	ok, why := MergeReady([]Commit{dependabotCommit, foldMerge, branchUpdate, releasePrep})
	if !ok || why != "" {
		t.Errorf("got %v %q", ok, why)
	}
}

func TestMergeReadyWaitsForTheReleasePreparation(t *testing.T) {
	for _, commits := range [][]Commit{nil, {dependabotCommit, foldMerge}} {
		ok, why := MergeReady(commits)
		if ok || !strings.HasPrefix(why, "no release preparation yet") {
			t.Errorf("%v: got %v %q", commits, ok, why)
		}
	}
}

func TestMergeReadyLeavesAnythingElseToAPerson(t *testing.T) {
	cases := []Commit{
		{SHA: "abcdef1234", Subject: "feat: a person's change", Author: "sebastienrousseau", Parents: 1},
		// A fold merge has two parents; one that does not is something else.
		{SHA: "abcdef1234", Subject: "Merge #5 into feat/v0.0.58", Parents: 1},
		{SHA: "abcdef1234", Subject: "Merge branch 'other' into main", Parents: 2},
		{SHA: "abc", Subject: "fix: short sha", Parents: 1},
	}
	for _, c := range cases {
		ok, why := MergeReady([]Commit{releasePrep, c})
		if ok || !strings.Contains(why, "("+c.Subject+") is not a Dependabot change") || !strings.Contains(why, short(c.SHA)) {
			t.Errorf("%+v: got %v %q", c, ok, why)
		}
	}
	if short("abc") != "abc" || short("abcdef1234") != "abcdef1" {
		t.Errorf("short: %q %q", short("abc"), short("abcdef1234"))
	}
}
