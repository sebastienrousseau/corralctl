// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"sync"
	"sync/atomic"

	"github.com/sebastienrousseau/corralctl/internal/engine"
	corralmcp "github.com/sebastienrousseau/corralctl/internal/mcp"
)

var doctorRefreshFinderTags = engine.RefreshRepoFinderTags

// refreshWorkspaceFinderTags audits local git posture and updates Finder tags
// and folder colors concurrently across all discovered repositories.
func refreshWorkspaceFinderTags(repos []corralmcp.RepoEntry) int {
	if len(repos) == 0 {
		return 0
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	var refreshedCount int64

	for _, repo := range repos {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if err := doctorRefreshFinderTags(path); err == nil {
				atomic.AddInt64(&refreshedCount, 1)
			}
		}(repo.Path)
	}

	wg.Wait()
	return int(refreshedCount)
}
