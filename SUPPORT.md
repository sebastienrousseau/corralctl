<!-- SPDX-License-Identifier: GPL-3.0-only -->

# Support

Where to take a question, in the order most likely to get you an answer.

## Before opening anything

Run the built-in diagnostics — most reports are answered by them:

```sh
corralctl --version
corralctl <owner> --dry-run --log-level debug
```

`--dry-run` shows exactly what corralctl intends to do without touching disk.
`--log-level debug` puts the reasoning on stderr while leaving stdout
parseable.

## Documentation

| You want | Read |
|---|---|
| Install, flags, usage | [README.md](README.md) |
| The rendered manual | <https://doc.corrallib.com> |
| Package reference | <https://pkg.go.dev/github.com/sebastienrousseau/corralctl> |
| How it works internally | [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) |
| Contributing and toolchain | [DEVELOPMENT.md](DEVELOPMENT.md) |
| Security posture | [docs/security-model.md](docs/security-model.md) |
| Packaging for a distro | [docs/packaging.md](docs/packaging.md) |

Once installed, `man corralctl` and `man corralctl-mcp` are available
offline, and every subcommand has its own page.

## Questions and discussion

[GitHub Discussions](https://github.com/sebastienrousseau/corralctl/discussions)
— for "how do I", "should corralctl do X", and anything open-ended.

## Bugs and feature requests

[GitHub Issues](https://github.com/sebastienrousseau/corralctl/issues), using
the templates. A good report includes:

- `corralctl --version`
- The exact command, with `--dry-run --log-level debug` output
- What you expected and what happened
- OS and `git --version`

Please redact tokens. corralctl keeps credentials out of its own output and
audit log, but a pasted shell transcript may still contain them.

## Security

**Do not open a public issue for a vulnerability.** Follow the private
reporting process in [SECURITY.md](SECURITY.md).

## Response expectations

corralctl is maintained by one person alongside other work. Issues are read
within a week; security reports are prioritised per the SLA in
SECURITY.md. There is no commercial support offering.
