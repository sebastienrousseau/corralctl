// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package deps

import (
	"fmt"
	"strings"
)

// ReleaseLabel marks the release pull requests `deps apply` opens, so a
// later run finds them with one search and can merge the ready ones.
const ReleaseLabel = "corral-deps"

// Commit is one commit on a release pull request.
type Commit struct {
	// SHA is the commit.
	SHA string
	// Subject is the first line of its message.
	Subject string
	// Author is the GitHub login of its author, or "".
	Author string
	// Parents is how many parents it has; a merge has two.
	Parents int
}

// MergeReady reports whether `deps apply` may merge a release pull request
// with these commits: every one is a Dependabot change, a deps fold
// merge, an update of the branch from its base, or the release
// preparation, and the preparation (a "chore(release):" commit) is there.
// A release pull request carrying anything else is a person's to merge;
// one not yet prepared would put an unversioned iteration on the default
// branch. reason says why not.
func MergeReady(commits []Commit) (ok bool, reason string) {
	prepared := false
	for _, c := range commits {
		switch {
		case strings.HasPrefix(c.Subject, "chore(release):"):
			prepared = true
		case c.Author == "dependabot[bot]", isFoldMerge(c), isBranchUpdate(c):
		default:
			return false, fmt.Sprintf("commit %s (%s) is not a Dependabot change, a deps fold or the release preparation", short(c.SHA), c.Subject)
		}
	}
	if !prepared {
		return false, "no release preparation yet: a chore(release): commit with the version and changelog"
	}
	return true, ""
}

// isFoldMerge reports a merge `deps apply` made: "Merge #N into feat/vX".
func isFoldMerge(c Commit) bool {
	return c.Parents == 2 && strings.HasPrefix(c.Subject, "Merge #") && strings.Contains(c.Subject, " into feat/v")
}

// isBranchUpdate reports GitHub's "Update branch" merge of the base into
// the release branch, which brings in only what the base already has.
func isBranchUpdate(c Commit) bool {
	return c.Parents == 2 && strings.HasPrefix(c.Subject, "Merge branch '") && strings.Contains(c.Subject, "' into feat/v")
}

// short is a commit's abbreviated SHA.
func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
