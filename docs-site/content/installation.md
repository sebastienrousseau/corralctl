---
author: "Sebastien Rousseau"
date: "2026-08-30"
language: "en-GB"
schema: "page"
changefreq: "weekly"
copyright_year: "2026"
locale_path: "/"
base_path: "/"
name: "corralctl"
short_name: "CO"
slug_install: "installation"
slug_usage: "usage"
slug_mcp: "mcp"
slug_ref: "reference"
nav_home: "Home"
nav_install: "Installation"
nav_usage: "Usage"
nav_mcp: "MCP Server"
nav_ref: "Reference"
label_skip: "Skip to main content"
label_menu: "Menu"
label_nav: "Main"
label_theme: "Theme"
label_theme_system: "System"
label_docs: "Documentation"
label_footer_nav: "Documentation"
label_docs_nav: "Documentation sections"
label_crumbs: "Breadcrumb"
label_pager: "Page"
label_prev: "Previous"
label_next: "Next"
label_toc: "On this page"
screenshot_alt: "corralctl organising repositories into a Finder-friendly directory hierarchy."
footer_note: "corralctl clones and organises repositories from six forges into a Finder-friendly hierarchy. Published under GPL-3.0-only."
copyright: "© 2026 Sebastien Rousseau. Licensed under GPL-3.0-only."
translation_key: "install"
title: "Installation — corralctl"
description: "Install corralctl with mise, Homebrew, the AUR, the Go toolchain, or from source, and verify the download against its signed checksums."
keywords: "install corralctl, mise, homebrew, aur, go install"
eyebrow: "Getting started"
headline: "Installation"
lead: "Five channels, all serving the same released binary. Whichever you pick, the download can be verified against checksums signed with cosign."
cur_install: ' aria-current="page"'
cur_usage: ""
cur_mcp: ""
cur_ref: ""
toc_1: "Choose a channel"
toc_1_id: "choose-a-channel"
toc_2: "Prerequisites"
toc_2_id: "prerequisites"
toc_3: "Verify the download"
toc_3_id: "verify-the-download"
prev_href: "/"
prev_label: "Home"
next_href: "/usage/"
next_label: "Usage"
layout: "doc"
---

## Choose a channel

### mise (macOS and Linux)

```bash
mise use -g github:sebastienrousseau/corralctl
```

Installs the latest released `corralctl` and keeps it managed alongside your
other mise tools.

### Homebrew (macOS only)

```bash
brew install sebastienrousseau/tap/corralctl
```

This is a cask, which is a macOS-only mechanism — `brew install` on Linux will
refuse it. On Linux use the `.deb` or `.rpm` packages, the tarballs attached to
each release, mise, or the Go toolchain.

### Arch Linux (AUR)

```bash
yay -S corralctl-bin
```

`paru -S corralctl-bin` works equally well.

### Go toolchain

```bash
go install github.com/sebastienrousseau/corralctl/cmd/corralctl@latest
```

Installs into `$(go env GOPATH)/bin`, or `$GOBIN` when it is set.

A binary built this way reports `corralctl version dev`. The real version is
stamped by the release pipeline through `-ldflags`, which `go install` does not
apply — so if you need `--version` to mean something, take a release artefact
instead.

### From source

Requires Go 1.26 or newer, and Git:

```bash
git clone https://github.com/sebastienrousseau/corralctl.git
cd corral
make install
```

That installs to `~/.local/bin/corralctl`.

## Prerequisites

corralctl shells out to `git`, and authenticates through the GitHub CLI or a
token. The table lists what to install first.

| Platform | Command |
| --- | --- |
| macOS | `brew install go git gh` |
| Ubuntu, Debian, WSL2 | `sudo apt install golang git` |
| Fedora, RHEL | `sudo dnf install golang git gh` |

On Debian-family systems `gh` is not in the default archives; follow the GitHub
CLI installation guide for it.

## Verify the download

Every release publishes `checksums.txt`, signed with keyless cosign, and SLSA
provenance as `checksums.txt.intoto.jsonl`. An artefact that was tampered with
in transit fails verification rather than merely being inconvenient to
intercept.

```bash
cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature checksums.txt.sig \
  --certificate-identity-regexp 'https://github.com/sebastienrousseau/corralctl' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
```

Then check the archive itself against that file:

```bash
sha256sum --check checksums.txt --ignore-missing
```
