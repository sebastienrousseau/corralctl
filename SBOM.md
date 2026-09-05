# Software Bill of Materials (SBOM)

**Project:** corralctl
**Format:** Go Modules (`go.mod` / `go.sum`)

corralctl has been migrated from a single-file Bash script to a compiled Go application.
The canonical source of truth for all runtime dependencies, version constraints, and cryptographic checksums is the `go.mod` and `go.sum` files located at the root of the repository.

## Core Dependencies

Versions pinned in `go.mod`. Refresh this table whenever a direct dependency is bumped.

| Component | Version | Purpose | License |
|:----------|:--------|:--------|:--------|
| `github.com/charmbracelet/bubbles` | v1.0.0 | TUI Components | MIT |
| `github.com/charmbracelet/bubbletea` | v1.3.10 | TUI Architecture | MIT |
| `github.com/charmbracelet/lipgloss` | v1.1.0 | TUI Styling | MIT |
| `github.com/google/go-github/v90` | v90.0.0 | GitHub API Client | BSD-3-Clause |
| `github.com/mattn/go-isatty` | v0.0.24 | Terminal Detection | MIT |
| `github.com/modelcontextprotocol/go-sdk` | v1.7.0 | MCP Server Protocol | Apache-2.0 |
| `github.com/spf13/cobra` | v1.10.2 | CLI Framework | Apache-2.0 |
| `github.com/spf13/pflag` | v1.0.10 | CLI Flag Parsing | BSD-3-Clause |
| `go.uber.org/goleak` | v1.3.0 | Goroutine leak detection (tests only) | MIT |
| `golang.org/x/sys` | v0.47.0 | Platform Syscalls | BSD-3-Clause |
| `howett.net/plist` | v1.0.1 | Native macOS Finder Tag property-list encoding | BSD-2-Clause |

This table lists every **direct** requirement in `go.mod`, and `make sbom-check`
(run in CI) fails if it drifts from that file in either direction. Indirect
dependencies are not enumerated here: `go.sum` is the authoritative, checksummed
record, and a hand-maintained copy of it would be wrong within a week.

The MCP SDK is mid-relicence: new contributions are Apache-2.0, and
contributions whose authors have not consented to relicensing remain MIT.

GitHub API authentication uses the `WithAuthToken` helper on the go-github client (no direct `golang.org/x/oauth2` dependency).

## System Dependencies

| Component | Type | Version Constraint | License | Source | Verification |
|:----------|:-----|:-------------------|:--------|:-------|:-------------|
| Git | CLI tool | Any | GPL-2.0 | OS package manager | `git --version` |

## Development & Build Dependencies

| Component | Type | Version Constraint | License |
|:----------|:-----|:-------------------|:--------|
| Go | Compiler/Toolchain | 1.26+ (see `go.mod`) | BSD-3-Clause |
| GoReleaser | Release Automation | v2+ | MIT |
| GNU Make | Build tool | Any | GPL-3.0 |

## Supply Chain Notes

- All third-party code is managed via Go modules with cryptographic checksums recorded in `go.sum`.
- Binaries are compiled natively via GitHub Actions and GoReleaser.
- All commits are cryptographically signed (ED25519/GPG).
