// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package deps

import (
	"regexp"
	"strconv"
	"strings"
)

// Jump is how far a version moves, which decides how much a bump can
// break.
type Jump string

const (
	// JumpPatch moves only the patch (or a finer) component.
	JumpPatch Jump = "patch"
	// JumpMinor moves the minor component of a 1.0-or-later version.
	JumpMinor Jump = "minor"
	// JumpMajor moves the major component, or the minor component of a
	// pre-1.0 version: under SemVer a 0.y change may break the API, so it
	// is handled like a major one.
	JumpMajor Jump = "major"
	// JumpDigest moves a commit digest, as a GitHub Action pinned by SHA
	// does.
	JumpDigest Jump = "digest"
	// JumpUnknown is a pair of versions this cannot compare: a group
	// update, a downgrade, or text that is not a version.
	JumpUnknown Jump = "unknown"
)

// digest matches a hexadecimal commit SHA, short or full.
var digest = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// Classify returns how far the version moves from from to to.
func Classify(from, to string) Jump {
	if digest.MatchString(from) && digest.MatchString(to) {
		return JumpDigest
	}
	a, okA := numbers(from)
	b, okB := numbers(to)
	if !okA || !okB {
		return JumpUnknown
	}
	return compare(a, b)
}

// compare classifies the first component that differs. A version that
// does not move forward is unknown: Dependabot only proposes upgrades, so
// a downgrade is a title someone edited.
func compare(a, b [3]int) Jump {
	switch {
	case b[0] != a[0]:
		return forward(b[0] > a[0], JumpMajor)
	case b[1] != a[1]:
		if a[0] == 0 {
			return forward(b[1] > a[1], JumpMajor)
		}
		return forward(b[1] > a[1], JumpMinor)
	case b[2] > a[2]:
		return JumpPatch
	default:
		return JumpUnknown
	}
}

// forward returns j for an upgrade and JumpUnknown for a downgrade.
func forward(up bool, j Jump) Jump {
	if up {
		return j
	}
	return JumpUnknown
}

// numbers reads the first three numeric components of a version such as
// "v1.2.3", "1.2.1+spec-1.1.0" or "10.36.0.15337". Missing components
// count as zero, so "4" compares as 4.0.0. Build metadata and
// pre-release text after the numbers are ignored.
func numbers(v string) ([3]int, bool) {
	var out [3]int
	v = strings.TrimPrefix(v, "v")
	if cut := strings.IndexAny(v, "+-"); cut >= 0 {
		v = v[:cut]
	}
	parts := strings.Split(v, ".")
	for i := 0; i < len(parts) && i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
