<!-- SPDX-License-Identifier: GPL-3.0-only -->

# Architecture

How corralctl works, for people changing it. For how to build and test it see
[DEVELOPMENT.md](../DEVELOPMENT.md); for the threat model see
[security-model.md](security-model.md).

## The shape of it

corralctl does two things that share one data model:

1. **Reconcile** a GitHub owner's repositories against a local directory
   tree — clone what is missing, sync what is stale, relocate what moved.
2. **Serve** that tree to AI coding agents over the Model Context Protocol,
   locally and without touching the network.

The second is why the first exists. GitHub's own MCP server already covers
the remote API; the dimension only corralctl can serve is a developer's
already-cloned local mirror.

## Package layout

```text
cmd/corralctl        main(); nothing but a call into cmd
cmd/                 cobra commands, flag validation, config file
internal/engine      the reconciliation loop: workers, layout, migration
internal/forge       one adapter per forge: list an owner (Forge) and hold a mirror (Target)
internal/mirror      `corralctl sync`: walk the tree, guard, push in parallel
internal/github      GitHub API client, paging, retry, token resolution
internal/git         every `git` subprocess, credentials, remote identity
internal/tui         Bubble Tea progress view and interactive selector
internal/mcp         the MCP server: index, tools, resources, audit
internal/diag        levelled diagnostics, always to stderr
internal/sanitize    bounds and de-fangs untrusted strings
internal/symbols     declaration extraction: where a symbol is defined
```

Dependencies run one way:

```text
cmd ──► engine ──► github
  │        ├────► git ◄──── mcp
  │        └────► tui
  └──────────────► mcp
```

`internal/github` imports nothing internal and stays a leaf deliberately —
see [ADR-0002](adr/0002-github-package-is-a-leaf.md).

## The reconciliation loop

`engine.RunE` is the whole flow. Reading it top to bottom is the fastest way
to understand corralctl.

```text
normalize options
  └─ one place a bad option becomes an error, so CLI, MCP and embedders
     all reject the same inputs for the same reasons

resolve repositories
  ├─ interactive?  TUI selector
  └─ otherwise     paginated GitHub fetch (5 pages in flight, ordered)

prepare workspace          (classic layout only, never on --dry-run)
  ├─ create collections    Public / Private / Work / Forks
  ├─ migrate legacy paths  identity-verified, never by name alone
  └─ normalise directory case for APFS/HFS+

execute jobs
  ├─ discover existing clones, keyed by canonical remote identity
  ├─ N workers, each: relocate → sync-or-clone → Finder tags
  └─ one consumer drains results into the chosen output format

report
  ├─ orphan detection (skipped when cancelled: the tree is mid-clone)
  ├─ aggregate JSON, or streamed NDJSON, or text
  └─ exit status: 0 clean, 1 failures, 130 cancelled
```

Two invariants hold throughout:

- **Every send and receive is guarded by `ctx`.** A cancelled run unwinds
  rather than blocking on a full channel, and `summary.Canceled` tells a
  scripted consumer the result set is partial.
- **No error escapes `processRepo`.** Every outcome is a `RepoResult`, so
  one repository failing never aborts the run.

## Identity

The single most load-bearing concept. `git.CanonicalRemote` normalises any
remote URL to `host/path`:

```text
https://github.com/acme/api.git       ─┐
git@github.com:acme/api.git           ─┼─► github.com/acme/api
https://github.com/acme/api           ─┘

https://gitlab.com/group/sub/api.git  ───► gitlab.com/group/sub/api
```

That value decides whether two clones are the same repository, and
therefore gates every destructive operation:

| Operation | Refuses when |
|---|---|
| `migrateLegacy` | the directory's origin is not this repository's |
| `processExistingClone` | the target's origin is not what we mean to sync |
| `prune` | the local remote does not resolve under this owner |
| `corral_delete_repo` | anything unpublished exists in the clone |

Comparing remote URLs with `strings.Contains` instead is how a `gitlab.com`
clone gets counted as a GitHub orphan.

## Layout

The on-disk shape is a Go text template, defaulting to:

```text
{{.Collection}}/{{.Bucket}}/{{.Name}}     →  Public/Go/corral
```

