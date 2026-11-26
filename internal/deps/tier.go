// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package deps

import "strings"

// CI is the state of a pull request's checks.
type CI string

const (
	// CISuccess means every check passed or was skipped.
	CISuccess CI = "success"
	// CIFailure means at least one check failed.
	CIFailure CI = "failure"
	// CIPending means a check has not finished.
	CIPending CI = "pending"
	// CINone means the pull request has no checks at all.
	CINone CI = "none"
)

// PR is one open Dependabot pull request, as triage sees it.
type PR struct {
	// Repo is "owner/name".
	Repo string `json:"repo"`
	// Number is the pull request number.
	Number int `json:"number"`
	// Title is the pull request title.
	Title string `json:"title"`
	// URL is the pull request's web address.
	URL string `json:"url"`
	// Branch is the head branch, which names the ecosystem.
	Branch string `json:"branch"`
	// CI is the state of its checks.
	CI CI `json:"ci"`
	// Mergeable is GitHub's mergeable state: "clean", "behind", "dirty",
	// "blocked", "unstable" or "unknown".
	Mergeable string `json:"mergeable"`
}

// Override is a known-bad version: Dependabot proposes Skip, and the
// release that fixes it is Use. An empty Use means no fixed release exists
// yet, so the pull request is closed rather than superseded.
type Override struct {
	// Package is the dependency name as Dependabot writes it.
	Package string `json:"package"`
	// Skip is the version not to adopt.
	Skip string `json:"skip"`
	// Use is the version to adopt instead.
	Use string `json:"use,omitempty"`
}

// Tier is how much handling a pull request needs.
type Tier int

const (
	// TierFast is a low-risk update with green checks: fold it into the
	// release branch as it is.
	TierFast Tier = 1
	// TierRemediate needs a mechanical fix first: a stale branch, a red or
	// missing check, or a superseding version.
	TierRemediate Tier = 2
	// TierQuarantine is a breaking-range update: it needs a person, or a
	// migration that the tests fully cover.
	TierQuarantine Tier = 3
	// TierClose is obsolete: close it with the reason.
	TierClose Tier = 4
)

// Decision is the tier for one pull request and why.
type Decision struct {
	// PR is the pull request decided on.
	PR PR `json:"pr"`
	// Bump is what its title says it changes.
	Bump Bump `json:"bump"`
	// Ecosystem is the package ecosystem its branch names.
	Ecosystem string `json:"ecosystem"`
	// Jump is how far the version moves.
	Jump Jump `json:"jump"`
	// Tier is the handling it needs.
	Tier Tier `json:"tier"`
	// Reason says why, in a sentence a report can show.
	Reason string `json:"reason"`
	// Action is what to do, under the release-branch rule.
	Action string `json:"action"`
}

// Fold is the action for any update that is kept: per ~/Code/AGENTS.md
// section 3 it reaches the default branch only as a commit on
// feat/v<next>, and its own pull request is closed with a link.
const Fold = "cherry-pick onto feat/v<next>; close the PR linking the release PR"

// Decide assigns a pull request its tier.
func Decide(pr PR, overrides []Override) Decision {
	d := Decision{PR: pr, Ecosystem: Ecosystem(pr.Branch)}
	b, ok := ParseTitle(pr.Title)
	if !ok {
		return d.set(TierRemediate, "the title is not in a form Dependabot writes", "triage by hand")
	}
	d.Bump = b
	d.Jump = JumpUnknown
	if !b.IsGroup() {
		d.Jump = Classify(b.From, b.To)
	}
	if o, hit := overrideFor(b, overrides); hit {
		return d.overridden(o)
	}
	if d.Jump == JumpMajor {
		return d.set(TierQuarantine, "a major (or pre-1.0 minor) version change may break the API",
			"read the changelog; migrate with full test coverage or label needs-manual-migration")
	}
	return d.byState()
}

// byState tiers an update whose version change is not breaking, by the
// state of its checks and branch.
func (d Decision) byState() Decision {
	pr := d.PR
	switch {
	case pr.CI == CIFailure:
		return d.set(TierRemediate, "checks fail", "find the cause; fix it on feat/v<next>")
	case pr.CI != CISuccess:
		return d.set(TierRemediate, "checks are "+string(pr.CI), "wait for or start the checks")
	case !d.lowRisk():
		return d.set(TierRemediate, "a group whose contents are not limited to minor and patch updates",
			"review each update in the group before folding it")
	// Only a conflict matters: a kept update is cherry-picked onto
	// feat/v<next>, never merged from its own pull request, so that pull
	// request being behind or blocked by base-branch protection is no
	// obstacle.
	case pr.Mergeable == "dirty":
		return d.set(TierRemediate, "the branch conflicts with its base", Fold+", resolving the conflict there")
	default:
		return d.set(TierFast, "a "+string(d.Jump)+" update with green checks", Fold)
	}
}

// lowRisk reports whether an update cannot move a major version: a single
// patch, minor or digest bump, a GitHub Actions group (actions are pinned
// by commit, so their updates are reviewed as digests), or a group whose
// name limits it to minor and patch updates.
func (d Decision) lowRisk() bool {
	if !d.Bump.IsGroup() {
		return d.Jump == JumpPatch || d.Jump == JumpMinor || d.Jump == JumpDigest
	}
	if d.Ecosystem == "github_actions" {
		return true
	}
	name := strings.ToLower(d.Bump.Group)
	return strings.Contains(name, "minor") || strings.Contains(name, "patch")
}

// overridden tiers an update that a known-bad-version override names.
func (d Decision) overridden(o Override) Decision {
	if o.Use == "" {
		return d.set(TierClose, o.Package+" "+o.Skip+" is known bad and has no fixed release",
			"close the PR naming the override")
	}
	return d.set(TierRemediate, o.Package+" "+o.Skip+" is known bad; "+o.Use+" fixes it",
		"adopt "+o.Use+" on feat/v<next>; close the PR linking the release PR")
}

// set fills in the tier, reason and action.
func (d Decision) set(t Tier, reason, action string) Decision {
	d.Tier, d.Reason, d.Action = t, reason, action
	return d
}

// overrideFor finds the override for a single bump's package and target.
func overrideFor(b Bump, overrides []Override) (Override, bool) {
	for _, o := range overrides {
		if o.Package == b.Package && o.Skip == b.To {
			return o, true
		}
	}
	return Override{}, false
}
