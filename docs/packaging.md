<!-- SPDX-License-Identifier: GPL-3.0-only -->

# Packaging corralctl

Written for distribution maintainers. Everything a packager needs to
decide whether corralctl fits their archive, and to build it without asking.

Maintainer contact: <sebastian.rousseau@gmail.com> · issues at
<https://github.com/sebastienrousseau/corralctl/issues>.

## What it is

`corralctl` — a CLI that clones and organises repositories from GitHub,
GitLab, Gitea, Forgejo, Codeberg and Bitbucket into a
structured local workspace, and serves that workspace to AI coding agents
over the Model Context Protocol. One static binary, no runtime data files
beyond the manpages and completions.

## Licence

**GPL-3.0-only**, SPDX-identified. `LICENSE` at the repository root is the
full text, and every source file carries an SPDX header — verified in CI by
`make spdx-check`, so the tree is machine-readable for REUSE-style tooling.

Dependencies are all permissive (MIT, BSD-3-Clause, Apache-2.0); the full
list with versions is in [SBOM.md](../SBOM.md), which CI checks against
`go.mod` so it cannot drift. A CycloneDX SBOM is attached to every release.

## Minimum toolchain policy

The floor is the `go` directive in `go.mod` — currently **Go 1.26.6**. It
is deliberately not restated anywhere else, so it cannot disagree with
itself; CI sets `GOTOOLCHAIN=auto` and lets `go.mod` decide.

**When it may rise:** on any release, if a standard-library fix or language
feature is worth it. corralctl makes no distro-LTS compatibility promise. If
your archive pins an older Go, check `go.mod` before packaging a new
version rather than assuming the floor held — an aspirational compatibility
claim here would be worse than none.

The reason for each rise is recorded in the CHANGELOG entry for the release
that raises it.

## Building

No code generation, no vendored tree, no CGO.

```sh
make build                     # or: go build -trimpath ./cmd/corralctl
make DESTDIR=$PWD/stage PREFIX=/usr install
```

`make install` honours `PREFIX` (default `/usr/local`) and `DESTDIR`, and
produces:

```text
$PREFIX/bin/corralctl
$PREFIX/share/man/man1/corralctl.1
$PREFIX/share/man/man1/corralctl-<subcommand>.1
$PREFIX/share/bash-completion/completions/corralctl
$PREFIX/share/zsh/site-functions/_corralctl
$PREFIX/share/fish/vendor_completions.d/corralctl.fish
$PREFIX/share/doc/corralctl/{README.md,CHANGELOG.md,LICENSE,SECURITY.md}
```

`make uninstall` removes exactly that set. Both are exercised in CI by
`make install-smoke`, which stages an install on a clean runner and asserts
the tree.

**Manpages and completions are generated, not committed.** `make install`
runs the generator; if you build without it, run `make docs` first. They
are rendered from the cobra command tree so they cannot drift from
`--help`.

## Dependency pin model

`go.mod` and `go.sum` are committed and authoritative. Builds are
reproducible:

- `-trimpath` — no build-machine paths in the binary
- `mod_timestamp` pinned to the commit
- `CGO_ENABLED=0` — static, no libc coupling

Two builds of the same commit are byte-identical. If your archive verifies
reproducibility, this should hold; please report it if it does not.

## Offline builds

Vendor the dependencies once, then build with no network:

```sh
go mod vendor
go build -mod=vendor -trimpath ./cmd/corralctl
go test -mod=vendor ./...
```

The test suite makes **no network calls** — the GitHub API is exercised
through an injected client pointed at an `httptest` server, and no test
clones from a real remote. It is safe in a sealed build environment.

## Runtime dependencies

- **`git`** — required. corralctl shells out for every clone, pull and
  inspection. Declare it as a hard dependency.
- **`gh`** (GitHub CLI) — optional. Only used by `--auth gh`, and only when
  no `GITHUB_TOKEN`/`GH_TOKEN` is set. A `Suggests`/`Recommends` at most.

No daemon, no system user, no configuration file required to run.

## Signature verification

Every release carries:

| Artefact | What it is |
|---|---|
| `checksums.txt` | SHA-256 of every asset |
| `checksums.txt.sigstore.json` | Keyless cosign signature (Sigstore bundle) |
| `checksums.txt.intoto.jsonl` | SLSA build provenance |
| `*.sbom.json` | CycloneDX SBOM |

Verify a download:

```console
$ cosign verify-blob checksums.txt \
    --bundle checksums.txt.sigstore.json \
    --certificate-identity-regexp 'https://github.com/sebastienrousseau/corralctl/.*' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com

$ sha256sum -c checksums.txt --ignore-missing
```

Or, with the GitHub CLI:

```sh
gh attestation verify corralctl_Linux_x86_64.tar.gz --owner sebastienrousseau
```

There is no long-lived signing key: signing is keyless via OIDC, so there
is no `KEYS.asc` to import and no key rotation for you to track.

## Pre-built packages

The release pipeline already publishes `.deb` and `.rpm` (via nfpm), an AUR
`PKGBUILD`, and a Homebrew cask. If you are packaging for an archive that
prefers to build from source, ignore those and use `make install` above —
they exist for users, not to forestall distribution packaging.

## What to watch when updating

- The version appears in `CHANGELOG.md`, `server.json` and the container
  tag; CI enforces that they agree, so a version bump is one coherent change.
- Manpage filenames follow the command tree. A new subcommand adds
  `corralctl-<name>.1`; a glob (`corralctl*.1`) is safer than a fixed list.
- `go.mod`'s `go` directive is the toolchain floor. Check it on every bump.
