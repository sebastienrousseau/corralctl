// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package engine_test

import (
	"context"

	"github.com/sebastienrousseau/corralctl/internal/engine"
	"github.com/sebastienrousseau/corralctl/internal/git"
	"github.com/sebastienrousseau/corralctl/internal/github"
)

// ExampleRun demonstrates a complete run: clone and organise every public Go and
// Rust repository for an owner over SSH using shallow single-branch clones,
// syncing existing checkouts, reporting orphaned local repositories, and emitting
// a machine-readable JSON summary.
func ExampleRun() {
	ctx := context.Background()
	engine.Run(ctx, engine.RunOptions{
		Owner:       "sebastienrousseau",
		BaseDir:     "/home/me/Code",
		Concurrency: 8,
		DoSync:      true,
		Orphans:     true,
		Protocol:    "ssh",
		Output:      engine.OutputJSON,
		Fetch: github.FetchOptions{
			Limit:            500,
			Visibility:       "public",
			IncludeLanguages: []string{"go", "rust"},
			AuthMode:         github.AuthModeAuto,
		},
		Clone: git.CloneOptions{
			SingleBranch: true,
			Depth:        1,
		},
		Sync: engine.SyncOptions{
			Force: false, // set to true to bypass cache and force pull
		},
	})
}

// ExampleRun_dryRun demonstrates previewing actions without making any changes,
// streaming one NDJSON record per repository.
func ExampleRun_dryRun() {
	ctx := context.Background()
	engine.Run(ctx, engine.RunOptions{
		Owner:       "sebastienrousseau",
		BaseDir:     "/home/me/Code",
		Concurrency: 1,
		DryRun:      true,
		Protocol:    "https",
		Output:      engine.OutputNDJSON,
		Fetch:       github.FetchOptions{Limit: 1000},
	})
}
