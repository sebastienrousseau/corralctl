// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package github_test

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/github"
)

// ExampleFetchReposWithOptions demonstrates fetching only private, non-fork,
// non-archived Go repositories using GitHub CLI credentials, with bounded
// exponential-backoff retries on transient API failures.
func ExampleFetchReposWithOptions() {
	ctx := context.Background()
	repos, err := github.FetchReposWithOptions(ctx, "sebastienrousseau", github.FetchOptions{
		Limit:            100,
		Visibility:       "private",
		IncludeForks:     false,
		IncludeArchived:  false,
		IncludeLanguages: []string{"go"},
		ExcludeLanguages: []string{"makefile"},
		AuthMode:         github.AuthModeGH,
		RetryMax:         4,
		RetryMinBackoff:  500 * time.Millisecond,
		RetryMaxBackoff:  8 * time.Second,
	})
	if err != nil {
		log.Fatal(err)
	}
	for _, r := range repos {
		fmt.Printf("%s (%s, %s)\n", r.Name, r.Visibility, r.Language)
	}
}

// ExampleFetchRepos demonstrates the simplest fetch: every repository for an
// owner, resolving credentials automatically from the environment or gh CLI.
func ExampleFetchRepos() {
	ctx := context.Background()
	repos, err := github.FetchRepos(ctx, "sebastienrousseau", 1000)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("fetched %d repositories\n", len(repos))
}
