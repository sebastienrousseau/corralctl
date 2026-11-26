// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package deps

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
)

// tagPattern matches a release tag such as "v0.0.57".
var tagPattern = regexp.MustCompile(`^v(\d+)\.(\d+)\.(\d+)$`)

// NextVersion is the release after tag, under the versioning rule every
// repository follows: each release moves by 0.0.1, and a component that
// would reach 1000 rolls into the one above it, so v0.0.999 is followed by
// v0.1.0. A repository with no tag yet releases v0.0.1 first.
func NextVersion(tag string) (string, error) {
	if tag == "" {
		return "v0.0.1", nil
	}
	m := tagPattern.FindStringSubmatch(tag)
	if m == nil {
		return "", fmt.Errorf("%q is not a vX.Y.Z release tag", tag)
	}
	var v [3]int
	for i := range v {
		v[i], _ = strconv.Atoi(m[i+1]) // the pattern admits only digits
	}
	v[2]++
	for i := 2; i > 0 && v[i] == 1000; i-- {
		v[i] = 0
		v[i-1]++
	}
	return fmt.Sprintf("v%d.%d.%d", v[0], v[1], v[2]), nil
}

// RepoState is what applying a plan needs to know about one repository.
type RepoState struct {
	// Repo is "owner/name".
	Repo string `json:"repo"`
	// DefaultBranch is where the release pull request is opened against.
	DefaultBranch string `json:"default_branch"`
	// LatestTag is the newest vX.Y.Z tag, or "" when there is none.
	LatestTag string `json:"latest_tag"`
	// BranchExists reports whether feat/v<next> already exists.
	BranchExists bool `json:"branch_exists"`
	// ReleasePR is the open pull request from feat/v<next> into the default
	// branch, or 0 when there is none.
	ReleasePR int `json:"release_pr"`
}

// RepoFold is what applying a plan does in one repository: fold the kept
// updates into its release branch, through its one release pull request,
// and close the pull requests that are done with.
type RepoFold struct {
	// Repo is "owner/name".
	Repo string `json:"repo"`
	// Branch is the release branch, feat/v<next>.
	Branch string `json:"branch"`
	// Base is the default branch the release pull request targets.
	Base string `json:"base"`
	// CreateBranch is true when Branch has to be created from Base.
	CreateBranch bool `json:"create_branch"`
	// OpenReleasePR is true when no release pull request is open yet.
	OpenReleasePR bool `json:"open_release_pr"`
	// ReleasePR is the open release pull request, when there is one.
	ReleasePR int `json:"release_pr,omitempty"`
	// Merge are the tier 1 pull requests whose heads are merged into Branch.
	Merge []Decision `json:"merge,omitempty"`
	// Close are the tier 4 pull requests, closed with their reason.
	Close []Decision `json:"close,omitempty"`
}

// Folds turns a plan into one RepoFold per repository that has a tier 1 or
// tier 4 pull request, in repository order. Tier 2 and 3 pull requests are
// left for a person. A repository with tier 1 work needs its state; one
// whose latest tag is not a release tag is reported in skipped, not folded.
func Folds(p Plan, states map[string]RepoState) (folds []RepoFold, skipped []string) {
	for _, f := range byRepository(p.Decisions) {
		if len(f.Merge) > 0 {
			if err := f.target(states[f.Repo]); err != nil {
				skipped = append(skipped, fmt.Sprintf("%s: %v", f.Repo, err))
				continue
			}
		}
		folds = append(folds, *f)
	}
	sort.Slice(folds, func(i, j int) bool { return folds[i].Repo < folds[j].Repo })
	sort.Strings(skipped)
	return folds, skipped
}

// byRepository groups the tier 1 decisions to merge and the tier 4 ones to
// close by repository; other tiers are left out.
func byRepository(ds []Decision) map[string]*RepoFold {
	byRepo := map[string]*RepoFold{}
	for _, d := range ds {
		if d.Tier != TierFast && d.Tier != TierClose {
			continue
		}
		f := byRepo[d.PR.Repo]
		if f == nil {
			f = &RepoFold{Repo: d.PR.Repo}
			byRepo[d.PR.Repo] = f
		}
		if d.Tier == TierFast {
			f.Merge = append(f.Merge, d)
		} else {
			f.Close = append(f.Close, d)
		}
	}
	return byRepo
}

// target fills in the release branch and pull request from the state.
func (f *RepoFold) target(s RepoState) error {
	if s.DefaultBranch == "" {
		return fmt.Errorf("no repository state")
	}
	next, err := NextVersion(s.LatestTag)
	if err != nil {
		return err
	}
	f.Branch, f.Base = "feat/"+next, s.DefaultBranch
	f.CreateBranch = !s.BranchExists
	f.ReleasePR = s.ReleasePR
	f.OpenReleasePR = s.ReleasePR == 0
	return nil
}
