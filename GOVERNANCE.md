# Governance

corralctl is a small, focused open-source project. This document describes how
decisions are made, who makes them, and what happens if key people become
unavailable.

## Roles

### Maintainer

The **Maintainer** is the person with commit access to
`sebastienrousseau/corralctl` who is responsible for the direction, quality,
release cadence, and security of the project.

**Current maintainer:** Sebastien Rousseau
(<sebastian.rousseau@gmail.com>, GitHub: `@sebastienrousseau`).

Responsibilities:

- Review and merge pull requests.
- Triage issues within a best-effort window of 7 days.
- Cut releases and sign every release tag.
- Respond to security disclosures per [SECURITY.md](SECURITY.md).
- Keep dependencies patched and CI green.

### Contributor

Anyone who opens an issue, comments on a discussion, or submits a pull
request is a **Contributor**. Contributors do not need commit access; the
Maintainer merges accepted contributions after review.

Contribution mechanics are documented in [CONTRIBUTING.md](CONTRIBUTING.md).

## Decision-making

Because corralctl has a single Maintainer, decisions are ultimately made by the
Maintainer after considering community input. The Maintainer commits to:

- Explaining the reasoning behind non-trivial rejections in the relevant
  issue or PR thread.
- Documenting significant architectural or scope decisions in the CHANGELOG
  and, where warranted, in the PR body of the change that implements them.
- Deferring stylistic tie-breakers to the existing code base's conventions.

For substantial changes that affect users (new subcommands, breaking flag
changes, or removing features), the Maintainer will open a tracking issue
and welcome comments for at least one week before landing the change.

## Access continuity and succession

The single-Maintainer model creates a real bus-factor risk. The full
succession procedure — voluntary hand-off, community fork after
≥ 6 months of unresponsiveness, and emergency compromise response —
is documented in [MAINTAINERS.md](MAINTAINERS.md). Key facts, in short:

- **Repository ownership**: The `sebastienrousseau/corralctl` repository is
  owned by the Maintainer's personal GitHub account. Any user may fork
  under GPL-3.0 without further permission.
- **Release signing key**: Release tags are signed with the Maintainer's
  SSH ed25519 key (fingerprint
  `SHA256:kIOPAavp1TCEauTr1tTIN3cv+tSs6F9m/4lZjuM9tqk`). Every release
  artefact is additionally keyless-signed with cosign and carries a
  SLSA v1.0 provenance attestation — those two remain publicly
  verifiable against Rekor even if the SSH key is later rotated.
- **External services**: A full catalogue (Homebrew tap, AUR, ghcr.io,
  docs DNS, MCP Registry, Sigstore integration) lives in
  [MAINTAINERS.md](MAINTAINERS.md) §"External services and accounts",
  each row noting the configuration file a successor must edit.
- **Community fork**: If the Maintainer is unresponsive for ≥ 6 months
  (no issue comments, no releases, no PR merges), the community is
  encouraged to fork per the procedure in
  [MAINTAINERS.md](MAINTAINERS.md) §"Community fork (unplanned)".
- **Security model**: The public assurance case in
  [docs/security-model.md](docs/security-model.md) records the claims,
  evidence, and out-of-scope threats a successor inherits, so
  handover is not tribal.

## Adding a co-Maintainer

If a regular contributor demonstrates sustained high-quality contributions
over ≥ 6 months and expresses interest in ongoing maintenance, the
Maintainer will consider granting them commit access and updating this
document. Criteria the current Maintainer will look for:

- Merged pull requests spanning multiple sub-systems.
- Constructive issue triage and code review comments.
- Willingness to be reachable for security disclosures.

## Changes to this document

Changes to `GOVERNANCE.md` are made through the usual pull request process
and require the Maintainer's approval. Because the Maintainer is currently
the only person with commit access, the community input mechanism for
substantial governance changes is the same as for substantial code changes:
open an issue for discussion at least one week before the change lands.
