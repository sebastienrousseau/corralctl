//go:build ignore

// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"fmt"
	"log"

	"github.com/sebastienrousseau/corralctl/internal/github"
)

func main() {
	ctx := context.Background()

	opts := github.FetchOptions{
		Limit:            5,
		Visibility:       "public",
		Type:             "sources",
		Sort:             "stars",
		IncludeLanguages: []string{"Go"},
		AuthMode:         github.AuthModeAuto,
	}

	fmt.Println("Fetching public Go repositories for 'sebastienrousseau'...")

	repos, err := github.FetchReposWithOptions(ctx, "sebastienrousseau", opts)
	if err != nil {
		log.Fatalf("Error fetching repositories: %v", err)
	}

	fmt.Printf("\nFetched %d repositories successfully:\n", len(repos))
	for _, r := range repos {
		fmt.Printf(" - Name: %s | Language: %s | Visibility: %s | Stars: %d\n",
			r.Name, r.Language, r.Visibility, r.Stars)
	}
}
