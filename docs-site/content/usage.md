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

The command line is interpreted by `/bin/sh -c` on Unix and `cmd.exe /C` on
Windows, whatever `$SHELL` names, so it means the same thing for everyone. To
use another shell, name it in the command: `corralctl exec "fish -c '...'"`.

`--timeout 2m` gives each repository its own deadline (the default, `0`,
means none). A command that overruns is killed with the processes it started,
reported as timed out and counted as failed; the rest carry on.

`--output ndjson` writes one record per repository as it finishes, and
`--output json` one document with every record and a summary, in the same
shapes `sync` uses. A record carries `repo` (the path under the base
directory), `exit_code`, `duration_ms`, `stdout`, `stderr`, `timed_out` and,
when the command failed, `error`:

```bash
corralctl exec "git status --porcelain" --output ndjson | jq -r 'select(.stdout != "") | .repo'
```

The same filtering flags apply, so you can scope a command to one ecosystem,
one collection, or both. `--visibility` takes `all` or any top-level
collection of the layout: `public`, `private`, `work` or `forks`. `--dry-run` works here too, and lists the
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

One destination setting is changed. When a GitHub, Gitea, Forgejo or Codeberg
repository's default branch no longer exists locally — deleted upstream, say —
the push would prune it, and the forge would reject the whole push. sync first
pushes the local default branch (origin's default, else the checked-out
branch), makes it the destination's default, and reports
`default branch <old> -> <new>`. A default that still exists locally is left
alone. GitLab and Bitbucket destinations are pushed as before: Bitbucket
Cloud's API cannot change a repository's main branch, and GitLab protects
its default branch, so repointing it would not let the prune through.
Change the default in the forge's settings when sync reports the rejection.

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
unpushed commits, detached heads, disk budget, cache integrity, and stale linked worktrees:

```bash
# Run workspace health audit
corralctl doctor

# Refresh macOS Finder tags and folder color labels across all clones
corralctl doctor --refresh-tags

# Automatically prune stale and ephemeral linked worktrees
corralctl doctor --prune-worktrees

# Exclude large third-party trees or specific repositories
corralctl doctor --exclude "winget-pkgs,vendor"

# Set a per-repository audit deadline
corralctl doctor --timeout 30s

# Output as JSON for automated pipelines
corralctl doctor --output json
```

Custom rule thresholds can be declared in `.corral.json` at your workspace root
to configure warnings and limits for unpushed work, dirty working trees, disk usage,
and stale worktrees.

| Option | Default | Description |
| --- | --- | --- |
| `--refresh-tags` | off | Refresh macOS Finder tags and folder color labels for all workspace repositories |
| `--prune-worktrees` | off | Automatically prune stale and temporary linked worktrees |
| `--exclude` | | Comma-separated repository names or path patterns to exclude from audit |
| `--timeout` | `30s` | Deadline per repository check (0 to disable) |
| `--config` | | Path to custom `.corral.json` configuration file |
| `--output` | `text` | Output format: `text` or `json` |


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

## macOS Finder color tags and taxonomy
 
On macOS, corralctl automatically categorises repository folders using native Finder tags and color labels (`com.apple.metadata:_kMDItemUserTags` and `com.apple.FinderInfo`). This taxonomy visually signals repository state and makes Finder searches and Smart Folders intuitive across large collections:

| Color | Tag Name | Meaning & Trigger Signal |
| --- | --- | --- |
| 🔴 Red | `Needs Fix` | **Sync error or merge conflict**: Merge conflicts, diverged tracking branches, or sync failures requiring manual intervention. |
| 🟢 Green | `In-Flight` | **Active modifications in progress**: Uncommitted working tree changes, unpushed commits, or stashes. |
| 🟣 Purple | `Worktree` / `Experiment` | **Linked worktrees & experimental clones**: Active linked worktrees, upstream forks, templates, and mirrors. |
| 🔵 Blue | `Pristine` | **Clean, synced ground truth**: Clean repository, fully pushed, in sync with upstream. |
| 🟡 Yellow | `On Hold` | **Archived repository**: Upstream repository has been archived on the remote forge. |
| ⚪ Grey | `Dormant` | **Untouched reference repository**: Clean repository with no commits for over 60 days. |
| 🟠 Orange | `New Repo` | **Untracked or local repository**: Discovered by `corralctl adopt` without an origin remote or not yet published upstream. |

### Searching in Finder

You can search by tag in macOS Finder or Spotlight using `tag:"In-Flight"`, `tag:Pristine`, `tag:"New Repo"`, `tag:"Needs Fix"`, or `tag:Go`, or click on any tag color in the Finder sidebar. Existing user-defined tags are always preserved.


