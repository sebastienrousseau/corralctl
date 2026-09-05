# Security Policy

corralctl is a Go-based CLI application. We take security seriously and follow industry best practices.

## Reporting a Vulnerability

Report security issues through [GitHub's private vulnerability reporting](https://github.com/sebastienrousseau/corralctl/security/advisories/new). Do not open a public issue.

You should receive a response within 48 hours. If confirmed, a fix will be released as soon as possible.

## Supported Versions

Only the latest release on the `main` branch is supported.

## Security Measures

Each item below names the workflow or file that enforces it, so the claim can
be checked rather than taken on trust. Nothing is listed here that is not
mechanically verified — this section stated that Gitleaks ran on every push
for several releases before any job actually did, and a policy that overstates
its controls is worse than one that omits them.

### Supply chain

- **Every GitHub Action is pinned to an immutable commit SHA**, never a
  mutable tag. Verified by OpenSSF Scorecard's Pinned-Dependencies check
  (`.github/workflows/scorecard.yml`), which also covers the container base
  image and the one `go install` in the release path.
- **Dependabot watches Go modules, GitHub Actions and the Dockerfile base
  image** weekly (`.github/dependabot.yml`).
- **`SBOM.md` is checked against `go.mod` in CI** — `make sbom-check`, run by
  `.github/workflows/ci.yml`. It fails if the bill of materials gains, loses
  or misstates a direct dependency, in either direction.
- **No third-party code is vendored or embedded.** Dependencies resolve
  through Go modules with checksums recorded in `go.sum`.

### Release integrity

- **Releases are signed with keyless Sigstore/cosign**, and the checksum file
  ships with its signature as a release asset.
- **SLSA build provenance is attested and published as a release asset**
  (`checksums.txt.intoto.jsonl`), so the build can be verified by anyone
  holding the artifact: `gh attestation verify <file> --owner sebastienrousseau`.
- **The release ref is verified before anything is built**: the tag must match
  a strict semver pattern, be an ancestor of `main`, and resolve to the commit
  being released (`.github/workflows/release.yml`).
- **`mcp-publisher` is Sigstore-verified before it runs**, because it
  publishes on the repository's behalf.

### Code and secrets

- **Gitleaks scans the full commit history** on every push and pull request,
  and weekly (`.github/workflows/secret-scan.yml`). Accepted findings are
  recorded per-fingerprint in `.gitleaksignore`, never by muting a rule.
- **CodeQL, `govulncheck`, `gosec` and `staticcheck` run on every pull
  request** (`.github/workflows/security.yml`, `.github/workflows/ci.yml`).
- **The MCP path sandbox is fuzzed** (`.github/workflows/fuzz.yml`). The
  invariant under test is that `Index.SafePath` never returns a path outside
  the configured workspace root.
- **Tests run with the race detector and randomised ordering**, plus three
  fixed seeds for reproducibility.
- **Commits are cryptographically signed (SSH/ED25519).** 258 of the 259
  commits in the history to date verify; the exception is `c3f48e1`, a
  documentation-only commit from before signing was habitual.

### Runtime

- **MCP mutations are gated and audited.** Destructive tools require two
  explicit flags, refuse clones holding uncommitted, unpushed, stashed,
  gitignored or submodule work, and write a durable intent record before
  acting. An audit write that fails aborts the mutation.
- **The audit log is rotated** at 8 MiB with three generations retained, so a
  long-lived server cannot fill the disk.
- **Bounded reads throughout**: 1 MiB per file, 2,000 tree entries, 10,000
  indexed repositories, 1 MiB of captured `corral exec` output.

Full software bill of materials in [SBOM.md](SBOM.md). The threat model,
including what is explicitly out of scope, is in
[docs/security-model.md](docs/security-model.md).
