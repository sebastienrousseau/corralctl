//go:build ignore

// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/github"
	"github.com/sebastienrousseau/corralctl/internal/tui"
)

func main() {
	opts := github.FetchOptions{
		Limit:      10,
		Visibility: "public",
		Type:       "sources",
		Sort:       "stars",
		AuthMode:   github.AuthModeAuto,
	}

	mockFetch := func() ([]github.Repo, error) {
		return []github.Repo{
			{
				Name:       "corral",
				Language:   "Go",
				Visibility: "Public",
				CloneURL:   "https://github.com/sebastienrousseau/corralctl.git",
				PushedAt:   time.Now(),
				Stars:      120,
			},
			{
				Name:       "openclaw",
				Language:   "C++",
				Visibility: "Public",
				CloneURL:   "https://github.com/openclaw/openclaw.git",
				PushedAt:   time.Now().Add(-24 * time.Hour),
				Stars:      450,
			},
		}, nil
	}

	fmt.Println("Starting interactive repository selector...")
	ctx := context.Background()

	selected, ok, err := tui.RunSelector(ctx, "sebastienrousseau", opts, mockFetch)
	if err != nil {
		log.Fatalf("Selector error: %v", err)
	}

	if !ok {
		fmt.Println("Selector cancelled silently by user.")
		os.Exit(0)
	}

	fmt.Printf("\nSelected %d repositories:\n", len(selected))
	for _, r := range selected {
		fmt.Printf(" - %s (%s, %d stars)\n", r.Name, r.Language, r.Stars)
	}
}
