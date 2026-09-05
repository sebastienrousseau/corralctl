<!-- SPDX-License-Identifier: GPL-3.0-only -->

# Architecture Decision Records

Decisions that will be questioned later, with the reasoning that produced
them. Each record states what was decided, what it cost, and what would
make it wrong — so a future change can tell "this was considered and
rejected" apart from "nobody thought about it".

Records 0003 and 0004 describe decisions whose implementation lands in
[PR #107](https://github.com/sebastienrousseau/corralctl/pull/107); this
branch should merge after it. The reasoning is recorded here either way —
an ADR documents a decision, not a diff.

Records are immutable once merged. A decision that changes gets a new
record superseding the old one, not an edit.

A record whose *reasoning* turns out to be overstated is the one case that
is corrected in place, in a dated section that leaves the original claim
visible — 0006 carries one. Rewriting the argument silently would defeat
the point of writing it down; removing the record would lose the fact that
the decision was made on a weaker basis than it appeared.

| # | Decision | Status |
|---|---|---|
| [0001](0001-sidecar-lives-in-the-git-directory.md) | The sync sidecar lives inside `.git/` | Accepted |
| [0002](0002-github-package-is-a-leaf.md) | `internal/github` imports nothing internal | Accepted |
| [0003](0003-sanitize-on-output-not-construction.md) | Untrusted strings are sanitised on output, not at construction | Accepted |
| [0004](0004-file-resource-allowlist.md) | The MCP file resource serves by allowlist | Accepted |
| [0005](0005-generated-manpages-and-completions.md) | Manpages and completions are generated, never committed | Accepted |
| [0006](0006-symbol-extraction-without-cgo.md) | Symbol extraction uses `go/ast`, not tree-sitter | Accepted, corrected |
