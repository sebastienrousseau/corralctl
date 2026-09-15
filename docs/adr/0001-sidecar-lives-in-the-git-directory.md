<!-- SPDX-License-Identifier: GPL-3.0-only -->

# 0001 — The sync sidecar lives inside `.git/`

**Status:** Accepted · **Date:** 2026-08-17 (v0.0.20)

## Context

corralctl skips a `git pull` when the upstream `pushed_at` has not advanced
since the last successful sync. That requires remembering, per clone, what
`pushed_at` was last seen — a small piece of state that has to live beside
the clone.

The first implementation wrote `.corral-state.json` into the working tree.

## Decision

The sidecar lives at `<repo>/.git/corral-state.json`, resolved through
`git.Dir()` so linked worktrees follow their `gitdir:` pointer.

## Consequences

A file in the working tree shows up in `git status` for every repository
corralctl manages. Users would then either commit a tool's cache file or add
it to a `.gitignore` they do not control — in someone else's repository,
neither is acceptable. Inside the git directory it is invisible to status,
diff and clean, which is where a tool's private per-clone state belongs.

The cost is that the sidecar does not survive a re-clone. That is correct:
a fresh clone has no sync history to remember.

The pre-v0.0.20 working-tree location is still *read* so clones written by
an older `corralctl` report accurate state, and is removed on the next
successful sync.

## What would make this wrong

If corralctl ever needed the state to be shared between machines, or to be
visible to other tooling, a git-directory file would be the wrong home.
Neither is true today: the sidecar is an optimisation, and a missing one
falls back to always pulling.
