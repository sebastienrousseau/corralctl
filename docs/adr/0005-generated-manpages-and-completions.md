<!-- SPDX-License-Identifier: GPL-3.0-only -->

# 0005 — Manpages and completions are generated, never committed

**Status:** Accepted · **Date:** 2026-09-02

## Context

corralctl ships a CLI, so packagers expect `corralctl.1` and shell completions
at FHS paths. The two ways to get them are to write and commit them, or to
render them from the cobra command tree at build time.

## Decision

Generate both from the live command tree via `scripts/gen_docs.go`, into
`build/`, which is git-ignored. Package them from there into every archive,
deb, rpm and `make install`.

## Consequences

A committed `.1` drifts from `--help` the first time a flag changes, and
nothing catches it — there is no test that compares prose to behaviour.
Deriving both from the same tree that serves `--help` makes drift
impossible rather than merely discouraged. CI renders every page with
`groff -ww` so a command-tree change that produces malformed roff fails
before a release rather than inside a tarball.

The roff is emitted directly rather than via cobra's `GenManTree`, which
would pull `cpuguy83/go-md2man` into `go.mod` for a build-time concern.
This module keeps eleven direct dependencies and a hand-maintained
`SBOM.md` that CI checks against `go.mod`; a dependency is not free.

Output goes to `build/`, deliberately not `dist/`: goreleaser owns that
directory and cleans it *after* running its before-hooks, which silently
deleted the generated pages before packaging when first wired up.

## What would make this wrong

If the manpages ever needed prose that the command tree cannot express —
worked examples, a FILES section, history — a hand-written page including a
generated options section would beat generating the whole thing.
