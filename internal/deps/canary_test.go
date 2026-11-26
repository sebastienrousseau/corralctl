// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package deps

import (
	"reflect"
	"testing"
)

// fleetPlan is the config-kit case: the same bump open in three repos,
// one of which also has an unrelated update and one a superseded pull
// request to close.
func fleetPlan(t *testing.T) ([]RepoFold, []Fleet) {
	t.Helper()
	kit := "bump kit from 0.0.9 to 0.0.10"
	p := Build([]PR{
		pr("o/a", 1, kit),
		pr("o/b", 2, kit),
		pr("o/b", 3, "bump other from 1.0.0 to 1.0.1"),
		pr("o/c", 4, kit),
		pr("o/c", 5, "bump z from 1.0.0 to 1.0.1"), // superseded by #6
		pr("o/c", 6, "bump z from 1.0.0 to 1.0.2"),
	}, nil)
	states := map[string]RepoState{}
	for _, r := range []string{"o/a", "o/b", "o/c"} {
		states[r] = RepoState{Repo: r, DefaultBranch: "main", LatestTag: "v0.0.1"}
	}
	states["o/c"] = RepoState{Repo: "o/c", DefaultBranch: "main", LatestTag: "v0.0.1", BranchExists: true, ReleasePR: 9}
	folds, skipped := Folds(p, states)
	if len(skipped) != 0 {
		t.Fatalf("skipped = %v", skipped)
	}
	return folds, p.Fleets
}

func TestStageFoldsTheCanaryFirstAndHoldsTheSiblings(t *testing.T) {
	folds, fleets := fleetPlan(t)
	first, gates := Stage(folds, fleets)

	gotFirst := map[string]string{}
	for _, f := range first {
		gotFirst[f.Repo] = summarise(f)
	}
	wantFirst := map[string]string{
		// The canary folds the fleet's bump at once.
		"o/a": "feat/v0.0.2<-main create=true open=true pr=0 merge=[1] close=[]",
		// A sibling's other update and closes do not wait.
		"o/b": "feat/v0.0.2<-main create=true open=true pr=0 merge=[3] close=[]",
		"o/c": "feat/v0.0.2<-main create=false open=false pr=9 merge=[6] close=[5]",
	}
	if !reflect.DeepEqual(gotFirst, wantFirst) {
		t.Errorf("first:\n got %v\nwant %v", gotFirst, wantFirst)
	}

	if len(gates) != 1 || gates[0].Canary != "o/a" || gates[0].Fleet.Package != "kit" {
		t.Fatalf("gates = %+v", gates)
	}
	var gotSiblings []string
	for _, s := range gates[0].Siblings {
		gotSiblings = append(gotSiblings, s.Repo+" "+summarise(s))
	}
	wantSiblings := []string{
		"o/b feat/v0.0.2<-main create=true open=true pr=0 merge=[2] close=[]",
		"o/c feat/v0.0.2<-main create=false open=false pr=9 merge=[4] close=[]",
	}
	if !reflect.DeepEqual(gotSiblings, wantSiblings) {
		t.Errorf("siblings:\n got %v\nwant %v", gotSiblings, wantSiblings)
	}
}

func TestStageLeavesAFoldWhollyHeldOutOfTheFirstStage(t *testing.T) {
	kit := "bump kit from 0.0.9 to 0.0.10"
	// o/d folds an unrelated update and is no part of the fleet.
	p := Build([]PR{pr("o/a", 1, kit), pr("o/b", 2, kit), pr("o/c", 3, kit), pr("o/d", 4, "bump q from 1.0.0 to 1.0.1")}, nil)
	states := map[string]RepoState{}
	for _, r := range []string{"o/a", "o/b", "o/c", "o/d"} {
		states[r] = RepoState{Repo: r, DefaultBranch: "main", LatestTag: "v0.0.1"}
	}
	folds, _ := Folds(p, states)
	first, gates := Stage(folds, p.Fleets)
	if len(first) != 2 || first[0].Repo != "o/a" || first[1].Repo != "o/d" {
		t.Errorf("the canary and the unrelated o/d run first, got %v", first)
	}
	if len(gates) != 1 || len(gates[0].Siblings) != 2 {
		t.Errorf("gates = %+v", gates)
	}
}

func TestStageNeedsTwoFoldableMembersToGate(t *testing.T) {
	kit := "bump kit from 0.0.9 to 0.0.10"
	// Three repositories share the bump, but only o/a's is green; the other
	// two are tier 2, which apply does not fold, so there is no gate.
	red := pr("o/b", 2, kit)
	red.CI = CIFailure
	pending := pr("o/c", 3, kit)
	pending.CI = CIPending
	p := Build([]PR{pr("o/a", 1, kit), red, pending}, nil)
	if len(p.Fleets) != 1 {
		t.Fatalf("the three repositories still form a fleet: %+v", p.Fleets)
	}
	folds, _ := Folds(p, map[string]RepoState{"o/a": {Repo: "o/a", DefaultBranch: "main"}})
	first, gates := Stage(folds, p.Fleets)
	if len(gates) != 0 || len(first) != 1 || len(first[0].Merge) != 1 {
		t.Errorf("first = %v, gates = %v; want o/a folded at once and no gate", first, gates)
	}
}
