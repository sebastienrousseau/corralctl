<!-- SPDX-License-Identifier: GPL-3.0-only -->

# 0006 — Symbol extraction uses `go/ast`, not tree-sitter

**Status:** Accepted · **Date:** 2026-09-03

## Context

The 2026 consensus for code indexing is tree-sitter: structural parsing,
one grammar per language, dozens of languages from one API. A repository
audit recommended it directly, citing a study where a tree-sitter knowledge
graph exposed over MCP cut agent token use roughly 10× and tool calls 2.1×.

So the obvious implementation is `github.com/tree-sitter/go-tree-sitter`
plus grammars.

## Decision

Do not use tree-sitter. Extract Go symbols with `go/ast` from the standard
library, behind an `Extractor` interface that another language can
implement later.

## Consequences

Tree-sitter's Go binding is a CGO wrapper around a C library, and corral
builds `CGO_ENABLED=0` everywhere. That is not a preference: it is what
makes the released binaries static, the container image work on Alpine's
musl, and cross-compilation to six targets a single `go build`.

Measured, not assumed:

```console
$ CGO_ENABLED=0 go build ./...
./main.go:8:30: undefined: ts.NewParser        # entirely behind cgo

$ CGO_ENABLED=1 GOOS=linux  GOARCH=amd64 go build ./...   # runtime/cgo
$ CGO_ENABLED=1 GOOS=linux  GOARCH=arm64 go build ./...   # runtime/cgo
$ CGO_ENABLED=1 GOOS=windows GOARCH=amd64 go build ./...  # runtime/cgo
$ CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 go build ./...   # ok (host only)
```

Three of four release targets fail without a per-target C cross-toolchain.
Adopting tree-sitter would mean giving up static binaries, adding a C
toolchain to the release pipeline, and reworking the container image —
to gain languages corral does not yet index anything for.

`go/ast` costs nothing: standard library, no CGO, no new dependency
against a module that keeps eleven and a CI-checked SBOM. It is also
strictly more accurate for Go than a tree-sitter grammar, because it is
the same parser the compiler uses.

The cost is real and should not be understated: **one language**. corralctl
indexes Go and nothing else until another extractor is written.

## Correction — 2026-09-03

The decision above stands. The argument for it was overstated, and this
records that rather than quietly editing it, because an ADR whose
reasoning cannot be trusted in part cannot be trusted at all.

**"Three of four release targets fail without a per-target C
cross-toolchain" framed a tooling inconvenience as though it were a
barrier.** It is not. Supplying cross toolchains reproducibly is precisely
what Nix does — `pkgsCross` for linux/arm64, mingw for Windows, musl for
static linking — and this repository now ships a `flake.nix` that could
host exactly that. More simply still: the ordinary answer to CGO
cross-compilation is not to cross-compile at all, but to build natively on
per-OS runners, which GitHub already provides and which sidesteps the one
case Nix genuinely struggles with (a darwin SDK from Linux).

So the honest cost of adopting tree-sitter is not "it cannot be built". It is:

- **Static linking.** `CGO_ENABLED=0` is what makes the released binaries
  fully static and the Alpine image work at all. CGO means glibc or musl
  coupling, which is solvable and is work.
- **Pipeline complexity.** One `goreleaser` invocation on one runner
  becomes a matrix plus an artifact merge — on machinery this project had
  just added a dry-run for, precisely because it is under-exercised.
- **Dependency surface.** tree-sitter plus a grammar per language, against
  a module that holds eleven direct dependencies and a hand-maintained
  SBOM that CI checks.

Those are real and they still point the same way for this change, because
none of them had to be paid to ship the symbol index. But they are a
weighing, not an impossibility, and the original text read as the latter.

## What would make this wrong

Two things, either of which should reopen it:

- corralctl's audience turning out to be mostly non-Go. The extractor
  interface exists so that is a new file rather than a rewrite.
- A pure-Go tree-sitter path maturing. See the 2026-09-05 amendment
  below: this was investigated and declined, and the condition is
  narrower than it looks — a WASM runtime was never the obstacle.

## Amendment — 2026-09-04: four more languages, without changing the decision

