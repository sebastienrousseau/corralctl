---
author: "Sebastien Rousseau"
date: "2026-08-30"
language: "en-GB"
schema: "page"
changefreq: "weekly"
copyright_year: "2026"
locale_path: "/"
base_path: "/"
name: "corralctl"
short_name: "CO"
slug_install: "installation"
slug_usage: "usage"
slug_mcp: "mcp"
slug_ref: "reference"
nav_home: "Home"
nav_install: "Installation"
nav_usage: "Usage"
nav_mcp: "MCP Server"
nav_ref: "Reference"
label_skip: "Skip to main content"
label_menu: "Menu"
label_nav: "Main"
label_theme: "Theme"
label_theme_system: "System"
label_docs: "Documentation"
label_footer_nav: "Documentation"
label_docs_nav: "Documentation sections"
label_crumbs: "Breadcrumb"
label_pager: "Page"
label_prev: "Previous"
label_next: "Next"
label_toc: "On this page"
screenshot_alt: "corralctl organising repositories into a Finder-friendly directory hierarchy."
footer_note: "corralctl clones and organises repositories from six forges into a Finder-friendly hierarchy. Published under GPL-3.0-only."
copyright: "© 2026 Sebastien Rousseau. Licensed under GPL-3.0-only."
translation_key: "usage"
title: "Usage — corralctl"
description: "Positional arguments, the full flag reference, smart syncing, mirroring the tree to other forges with sync, and running a command across every clone with exec mode."
keywords: "corralctl flags, corralctl sync, mirror to gitlab, exec mode, smart syncing, dry run"
eyebrow: "Reference"
headline: "Usage"
lead: "corralctl takes an owner and converges your local tree to match. Everything else is a flag, and every flag has a default that works."
cur_install: ""
cur_usage: ' aria-current="page"'
cur_mcp: ""
cur_ref: ""
toc_1: "Running it"
toc_1_id: "running-it"
toc_2: "Flags"
toc_2_id: "flags"
toc_3: "Mirror to other forges"
toc_3_id: "mirror-to-other-forges"
prev_href: "/installation/"
prev_label: "Installation"
next_href: "/mcp/"
next_label: "MCP Server"
layout: "doc"
---

## Running it

```bash
corralctl clone <owner> [base_dir] [limit]
```

`clone` is the documented spelling; the bare form, `corralctl <owner>`, does
the same and keeps every existing script working. Every other operation is a
subcommand too: `sync`, `status`, `plan`, `prune`, `exec` and `mcp`.

`<owner>` is a GitHub username or organisation and is the only required
argument. `base_dir` defaults to `$HOME/Code`, and `limit` to 1000
repositories.

Authenticate first, either with the GitHub CLI or by setting `GITHUB_TOKEN`:

```bash
gh auth login
corralctl clone my-username
```

Nothing is written until you are happy with it — `--dry-run` prints what would
happen and stops.

```bash
corralctl clone my-username --dry-run
```

### Smart syncing

corralctl keeps a `.corral-state.json` sidecar next to each repository's `.git`
directory and compares the remote `pushed_at` against it. If nothing has been
pushed since the last run, `git pull` is skipped rather than attempted, which
is where the 10x to 50x speed-up on repeat runs comes from.

Pass `--force-sync` to pull regardless, or `--no-sync` to skip updates
entirely.

## Flags

| Option | Short | Default | Description |
| --- | --- | --- | --- |
| `--base-dir` | | `$HOME/Code` | Root directory for cloned repositories |
| `--limit` | `-l` | `1000` | Maximum repositories to fetch |
| `--concurrency` | `-c` | `1` | Concurrent workers |
| `--dry-run` | `-n` | off | Preview actions without making changes |
| `--orphans` | `-o` | off | Detect local repositories no longer on GitHub |
| `--protocol` | `-p` | `https` | Clone protocol: `ssh` or `https` |
| `--no-sync` | | off | Skip pulling changes for existing clones |
| `--force-sync` | | off | Pull regardless of cached state |
| `--layout` | | templated | Path layout for repositories |
| `--finder-tags` | | on (macOS) | Apply managed native Finder tags |
| `--interactive` | `-i` | off | Launch the selector TUI |
| `--recurse-submodules` | | off | Initialise submodules on clone and sync |
| `--output` | | `text` | Output format: `text`, `json`, `ndjson` |
| `--auth` | | `auto` | Auth mode: `auto`, `token`, `gh` |
| `--visibility` | | `all` | Filter: `all`, `public`, `private` |
| `--include-forks` | | on | Include forks under `Forks/` |
| `--include-archived` | | on | Include archived repositories, tagged On Hold |
| `--languages` | | | Comma-separated language filter |
| `--exclude-languages` | | | Comma-separated exclude list |
| `--clone-depth` | | `0` | Shallow clone depth; `0` disables |
| `--blobless` | | off | Partial clone with filter=blob:none |
| `--api-timeout` | | `30s` | Deadline for GitHub API operations |
| `--log-level` | | `info` | Verbosity on stderr: `error`, `warn`, `info`, `debug` |

Results go to stdout in whichever format `--output` selects. Diagnostics go to
stderr, so `--output json` stays machine-readable when you redirect it.

## Exec mode

`exec` runs a shell command across every clone that matches your filters,
concurrently:

```bash
corralctl exec "git status -s" --languages go,rust --visibility private
```

The same filtering flags apply, so you can scope a command to one ecosystem,
one visibility, or both. `--dry-run` works here too, and lists the
repositories a command would run against without running it.

