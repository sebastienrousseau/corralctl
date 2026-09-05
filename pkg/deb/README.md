<!--
SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
SPDX-License-Identifier: GPL-3.0-only
-->

# Debian package

**Recipe:** the `nfpms` section of [`.goreleaser.yaml`](../../.goreleaser.yaml).
Not duplicated here; see [`../README.md`](../README.md) for why.

## What is in it

- `/usr/bin/corralctl`
- manpages under `/usr/share/man/man1/`, generated from the cobra
  command tree at release time — never committed, so they cannot drift
  from the flags
- shell completions for bash, zsh and fish, installed under the names
  each shell actually looks the command up by
- `README.md`, `CHANGELOG.md` and `LICENSE` under
  `/usr/share/doc/corralctl/`

## Runtime dependencies

`git` (≥ 2.20). Nothing else: the binary is statically linked, built
`CGO_ENABLED=0`.

## Install

```bash
sudo dpkg -i corralctl_<version>_linux_amd64.deb
```

Verify it first — see [`../VERIFY.md`](../VERIFY.md).

## Packaging for Debian proper

Build from source rather than repacking the release binary; `make
install` honours `PREFIX` and `DESTDIR`. `docs/packaging.md` has the
offline-build instructions and the dependency pin model.
