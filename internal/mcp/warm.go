// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Background warming for the symbol cache.
//
// Sizing the cache correctly made a repeated query fast — measured on a
// 234-repository workspace, a warm corral_find_symbol went from ~60s to ~1s.
// It did nothing for the FIRST query, which still walks and stats every source
// file in every repository: ~29s on that workspace. Since entries expire after
// symbolCacheTTL, an agent that pauses between questions meets that cost again.
//
// The work itself cannot be avoided — the on-disk cache is keyed by a
// fingerprint computed from exactly that walk — but it does not have to happen
// while a client waits. Moving it off the request path is the difference
// between a tool that occasionally takes half a minute and one that is
// consistently around a second, which is what decides whether an agent times
// out and retries.
//
// Two constraints shape this:
//
//   - HTTP only. An HTTP server is long-lived and serves many sessions, so
//     paying the scan once in the background is clearly worth it. A stdio
//     server is started per session and may be asked nothing about symbols at
//     all; warming it would burn a workspace scan on every launch for no
//     benefit. ServeStdio deliberately does not call this.
//
//   - Activity-gated. Refreshing forever would keep a laptop busy long after
//     anyone stopped asking. After the initial pass, a refresh happens only if
//     a symbol query arrived recently, so an idle server settles to doing
//     nothing.

// warmIdleAfter is how long without a symbol query before refreshing stops.
//
// Long enough to cover thinking time between an agent's questions, short
// enough that a server left running overnight is not still scanning.
const warmIdleAfter = 10 * time.Minute

// warmInterval is how often the cache is topped up while in use. Half the TTL,
// so an entry is refreshed before it expires rather than after a client has
// already missed on it.
const warmInterval = symbolCacheTTL / 2

// noteSymbolQuery records that a client asked something a warmed cache serves.
// Called from the symbol tools and from the content search.
func (s *Server) noteSymbolQuery() {
	s.lastSymbolQuery.Store(time.Now().UnixNano())
}

// symbolQueryIdle reports whether nothing has asked for symbols recently.
func (s *Server) symbolQueryIdle() bool {
	last := s.lastSymbolQuery.Load()
	if last == 0 {
		return true
	}
	return time.Since(time.Unix(0, last)) > warmIdleAfter
}

// startSymbolWarmer warms the symbol cache in the background until ctx ends.
//
// The first pass always runs: it is what removes the cold start from the
// request path. Later passes run only while the server is being used.
//
// Returns a channel closed when the warmer has stopped, which is what lets a
// test wait for it rather than sleep.
func (s *Server) startSymbolWarmer(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.warmSymbols(ctx)
		ticker := time.NewTicker(warmInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if s.symbolQueryIdle() {
					continue
				}
				s.warmSymbols(ctx)
			}
		}
	}()
	return done
}

// warmSymbols extracts every repository once, populating both caches.
//
// Errors are ignored on purpose: this is speculative work, and a repository
// that cannot be read now will report its error when a client actually asks
// for it. Failing the warmer would only mean a slower query later.
func (s *Server) warmSymbols(ctx context.Context) {
	idx, err := s.scan()
	if err != nil {
		return
	}
	repos := idx.Repos
	if len(repos) == 0 {
		return
	}

	// The same bounded fan-out the query path uses. Going wider does not help:
	// each repository's own extraction is already parallel underneath, and the
	// work is dominated by waiting on the filesystem.
	workers := repoFanOut()
	if workers > len(repos) {
		workers = len(repos)
	}

	var (
		next atomic.Int64
		wg   sync.WaitGroup
	)
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= len(repos) || ctx.Err() != nil {
					return
				}
				// Build the content index first: it is what a search
				// needs, and a search is the more common question.
				s.indexFor(ctx, &repos[i])

				// Skip what is already fresh, so a refresh pass costs
				// nothing for the repositories that have not expired.
				if _, ok := s.symbolCache.get(repos[i].Path); ok {
					continue
				}
				//nolint:errcheck // speculative; a real error surfaces on the query path
				_, _ = s.symbolsFor(ctx, &repos[i])
			}
		}()
	}
	wg.Wait()
}
