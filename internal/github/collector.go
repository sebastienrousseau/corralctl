// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package github

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	gh "github.com/google/go-github/v90/github"
)

// perPage is the listing page size. GitHub's maximum, so a large
// organisation costs the fewest possible round-trips.
const perPage = 100

// maxConcurrentPageFetches bounds how many listing pages are requested at
// once. Five keeps a large organisation responsive without tripping GitHub's
// secondary rate limits.
//
// A var rather than a const so a test can shrink it to zero, which makes the
// semaphore unacquirable and the cancellation branch in collectConcurrently
// deterministic instead of a scheduling race.
var maxConcurrentPageFetches = 5

// repoCollector accumulates mapped repositories, applying the language and
// visibility filters and stopping at the requested limit.
type repoCollector struct {
	repos       []Repo
	limit       int
	includeLang map[string]struct{}
	excludeLang map[string]struct{}
	opts        FetchOptions
}

// wants reports whether the collector can still take more repositories.
func (c *repoCollector) wants() bool {
	return c.limit == 0 || len(c.repos) < c.limit
}

// remaining reports how many more repositories the collector can accept,
// or 0 when it is unlimited. Used to decide how many pages are worth
// fetching at all.
func (c *repoCollector) remaining() int {
	if c.limit == 0 {
		return 0
	}
	if n := c.limit - len(c.repos); n > 0 {
		return n
	}
	return 0
}

// absorb maps and filters one page of API results into the collection.
func (c *repoCollector) absorb(page []*gh.Repository) {
	for _, r := range page {
		if !c.wants() {
			return
		}
		c.take(mapRepository(r))
	}
}

// absorbMapped filters an already-mapped page into the collection. The
// concurrent fetch maps inside its workers so it never holds a page of
// go-github Repository values longer than it must.
func (c *repoCollector) absorbMapped(page []Repo) {
	for _, repo := range page {
		if !c.wants() {
			return
		}
		c.take(repo)
	}
}

// take applies the filters and keeps the repository if it passes.
func (c *repoCollector) take(repo Repo) {
	if !matchesFilters(repo, c.includeLang, c.excludeLang, c.opts) {
		return
	}
	c.repos = append(c.repos, repo)
}

// collectConcurrently fetches pages 2..lastPage in parallel, bounded to five
// in flight, and absorbs them in page order so the result stays
// deterministic regardless of completion order.
func collectConcurrently(ctx context.Context, owner string, fetchPage pageFetcher, lastPage int, collector *repoCollector) error {
	if collector.limit > 0 {
		want := collector.remaining()
		if want == 0 {
			return nil
		}
		needed := (want+perPage-1)/perPage + 1
		if capped := 1 + needed; capped < lastPage {
			lastPage = capped
		}
	}

	var (
		mu         sync.Mutex
		errs       []error
		resultsMap = make(map[int][]Repo)
		sem        = make(chan struct{}, maxConcurrentPageFetches)
		wg         sync.WaitGroup
	)

	for p := 2; p <= lastPage; p++ {
		wg.Add(1)
		go func(pNum int) {
			defer wg.Done()
			if err := acquirePageSlot(ctx, sem); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				return
			}
			defer func() { <-sem }()

			pRepos, _, pErr := fetchPage(pNum)

			var mapped []Repo
			if pErr == nil {
				mapped = make([]Repo, 0, len(pRepos))
				for _, r := range pRepos {
					mapped = append(mapped, mapRepository(r))
				}
			}

			mu.Lock()
			defer mu.Unlock()
			if pErr != nil {
				errs = append(errs, pErr)
				return
			}
			resultsMap[pNum] = mapped
		}(p)
	}
	wg.Wait()

	if len(errs) > 0 {
		return fmt.Errorf("failed listing repositories for '%s' (concurrent fetch failed): %w", owner, errs[0])
	}

	for p := 2; p <= lastPage; p++ {
		collector.absorbMapped(resultsMap[p])
	}
	return nil
}

// collectSequentially walks the NextPage chain from firstPage until the
// listing is exhausted or the collector is full.
func collectSequentially(owner string, fetchPage pageFetcher, firstPage int, collector *repoCollector) error {
	page := firstPage
	for {
		pRepos, pResp, pErr := fetchPage(page)
		if pErr != nil {
			return fmt.Errorf("failed listing repositories for '%s' (fallback fetch failed): %w", owner, pErr)
		}
		collector.absorb(pRepos)
		if pResp.NextPage == 0 || !collector.wants() {
			return nil
		}
		page = pResp.NextPage
	}
}

