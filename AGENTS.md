<!-- SPDX-License-Identifier: GPL-3.0-only -->

# Working on corralctl as an AI agent

Invariants for AI-assisted contributions. These are the things that are
expensive to discover from the diff alone, and the ones where a plausible
change is wrong for a reason the code does not state.

Read [DEVELOPMENT.md](DEVELOPMENT.md) first for the toolchain and the local
equivalent of every CI gate. This file is only the constraints.

## Hard gates

Nothing merges without these. All of them are reproducible locally.

| Gate | Command |
|---|---|
| 100% statement coverage, every package | `go test ./... -cover` |
| Race detector, randomised order | `go test -race -shuffle=on ./...` |
| Documentation on every exported declaration | `make doc-check` |
| SPDX header on every source file | `make spdx-check` |
| SBOM / `server.json` / CHANGELOG agree | `make sbom-check` |
| Examples still compile | `make example-check` |
| Install tree is correct | `make install-smoke` |

Coverage is not negotiable downward. If a new branch is hard to reach, add
a package-level seam (`var gitClone = git.Clone`) and stub it — that is the
established pattern throughout, not an exception made for the new code.

## Commits

- **Every commit must be cryptographically signed and carry a DCO
  `Signed-off-by` trailer.** An agent's shell usually cannot reach the
  maintainer's ssh-agent; hand the commits over as a script rather than
  producing unsigned history that has to be rewritten.
- Conventional Commits for the subject line.
- Never rewrite published history. `main` and any pushed branch are
  append-only.

## Versioning

- SemVer. Pre-1.0, the patch digit moves for everything.
- The version lives in **three** places that a gate compares: the newest
  `## [x.y.z]` heading in `CHANGELOG.md`, the `version` field in
  `server.json`, and the OCI image tag in that same file. Changing one
  without the others fails `make sbom-check`.
- `cmd.Version` is injected at build time. Never hard-code a version there.
- A CHANGELOG section that exists is not a release. Check `git tag` and the
  GitHub releases before assuming a version is spent — 0.0.28 sat written
  but untagged for days.

## Things that look like bugs and are not

- **`internal/github` imports nothing internal.** The duplicated
  `levenshtein` is deliberate: keeping that package a leaf is worth twenty
  lines of arithmetic. Do not "fix" it by extracting a shared helper.
- **Seams are package-level `var`s, not interfaces.** `runGitOutput`,
  `statPath`, `fetchRepos` and friends exist so tests can stub the
  dangerous path. Converting them to interfaces would be a large diff that
  buys nothing.
- **`Redacted()` is applied at output sites, not at index construction.**
  `RepoEntry.Path` is what `SafeMutationPath` resolves and what git is
  handed, so the stored value must stay byte-exact. Sanitising earlier
  would make the index disagree with the filesystem.
- **Manpages and completions are absent from the tree by design.** They are
  generated into `build/`. Do not commit them.
- **`build/` is not `dist/`.** goreleaser owns `dist/` and cleans it after
  its before-hooks run, which would delete generated pages before
  packaging.

## Things that are load-bearing

- **Stdout carries the selected output format; diagnostics go to stderr.**
  This is what keeps `--output json` and the MCP stdio stream parseable. A
  `fmt.Println` on a diagnostic path is a bug, and on the MCP path it
  corrupts the JSON-RPC stream.
- **Refusals are the product.** `prune`, `corral_delete_repo` and
  `migrateLegacy` decline when evidence is missing — unpushed commits,
  stashes, gitignored files, a mismatched origin, a truncated listing.
  Weakening a refusal to make a test pass is the most damaging change
  available in this codebase.
- **A filter that cannot be honoured is an error, never an empty result.**
  `--type sponsored` returned nothing for months because the field behind
  it is never populated, and an empty result is indistinguishable from a
  correct one. New filters either work or refuse with a reason.
- **Identity is `git.CanonicalRemote`.** It is what decides whether two
  clones are the same repository, and therefore what gates migration, sync
  and deletion. Do not compare remote URLs with `strings.Contains`.
- **Everything the MCP server returns is untrusted.** Repository names,
  branch names, paths and file contents are all chosen by whoever owns the
  repository. New output paths need `Redacted()`; new file-serving paths
  need the allowlist in `internal/mcp/filepolicy.go`.

## Scope

- Do not couple a structure or documentation cleanup to a behaviour change.
  They review differently and the cleanup is what gets dropped.
- Do not add a dependency without saying why in the commit. The module has
  eleven direct dependencies and a hand-maintained `SBOM.md` that CI checks
  against `go.mod`; a new one is not free.
- Do not add a CI gate that does not currently pass. A red gate on arrival
  teaches everyone to ignore it.
