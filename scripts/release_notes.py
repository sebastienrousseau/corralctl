#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
# SPDX-License-Identifier: GPL-3.0-only
"""Compose a release's notes in the layout ~/Code/AGENTS.md section 3 requires.

goreleaser writes its own notes: a header and a grouped commit log. The
standard wants Highlights, What's Changed, New Contributors, Checksums and a
Full Changelog link, and until 0.0.58 every release was corrected by hand
after it published. This prints the standard's notes so the release job can
apply them with `gh release edit`.

  Highlights        the `* **Headline**: sentence` bullets from
                    .github/releases/<tag>.md, read at the tag
  What's Changed    each pull request (or, without one, commit) on main's
                    first-parent line since the previous tag
  New Contributors  authors other than the owner with no earlier merged PR
  Checksums         SHA-256 of every asset in --assets, which is every asset
                    attached to the release
  Full Changelog    the compare link from the previous tag

Adapted from scripts/release.py in sebastienrousseau/standards (Apache-2.0
OR MIT). Standard library only, like release_preflight.py's checks that do
not need a schema.

Usage: release_notes.py TAG --assets DIR [--highlights FILE]
"""

from __future__ import annotations

import argparse
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
from pathlib import Path

REPO = os.environ.get("GITHUB_REPOSITORY", "sebastienrousseau/corralctl")
HIGHLIGHT = re.compile(r"^\* \*\*[^*]+\*\*: \S")
EM_DASH = "—"


class NotesError(Exception):
    """A reason the notes cannot be composed."""


def run(*argv: str) -> str:
    """Run a fixed git or gh command; return stdout, or raise on failure."""
    exe = shutil.which(argv[0]) or argv[0]
    done = subprocess.run([exe, *argv[1:]], capture_output=True, text=True, check=False)
    if done.returncode != 0:
        raise NotesError(f"{' '.join(argv)}: {done.stderr.strip()}")
    return done.stdout


def highlights_of(text: str) -> list[str]:
    """The Highlights bullets in `text`; the standard allows two to four."""
    bullets = [line for line in text.splitlines() if HIGHLIGHT.match(line)]
    if not 2 <= len(bullets) <= 4:
        raise NotesError(f"{len(bullets)} Highlights bullets; the standard requires 2 to 4")
    return bullets


def previous_tag(tag: str) -> str | None:
    """The release tag before `tag`, if any."""
    tags = run("git", "tag", "--list", "v*", "--sort=-v:refname").split()
    older = tags[tags.index(tag) + 1 :] if tag in tags else []
    return older[0] if older else None


def entry_for(sha: str) -> tuple[str, dict[str, str]]:
    """The pull request that merged `sha`, or the commit itself."""
    pulls = json.loads(run("gh", "api", f"repos/{REPO}/commits/{sha}/pulls"))
    if pulls:
        pr = pulls[0]
        return pr["html_url"], {"title": pr["title"], "author": pr["user"]["login"], "url": pr["html_url"]}
    commit = json.loads(run("gh", "api", f"repos/{REPO}/commits/{sha}"))
    author = (commit.get("author") or {}).get("login", "unknown")
    subject = commit["commit"]["message"].splitlines()[0]
    return sha, {"title": subject, "author": author, "url": commit["html_url"]}


def changes(tag: str, prev: str | None) -> list[dict[str, str]]:
    """Each pull request (or commit without one) in the range, oldest first."""
    span = f"{prev}..{tag}" if prev else tag
    seen: dict[str, dict[str, str]] = {}
    for sha in run("git", "rev-list", "--reverse", "--first-parent", span).split():
        key, entry = entry_for(sha)
        seen.setdefault(key, entry)
    return list(seen.values())


def new_contributors(entries: list[dict[str, str]]) -> list[dict[str, str]]:
    """Entries whose author, not the owner, has no merged pull request outside this release."""
    first: list[dict[str, str]] = []
    owner = REPO.split("/", maxsplit=1)[0]
    for author in dict.fromkeys(e["author"] for e in entries if e["author"] != owner):
        query = f"repo:{REPO} is:pr is:merged author:{author}"
        total = json.loads(run("gh", "api", "-X", "GET", "search/issues", "-f", f"q={query}"))["total_count"]
        mine = [e for e in entries if e["author"] == author and "/pull/" in e["url"]]
        if mine and total <= len(mine):
            first.append(mine[0])
    return first


def checksums(assets: Path) -> list[str]:
    """`<sha256>  <name>` for every file in `assets`, sorted by name."""
    if not assets.is_dir():
        raise NotesError(f"{assets} is not a directory")
    files = sorted(p for p in assets.iterdir() if p.is_file())
    if not files:
        raise NotesError(f"{assets} holds no assets")
    return [f"{hashlib.sha256(p.read_bytes()).hexdigest()}  {p.name}" for p in files]


def compose(tag: str, prev: str | None, parts: dict[str, list]) -> str:
    """The notes, in the section order of AGENTS.md section 3."""
    lines = ["## Highlights ⭐️", "", *parts["highlights"], "", "## What's Changed", ""]
    lines += [f"* {e['title']} by @{e['author']} in {e['url']}" for e in parts["entries"]]
    if parts["newcomers"]:
        lines += ["", "## New Contributors", ""]
        lines += [f"* @{e['author']} made their first contribution in {e['url']}" for e in parts["newcomers"]]
    lines += ["", "## Checksums", "", "SHA-256 of every asset attached to this release:", ""]
    lines += ["```text", *parts["sums"], "```", ""]
    full = f"compare/{prev}...{tag}" if prev else f"commits/{tag}"
    lines += [f"**Full Changelog**: https://github.com/{REPO}/{full}", ""]
    notes = "\n".join(lines)
    if EM_DASH in notes:
        raise NotesError("the notes contain an em dash; the standard forbids it")
    return notes


def main(argv: list[str] | None = None) -> int:
    """Print the notes for a tag, or report why they cannot be composed."""
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("tag")
    parser.add_argument("--assets", type=Path, required=True, help="directory holding every release asset")
    parser.add_argument("--highlights", type=Path, help="read Highlights from this file instead of the tag")
    args = parser.parse_args(argv)
    try:
        if args.highlights:
            text = args.highlights.read_text(encoding="utf-8")
        else:
            text = run("git", "show", f"{args.tag}:.github/releases/{args.tag}.md")
        prev = previous_tag(args.tag)
        entries = changes(args.tag, prev)
        parts = {
            "highlights": highlights_of(text),
            "entries": entries,
            "newcomers": new_contributors(entries),
            "sums": checksums(args.assets),
        }
        print(compose(args.tag, prev, parts), end="")
    except NotesError as error:
        print(f"release_notes: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
