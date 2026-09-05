<!--
SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
SPDX-License-Identifier: GPL-3.0-only
-->

# Container image

**Recipe:** [`Dockerfile`](../../Dockerfile) at the repository root,
built and pushed by the release workflow. Not duplicated here; see
[`../README.md`](../README.md) for why.

## Run

```bash
docker run --rm -v "$HOME/Code:/workspace" \
  ghcr.io/sebastienrousseau/corralctl:latest --base-dir /workspace --help
```

Mount the workspace read-write only if you intend to clone or sync;
corral's read paths need nothing but read access.

## As an MCP server

The image speaks stdio by default, which a container makes awkward. Use
the HTTP transport instead, and keep it on loopback:

```bash
docker run --rm -p 127.0.0.1:7777:7777 \
  -v "$HOME/Code:/workspace:ro" \
  ghcr.io/sebastienrousseau/corralctl:latest \
  mcp --root /workspace --http 0.0.0.0:7777 --allow-remote
```

`--allow-remote` is required here because inside the container the bind
address is not loopback — the loopback boundary is the published port,
which is why `-p` above binds `127.0.0.1` explicitly. Publishing the
port on `0.0.0.0` would expose an unauthenticated server that can read
every repository in the mounted workspace.

## Base image

Digest-pinned, which OpenSSF Scorecard checks. The tag matches the
release version, and CI enforces that it agrees with `CHANGELOG.md` and
`server.json`.

## Verify

```bash
cosign verify ghcr.io/sebastienrousseau/corralctl:<version> \
  --certificate-identity-regexp '^https://github\.com/sebastienrousseau/corralctl/' \
  --certificate-oidc-issuer 'https://token.actions.githubusercontent.com'
```
