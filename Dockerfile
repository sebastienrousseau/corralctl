# SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
# SPDX-License-Identifier: GPL-3.0-only

# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e
#
# Runtime image for the corralctl MCP server.
#
# The binary is expected to already be built by goreleaser and dropped at
# the build context root as `corralctl`. Do NOT compile from source in this
# Dockerfile — goreleaser's dockers: section produces one image per arch
# reusing the same statically-linked binary it publishes as a tar.gz.
#
# The `io.modelcontextprotocol.server.name` LABEL is the ownership marker
# the MCP registry uses to verify that
# https://ghcr.io/sebastienrousseau/corralctl belongs to the
# io.github.sebastienrousseau/corralctl server entry.
#
# Base image is pinned to a digest per OpenSSF Scorecard
# PinnedDependenciesID: an immutable reference protects the release
# supply chain from a poisoned `alpine:3.20` tag rotation. Update the
# digest when refreshing Alpine (e.g. moving to 3.21) or when the
# upstream image publishes a security fix.
FROM alpine:3.24@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

# Runtime deps: git is required for clone/pull, ca-certificates for TLS.
# Pin an explicit numeric uid/gid. hadolint DL3066 flags a non-numeric
# USER because the host cannot resolve the name, and Kubernetes
# `runAsNonRoot` needs a numeric id to verify the container is not root.
# 65532 is the conventional "nonroot" id used by distroless.
RUN apk add --no-cache git ca-certificates \
    && addgroup -S -g 65532 corral \
    && adduser -S -u 65532 -G corral -h /home/corral corral

COPY corralctl /usr/local/bin/corralctl

# OCI-standard labels for image indexing.
LABEL org.opencontainers.image.source="https://github.com/sebastienrousseau/corralctl" \
      org.opencontainers.image.description="corralctl: local index for AI coding agents. MCP server exposes the corralctl-organised workspace to Claude Code, Cursor, Cline, and other MCP clients." \
      org.opencontainers.image.licenses="GPL-3.0" \
      org.opencontainers.image.title="corral" \
      org.opencontainers.image.vendor="Sebastien Rousseau"

# MCP registry ownership label. MUST match the `name` field in server.json;
# the registry rejects publish attempts when they diverge.
LABEL io.modelcontextprotocol.server.name="io.github.sebastienrousseau/corralctl"

USER 65532:65532
WORKDIR /home/corral

# Default entrypoint runs the MCP server on stdio. Override with e.g.
# `docker run … corralctl <owner>` for the classic clone/sync workflow.
ENTRYPOINT ["/usr/local/bin/corralctl"]
CMD ["mcp"]
