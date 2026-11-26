// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package deps

import (
	"strings"
	"testing"
)

const (
	npmBranch     = "dependabot/npm_and_yarn/x"
	actionsBranch = "dependabot/github_actions/x"
)

// green returns a clean, passing pull request with the given title.
func green(title, branch string) PR {
	return PR{Repo: "o/r", Number: 1, Title: title, Branch: branch, CI: CISuccess, Mergeable: "clean"}
}

// configKit is the override that the family's 2026-11-26 sweep needed:
// config-kit 0.0.9 failed every consumer's tarball check under npm 12.
var configKit = []Override{{Package: "@sebastienrousseau/config-kit", Skip: "0.0.9", Use: "0.0.10"}}

func TestDecideTiersEachKindOfUpdate(t *testing.T) {
	cases := []struct {
		name   string
		pr     PR
		tier   Tier
		reason string
	}{
		{"patch, green, clean", green("bump a from 1.2.3 to 1.2.4", npmBranch), TierFast, "a patch update"},
		{"minor, green, clean", green("bump a from 1.2.3 to 1.3.0", npmBranch), TierFast, "a minor update"},
		{"digest", green("bump a from 3d3c42e to 4d10147", actionsBranch), TierFast, "a digest update"},
		{"actions group", green("bump the github-actions group with 4 updates", actionsBranch), TierFast, "unknown update"},
		{"minor-and-patch group", green("bump the minor-and-patch group with 2 updates", npmBranch), TierFast, "unknown update"},
		{"major", green("bump pypdf from 6.21.4 to 7.1.1", npmBranch), TierQuarantine, "major"},
		{"pre-1.0 minor", green("bump g from 0.6.15 to 0.12.1", npmBranch), TierQuarantine, "pre-1.0"},
		{"other group", green("bump the python-dependencies group with 17 updates", npmBranch), TierRemediate, "not limited"},
		{"failing", PR{Title: "bump a from 1.2.3 to 1.2.4", CI: CIFailure, Mergeable: "clean"}, TierRemediate, "checks fail"},
		{"pending", PR{Title: "bump a from 1.2.3 to 1.2.4", CI: CIPending, Mergeable: "clean"}, TierRemediate, "checks are pending"},
		{"no checks", PR{Title: "bump a from 1.2.3 to 1.2.4", CI: CINone, Mergeable: "clean"}, TierRemediate, "checks are none"},
		{"conflicting", PR{Title: "bump a from 1.2.3 to 1.2.4", CI: CISuccess, Mergeable: "dirty"}, TierRemediate, "conflicts with its base"},
		// Funnelled updates are cherry-picked, never merged from their own
		// pull request, so its base-branch protection is no obstacle.
		{"behind", PR{Title: "bump a from 1.2.3 to 1.2.4", CI: CISuccess, Mergeable: "behind"}, TierFast, "a patch update"},
		{"blocked", PR{Title: "bump a from 1.2.3 to 1.2.4", CI: CISuccess, Mergeable: "blocked"}, TierFast, "a patch update"},
		{"requirement range", green("update s requirement from >=0.6.0,<0.8 to >=0.6.0,<0.9", npmBranch), TierRemediate, "not limited"},
		{"downgrade", green("bump a from 1.2.4 to 1.2.3", npmBranch), TierRemediate, "not limited"},
		{"rewritten title", green("Update dependencies", npmBranch), TierRemediate, "not in a form"},
	}
	for _, c := range cases {
		d := Decide(c.pr, nil)
		if d.Tier != c.tier || !strings.Contains(d.Reason, c.reason) {
			t.Errorf("%s: tier %d %q; want tier %d containing %q", c.name, d.Tier, d.Reason, c.tier, c.reason)
		}
	}
}

func TestDecideKeepsEveryUpdateOffMain(t *testing.T) {
	d := Decide(green("bump a from 1.2.3 to 1.2.4", npmBranch), nil)
	if d.Action != Fold || !strings.Contains(d.Action, "feat/v<next>") {
		t.Errorf("a fast-path update must fold into the release branch, got %q", d.Action)
	}
}

func TestDecideSupersedesAKnownBadVersion(t *testing.T) {
	d := Decide(green("chore(deps-dev): Bump @sebastienrousseau/config-kit from 0.0.8 to 0.0.9 in the npm group", npmBranch), configKit)
	if d.Tier != TierRemediate || !strings.Contains(d.Action, "adopt 0.0.10") {
		t.Errorf("config-kit 0.0.9 should be superseded by 0.0.10, got tier %d %q", d.Tier, d.Action)
	}
}

func TestDecideClosesAKnownBadVersionWithNoFix(t *testing.T) {
	d := Decide(green("bump a from 1.0.0 to 1.0.1", npmBranch), []Override{{Package: "a", Skip: "1.0.1"}})
	if d.Tier != TierClose || !strings.Contains(d.Reason, "no fixed release") {
		t.Errorf("got tier %d %q", d.Tier, d.Reason)
	}
}

func TestDecideIgnoresAnOverrideForAnotherVersion(t *testing.T) {
	d := Decide(green("bump @sebastienrousseau/config-kit from 0.0.9 to 0.0.10", npmBranch), configKit)
	if d.Tier != TierFast {
		t.Errorf("0.0.10 is the fix, not the bad version: got tier %d %q", d.Tier, d.Reason)
	}
}
