// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package deps

import "testing"

// Every title below is one Dependabot opened on this account; the forms
// differ by the repository's commit-message prefix and grouping rules.
func TestParseTitleReadsEveryFormDependabotUses(t *testing.T) {
	cases := []struct {
		title string
		want  Bump
	}{
		{"chore(deps): bump wrangler from 4.165.0 to 4.166.0 in the minor-and-patch group",
			Bump{Package: "wrangler", From: "4.165.0", To: "4.166.0", Group: "minor-and-patch", Updates: 1}},
		{"deps: Bump pypdf from 6.21.4 to 7.1.1",
			Bump{Package: "pypdf", From: "6.21.4", To: "7.1.1", Updates: 1}},
		{"chore(deps-dev): Bump @sebastienrousseau/config-kit from 0.0.8 to 0.0.9 in the npm group",
			Bump{Package: "@sebastienrousseau/config-kit", From: "0.0.8", To: "0.0.9", Group: "npm", Updates: 1}},
		{"build(deps): bump toml from 1.2.1+spec-1.1.0 to 1.2.2+spec-1.1.0 in the minor-and-patch group across 1 directory",
			Bump{Package: "toml", From: "1.2.1+spec-1.1.0", To: "1.2.2+spec-1.1.0", Group: "minor-and-patch", Updates: 1}},
		{"build(deps): Bump mkdocs-material from 9.8.1 to 9.8.2 in /ci in the python-tools group",
			Bump{Package: "mkdocs-material", From: "9.8.1", To: "9.8.2", Group: "python-tools", Updates: 1}},
		{"chore(deps): bump the github-actions group with 2 updates",
			Bump{Group: "github-actions", Updates: 2}},
		{"build(deps): Bump the python-dependencies group across 1 directory with 17 updates",
			Bump{Group: "python-dependencies", Updates: 17}},
		{"ci: Bump the github-actions group with 1 update",
			Bump{Group: "github-actions", Updates: 1}},
	}
	for _, c := range cases {
		got, ok := ParseTitle(c.title)
		if !ok || got != c.want {
			t.Errorf("ParseTitle(%q) = %+v, %v; want %+v", c.title, got, ok, c.want)
		}
	}
}

func TestParseTitleRefusesATitleAPersonRewrote(t *testing.T) {
	for _, title := range []string{"", "Update dependencies", "chore: bump wrangler to 4.166.0"} {
		if b, ok := ParseTitle(title); ok {
			t.Errorf("ParseTitle(%q) = %+v, true; want not ok", title, b)
		}
	}
}

func TestIsGroupDistinguishesAGroupFromABumpInsideOne(t *testing.T) {
	if !(Bump{Group: "npm", Updates: 3}).IsGroup() {
		t.Error("a group update should be a group")
	}
	if (Bump{Package: "a", From: "1", To: "2", Group: "npm", Updates: 1}).IsGroup() {
		t.Error("a single bump made in a group is not a group update")
	}
}

func TestEcosystemReadsTheDependabotBranch(t *testing.T) {
	cases := map[string]string{
		"dependabot/npm_and_yarn/minor-and-patch-04d8bb7433":  "npm_and_yarn",
		"dependabot/github_actions/github-actions-9cc24aa95a": "github_actions",
		"dependabot/cargo/toml-1.2.2":                         "cargo",
		"feat/v0.0.58":                                        "",
		"dependabot/pip":                                      "",
		"renovate/npm_and_yarn/x":                             "",
	}
	for branch, want := range cases {
		if got := Ecosystem(branch); got != want {
			t.Errorf("Ecosystem(%q) = %q, want %q", branch, got, want)
		}
	}
}