The first bullet above fired. A workspace is polyglot, and a
cross-repository symbol lookup that silently covered only the Go clones
was not answering the question it appeared to answer: not "no match", but
a match set missing every Python, TypeScript and Rust repository, with no
indication anything was left out.

Python, TypeScript, JavaScript and Rust are now indexed. Neither branch
this ADR anticipated was taken — no CGO, and no wazero-hosted
tree-sitter. The third option is the oldest one: a line scanner, which is
what ctags has done since 1992.

**Why a scanner rather than the wazero path.** wazero remains the right
answer eventually, and nothing here forecloses it — `Extractor` is the
same interface either way, so replacing a scanner with a WASM grammar is
one file per language. It was not taken now because it is a large amount
of new machinery (a WASM runtime, a grammar per language, a build step to
produce the `.wasm`, and a query language to map nodes to symbols) for an
index whose contract is deliberately shallow: a name, a kind, a file and a
line. Nothing in that contract needs a syntax tree. The scanner reaches it
in roughly two hundred lines per language with no new dependency.

**What the scanner gives up, stated plainly**, because a shallow tool that
oversells itself is worse than one that does not exist:

- No type resolution. It cannot tell a re-export from a declaration.
- No macro expansion, no decorators that rewrite, no import-time
  generation.
- A declaration split across lines unusually will be missed.
- Python tuple unpacking (`a, b = f()`) is deliberately not read.

**What it does not give up** is trustworthiness, which is the property
that actually matters for this index. Every scanner runs over source whose
comment and string *contents* have been blanked — offsets and line numbers
preserved exactly — so a `class` in a docstring, a `function` in a
template literal, and a `struct` in a nested Rust block comment are all
invisible to it. A missed symbol degrades to reading files, which is what
an agent would have done anyway. A *fabricated* symbol would be a
confident lie about a line where nothing is declared, and that is the
failure this design spends its complexity preventing. Every language's
test fixture plants such a trap and asserts it does not appear.

Go keeps `go/ast`. The scanner is for languages that have no equivalent
available here, not a replacement for one that does.

## Amendment — 2026-09-05: the wazero path, investigated and declined

"What would make this wrong" named a wazero-hosted tree-sitter as the
change that should reopen this decision, and called it "too speculative
to build on here, not wrong in principle". That was written without
looking. This records what looking found, so the next person does not
repeat the investigation.

**There is no pure-Go tree-sitter.** Both maintained Go bindings are CGO:

| Module | Version | C sources | Verdict |
| --- | --- | --- | --- |
| `github.com/tree-sitter/go-tree-sitter` | v0.24.0 | 46 | `import "C"` |
| `github.com/alexaandru/go-tree-sitter-bare` | v1.11.0 | 44 | `import "C"` |

No module bridging tree-sitter to wazero exists. `wazero` itself is
healthy (v1.12.0, May 2026) — the runtime was never the missing piece.

**So the work is not "adopt a library", it is:**

1. Compile tree-sitter's C runtime to WASM, which needs a WASI toolchain
   in the build.
2. Compile, or obtain, a `.wasm` for each of the five languages.
3. Write the host ABI bridge that `web-tree-sitter` implements in
   JavaScript, in Go, over wazero.
4. Map parse trees to `symbols.Symbol` — tree-sitter queries, or a
   hand-written walk per grammar.

**And the fourth cost is the one that decides it.** Prebuilt grammar
`.wasm` files are distributed through npm. Fetching them at build time is
precisely the unpinnable-installer pattern this project already removed
once, at OpenSSF Scorecard's prompting, when `pip install` and
`npm install -g` were deleted from the devcontainer. Vendoring them
instead means committing five opaque binary blobs to a repository whose
posture is that everything is pinned, auditable and listed in a
hand-maintained SBOM — and a `.wasm` is not reviewable in a pull request.

**The decision stands, and the reopening condition is narrower than it
was.** What would make this worth revisiting is a maintained, pure-Go
tree-sitter binding that carries its grammars as Go source — not a WASM
runtime, which was never the obstacle. Until such a thing exists, the
line scanners stay, and the honest description of them is the one in
`internal/symbols/scan.go`: they answer "where is X defined" for the
shape declarations actually take, and they are wrong cheaply.
