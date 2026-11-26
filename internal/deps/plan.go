// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package deps

import (
	"fmt"
	"sort"
)

// FleetMin is how many repositories must carry the same bump before it is
// handled as one fleet batch: fixed on a canary repository first, then
// applied to the rest by script.
const FleetMin = 3

// Fleet is one bump that several repositories share.
type Fleet struct {
	// Package and To identify the bump.
	Package string `json:"package"`
	// To is the version every member moves to.
	To string `json:"to"`
	// Repos are the members, sorted; the first is the canary.
	Repos []string `json:"repos"`
}

// Plan is a triage of every open Dependabot pull request.
type Plan struct {
	// Decisions holds one decision per pull request, by tier, then repo,
	// then number.
	Decisions []Decision `json:"decisions"`
	// Fleets are the bumps shared by FleetMin or more repositories.
	Fleets []Fleet `json:"fleets"`
}

// Build triages prs: it decides each one, marks the older of two pull
// requests that bump the same package in the same repository as
// superseded, and finds the fleets.
func Build(prs []PR, overrides []Override) Plan {
	decisions := make([]Decision, 0, len(prs))
	for _, pr := range prs {
		decisions = append(decisions, Decide(pr, overrides))
	}
	markSuperseded(decisions)
	sort.SliceStable(decisions, func(i, j int) bool { return less(decisions[i], decisions[j]) })
	return Plan{Decisions: decisions, Fleets: fleets(decisions)}
}

// less orders decisions by tier, then repository, then number.
func less(a, b Decision) bool {
	if a.Tier != b.Tier {
		return a.Tier < b.Tier
	}
	if a.PR.Repo != b.PR.Repo {
		return a.PR.Repo < b.PR.Repo
	}
	return a.PR.Number < b.PR.Number
}

// markSuperseded closes every single bump that a newer pull request in the
// same repository replaces: Dependabot opens a new one when a newer
// version ships, and the old one is then obsolete.
func markSuperseded(ds []Decision) {
	newest := newestByPackage(ds)
	for i, d := range ds {
		j, seen := newest[packageKey(d)]
		if !seen || j == i || !isSingle(d) {
			continue
		}
		n := ds[j].PR.Number
		ds[i] = d.set(TierClose, fmt.Sprintf("superseded by #%d", n), fmt.Sprintf("close the PR linking #%d", n))
	}
}

// newestByPackage maps each repository-and-package to the index of its
// highest-numbered single bump.
func newestByPackage(ds []Decision) map[string]int {
	newest := map[string]int{}
	for i, d := range ds {
		if !isSingle(d) {
			continue
		}
		key := packageKey(d)
		if j, seen := newest[key]; !seen || ds[j].PR.Number < d.PR.Number {
			newest[key] = i
		}
	}
	return newest
}

// isSingle reports whether a decision is about one named package.
func isSingle(d Decision) bool { return d.Bump.Package != "" && !d.Bump.IsGroup() }

// packageKey identifies a package within a repository.
func packageKey(d Decision) string { return d.PR.Repo + "\x00" + d.Bump.Package }

// fleets groups the single bumps still to be handled by package and
// target version, and keeps the groups with FleetMin or more repositories.
func fleets(ds []Decision) []Fleet {
	var out []Fleet
	for key, repos := range fleetMembers(ds) {
		if len(repos) >= FleetMin {
			out = append(out, Fleet{Package: key[0], To: key[1], Repos: sortedKeys(repos)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return fleetBefore(out[i], out[j]) })
	return out
}

// fleetMembers collects, per package and target version, the repositories
// whose open single bump is still to be handled.
func fleetMembers(ds []Decision) map[[2]string]map[string]bool {
	members := map[[2]string]map[string]bool{}
	for _, d := range ds {
		if d.Tier == TierClose || !isSingle(d) {
			continue
		}
		key := [2]string{d.Bump.Package, d.Bump.To}
		if members[key] == nil {
			members[key] = map[string]bool{}
		}
		members[key][d.PR.Repo] = true
	}
	return members
}

// fleetBefore orders the largest fleets first, then by package and version.
func fleetBefore(a, b Fleet) bool {
	if len(a.Repos) != len(b.Repos) {
		return len(a.Repos) > len(b.Repos)
	}
	return a.Package+a.To < b.Package+b.To
}

// sortedKeys returns a set's members in order.
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
