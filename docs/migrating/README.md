<!--
SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
SPDX-License-Identifier: GPL-3.0-only
-->

# Migrating to corral

corralctl does two things that are usually separate tools: it organises the
repositories you clone, and it serves them to AI coding agents over MCP.
Most people arrive from a tool that does one of those, so these guides
are written per tool rather than as one document.

| Coming from | What carries over | What is genuinely different |
| --- | --- | --- |
| [ghq](from-ghq.md) | The idea of one root and a predictable path per repository | corralctl's path encodes visibility and language, not the host and owner |
| [a hand-written clone script](from-a-script.md) | Everything — corral is the script, with the edge cases handled | Refusal to destroy unpublished work; an audit log; agents can query it |
| [a single-repository code index](from-a-code-index.md) | Symbol lookup, file reading | The lookup spans every clone on the machine, not the one that is open |
| [nothing — an unsorted `~/src`](from-an-unsorted-directory.md) | — | Adopting a layout without re-cloning anything |

## What corral will not do

Stated here so a migration does not end in disappointment:

- **It is not a git wrapper.** It clones, pulls and organises. It does
  not branch, commit, rebase or push, and it never will — `git` is
  better at that than any wrapper.
- **It is not a code intelligence server.** Symbol lookup answers "where
  is this declared" across every clone. It does not resolve types, find
  references, or rename. An LSP does those, for one project at a time.
- **It does not sync anything to a server.** Every read is local. The
  GitHub API is contacted only when you ask it to clone or list.

## Before you start

Nothing here requires re-cloning, and nothing requires a decision you
cannot reverse.

Every command that touches disk takes `--dry-run`, and `corralctl plan`
exists to show you the reconciliation without performing it. Start
there. If you decide against corral afterwards, what you are left with
is ordinary clones in ordinary directories — there is no database, no
lock file, and nothing to uninstall beyond the binary.
