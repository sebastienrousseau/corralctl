<!-- SPDX-License-Identifier: GPL-3.0-only -->

# 0007 — Serve stdio, Streamable HTTP and SSE from one command line

**Status:** Accepted · **Date:** 2026-09-19

## Context

`corralctl mcp` spoke stdio, and from v0.0.29 Streamable HTTP behind
`--http`, served stateless. That fits a developer's laptop. It does not
fit a shared deployment, a gateway that fans one server out to many
agents, or an auditor that speaks HTTP and expects the endpoint to have a
name. The MCP specification now has two current revisions on the HTTP
binding — `2025-11-25`, an `initialize` handshake and a session header,
and `2026-07-28`, stateless with per-request `_meta` and
`server/discover` — and clients on either must be served. The older
HTTP+SSE transport (`2024-11-05`) is still what some hosts expect.

The SDK this server is built on (`go-sdk` v1.7.0) serves `2026-07-28`
only from a handler configured stateless, and a stateless handler neither
issues nor honours `Mcp-Session-Id`, which is the whole of `2025-11-25`'s
session model. One handler cannot serve both.

## Options considered

1. Stay stateless and leave `2025-11-25` clients to a wrapper process.
2. Serve the two revisions on two endpoints, and tell users which to use.
3. Two SDK handlers over the one `*mcp.Server`, on one endpoint, with a
   router that sends each request to the one that speaks its revision;
   the legacy SSE transport on its own listener; three flags to pick
   between them, the same across every server in this fleet.

## Decision

Option 3. `corralctl mcp` runs stdio; `--transport streamable-http`
listens on `--host`/`--port` at `/mcp` and serves both current revisions
there; `--transport sse` serves the older HTTP+SSE transport at `/sse`.
The router reads the `Mcp-Protocol-Version` header when there is one.
Without one, the session handler gets exactly what concerns a session —
`initialize`, a POST naming `Mcp-Session-Id`, the `GET` that opens the
standalone stream, the `DELETE` that ends it — and every other POST is
served without a session, as it was before there was a choice. A session
idle for an hour is closed, and every session is ended when the listener
stops.

`--http HOST:PORT` stays as a deprecated spelling of
`--transport streamable-http`, its address winning whole over the new
flags' defaults, so a configured client keeps working. The loopback guard
that covered it covers `--host`. None of the transports carries
authentication: a routable deployment sits behind a gateway the operator
trusts, and says so with `--allow-remote`.

## Consequences

One endpoint answers whichever revision a client sends, which is what
lets a fleet of servers be started, documented and audited the same way.
The server is verified over Streamable HTTP with an external auditor in
both revisions and over SSE with the reference client before release;
the unit tests drive each revision through the real SDK chain, and the
SSE handler through the SDK's own client.

Two things changed on the way. A JSON-RPC rewrite that turned every
transport-level refusal into a 200 had to be narrowed to what the SDK
refuses at the JSON-RPC layer, since a `2025-11-25` client re-initialises
on a 404 and would not on a 200. And a call to a tool the server does not
have now returns an `isError` result rather than the SDK's `-32602`,
which under `2026-07-28` carries HTTP 400 — the status a transport layer
retries rather than reads.

## What would make this wrong

If the SDK served both revisions from one handler, the router would be
dead weight and should go. If a session revision were retired by the
specification, the session handler could go with it and the endpoint
would be stateless again, as it was in v0.0.29.
