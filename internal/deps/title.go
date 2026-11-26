// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package deps reads and triages the open Dependabot pull requests across an
// account: what each one bumps, how far, and which tier of handling it needs.
//
// It holds the decisions only. Fetching pull requests and acting on them
// live elsewhere, so every rule here is a pure function a test can pin. The
// rules follow ~/Code/AGENTS.md: Dependabot updates reach the default
// branch only through the repository's feat/v<next> release branch, so
// nothing here merges a Dependabot pull request directly.
package deps

import (
	"regexp"
	"strings"
)

// Bump is what a Dependabot pull request title says it changes.
type Bump struct {
	// Package is the dependency being bumped; empty for a group update.
	Package string
	// From is the version being replaced; empty for a group update.
	From string
	// To is the version being moved to; empty for a group update.
	To string
	// Group names the Dependabot group for a grouped update, such as
	// "github-actions" or "minor-and-patch". It is set both for a group
	// update ("bump the X group with N updates") and for a single bump
	// made inside a group ("bump a from 1 to 2 in the X group").
	Group string
	// Updates is how many dependencies a group update moves; 1 for a
	// single bump.
	Updates int
}

// IsGroup reports whether the pull request moves several dependencies at
// once, so it has no single package or version pair.
func (b Bump) IsGroup() bool { return b.Package == "" && b.Group != "" }

var (
	// single matches "bump <pkg> from <a> to <b>", whatever precedes it
	// ("chore(deps): ", "build(deps-dev): Bump ", "deps: Bump ").
	single = regexp.MustCompile(`(?i)\bbump\s+(\S+)\s+from\s+(\S+)\s+to\s+(\S+)`)
	// group matches "bump the <name> group ... with <n> update(s)".
	group = regexp.MustCompile(`(?i)\bbump\s+the\s+(\S+)\s+group\b.*?\bwith\s+(\d+)\s+updates?\b`)
	// inGroup matches the group a single bump was made in.
	inGroup = regexp.MustCompile(`(?i)\bin\s+the\s+(\S+)\s+group\b`)
)

// ParseTitle reads a Dependabot pull request title. ok is false when the
// title is in neither the single nor the group form, which is a title a
// person rewrote; such a pull request is triaged by hand.
func ParseTitle(title string) (b Bump, ok bool) {
	if m := single.FindStringSubmatch(title); m != nil {
		b = Bump{Package: m[1], From: m[2], To: m[3], Updates: 1}
		if g := inGroup.FindStringSubmatch(title); g != nil {
			b.Group = g[1]
		}
		return b, true
	}
	if m := group.FindStringSubmatch(title); m != nil {
		return Bump{Group: m[1], Updates: atoi(m[2])}, true
	}
	return Bump{}, false
}

// atoi converts the digits the group pattern captured. The pattern admits
// only digits, so there is no error to report.
func atoi(digits string) int {
	n := 0
	for _, r := range digits {
		n = n*10 + int(r-'0')
	}
	return n
}

// Ecosystem names the package ecosystem from a Dependabot branch such as
// "dependabot/npm_and_yarn/minor-and-patch-1a2b3c". It returns "" for a
// branch Dependabot did not name.
func Ecosystem(branch string) string {
	parts := strings.SplitN(branch, "/", 3)
	if len(parts) < 3 || parts[0] != "dependabot" {
		return ""
	}
	return parts[1]
}
