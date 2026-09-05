<!--
SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
SPDX-License-Identifier: GPL-3.0-only
-->

# Migrating from a hand-written clone script

Almost everyone who ends up here has a version of this in their
dotfiles:

```bash
gh repo list "$OWNER" --limit 200 --json name,sshUrl |
  jq -r '.[] | .sshUrl' |
  while read -r url; do
    git clone "$url" || git -C "$(basename "$url" .git)" pull
  done
```

It works. corralctl is that script with the cases it does not handle.

## What the script gets wrong

Not hypotheticals — these are the failures that motivated corral.

- **`--limit 200`.** GitHub paginates. A script with a limit silently
  stops at it, and the repository you were looking for is the 201st.
- **`git pull` on a dirty tree.** Fails, prints an error somewhere in
  five hundred lines of output, and the loop continues. You find out
  weeks later.
- **A repository with no commits.** `git pull` fails with "couldn't find
  remote ref HEAD", which reads like a network problem and is not.
- **Rate limits.** A loop over three hundred repositories hits them, and
  the failures look like network flakiness rather than a budget you
  exhausted.
- **The flat directory.** Every clone in one directory, sorted by
  whatever the owner happened to name them.
- **Deleting.** Eventually you write the pruning half, and eventually it
  deletes something that only existed locally.

## The equivalent

```bash
corralctl <owner>
```

Paginated properly, concurrent, rate-limit aware with backoff, organised
into `Visibility/Language/Repo`, and it skips rather than fails on the
repositories that break the script above.

To see what it would do first:

```bash
corralctl plan <owner>
```

## Keep the parts of your script that were yours

The script probably did something after cloning. That is `exec`:

```bash
corralctl exec -- git status --short
corralctl exec -- go mod tidy
```

And if your script ran on a schedule over several owners, that is a
profile:

```bash
corralctl config init
corralctl profile
```

## The part you did not write

Two things a script generally does not have:

**Deletion that refuses.** `corralctl prune` removes clones whose
upstream is gone — but it refuses when a clone holds uncommitted
changes, commits not reachable from any remote, stashes, local-only
tags, gitignored files (a `.env`, a local database — invisible to `git
status`, and the least recoverable thing in a clone), or submodules with
unpublished work. Each refusal names its reason.

**An MCP server.** `corralctl mcp` serves the workspace to Claude Code,
Cursor, Cline and anything else that speaks MCP: which repositories
exist, what state they are in, where a symbol is defined across all of
them at once. This is the thing a script cannot become.

## What to do with the script

Keep it until `corralctl plan` output stops surprising you. Then delete
it, and delete the pruning half first.
