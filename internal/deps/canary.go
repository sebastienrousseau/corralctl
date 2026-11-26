// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package deps

// Gate holds one fleet's merges in its sibling repositories until the
// canary repository, which folds the same bump first, has passed its
// release pull request's checks.
type Gate struct {
	// Fleet is the shared bump.
	Fleet Fleet `json:"fleet"`
	// Canary is the repository that folds the bump first.
	Canary string `json:"canary"`
	// Siblings are the other repositories' folds of the bump, each holding
	// only that fleet's merges (and the closes that go with them).
	Siblings []RepoFold `json:"siblings"`
}

// fleetKey identifies the bump a decision makes, as fleets do.
func fleetKey(pkg, to string) string { return pkg + "@" + to }

// Stage splits folds into what runs at once and what waits on a canary.
// For each fleet, the canary is a repository that folds the fleet's bump
// as a tier 1 merge, preferring one whose release branch is new
// (pickCanary); every other repository's merge
// of that bump moves into the fleet's Gate. A fleet with fewer than two
// such repositories has nothing to prove and no gate. first keeps every
// fold that still has a merge or a close.
func Stage(folds []RepoFold, fleets []Fleet) (first []RepoFold, gates []Gate) {
	canary, order := canaries(folds, fleets)
	held := map[string]map[string]*RepoFold{} // fleet key -> repo -> fold
	for _, f := range folds {
		kept := f
		kept.Merge = nil
		for _, d := range f.Merge {
			key := fleetKey(d.Bump.Package, d.Bump.To)
			c, gated := canary[key]
			if !gated || c == f.Repo {
				kept.Merge = append(kept.Merge, d)
				continue
			}
			hold(held, key, f, d)
		}
		if len(kept.Merge) > 0 || len(kept.Close) > 0 {
			first = append(first, kept)
		}
	}
	for _, fl := range order {
		key := fleetKey(fl.Package, fl.To)
		gates = append(gates, Gate{Fleet: fl, Canary: canary[key], Siblings: siblings(folds, held[key])})
	}
	return first, gates
}

// canaries picks each fleet's canary among the folds that merge the
// fleet's bump, when at least two do (pickCanary). order lists the gated
// fleets in the plan's order.
func canaries(folds []RepoFold, fleets []Fleet) (canary map[string]string, order []Fleet) {
	canary = map[string]string{}
	for _, fl := range fleets {
		key := fleetKey(fl.Package, fl.To)
		var members []RepoFold
		for _, f := range folds {
			if merges(f, key) {
				members = append(members, f)
			}
		}
		if len(members) >= 2 {
			canary[key] = pickCanary(members)
			order = append(order, fl)
		}
	}
	return canary, order
}

// pickCanary prefers the first member whose release branch this run
// creates: a fresh feat/v<next> carries only this run's folds, so its
// checks speak for the bump, where an existing branch may hold unreleased
// work that fails on its own. Without one, it takes the first member.
func pickCanary(members []RepoFold) string {
	for _, f := range members {
		if f.CreateBranch {
			return f.Repo
		}
	}
	return members[0].Repo
}

// merges reports whether f folds the bump key as a tier 1 merge.
func merges(f RepoFold, key string) bool {
	for _, d := range f.Merge {
		if fleetKey(d.Bump.Package, d.Bump.To) == key {
			return true
		}
	}
	return false
}

// hold moves decision d of fold f into the fleet key's held folds, keeping
// f's branch and release pull request so the sibling can be folded later.
func hold(held map[string]map[string]*RepoFold, key string, f RepoFold, d Decision) {
	if held[key] == nil {
		held[key] = map[string]*RepoFold{}
	}
	h := held[key][f.Repo]
	if h == nil {
		h = &RepoFold{Repo: f.Repo, Branch: f.Branch, Base: f.Base,
			CreateBranch: f.CreateBranch, OpenReleasePR: f.OpenReleasePR, ReleasePR: f.ReleasePR}
		held[key][f.Repo] = h
	}
	h.Merge = append(h.Merge, d)
}

// siblings returns the held folds in fold order.
func siblings(folds []RepoFold, held map[string]*RepoFold) []RepoFold {
	var out []RepoFold
	for _, f := range folds {
		if h := held[f.Repo]; h != nil {
			out = append(out, *h)
		}
	}
	return out
}
