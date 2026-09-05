<!--
SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
SPDX-License-Identifier: GPL-3.0-only
-->

# Migrating from ghq

[ghq](https://github.com/x-motemen/ghq) gives every repository one
predictable path under one root. corralctl does too, and if that is all you
want from ghq, corral is a lateral move you can skip.

The reason to switch is what the layout is *for*.

## The difference that matters

ghq's path mirrors the remote:

```text
~/ghq/github.com/sebastienrousseau/corralctl
~/ghq/gitlab.com/acme/internal-tool
```

corralctl's mirrors how you think about the work:

```text
~/Code/Public/Go/corral
~/Code/Private/Python/internal-tool
~/Code/Forks/Rust/ripgrep
```

Host and owner are recoverable from `git remote` at any time, so
encoding them in the path buys little. Visibility and language are not
recoverable from anything, and they are what you actually navigate by —
"the private Python one", not "the one on gitlab.com under acme".

That is also what makes the layout useful to an agent: `Private/` is a
directory an agent can be told to leave alone, and `Go/` is a filter it
can apply without opening anything.

## Command equivalents

| ghq | corral |
| --- | --- |
| `ghq get <owner>/<repo>` | `corralctl <owner>` clones the owner's repositories; there is no single-repository fetch |
| `ghq list` | `corralctl status` |
| `ghq list --full-path` | `corralctl status --format json` |
| `ghq root` | `--base-dir`, or `base_dir` in the config file |
| `ghq look <repo>` | No direct equivalent; `corralctl status --format json` gives paths to `cd` into, or use the MCP server |
| — | `corralctl plan <owner>` — a dry run of the reconciliation, which ghq has no equivalent for |
| — | `corralctl exec -- <cmd>` — run a command in every organised clone |
| — | `corralctl mcp` — serve the workspace to an AI agent |

The gap in that table is deliberate: corral is owner-, topic- and
language-oriented, not repository-oriented. `corralctl <owner>` clones
everything that owner has, which is a different habit from `ghq get` one
at a time. If you want exactly one repository, `git clone` into the
right directory is the honest answer.

## Moving an existing ghq tree

There is no importer, and one would be the wrong shape: corral cannot
know which of your clones you consider private, or which language you
file a polyglot repository under. What it can do is take over from where
you are.

Point corral at the ghq root and see what it proposes, without changing
anything:

```bash
corralctl plan <owner> --base-dir ~/ghq
```

Most people find it cleaner to start a new root and let corral clone
into it, keeping the ghq tree until they stop reaching for it:

```bash
corralctl <owner> ~/Code --dry-run   # look first
corralctl <owner> ~/Code
```

Disk is cheaper than a migration that has to be right the first time.

## What you lose

- **`ghq get` for a single repository.** Covered above.
- **Arbitrary hosts.** ghq handles any remote. corralctl clones from
  GitHub, GitLab, Gitea, Forgejo, Codeberg and Bitbucket (`--forge`, `--forge-url`),
  which covers most of what ghq is used for but is not "any remote". Its
  *reading* has no such limit: the MCP server, `status`, `exec` and
  symbol lookup work against any clone in the workspace, whatever its
  origin, so a repository you cloned by hand from anywhere is a
  first-class citizen everywhere except `corralctl <owner>`.
- **`ghq look`.** There is no shell-integration subcommand.