`Collection` is Forks / Private / Public; `Bucket` is the canonicalised
language. The template is validated at flag-parse time so a typo fails
before a paginated fetch, and the rendered path is rejected if it escapes
the base directory.

## The MCP server

Stdio JSON-RPC. Seven read tools and three write tools, the write side gated
behind `--enable-mutations` and deletion behind a second flag.

```text
Scan(root)  ──►  Index{ Repos []RepoEntry }   cached for scanTTL
                    │
                    ├─ tools:     list, find, metadata, summary, index
                    ├─ symbols:   find_symbol, repo_overview
                    ├─ resources: workspace index, repo state, tree, file
                    └─ mutations: sync, clone, delete   (audited, gated)
```

## The symbol index

`corral_find_symbol` is the one thing this server can do that a
single-repository code index cannot: resolve a declaration across every
clone on the machine at once.

```text
RepoEntry.Path ──► symbols.ExtractRepo ──► []Symbol   cached, 2-minute TTL
                     │                                 bounded to 24 repos
                     ├─ walk:    serial, skips vendor/node_modules/generated
                     └─ parse:   concurrent, GOMAXPROCS workers
```

Three properties are load-bearing:

- **Definitions only.** A symbol is a name, a kind and a location. The agent
  reads the file; this tells it which file and which line. A call graph
  would be an order of magnitude more to build and to keep correct.
- **`go/ast`, not tree-sitter.** Its Go binding is CGO, and corral builds
  `CGO_ENABLED=0` — three of four release targets fail to cross-compile
  with it. See [ADR-0006](adr/0006-symbol-extraction-without-cgo.md). The
  cost is that Go is the only language indexed; the `Extractor` interface
  exists so a second is a new file rather than a rewrite.
- **Truncation is reported, never silent.** A repository that exceeds the
  file or symbol cap is named in the response, because a caller cannot
  otherwise tell a missing symbol from an absent one.

Three rules govern everything the server returns:

1. **Sandbox.** `SafePath` canonicalises symlinks on *both* sides and
   compares path segments, not string prefixes, so a repository legitimately
   named `..foo` is inside the root while `../../etc` is not.
   `SafeMutationPath` additionally refuses the workspace root itself.
2. **Untrusted output.** Repository names, branch names, paths and origin
   URLs are chosen by whoever owns the repository. `RepoEntry.Redacted()`
   bounds and de-fangs them at every output site — never at construction,
   because `Path` is what git is handed. See
   [ADR-0003](adr/0003-sanitize-on-output-not-construction.md).
3. **Allowlisted files.** The file resource serves recognised source,
   documentation and non-secret configuration, with a credential denylist
   behind the allowlist. See
   [ADR-0004](adr/0004-file-resource-allowlist.md).

## Concurrency

| Site | Bound | Why |
|---|---|---|
| Repository workers | `--concurrency`, default from CPU count | Each is a `git` subprocess; more is not faster past network limits |
| GitHub page fetches | 5 in flight | Keeps a large org responsive without tripping secondary rate limits |
| `exec` | `--concurrency`, default 4 | One shell per repository |
| MCP scan | serial walk | Syscall-bound; see the note below |

The MCP workspace scan is the one place where the current design is known
to be leaving performance behind: it is 97% syscall wait and enriches each
repository serially, so a 1,000-repository workspace takes ~140 ms per
uncached scan.

## Output discipline

**Stdout carries the selected output format. Diagnostics go to stderr.**

This is not stylistic. It is what makes `--output json` pipeable, and on the
MCP path stdout *is* the JSON-RPC stream — a stray `fmt.Println` there
corrupts the protocol. `internal/diag` exists so there is a levelled,
stderr-only channel with no temptation to reach for `fmt`.

## Where state lives

| State | Location | Notes |
|---|---|---|
| Sync sidecar | `<repo>/.git/corral-state.json` | Inside the git dir so it never shows in `git status` |
| Config | `$XDG_CONFIG_HOME/corral/config.json` | Keyed by flag name |
| MCP audit log | `$XDG_STATE_HOME/corral/mutations.log` | JSONL, `0600`, size-rotated |

The sidecar is an optimisation only: a missing or corrupt one falls back to
always pulling, so it can never cause a stale working tree.