// sortRepos applies the requested post-fetch ordering in place. An unknown
// or empty sort key leaves the API's own ordering untouched.
func sortRepos(repos []Repo, key string) {
	switch strings.ToLower(key) {
	case "name":
		sort.Slice(repos, func(i, j int) bool {
			return strings.ToLower(repos[i].Name) < strings.ToLower(repos[j].Name)
		})
	case "stars":
		sort.Slice(repos, func(i, j int) bool {
			return repos[i].Stars > repos[j].Stars
		})
	case "last updated", "updated":
		sort.Slice(repos, func(i, j int) bool {
			return repos[i].PushedAt.After(repos[j].PushedAt)
		})
	}
}

func acquirePageSlot(ctx context.Context, sem chan struct{}) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case sem <- struct{}{}:
		return nil
	}
}

func toLookupSet(values []string) map[string]struct{} {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(values))
	for _, v := range values {
		norm := strings.ToLower(strings.TrimSpace(v))
		if norm == "" {
			continue
		}
		out[norm] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func matchesFilters(repo Repo, includeLang, excludeLang map[string]struct{}, opts FetchOptions) bool {
	if !opts.IncludeForks && repo.Fork {
		return false
	}
	if !opts.IncludeArchived && repo.Archived {
		return false
	}
	if opts.Visibility == "public" && repo.Visibility != "Public" {
		return false
	}
	if opts.Visibility == "private" && repo.Visibility != "Private" {
		return false
	}

	if opts.Type != "" {
		switch strings.ToLower(opts.Type) {
		case "public":
			if repo.Visibility != "Public" {
				return false
			}
		case "private":
			if repo.Visibility != "Private" {
				return false
			}
		case "sources":
			if repo.Fork {
				return false
			}
		case "forks":
			if !repo.Fork {
				return false
			}
		case "archived":
			if !repo.Archived {
				return false
			}
		case "can be sponsored", "sponsored":
			return repo.CanBeSponsored
		case "mirrors":
			if !repo.IsMirror {
				return false
			}
		case "templates":
			if !repo.IsTemplate {
				return false
			}
		}
	}

	lang := strings.ToLower(strings.TrimSpace(repo.Language))
	if includeLang != nil {
		if _, ok := includeLang[lang]; !ok {
			return false
		}
	}
	if excludeLang != nil {
		if _, ok := excludeLang[lang]; ok {
			return false
		}
	}

	return true
}

func mapRepository(r *gh.Repository) Repo {
	owner := r.GetOwner().GetLogin()
	fullName := r.GetFullName()
	if fullName == "" && owner != "" && r.GetName() != "" {
		fullName = owner + "/" + r.GetName()
	}
	lang := "Other"
	if r.Language != nil {
		lang = *r.Language
	}

	visibility := "Public"
	if r.GetPrivate() {
		visibility = "Private"
	}
	if r.Visibility != nil && (*r.Visibility == "private" || *r.Visibility == "internal") {
		visibility = "Private"
	}

	defaultBranch := "main"
	if r.DefaultBranch != nil {
		defaultBranch = *r.DefaultBranch
	}

	cloneURL := ""
	if r.CloneURL != nil {
		cloneURL = *r.CloneURL
	}
	sshURL := ""
	if r.SSHURL != nil {
		sshURL = *r.SSHURL
	}

	return Repo{
		ID:             r.GetID(),
		Owner:          owner,
		FullName:       fullName,
		Name:           r.GetName(),
		Language:       lang,
		Visibility:     visibility,
		DefaultBranch:  defaultBranch,
		CloneURL:       cloneURL,
		SSHURL:         sshURL,
		Fork:           r.GetFork(),
		Archived:       r.GetArchived(),
		PushedAt:       r.GetPushedAt().Time,
		Stars:          r.GetStargazersCount(),
		IsTemplate:     r.GetIsTemplate(),
		IsMirror:       r.GetMirrorURL() != "",
		CanBeSponsored: false,
	}
}
