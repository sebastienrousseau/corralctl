// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package deps

import (
	"fmt"
	"reflect"
	"testing"
)

func TestNextVersionMovesByOneThousandth(t *testing.T) {
	cases := map[string]string{
		"":           "v0.0.1",
		"v0.0.57":    "v0.0.58",
		"v0.0.9":     "v0.0.10",
		"v0.0.999":   "v0.1.0",
		"v0.1.999":   "v0.2.0",
		"v0.999.999": "v1.0.0",
		"v1.2.3":     "v1.2.4",
	}
	for tag, want := range cases {
		got, err := NextVersion(tag)
		if err != nil || got != want {
			t.Errorf("NextVersion(%q) = %q, %v; want %q", tag, got, err, want)
		}
	}
}

func TestNextVersionRefusesATagThatIsNotARelease(t *testing.T) {
	for _, tag := range []string{"0.0.57", "v0.0", "release-1", "v1.2.3-rc1"} {
		if got, err := NextVersion(tag); err == nil {
			t.Errorf("NextVersion(%q) = %q, want an error", tag, got)
		}
	}
}

func TestFoldsMergesTierOneAndClosesTierFourPerRepository(t *testing.T) {
	p := Build([]PR{
		pr("o/a", 3, "bump x from 1.0.0 to 1.0.1"), // tier 1
		pr("o/a", 4, "bump y from 1.0.0 to 2.0.0"), // tier 3: left alone
		pr("o/b", 5, "bump z from 1.0.0 to 1.0.1"), // tier 4: superseded by #6
		pr("o/b", 6, "bump z from 1.0.0 to 1.0.2"), // tier 1
		pr("o/c", 7, "bump w from 1.0.0 to 1.0.1"), // superseded by #8
		pr("o/c", 8, "bump w from 1.0.0 to 2.0.0"), // tier 3
	}, nil)
	states := map[string]RepoState{
		"o/a": {Repo: "o/a", DefaultBranch: "main", LatestTag: "v0.0.57"},
		"o/b": {Repo: "o/b", DefaultBranch: "master", LatestTag: "v0.0.9", BranchExists: true, ReleasePR: 12},
	}
	folds, skipped := Folds(p, states)
	if len(skipped) != 0 {
		t.Fatalf("skipped = %v", skipped)
	}
	got := map[string]string{}
	for _, f := range folds {
		got[f.Repo] = summarise(f)
	}
	want := map[string]string{
		"o/a": "feat/v0.0.58<-main create=true open=true pr=0 merge=[3] close=[]",
		"o/b": "feat/v0.0.10<-master create=false open=false pr=12 merge=[6] close=[5]",
		// A repository with only closes needs no branch or state.
		"o/c": "<- create=false open=false pr=0 merge=[] close=[7]",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("folds:\n got %v\nwant %v", got, want)
	}
	if folds[0].Repo != "o/a" || folds[2].Repo != "o/c" {
		t.Errorf("folds are not in repository order: %v", folds)
	}
}

func TestFoldsSkipsARepositoryItCannotTarget(t *testing.T) {
	p := Build([]PR{
		pr("o/nostate", 1, "bump x from 1.0.0 to 1.0.1"),
		pr("o/badtag", 2, "bump x from 1.0.0 to 1.0.1"),
	}, nil)
	folds, skipped := Folds(p, map[string]RepoState{
		"o/badtag": {Repo: "o/badtag", DefaultBranch: "main", LatestTag: "nightly"},
	})
	if len(folds) != 0 {
		t.Errorf("folds = %v, want none", folds)
	}
	want := []string{`o/badtag: "nightly" is not a vX.Y.Z release tag`, "o/nostate: no repository state"}
	if !reflect.DeepEqual(skipped, want) {
		t.Errorf("skipped = %v, want %v", skipped, want)
	}
}

// summarise renders a RepoFold compactly for comparison.
func summarise(f RepoFold) string {
	nums := func(ds []Decision) []int {
		out := []int{}
		for _, d := range ds {
			out = append(out, d.PR.Number)
		}
		return out
	}
	return fmt.Sprintf("%s<-%s create=%t open=%t pr=%d merge=%v close=%v",
		f.Branch, f.Base, f.CreateBranch, f.OpenReleasePR, f.ReleasePR, nums(f.Merge), nums(f.Close))
}

func TestFoldsKeepsOneReleaseAtATime(t *testing.T) {
	p := Build([]PR{
		pr("o/other", 1, "bump x from 1.0.0 to 1.0.1"),
		pr("o/untagged", 2, "bump x from 1.0.0 to 1.0.1"),
	}, nil)
	folds, skipped := Folds(p, map[string]RepoState{
		"o/other":    {Repo: "o/other", DefaultBranch: "main", LatestTag: "v0.0.57", OtherReleasePR: 7, OtherReleaseBranch: "feat/v0.0.60"},
		"o/untagged": {Repo: "o/untagged", DefaultBranch: "main", LatestTag: "v0.0.24", BranchExists: true, MergedReleasePR: 8},
	})
	want := []string{
		"o/other: release pull request #7 from feat/v0.0.60 is already open; a repository has one at a time",
		"o/untagged: feat/v0.0.25 was merged in #8 but v0.0.25 is not tagged; tag that release first",
	}
	if len(folds) != 0 || !reflect.DeepEqual(skipped, want) {
		t.Errorf("folds = %v\nskipped = %v\nwant %v", folds, skipped, want)
	}
}
