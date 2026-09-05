<!--
SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
SPDX-License-Identifier: GPL-3.0-only
-->

# Nix

**Recipe:** [`flake.nix`](../../flake.nix) at the repository root. Unlike
the other formats this one is not generated at release time — it is the
source of truth, built from source on demand, and usable from a checkout
today.

## Run without installing

```bash
nix run github:sebastienrousseau/corralctl -- --help
```

## Build

```bash
nix build github:sebastienrousseau/corralctl
./result/bin/corralctl --version
```

The package installs manpages and shell completions alongside the
binary, generated from the cobra command tree during `postInstall`
rather than committed.

## Development shell

```bash
nix develop
```

Every tool the CI gates need, pinned by `flake.lock`. This exists
because the devcontainer's `pip install` and `npm install -g` steps were
flagged by OpenSSF Scorecard as unpinnable, and the resolution at the
time was to delete the tools — which removed capability rather than
securing it. Nix pins them by hash by construction.

## Version

Read from the newest release heading in `CHANGELOG.md`, so the flake
cannot drift from the manifest gate that already keeps `CHANGELOG.md`,
`server.json` and the container tag in agreement.

## nixpkgs

Not yet submitted. A packager taking this upstream should build from
source with `buildGoModule` as the flake does; `vendorHash` is recorded
in `flake.nix` and `nix build` reports the correct value when `go.mod`
changes.
