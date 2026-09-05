<!-- SPDX-License-Identifier: GPL-3.0-only -->

# Development

The single entry point for working on corralctl: toolchain, how to reproduce
every CI gate locally, how the tests are laid out, and how a release is cut.

If a gate fails in CI and you cannot reproduce it from this file, that is a
bug in this file — please report it.

## Contents

- [Toolchain](#toolchain)
- [Everyday tasks](#everyday-tasks)
- [Reproducing every CI gate](#reproducing-every-ci-gate)
- [Test layout](#test-layout)
- [Generated artefacts](#generated-artefacts)
- [Release model](#release-model)
- [Conventions](#conventions)

## Toolchain

| Tool | Version | Why |
|---|---|---|
| Go | as pinned by the `go` directive in `go.mod` | `GOTOOLCHAIN=auto` downloads it; CI never pins a version separately, so `go.mod` is the single source of truth |
| git | 2.30+ | corralctl shells out to `git` for every clone, pull and inspection |
| make | any | Task runner for everything below |

Optional, only needed for the gate that uses them:

| Tool | Used by |
|---|---|
| `golangci-lint` | `make lint` |
| `goreleaser` | release dry runs |
| `groff` | manpage rendering check |
| `markdownlint-cli2`, `codespell`, `lychee` | `make docs-lint` and the Docs Lint workflow |
| `nix` | optional; `nix develop` provides every row above, pinned |

### Nix (every tool, pinned)

```sh
nix develop        # Go, linters, prose tools, release tooling — all pinned
nix build          # the package, with manpages and completions
nix run . -- --help
```

`nix develop` is the shortest path to a machine that can run every gate in
this file, because `flake.lock` pins each tool by hash.

That matters beyond convenience. The devcontainer deliberately does **not**
install the prose tools: `pip install` and `npm install -g` cannot be
pinned by hash without a hash-locked requirements file and a lockfile, and
OpenSSF Scorecard is right to flag an installer that resolves at build time
and then runs with your credentials. Nix pins them by construction, so the
dev shell has them and the devcontainer does not.

Without Nix, install them yourself — `make docs-lint` skips whichever is
absent, and the Docs Lint workflow is authoritative:

```sh
pip install codespell pre-commit
npm install -g markdownlint-cli2
brew install lychee          # or: cargo install lychee
```

Nothing else is required. There is no code generation step, no vendored
dependency tree, and no CGO — `CGO_ENABLED=0` everywhere, which is what
makes the released binaries static and the cross-compilation trivial.

```sh
git clone https://github.com/sebastienrousseau/corralctl.git
cd corral
make            # format, vet, licence + manifest + example checks, tests, build
```

## Everyday tasks

`make help` lists every target. The ones you will actually use:

| Command | What it does |
|---|---|
| `make build` | Compile `corralctl` with version metadata and `-trimpath` |
| `make test` | Run the suite |
| `make test-race` | Race detector with randomised test order |
| `make docs` | Generate manpages and completions into `build/` |
| `make install` | Install under `PREFIX` (default `/usr/local`) |
| `make uninstall` | Remove everything `install` placed |
| `make clean` | Remove build output |

## Reproducing every CI gate

Every gate below is a job in `.github/workflows/`. The left column is what
CI runs; the right column is the identical command locally. If you run all
of them and they pass, CI will pass — the only thing you cannot reproduce
is the cross-platform matrix.

| CI job | Reproduce locally |
|---|---|
| Go CI (build, test, cross-platform) | `go build ./... && go test ./...` |
| Race & Shuffled Tests | `go test -race -shuffle=on ./...` |
| Fixed shuffle seeds | `for s in 1 2 3; do go test -count=1 -shuffle=$s ./...; done` |
| Benchmarks smoke | `go test -run '^$' -bench . -benchtime 1x ./...` |
| Vulnerability Scan | `go run golang.org/x/vuln/cmd/govulncheck@latest ./...` |
| Documentation Coverage | `make doc-check` |
| Licence headers (SPDX) | `make spdx-check` |
| Manifest drift | `make sbom-check` |
| Example compilation | `make example-check` |
| Install Contract | `make install-smoke` |
| Manpage rendering | `make docs && groff -man -Tutf8 -ww build/man/corralctl.1 >/dev/null` |
| Docs Lint (markdown, spelling, links) | `make docs-lint` |
| Lint | `make lint` |

Coverage is not a separate job — the suite reports it. The project runs at
100% statement coverage across every package, and the rationale is in
[Conventions](#conventions) below.

To check a release without publishing anything:

```sh
goreleaser release --snapshot --clean --skip=publish,sign,announce
```

That builds every target, runs the manpage/completion generation hook, and
produces the archives and packages in `dist/` for inspection. The same path
runs in CI via the release workflow's `workflow_dispatch` dry-run.

## Test layout

Tests live beside the code they cover — there is no top-level `tests/`
directory, which is the Go convention and keeps a package's seams private
to it.

| Pattern | Purpose |
|---|---|
| `internal/<pkg>/<pkg>_test.go` | The package's main suite |
| `internal/<pkg>/coverage_paths_test.go` | Error branches that need a stubbed seam to reach |
| `internal/<pkg>/*_fuzz_test.go` | Fuzz targets; run for a fixed duration per push |
| `internal/<pkg>/bench_test.go` | Benchmarks; compiled and smoke-run, never asserted |
| `cmd/*_test.go` | Flag validation and command wiring |

Seams are package-level `var`s holding function values (`gitClone`,
`fetchRepos`, `statPath`), stubbed by tests and restored with `t.Cleanup`.
That is why the suite needs no interfaces for what are, in production,
direct calls.

Two properties the suite deliberately enforces:

- **Order independence.** `-shuffle=on` plus three fixed seeds. A
  100%-covered suite turned out to be order-dependent once, because one
  test left cobra's help flag set on the package-level `rootCmd`.
- **No network.** No test contacts GitHub. The API is exercised through an
  injected `*github.Client` pointed at an `httptest` server.

## Generated artefacts

Manpages and shell completions are **generated, never committed**:

```sh
make docs        # -> build/man/*.1, build/completions/*
```

They are rendered from the live cobra command tree by
`scripts/gen_docs.go`, which is what keeps them in step with `--help`. A
committed `.1` drifts the first time a flag changes and nothing catches it.

`build/` is git-ignored. It is deliberately **not** `dist/`: goreleaser owns
that directory and cleans it after running its before-hooks, which would
delete the generated pages before packaging.

## Release model

Releases are tag-triggered and fully automated. Nothing is published by
hand.

1. Land everything for the release on `main`, with `CHANGELOG.md` updated
   under a `## [x.y.z]` heading and `server.json`'s version matching. The
   manifest gate enforces that they agree.
2. Dry-run the pipeline: run the Release workflow via `workflow_dispatch`
   with `dry_run: true`. It builds and packages everything and stops before
   publishing, signing and attesting.
3. Tag and push: `git tag -s vX.Y.Z && git push origin vX.Y.Z`.
4. The workflow builds the target matrix, signs with keyless cosign,
   attaches SLSA provenance and a CycloneDX SBOM, publishes archives, deb
   and rpm packages, the Homebrew cask, the AUR package, the container
   image, and the MCP registry entry.

Every commit must be **cryptographically signed** and carry a DCO
`Signed-off-by` trailer. See [CONTRIBUTING.md](CONTRIBUTING.md).

## Conventions

**Coverage threshold: 100% of statements, enforced per package.**

A threshold chosen once and defended beats chasing a number, so here is the
defence. corralctl's failure modes are destructive — it moves, deletes and
overwrites directories in a developer's workspace — and the branches that
matter most are the refusals: the paths that decline to delete a clone with
unpushed work, decline to migrate a directory whose origin does not match,
decline to prune against a truncated listing. Those branches are only ever
taken when something has already gone wrong, so they are exactly the ones
that rot untested. Requiring every statement means a new refusal cannot
land without a test that reaches it.

The number is a floor, not a claim of completeness: statement coverage is
not branch coverage, and 100% here does not mean every input combination is
exercised. It means no statement ships unexecuted.

**Documentation coverage: 100% of exported declarations.** Enforced by
`make doc-check`. The generated package reference is the public face of the
library packages, and an undocumented export renders as an empty paragraph.

**Licence headers on every file.** Enforced by `make spdx-check`. Run
`go run scripts/spdx_sweep.go` to add missing ones.

**Diagnostics go to stderr; stdout carries the selected output format.**
This is what keeps `--output json` pipeable. A `fmt.Println` on a
diagnostic path is a bug.
