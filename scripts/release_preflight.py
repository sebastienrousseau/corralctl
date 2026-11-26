#!/usr/bin/env python3

# SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
# SPDX-License-Identifier: GPL-3.0-only

"""Prove a release can publish, before the tag exists.

Two consecutive releases published their artefacts and then failed:

  v0.0.33  the Homebrew tap refused a direct push (409), because the tap
           protects main with enforce_admins. The cask step ran inside the
           release job, so the failure also skipped the AUR publish and the
           SLSA attestation.
  v0.0.35  the MCP registry rejected server.json (422, "expected length
           <= 100"): a description had been rewritten from 91 characters to
           143. Correct prose, and unpublishable.

Neither was reachable by the existing dry run, because a dry run *skips
publishing* — and publishing is where both failed. Both were also
discoverable beforehand: the length limit is written down in the schema
server.json already points at, and the job layout is readable in the
workflow.

So this checks the things a tag push would otherwise be the first to try:

  1. server.json validates against its own declared $schema, in full. Not a
     hand-copied list of limits — the registry's own constraints, so a field
     nobody here anticipated is still caught.
  2. Every version reference agrees with the CHANGELOG.
  3. No publisher can take the release down with it. This is the one that
     recurred: after v0.0.33 the cask was moved into its own job, and
     v0.0.35 then failed the same way through a different publisher that had
     been left behind.

Exits non-zero listing everything wrong, rather than stopping at the first.

    python3 scripts/release_preflight.py [--offline]
"""

import json
import re
import sys
import urllib.error
import urllib.request

MANIFEST = "server.json"
CHANGELOG = "CHANGELOG.md"
WORKFLOW = ".github/workflows/release.yml"
HIGHLIGHTS_DIR = ".github/releases"

# Jobs that publish somewhere outside this repository. Each must be able to
# fail without taking the release, or the other publishers, with it.
PUBLISHER_JOBS = ("registry", "brew", "aur", "notes")

# Steps that must NOT live in the release job. A publish inside it aborts
# everything after it — which is how v0.0.33 lost its provenance and v0.0.35
# lost its AUR and Homebrew packages.
FORBIDDEN_IN_RELEASE = ("mcp-publisher publish", "publish_aur.sh")


def fail(problems, msg):
    problems.append(msg)


def load_json(path, problems):
    try:
        with open(path, encoding="utf-8") as fh:
            return json.load(fh)
    except (OSError, json.JSONDecodeError) as exc:
        fail(problems, f"{path}: {exc}")
        return None


def check_schema(manifest, problems, offline):
    """Validate server.json against the schema it declares."""
    url = manifest.get("$schema")
    if not url:
        fail(problems, f"{MANIFEST} declares no $schema, so nothing can validate it")
        return
    if offline:
        print(f"  schema      SKIPPED (--offline): {url}")
        return

    try:
        with urllib.request.urlopen(url, timeout=30) as resp:  # noqa: S310
            schema = json.loads(resp.read().decode("utf-8"))
    except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as exc:
        # A schema that cannot be fetched is not a passing check. The whole
        # point is to fail here rather than at publish time.
        fail(problems, f"fetching {url}: {exc}")
        return

    try:
        import jsonschema
    except ImportError:
        fail(problems, "jsonschema is not installed; the manifest cannot be validated")
        return

    validator = jsonschema.Draft202012Validator(schema)
    errors = sorted(validator.iter_errors(manifest), key=lambda e: list(e.path))
    for err in errors:
        where = "/".join(str(p) for p in err.path) or "(root)"
        fail(problems, f"{MANIFEST}: {where}: {err.message}")
    if not errors:
        print(f"  schema      OK ({len(json.dumps(manifest))} bytes against {url.rsplit('/', 1)[-1]})")


def latest_release(problems):
    """The newest version named in the CHANGELOG."""
    try:
        with open(CHANGELOG, encoding="utf-8") as fh:
            for line in fh:
                m = re.match(r"^## \[(\d+\.\d+\.\d+)\]", line)
                if m:
                    return m.group(1)
    except OSError as exc:
        fail(problems, f"{CHANGELOG}: {exc}")
        return None
    fail(problems, f"{CHANGELOG} names no released version")
    return None


