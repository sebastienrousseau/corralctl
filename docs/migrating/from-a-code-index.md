<!--
SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
SPDX-License-Identifier: GPL-3.0-only
-->

# Migrating from a single-repository code index

If you already give your agent a code-intelligence server — an LSP
bridge, a repository indexer, a codebase-aware MCP server — corral is
not a replacement for it. It answers a different question, and the two
are better together than either is alone.

## The question each one answers

A single-repository index answers **"where is this, in the project I
have open"**, and it answers it well: types resolved, references found,
renames safe.

corralctl answers **"which of the two hundred repositories on this machine
is this in"**. No project has to be open. Nothing has to be configured
per repository.

That question has no answer from a single-repository tool, because the
premise of a single-repository tool is that you already know which
repository. In practice you often do not:

- "We wrote a retry helper for this. Which service was it in?"
- "This error string is in a ticket. Where does it come from?"
- "Which of our repositories still import the old auth client?"

## What corral gives an agent

| Tool | Question |
| --- | --- |
| `corral_find_symbol` | Where is this declared, across *every* clone |
| `corral_search_code` | Where does this text appear, across *every* clone |
| `corral_list_repos` | Which repositories exist, by visibility, language, sync state |
| `corral_repo_overview` | What shape is this repository, in one call |
| `corral_get_repo_metadata` | Current branch, origin, last sync |

Indexed languages for symbol lookup: Go, Python, TypeScript, JavaScript,
Rust.

## What corral does not give it

Stated plainly, because a tool that oversells is worse than one that
does not exist:

- **No type resolution.** Go is parsed with `go/ast`; the rest are read
  by a line scanner in the tradition of ctags. It knows `Parse` is
  declared at `parser.go:41`. It does not know which `Parse`.
- **No references, no call graph, no rename.** Declarations and text
  matches only.
- **No incremental index.** Extraction is cached between runs and
  invalidated by a cheap fingerprint, but there is no watcher and no
  language server protocol.

If you need those, keep the tool that provides them. corralctl is the layer
above it: it tells the agent *which repository to open*, and the
existing tool takes over from there.

## Running both

They do not conflict. corralctl is read-only by default, makes no network
calls, and holds no lock on anything. Add it alongside:

```bash
claude mcp add corral -- corralctl mcp
```

A useful division of labour to put in your agent's instructions: reach
for corral when the repository is not yet known, and for the
project-scoped index once it is.

## If you were considering building this

The reason a single-repository index cannot simply be pointed at a
parent directory is that it has no model of what the directories *are* —
which are yours, which are forks, which are private, which are vendored
copies of somebody else's code. corralctl's layout carries exactly that,
which is why the cross-repository lookup is a few hundred lines rather
than a project.
