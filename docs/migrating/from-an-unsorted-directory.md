<!--
SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
SPDX-License-Identifier: GPL-3.0-only
-->

# Adopting corral over an unsorted `~/src`

The most common starting point: one directory, everything cloned into
it, no scheme.

```text
~/src/
  corral/
  some-fork-of-a-thing/
  work-service/
  scratch/
  another-persons-repo/
```

You do not have to move any of it, and you should not move all of it at
once.

## Step 1: read-only, over what you already have

corralctl's read side does not care about the layout. Point the MCP server
at the directory as it stands:

```bash
corralctl mcp --root ~/src
```

Every read tool works: listing, metadata, symbol lookup across all of
them, content search. `Visibility` and `Language` come out empty for
repositories that are not in `Visibility/Language/Repo` form, and
everything else is populated from the clone itself.

This is the cheapest way to find out whether the cross-repository tools
are worth anything to you, and it costs one command and no changes.

## Step 2: see what an organised layout would look like

```bash
corralctl plan <your-github-username>
```

`plan` changes nothing. It prints the reconciliation corral would
perform: what it would clone, what it already has, where each would go.

## Step 3: adopt the layout for new clones only

```bash
corralctl <your-github-username> ~/Code
```

Now `~/Code` is organised and `~/src` is untouched. Run the MCP server
against a parent of both if you want one view:

```bash
corralctl mcp --root ~
```

...though on a large home directory that is slow, and `--root` on a
directory holding both is better.

## Step 4: move the rest, when you feel like it

There is no import command, deliberately. corralctl cannot know which
clones you consider private, or which language a polyglot repository
belongs under, and guessing wrong would scatter your work into
directories you did not choose. Moving a repository is `mv`, and the
sidecar corral keeps lives inside `.git/`, so it moves with it.

## What the layout buys

```text
~/Code/
  Public/Go/corral
  Private/Python/work-service
  Forks/Rust/another-persons-repo
```

- `Private/` is a directory you can tell an agent to leave alone.
- `Go/` is a filter that needs nothing opened to apply.
- `Forks/` stops somebody else's code answering a question about yours.

None of that requires corral to keep running. It is a directory layout;
`ls` still works.