def check_versions(manifest, problems):
    """Every place a version is stated must agree with the CHANGELOG."""
    released = latest_release(problems)
    if released is None:
        return

    if manifest.get("version") != released:
        fail(problems, f"{MANIFEST} version is {manifest.get('version')}, "
                       f"CHANGELOG says {released}")
    for pkg in manifest.get("packages", []):
        ident = pkg.get("identifier", "")
        _, _, tag = ident.partition(":")
        if tag and tag != released:
            fail(problems, f"{MANIFEST} image tag is {tag}, version is {released}")

    for path, pattern in (
        ("pkg/VERIFY.md", r"(?m)^VERSION=(\d+\.\d+\.\d+)"),
        ("docs-site/content/index.md", r'(?m)^hero_tag:\s*"v(\d+\.\d+\.\d+)"'),
    ):
        try:
            with open(path, encoding="utf-8") as fh:
                m = re.search(pattern, fh.read())
        except OSError as exc:
            fail(problems, f"{path}: {exc}")
            continue
        if not m:
            fail(problems, f"{path} no longer states a version where one is expected")
        elif m.group(1) != released:
            fail(problems, f"{path} states {m.group(1)}, CHANGELOG says {released}")

    if not problems:
        print(f"  versions    OK (everything agrees on {released})")


def check_highlights(problems):
    """The newest version has the hand-written Highlights its notes are built from.

    release_notes.py composes every other section from the tag, the pull
    requests and the assets; the Highlights are the one part a person
    writes. A release without them would publish notes the standard
    rejects, so the release PR fails here instead.
    """
    released = latest_release(problems)
    if released is None:
        return
    path = f"{HIGHLIGHTS_DIR}/v{released}.md"
    try:
        with open(path, encoding="utf-8") as fh:
            text = fh.read()
    except OSError:
        fail(problems, f"{path} is missing: write the 2 to 4 Highlights for v{released}")
        return
    bullets = [line for line in text.splitlines() if re.match(r"^\* \*\*[^*]+\*\*: \S", line)]
    if not 2 <= len(bullets) <= 4:
        fail(problems, f"{path} has {len(bullets)} Highlights bullets; the standard requires 2 to 4")
    elif "\u2014" in text:
        fail(problems, f"{path} contains an em dash; the standard forbids it")
    else:
        print(f"  highlights  OK ({len(bullets)} for v{released})")


def check_publisher_isolation(problems):
    """No publisher may be able to fail the release, or each other."""
    try:
        import yaml
    except ImportError:
        fail(problems, "PyYAML is not installed; the workflow cannot be checked")
        return
    try:
        with open(WORKFLOW, encoding="utf-8") as fh:
            wf = yaml.safe_load(fh)
    except (OSError, yaml.YAMLError) as exc:
        fail(problems, f"{WORKFLOW}: {exc}")
        return

    jobs = wf.get("jobs", {})

    release = jobs.get("release")
    if release is None:
        fail(problems, f"{WORKFLOW} has no release job")
    else:
        body = yaml.safe_dump(release)
        for needle in FORBIDDEN_IN_RELEASE:
            if needle in body:
                fail(problems, f"the release job runs `{needle}`; a publish inside it "
                               f"aborts every step after it, which is how v0.0.33 lost "
                               f"its provenance and v0.0.35 its packages")

    for name in PUBLISHER_JOBS:
        job = jobs.get(name)
        if job is None:
            fail(problems, f"{WORKFLOW} has no `{name}` job; a publisher that is not "
                           f"its own job cannot fail independently")
            continue
        if job.get("continue-on-error") is not True:
            fail(problems, f"job `{name}` is missing continue-on-error: true, so a "
                           f"failure there fails the release")
        needs = job.get("needs")
        needs = [needs] if isinstance(needs, str) else (needs or [])
        if "release" not in needs:
            fail(problems, f"job `{name}` does not declare `needs: release`, so it can "
                           f"publish for a release that never succeeded")

    print(f"  publishers  checked ({', '.join(PUBLISHER_JOBS)})")


def main() -> int:
    offline = "--offline" in sys.argv
    problems: list[str] = []

    print("Release preflight")
    manifest = load_json(MANIFEST, problems)
    if manifest is not None:
        check_schema(manifest, problems, offline)
        check_versions(manifest, problems)
    check_highlights(problems)
    check_publisher_isolation(problems)

    if problems:
        print("\nrelease preflight failed:", file=sys.stderr)
        for p in problems:
            print("  - " + p, file=sys.stderr)
        return 1
    print("\nRelease preflight: the manifest validates, versions agree, and no "
          "publisher can take the release down with it")
    return 0


if __name__ == "__main__":
    sys.exit(main())
