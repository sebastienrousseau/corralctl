// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package deps

import (
	"reflect"
	"testing"
)

func pr(repo string, n int, title string) PR {
	return PR{Repo: repo, Number: n, Title: title, Branch: npmBranch, CI: CISuccess, Mergeable: "clean"}
}

func TestBuildOrdersByTierThenRepoThenNumber(t *testing.T) {
	p := Build([]PR{
		pr("o/b", 2, "bump a from 1.0.0 to 2.0.0"), // tier 3
		pr("o/b", 1, "bump c from 1.0.0 to 1.0.1"), // tier 1
		pr("o/a", 9, "bump d from 1.0.0 to 1.0.1"), // tier 1
		pr("o/a", 3, "bump e from 1.0.0 to 1.0.1"), // tier 1
	}, nil)
	var got [][2]any
	for _, d := range p.Decisions {
		got = append(got, [2]any{d.PR.Repo, d.PR.Number})
	}
	want := [][2]any{{"o/a", 3}, {"o/a", 9}, {"o/b", 1}, {"o/b", 2}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestBuildClosesTheOlderOfTwoBumpsOfOnePackage(t *testing.T) {
	p := Build([]PR{
		pr("o/r", 5, "bump fast-check from 4.10.2 to 4.10.3"),
		pr("o/r", 8, "bump fast-check from 4.10.2 to 4.11.0"),
		pr("o/other", 2, "bump fast-check from 4.10.2 to 4.10.3"),
		pr("o/r", 9, "bump the github-actions group with 2 updates"),
		pr("o/r", 10, "bump the github-actions group with 3 updates"),
	}, nil)
	byNum := map[int]Decision{}
	for _, d := range p.Decisions {
		if d.PR.Repo == "o/r" {
			byNum[d.PR.Number] = d
		}
	}
	if d := byNum[5]; d.Tier != TierClose || d.Reason != "superseded by #8" {
		t.Errorf("#5 should be superseded by #8, got tier %d %q", d.Tier, d.Reason)
	}
	if d := byNum[8]; d.Tier != TierFast {
		t.Errorf("#8 is the newest and should stay, got tier %d", d.Tier)
	}
	if byNum[9].Tier == TierClose || byNum[10].Tier == TierClose {
		t.Error("group updates are not superseded by package")
	}
	for _, d := range p.Decisions {
		if d.PR.Repo == "o/other" && d.Tier == TierClose {
			t.Error("a bump in another repository is not superseded")
		}
	}
}

func TestBuildFindsFleetsOfThreeOrMore(t *testing.T) {
	kit := "chore(deps-dev): Bump @sebastienrousseau/config-kit from 0.0.8 to 0.0.9 in the npm group"
	p := Build([]PR{
		pr("o/c-config", 17, kit),
		pr("o/zig-config", 19, kit),
		pr("o/go-config", 17, kit),
		pr("o/x", 1, "bump a from 1.0.0 to 1.0.1"),
		pr("o/y", 1, "bump a from 1.0.0 to 1.0.1"),
		pr("o/z", 1, "bump the npm group with 2 updates"),
		pr("o/w", 1, "bump the npm group with 2 updates"),
		pr("o/v", 1, "bump the npm group with 2 updates"),
		pr("o/u", 1, "Update dependencies"),
	}, configKit)
	want := []Fleet{{Package: "@sebastienrousseau/config-kit", To: "0.0.9", Repos: []string{"o/c-config", "o/go-config", "o/zig-config"}}}
	if !reflect.DeepEqual(p.Fleets, want) {
		t.Errorf("fleets = %+v, want %+v", p.Fleets, want)
	}
}

func TestBuildSortsFleetsBySizeThenName(t *testing.T) {
	var prs []PR
	for i, r := range []string{"o/1", "o/2", "o/3", "o/4"} {
		prs = append(prs, pr(r, i, "bump big from 1.0.0 to 1.0.1"))
	}
	for i, r := range []string{"o/1", "o/2", "o/3"} {
		prs = append(prs, pr(r, 10+i, "bump b from 1.0.0 to 1.0.1"), pr(r, 20+i, "bump a from 1.0.0 to 1.0.1"))
	}
	var got []string
	for _, f := range Build(prs, nil).Fleets {
		got = append(got, f.Package)
	}
	if want := []string{"big", "a", "b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("fleet order = %v, want %v", got, want)
	}
}
