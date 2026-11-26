// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package deps

import (
	"strings"
	"testing"
)

func TestReportCountsTiersAndListsFleetsAndRows(t *testing.T) {
	kit := "chore(deps-dev): Bump @sebastienrousseau/config-kit from 0.0.8 to 0.0.9 in the npm group"
	actions := pr("o/r", 2, "bump the github-actions group with 4 updates")
	actions.Branch = actionsBranch
	p := Build([]PR{
		pr("o/c-config", 17, kit),
		pr("o/go-config", 17, kit),
		pr("o/zig-config", 19, kit),
		pr("o/r", 1, "bump a from 1.0.0 to 1.0.1"),
		actions,
		pr("o/r", 3, "Update | everything"),
	}, configKit)
	r := Report(p)
	for _, want := range []string{
		"6 open pull request(s).",
		"* Tier 1, fold as is: 2",
		"* Tier 2, fix first: 4",
		"* Tier 3, needs a person: 0",
		"* Tier 4, close: 0",
		"* `@sebastienrousseau/config-kit` to 0.0.9 in 3 repositories; canary o/c-config, then o/go-config, o/zig-config",
		"| o/r | [#1](", "a 1.0.0 to 1.0.1 (patch)",
		"github-actions group, 4 update(s)",
		`Update \| everything`,
		"adopt 0.0.10 on feat/v<next>",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("report lacks %q\n%s", want, r)
		}
	}
}

func TestReportSaysWhenThereAreNoFleets(t *testing.T) {
	r := Report(Build([]PR{pr("o/r", 1, "bump a from 1.0.0 to 1.0.1")}, nil))
	if !strings.Contains(r, "None: no bump is open in 3 or more repositories.") {
		t.Errorf("report should say there are no fleets:\n%s", r)
	}
}