## Mirror to other forges

`corralctl sync` pushes the organised tree out to other forges, creating each
destination repository when it is missing and bringing its branches and tags
to parity with the local clone. Branches are never forced.

```bash
export GITLAB_TOKEN=glpat-…
export GITEA_TOKEN=…

corralctl sync --to gitlab --to gitea@https://git.example.com --dry-run
corralctl sync --to gitlab --to gitea@https://git.example.com
```

A destination is `<forge>[:<owner>][@<url>]` — `gitlab`, `gitlab:my-group`,
`github:my-org`, `codeberg`, `bitbucket:workspace`, or
`gitea@https://git.example.com` for a self-hosted instance. The token for each
forge comes from the environment variable its own tooling uses, and over
HTTPS it authenticates the push too; pass `--protocol ssh` to use your keys.

| Option | Default | Description |
| --- | --- | --- |
| `--to` | | Destination; repeatable, at least one required |
| `--protocol` | `https` | Push transport: `https` or `ssh` |
| `--concurrency` | 4–8 | Repositories mirrored at once |
| `--timeout` | `5m` | Deadline for one repository on one destination |
| `--output` | `text` | `text`, `json` or `ndjson` |
| `--dry-run` | off | Report what would happen without creating or pushing |

Two things are refused. A repository is never pushed to the forge its origin
lives on, so a clone taken *from* GitLab is not pruned against GitLab. And a
destination that already holds a same-named repository with the other
visibility is an error rather than a silent reuse.

## Worktree management

`corralctl worktree` creates, lists, removes, and prunes linked Git worktrees
for repository clones across your workspace:

```bash
# List all linked worktrees across workspace repositories
corralctl worktree list

# Create an isolated worktree for a feature branch
corralctl worktree create my-repo feature-branch

# Remove a worktree
corralctl worktree remove my-repo /path/to/worktree

# Prune stale and temporary worktrees
corralctl worktree prune --dry-run
corralctl worktree prune --temp-only
```

| Option | Default | Description |
| --- | --- | --- |
| `--repo` | | Filter operation to a specific repository |
| `--dry-run` | off | Preview pruning without deleting files |
| `--temp-only` | off | Only prune ephemeral scratchpad worktrees |
| `--force` | off | Force removal even if uncommitted changes exist |
| `--output` | `text` | Output format: `text` or `json` |

## Multi-repository branching

`corralctl branch` coordinates Git branching operations across multiple
workspace repositories matching your criteria:

```bash
# List branches across repositories
corralctl branch list --language go --visibility public

# Create a feature branch across all Go repositories
corralctl branch create feat/new-api --language go --dry-run
corralctl branch create feat/new-api --language go

# Switch to the branch across all matching repositories
corralctl branch switch feat/new-api --language go
```

| Option | Default | Description |
| --- | --- | --- |
| `--repo` | | Target a specific repository |
| `--language` | | Filter repositories by language |
| `--visibility` | | Filter repositories by visibility |
| `--start-point` | HEAD | Starting point for new branch |
| `--dry-run` | off | Simulate operations without modifying repositories |
| `--all` | off | List remote branches as well as local branches |
| `--output` | `text` | Output format: `text` or `json` |

## Workspace health audits

`corralctl doctor` audits all clones in your workspace for uncommitted changes,
unpushed commits, detached heads, and stale linked worktrees:

```bash
# Run doctor audit
corralctl doctor

# Output as JSON for automated pipelines
corralctl doctor --json
```

Custom rule thresholds can be declared in `.corral.json` at your workspace root
to configure warnings and limits for unpushed work and stale worktrees.

## Adopting local repositories

`corralctl adopt` discovers untracked or unmanaged repositories on your filesystem,
optionally relocates them into your structured corral layout, provisions the remote
repository on your target forge (GitHub, GitLab, Gitea, Forgejo, Codeberg, Bitbucket),
and links the local repository's origin remote:

```bash
# Discover untracked repositories and run interactive selection
corralctl adopt ~/Projects --interactive

# Adopt into your workspace and provision on GitHub
corralctl adopt ~/Projects --forge github --owner sebastienrousseau --relocate --target-dir ~/Code

# Dry run adoption preview
corralctl adopt ~/Projects --dry-run
```

| Option | Default | Description |
| --- | --- | --- |
| `--forge` | | Target remote forge (github, gitlab, gitea, forgejo, codeberg, bitbucket) |
| `--forge-url` | | Base URL for self-hosted instances |
| `--owner` | | Remote namespace or user |
| `--visibility` | `private` | Visibility of created remote repo (`private`, `public`) |
| `--relocate` | off | Relocate local repository into structured corral workspace layout |
| `--collection` | | Target collection directory (`Public`, `Private`, `Forks`, `Work`) |
| `--target-dir` | | Destination root directory for relocation |
| `--max-depth` | `0` | Maximum traversal depth to discover repositories (0 for unlimited) |
| `--protocol` | `https` | Git transfer protocol (`https`, `ssh`) |
| `--interactive`, `-i` | off | Enable interactive Bubble Tea candidate selection wizard |
| `--untracked-only` | `true` | Only inspect untracked repositories |
| `--finder-tags` | on (macOS) | Apply managed macOS Finder Tags (e.g. Orange New Repo) |
| `--tag-only` | off | Flag untracked repositories in macOS Finder without moving files |
| `--dry-run` | off | Simulate operations without modifying local disk or remotes |
| `--output` | `text` | Output format: `text`, `json`, or `ndjson` |

