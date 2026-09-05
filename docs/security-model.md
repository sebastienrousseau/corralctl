# Corral — Security Model & Assurance Case

**Status:** Living document. Last full review: 2026-07-01.
**Owner:** Sebastien Rousseau ([@sebastienrousseau](https://github.com/sebastienrousseau)).
**Scope:** the `corralctl` binary, the MCP server it ships, the release
pipeline that produces its artefacts, and the on-disk state it manages.

This document is Corral's **assurance case**: a structured argument that the
project is secure to a stated level, with the evidence that backs each
claim. It is deliberately narrower and more explicit than a marketing-style
"security policy". Reviewers, packagers, and downstream users should be
able to read this document and understand *what Corral protects*, *what it
does not protect*, *what could go wrong*, and *what compensating controls
exist*.

It maps onto the CII Best Practices Silver-tier `assurance_case` criterion
and the OSPS Baseline `SEC-*` controls; individual claims are tagged with
the specific criterion or control they satisfy.

---

## 1. What Corral is

Corral (`corralctl`) is a Go CLI that clones and organises a user's own
repositories — from GitHub, GitLab, Gitea, Forgejo, Codeberg or Bitbucket —
into a local directory tree grouped by visibility and
language. It also ships an MCP server that lets an LLM query the local
mirror without network access.

It is:

- A **read-mostly** tool from GitHub's perspective (clones, fetches).
- A **write** tool from the local-filesystem perspective (creates
  directories, `git clone`, `git pull`).
- **Never** a tool that pushes, force-updates, deletes remote content, or
  modifies GitHub state via the API.

## 2. Trust boundaries

Corral operates across four trust boundaries:

| # | Boundary                              | Direction     | What crosses it                                  |
|---|---------------------------------------|---------------|--------------------------------------------------|
| 1 | User's shell → `corralctl` process    | in            | CLI flags, environment (`GITHUB_TOKEN`), stdin   |
| 2 | `corralctl` → GitHub API              | out           | REST requests with the user's PAT                |
| 3 | `corralctl` → local filesystem        | out           | `git clone`/`git pull` writes under target dir   |
| 4 | MCP client (LLM host) → MCP server    | in            | JSON-RPC calls; gated mutations may write locally |

Boundary 2 also includes `git clone` traffic to `github.com` over HTTPS.

Boundary 4 is a pipe by default: the client launches the server as a
subprocess and nothing else can reach it. `--http` turns it into a socket.
The server carries no authentication of its own, so the address is required
to be on loopback — a routable bind is refused unless the operator also
passes `--allow-remote`, which asserts that they have put authentication in
front of it. Anyone who can reach the socket has the server's full
capability, including whatever mutations were enabled at startup.

## 3. Security properties (claims)

We claim the following properties. Each is followed by the evidence.

### C1. Corral does not persist or log resolved GitHub credentials

**Argument.** Corral resolves credentials from `GITHUB_TOKEN`, `GH_TOKEN`,
or `gh auth token`. HTTPS Git authentication is injected through process
environment configuration, not command arguments or repository config. Clone
URLs accepted by the opt-in MCP mutation API may target other hosts, but their
userinfo and query strings are redacted before audit logging.

**Evidence.**

- `internal/git/git.go` supplies the GitHub authorization header through
  `GIT_CONFIG_*` environment variables scoped to `https://github.com/`.
- Mutation tests assert credential-bearing URLs are redacted in audit records.
- No telemetry / analytics dependency. `go list -deps ./...` includes no
  analytics SDK.
- `--dry-run` prints the exact operations without executing them, so an
  auditor can inspect intended I/O.

### C2. Corral cannot write outside the user-specified target directory

**Argument.** Engine layout results reject absolute paths and traversal.
MCP paths are canonicalised against the workspace, while file resources are
additionally confined to the selected repository. Mutation targets are checked
with the same root sandbox before any filesystem operation.

**Evidence.**

- Preflight (`cmd/preflight.go`) prints the absolute target path before
  any network call and requires a TTY confirmation when the directory
  doesn't exist and `--yes`/`--dry-run` isn't set.
- Engine tests assert no writes occur when `--dry-run` is passed.
- `internal/git/git.go` invokes `git` with `-C <targetDir>` and never
  interpolates untrusted input into shell.
- `internal/mcp/sandbox_fuzz_test.go` fuzzes `Index.SafePath` and
  `Index.SafeMutationPath` against the invariant that an accepted path always
  resolves inside the configured root, comparing whole path segments rather
  than string prefixes. Run continuously in CI (`.github/workflows/fuzz.yml`)
  with the largest budget of the four fuzz targets.

### C3. The release artefacts you download are the artefacts we built

**Argument.** Every release is:

1. Built by a GitHub-hosted runner from a semantic-version tag whose commit is
   verified as reachable from `main`.
2. Signed keylessly with cosign (Sigstore/Fulcio/Rekor).
3. Accompanied by an SLSA v1.0 provenance attestation produced by
   `actions/attest-build-provenance`.
4. Published together to the same GitHub Release under the tag. Since v0.0.26
   the provenance bundle is attached as a release asset,
   `checksums.txt.intoto.jsonl`, as well as being recorded in GitHub's
   attestation store — so provenance is verifiable by anyone holding the
   artefact, without knowing the attestation API exists.

Users can verify with:

```bash
gh attestation verify corralctl_*_linux_amd64.tar.gz \
  --owner sebastienrousseau
cosign verify \
  --certificate-identity-regexp='https://github.com/sebastienrousseau/corral/.github/workflows/release.yml@refs/tags/.*' \
  --certificate-oidc-issuer=https://token.actions.githubusercontent.com \
  ghcr.io/sebastienrousseau/corral:VERSION
```

**Evidence.**

- `.github/workflows/release.yml` shows the full pipeline; every action
  is SHA-pinned per OpenSSF Scorecard `Pinned-Dependencies`.
- The release workflow runs `go test ./...` before GoReleaser and attests the
  generated checksum manifest; OCI images are signed separately with cosign.

### C4. MCP mutations require explicit capability gates and durable audit intent

**Argument.** The default server registers only read operations. Clone and sync
require `--enable-mutations`; deletion additionally requires
`--enable-destructive-mutations`. A mutation refuses to start unless its intent
record is durably appended. Completion or failure is linked by operation ID.
Deletion refuses dirty or unpublished state across every branch, stashes,
local-only or divergent tags, gitignored content, and submodules holding
unpublished commits; it refuses a target that is not a git repository, and the
workspace root itself. It fails closed when verification is unavailable.

Those refusals bound *mistakes*, not intent: an agent that has been talked
into deleting the one clone holding no unpublished work passes every one of
them, because the request is well-formed and the checks all succeed. So the
capability gate, decided once at startup, is not the only authority a
deletion needs. Each individual deletion is put to a person over MCP
elicitation before it runs, which is access control at the execution layer
rather than in prompt text. A client that cannot ask its user anything cannot
delete; the confirmation fails closed when it cannot be obtained.
`--no-confirm-deletes` removes this, and is documented as appropriate only
for an unattended workspace the operator is willing to lose.

**Evidence.**

- `internal/mcp/mutations_test.go` covers capability gates, audit failures,
  credential redaction, and delete refusal paths.
- `internal/mcp/confirm_test.go` covers the confirmation: approval, refusal,
  a dismissed prompt, a client that cannot be asked, and a client that fails
  while answering — each asserted against whether the directory still exists,
  and driven over a real client session rather than a direct handler call.
  It also asserts that a deletion the refusal cascade would decline never
  reaches a person, so the prompt does not become something people learn to
  click through.
- `internal/mcp/coverage_paths_test.go` covers the audit-failure arm of every
  refusal and of every completed mutation, so an unloggable mutation cannot
  proceed quietly.
- `internal/git/git_test.go` covers non-current branches, stashes, tags, and
  linked worktrees.
- `internal/git/submodule_test.go` covers the submodule guard against real
  repositories with real submodules, and the ignored-content guard.
- The mutation audit log rotates at 8 MiB keeping three generations
  (`internal/mcp/audit.go`), so audit retention cannot be defeated by filling
  the disk.

### C5. Corral fails closed on empty or hostile upstream state

**Argument.** For repositories with no commits, Corral detects the
condition with `git rev-parse --verify -q HEAD^{commit}` (`internal/git`)
and marks the repo `SKIP` instead of running `git pull` (which would
error with "couldn't find remote ref HEAD"). No further work is
attempted on that repo.

**Evidence.**

- `internal/engine/engine_empty_test.go` asserts SKIP + no pull.

### C6. `corralctl sync` never rewrites a branch, and never pushes a clone onto its own forge

**Argument.** The only two functions that push are `git.EnsureRemote` and
`git.PushMirror` in `internal/git/mirror.go`. The push is a single
`git push --prune --no-verify <remote> refs/heads/*:refs/heads/*
+refs/tags/*:refs/tags/*`: branches carry no force marker, so a
non-fast-forward is refused by git and surfaced as an error, while tags are
forced because the local tag namespace is authoritative for a mirror. Before
any push, `internal/mirror` compares the repository's `origin` host with the
destination's host and skips the pair when they match, so a clone taken
*from* a forge is never pruned against that forge. A destination repository
that already exists with the other visibility is refused rather than reused.

**Evidence.** `internal/git/mirror_test.go` pushes to a real bare repository
and asserts all three properties; `internal/mirror/mirror_test.go` covers the
origin guard in real and dry runs and the visibility refusal.

### C7. `corralctl sync` presents each credential to exactly one forge

**Argument.** A destination's token reaches git as an
`http.<scheme>://<host>/.extraheader` config entry in the process
environment, scoped to that destination's origin, and is never written to
`.git/config` or placed in a URL. The push path deliberately does not use
the helper that attaches the GitHub token to every git invocation, so a push
to GitLab carries the GitLab credential and nothing else.

**Evidence.** `pushAuthEnv` in `internal/git/mirror.go`, and
`TestPushMirrorScopesTheCredential`.

## 4. Threats considered and out of scope

### In scope

- **Malicious CLI arg**: mitigated by preflight banner + confirm (C2).
- **Token leakage in logs**: PAT is never logged; only length is printed.
- **Supply chain against release**: mitigated by SHA-pinned actions +
  cosign + SLSA (C3).
- **Dependency compromise**: mitigated by Dependabot on `go.mod` and by
  `govulncheck` in CI; `go.sum` locks transitive hashes.

### Out of scope

- **Compromise of the maintainer's laptop or GitHub account.** The
  maintainer's account is the root of trust; if it is compromised, a
  malicious release could be signed and shipped. Sigstore's Rekor
  transparency log makes such a release publicly auditable after the
  fact but does not prevent it. Users concerned about this scenario
  should pin to a specific release tag+digest.
- **Attacks against GitHub itself.** Corral trusts `api.github.com` and
  `github.com` as authoritative for repository state.
- **Local privilege escalation via git hooks.** `corralctl` runs `git
  clone`, and `git` executes hooks from the cloned repository during
  some operations. Users cloning arbitrary attacker-controlled repos
  should be aware of `core.hooksPath` behaviour.
- **Denial of service via GitHub rate limits.** Corral respects
  `X-RateLimit-*` headers and backs off, but cannot prevent a mistuned
  cron job from exhausting the user's own rate budget.

## 5. Assumptions

- The user's `GITHUB_TOKEN` has only the scopes Corral needs
  (`repo`, `read:user`). Corral does not need `write:*` scopes.
- `git` is installed and version ≥ 2.20 (for `-C`, `-c`, and modern
  transports).
- The user's clock is roughly correct (needed for TLS validation and
  Rekor entry timestamps).
- The user runs a supported OS: recent Linux, macOS ≥ 14, or Windows 11.
- The filesystem under the target directory is not simultaneously
  written to by another Corral process.

## 6. Compensating controls (bus factor + solo maintainer)

Corral has a single maintainer. This is a real risk to sustained
security response. Mitigations:

- **Public assurance case (this doc)**: a successor maintainer or
  reviewing packager can pick up where the current maintainer left off
  without back-channel context.
- **Documented signing keys**: `GOVERNANCE.md` records the SSH signing
  key fingerprint; a successor can publish a new key and users can
  reason about the transition.
- **Documented external services**: `MAINTAINERS.md` catalogues every
  external account (Homebrew tap, AUR, ghcr.io, DNS) so continuity is
  auditable rather than tribal.
- **Fork-and-continue is explicit**: GPL-3.0 licensing + the six-month
  unresponsive-maintainer clause in `GOVERNANCE.md` normalise the
  community-fork path.

## 7. Review and update

This document is re-reviewed on every release that touches:

- Any file in `internal/git/`, `internal/github/`, `internal/engine/`.
- Any file in `.github/workflows/`.
- Any `.bestpractices.json` entry.
- Any new outbound-network dependency.

Otherwise it is re-reviewed annually.

If you have questions or believe a claim above is not adequately
supported by the linked evidence, please file a security advisory per
[SECURITY.md](../SECURITY.md).
