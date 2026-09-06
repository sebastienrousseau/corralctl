# Maintainers

corralctl is currently maintained by a single person. This document
records the current maintainer, the external services and accounts
that are load-bearing for the project, and the procedure for
succession or fork.

## Current maintainer

| Role       | Name               | Contact                           | Time zone |
|------------|--------------------|-----------------------------------|-----------|
| Maintainer | Sebastien Rousseau | <sebastian.rousseau@gmail.com>    | Europe/London |
|            |                    | GitHub: [@sebastienrousseau](https://github.com/sebastienrousseau) |               |

Best-effort response windows:

- **Security advisories** (via GitHub private vulnerability reporting):
  first response within **48 hours**.
- **Pull requests**: first review within **7 days**.
- **Issues**: triaged within **7 days**.

## External services and accounts

These are the services and accounts that corralctl depends on. A succession
event requires the new maintainer to either take over each account
(where possible) or reconstitute the same integration under their own
account and update the linked configuration file.

| # | Service                            | Account / Location                                       | Purpose                                     | Configuration reference                     |
|---|------------------------------------|----------------------------------------------------------|---------------------------------------------|---------------------------------------------|
| 1 | GitHub repository                  | `github.com/sebastienrousseau/corralctl`                    | Source of truth for code, issues, releases  | this repository                             |
| 2 | GitHub Actions                     | Same repo                                                | CI, release pipeline, SLSA provenance       | `.github/workflows/`                        |
| 3 | GitHub Container Registry (ghcr)   | `ghcr.io/sebastienrousseau/corralctl`                       | Multi-arch OCI images                       | `.goreleaser.yaml`                          |
| 4 | Homebrew tap                       | `github.com/sebastienrousseau/homebrew-tap`              | macOS/Linux Homebrew installs               | `.goreleaser.yaml` (`brews:` block)         |
| 5 | Arch User Repository (AUR)         | `aur.archlinux.org/packages/corralctl-bin`               | Arch Linux installs                         | `.goreleaser.yaml` (`aurs:` block)          |
| 6 | MCP Registry                       | `io.github.sebastienrousseau/corralctl`                     | MCP server discovery                        | `server.json`                               |
| 7 | Docs site                          | `doc.corrallib.com` (Cloudflare Pages)                   | Public documentation                        | separate repo; DNS via Cloudflare           |
| 8 | Signing key (SSH ed25519)          | `SHA256:kIOPAavp1TCEauTr1tTIN3cv+tSs6F9m/4lZjuM9tqk`     | Signs release tags and commits              | `.github/workflows/release.yml`             |
| 9 | Sigstore keyless signing           | Fulcio + Rekor (via GitHub OIDC)                         | Cosigns every release artefact              | `.goreleaser.yaml` (`sboms`/`signs` blocks) |
| 10 | Dependabot / Scorecard            | GitHub-native, tied to the repo                          | Vulnerability alerts, OSSF score            | `.github/dependabot.yml`                    |

## Succession procedure

The single-maintainer model creates real bus-factor risk. This is the
concrete plan for handing over — either voluntarily to a co-maintainer
or, after prolonged unavailability, to a community fork.

### Voluntary hand-off (planned)

1. **Announce**: open a public issue on the repository at least
   **two weeks** before the change. Link this document.
2. **Add the new maintainer as a repository owner** (or move ownership
   to a new GitHub organisation both parties can access).
3. **Update `MAINTAINERS.md`** with the new maintainer's contact,
   response-window commitments, and time zone.
4. **Rotate signing key** in a coordinated release:
   - The outgoing maintainer publishes a final release note revoking
     the old fingerprint.
   - The incoming maintainer generates a new SSH ed25519 key, updates
     `GOVERNANCE.md` §"Access continuity" with the new fingerprint,
     and cuts the next release using the new key.
5. **Transfer external accounts** in this order (each independent):
   - Homebrew tap: transfer repository ownership or fork + retire old.
   - AUR: `aur` supports co-maintainers; add the new maintainer as
     co-maintainer for one release cycle, then transfer primary.
   - ghcr.io: change the OCI image path in `.goreleaser.yaml` (releases
     from the new owner will publish to a new namespace; the old
     namespace continues to hold prior releases).
   - DNS for `doc.corrallib.com`: transfer at the registrar level or
     update the CNAME target in the new maintainer's DNS.
6. **Publish a "governance change" release note** listing every
   updated identifier so downstream users can reason about the
   transition.

### Community fork (unplanned)

corralctl is licensed GPL-3.0. If the maintainer becomes unresponsive
for **≥ 6 months** (no issue comments, no releases, no PR merges), the
community is explicitly encouraged to fork the project. `GOVERNANCE.md`
codifies this window. A community fork:

- May keep the name **only after** the old repository is archived by
  its owner. Otherwise it should adopt a distinguishing name.
- Must **not** reuse the outgoing maintainer's signing key — the new
  fork publishes its own key fingerprint in its `GOVERNANCE.md`.
- Should re-verify each external-service entry in this table under
  its own accounts; the outgoing accounts (Homebrew tap, AUR,
  ghcr.io) are not transferable without cooperation.

### Emergency (compromise or coercion)

If the maintainer's account or signing key is compromised:

1. **Immediately** publish a security advisory on the repository (or on
   any working communication channel if the repo is inaccessible)
   naming the last known-good release tag.
2. Rotate the SSH signing key and update `GOVERNANCE.md`.
3. Revoke the compromised GitHub PAT / OIDC subject; Sigstore-signed
   artefacts remain verifiable against Rekor as long as the entries
   themselves are legitimate.
4. If in doubt, users should treat all releases signed after the
   compromise as untrusted until re-verified against Rekor entries and
   the SLSA provenance.

## Contact

For anything not covered here, or to propose becoming a co-maintainer,
open an issue on the repository. For security-sensitive matters,
follow [SECURITY.md](SECURITY.md) instead.
