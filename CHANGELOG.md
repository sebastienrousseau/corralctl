# Changelog

All notable changes to this project are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.0.37] — 2026-09-06

One binary, one base command. `corralctl` now carries both halves of the
workflow — cloning a forge into the organised tree, and mirroring that tree
back out — and every operation is a subcommand of it.

### Added

- **`corralctl sync`: mirror the organised tree to any forge corral can
  clone from.** GitHub, GitLab, Gitea, Forgejo, Codeberg and Bitbucket are
  all destinations, named as `--to <forge>[:<owner>][@<url>]` and
  repeatable. For every repository under the base directory, on every
  destination, it creates the repository when it is missing — with the
  visibility the local layout says — points a remote named after the forge
  at it, and pushes branches and tags to parity in a single round trip:

  ```text
  git push --prune --no-verify <forge> refs/heads/*:refs/heads/* +refs/tags/*:refs/tags/*
  ```

  Branches are never forced; a destination that has moved on is refused and
  reported. Tags follow the local namespace. Both properties are pinned by a
  test against a real bare repository.

  This is the standalone `corral-sync` tool folded in, and it inherits the
  things that tool had learned: the non-interactive git environment, the
  empty-repository skip, the refusal to reuse a same-named repository with
  the wrong visibility, and the `gitlab` and `gitea` remote names, so clones
  that tool already configured carry over unchanged. It also inherits its
  most recent fix, which the multi-forge support in 0.0.30 made necessary: a
  repository is **never pushed to the forge its `origin` lives on**. The
  tree can now hold a clone whose origin *is* a destination, and pushing it
  back would run `--prune` against its own upstream — with a single-branch
  clone, deleting every branch the local copy does not carry.

  Credentials are the environment variables `clone` already reads for each
  forge, and over HTTPS the same token authenticates the push, handed to git
  as an `http.<origin>/.extraheader` scoped to that forge and never written
  to `.git/config`. `--protocol ssh` uses your keys instead.

- **`corralctl clone`, the named form of what the bare command does.** The
  CLI grew from one verb into several and the one it started with was the
  only one without a name, which read as two tools sharing a binary. The
  bare form, `corralctl <owner>`, keeps working exactly as before.

- **Every forge adapter has a receiving side.** `internal/forge` gains a
  `Target` contract — who the credential belongs to, and ensure that a
  repository exists — implemented by all six adapters over the same REST
  client the listings use, plus a non-paginated `call` for creates. GitLab's
  create omits `namespace_id` for a personal namespace, because the id from
  `/user` is not the id of the namespace and sending it yields "namespace:
  is not valid"; a group is looked up by path. Each target also says how
  git should present the token over HTTPS, since each forge spells the
  username differently.

- **`internal/mirror`, the orchestration behind `sync`.** A walker that
  reads visibility from either spelling of `Public`/`Private`, treats
  `Forks` as private and never consults the repository's own name; a
  bounded worker pool; and results reported as they happen so `--output
  ndjson` streams. Every failure is a result rather than an abort, so one
  repository cannot stop the others, and the exit code is non-zero if any
  failed.

- **`git.EnsureRemote` and `git.PushMirror`**, the only two functions in
  the codebase that push. They deliberately bypass the helper that attaches
  the GitHub token to every git invocation: a push to another forge should
  carry exactly one credential, the one meant for it.

### Changed

- **The repository is now `sebastienrousseau/corralctl`, and so is the
  brand.** `corralctl` was already the binary, the Homebrew cask and the AUR
  package; the repository, the Go module, the container image and the MCP
  registry entry all said `corral`, and `corral` is taken on Homebrew. One
  name now, everywhere: the module is `github.com/sebastienrousseau/corralctl`,
  the image is `ghcr.io/sebastienrousseau/corralctl`, the registry entry is
  `io.github.sebastienrousseau/corralctl`, and installed documentation lives
  under `share/doc/corralctl`. GitHub redirects the old repository URL, and
  earlier module versions stay resolvable under the old path; the previous
  registry entry and image tags remain where they were. The `CORRAL_*`
  environment variables are unchanged, because renaming them would break
  every shell that exports one.
- The forge package's contract is now two things rather than "exactly one":
  list what an owner has, and hold a mirror of what the user has. Its
  package documentation says so.
- The statement-coverage claim in `.bestpractices.json` now names thirteen
  packages. `make claims-check` counts them.

## [0.0.36] — 2026-09-05

### Fixed

- **v0.0.35 published its artefacts and then failed, and I caused it.**
  Removing GitHub-only wording from `server.json` in 0.0.35 grew its
  description from 91 characters to 143. The MCP registry caps it at 100 and
  enforces that only at publish time, so the release rejected with

  ```text
  422 validation failed: expected length <= 100
  ```

  The rewrite was correct prose and passed every gate the project had. It was
  simply unpublishable, and the only thing that could say so was the registry
  itself, at the worst possible moment. The description is now 88 characters
  and says what the server is for rather than listing the forges.

- **A third publisher could still abort two unrelated ones.** The registry
  publish ran inside the release job, so its 422 skipped the AUR and Homebrew
  jobs — both carry `needs: release` — after the artefacts and their
  provenance were already out.

  This is the same fault as v0.0.33's Homebrew failure, in a different step,
  one release after the cask was moved out for exactly this reason. Moving
  one publisher out of the critical path fixed that publisher; it did not fix
  the shape. All three now run in their own jobs with `continue-on-error`, so
  no package host can cost a release its other channels.

### Added

- **goleak found a real leak on its first day.** Two HTTP tests in
  `internal/mcp` left goroutines behind: one cancelled the server's context
  and walked away without waiting for `ServeHTTP` to return, so on a slow
  runner `http.Server.Shutdown` was still draining after the last test in the
  package finished; both left `http.DefaultClient`'s keep-alive connection
  open, parking a `writeLoop` goroutine on a connection nothing would reuse
  because the server behind it had just stopped.

  It failed on CI and has never once failed locally — not under
  `GOMAXPROCS=1`, not under `-race`. That is the argument for the check
  rather than an objection to it: a leak that depends on timing is invisible
  on the machine that wrote it.

- **`make release-preflight`, which proves a release can publish before the
  tag exists.** Two releases in a row published their artefacts and then
  failed — the Homebrew tap refused a push in v0.0.33, the MCP registry
  rejected `server.json` in v0.0.35 — and the existing dry run could not have
  caught either, because a dry run *skips publishing* and publishing is where
  both failed.

  Both were discoverable beforehand. The length limit that broke v0.0.35 is
  written down in the schema `server.json` already points at, and the job
  layout that turned one publisher's failure into three is readable in the
  workflow. Nothing looked.

  The preflight validates `server.json` against its **own declared
  `$schema`**, in full, rather than against a copied list of limits — so a
  constraint nobody here anticipated is still caught. It checks every version
  reference agrees with the CHANGELOG. And it asserts that no publisher can
  take the release down with it: each of `registry`, `brew` and `aur` must be
  its own job, must declare `needs: release`, and must carry
  `continue-on-error`, while the release job itself must run no publish step.

  That last check is the one that matters most. The same fault occurred
  twice — v0.0.33 lost its provenance to the cask, and v0.0.35 lost its AUR
  and Homebrew packages to the registry, one release after the cask had been
  moved out for exactly that reason. Fixing a publisher is not the same as
  fixing the shape, and only the shape can be regression-tested.

  All three cases are in its regression set and confirmed to fail: the exact
  143-character description from v0.0.35, a publish step moved back into the
  release job, and a publisher losing `continue-on-error`.

  Its two Python dependencies are installed with `--require-hashes` from
  `scripts/requirements-preflight.txt`, which pins every version and every
  digest pip could choose on any platform. The first version of this step
  installed them unpinned, and Scorecard's Pinned-Dependencies check caught
  it on the pull request — a supply-chain hole in the very step whose job is
  to be trustworthy, found by a gate this project already had.

- **`server.json` is checked against the registry's field limits.** Nothing
  local knew the 100-character cap existed. `make manifest-check` now fails
  on a description over it, counted in runes rather than bytes because this
  project's prose uses en dashes freely enough for the two to diverge.
  Confirmed to fail on the exact 143-character description that broke
  v0.0.35.

## [0.0.35] — 2026-09-05

### Added

- **Goroutine-leak detection across every package that starts one.** Seven
  packages spawn goroutines — `internal/search`, `internal/mcp`,
  `internal/github`, `internal/symbols`, `internal/engine`, `internal/tui`
  and `cmd` — and nothing asserted they finished. A worker that missed one
  of its exit conditions would have left the suite green: `wg.Wait()` still
  returns for every worker that *did* finish, so the only symptom was a test
  binary holding more memory than it should.

  `go.uber.org/goleak` now runs after every package's tests. Confirmed by
  detaching a goroutine inside `SearchRepo` that never returns: the suite
  still passed, and goleak failed the package naming `walk.go:144` as the
  leak site. A second shape — a worker ignoring `hitsCap` and the context —
  was caught too, by timeout rather than by report, which is the less useful
  failure of the two but still a failure.

  `internal/mcp` and `cmd` use `goleak.Find` after their existing `TestMain`
  cleanup rather than `VerifyTestMain`, which exits the process itself, and
  only on an otherwise-passing run so a genuine failure is not buried under
  leak output from a test that returned early.

- **The `nilerr` linter**, which finds code returning a nil error when an
  error is not nil. It reported five places, and all five turned out to be
  deliberate — a walk callback skipping an unreadable entry, "not a
  repository" being read as "no state", and a cancelled MCP scan reported in
  the tool result rather than as a transport error, because returning a Go
  error there would throw away the partial count the agent can still use.

  None of them was a defect, and that is the reason to enable it: each now
  carries a written justification at the point of the return instead of
  relying on the reader working out that the nil was on purpose. A sixth
  case, in `scripts/`, needed no suppression — those files are
  `//go:build ignore` and are not linted.

### Fixed

- **The coverage gate could not see a three-statement regression.** It read
  the total off `go tool cover -func`, which rounds to one decimal — so 4874
  of 4875 statements printed as `100.0%` and agreed with a claim of 100%.
  `-func` also attributes blocks to named functions, so a statement inside a
  `var f = func(){…}` literal was absent from the report altogether.

  Between the two, the `internal/search` branch that only ran on some
  scheduler interleavings sat uncovered under `-race` while this gate
  reported agreement.

  Coverage is now counted from the profile directly, unrounded, and a claim
  of exactly 100% requires every statement — the failure names the blocks
  rather than only reporting that a number moved. Verified against a
  deliberately uncovered statement: `go tool cover` still says `100.0%`
  while the gate fails with `99.9795%` and the block's position.

  The profile is merged by taking each block's highest count before
  counting, because a `./...` profile reports a file once per test binary
  that compiled it. Summing the lines instead undercounts and produces a
  plausible-looking wrong answer — which it did, twice, while this was being
  written.

- **One branch in `SearchRepo` was covered or not depending on the
  scheduler.** Each search worker stops once its own slice reaches
  `MaxHits`, so the *aggregate* can exceed the cap only when several workers
  contribute before any of them trips it. Whether that happened was a matter
  of timing: under `-race` the scheduler serialises enough that one worker
  usually reaches the cap first, and the aggregate truncation after the wait
  never ran.

  So the same suite measured 100% without `-race` and 99.94% with it —
  which is what Codecov had been reporting all along, correctly, the moment
  uploads started working. `scripts/claims_check.go` could not see the
  difference: `go tool cover` rounds to one decimal, and 4874 of 4875
  statements still prints as `100.0%`.

  The comment in `claims_check.go` had guessed at this exactly — "a
  worker-pool branch in internal/search that does not always execute under
  contention" — and recorded that it could not be reproduced in thirty-seven
  attempts. It is now pinned by a test whose shape removes the timing from
  the question: eight files of three matches each against a cap of ten, so
  whether the work spreads across four workers or lands on one, the
  aggregate is twenty-four or twelve and both exceed ten. There is no
  interleaving that leaves it at or below the cap.

  Confirmed to fail — 24 hits against a cap of 10 — with the branch removed,
  and `-race` coverage of `internal/search` is now identical across six
  consecutive runs.

  One statement remains uncovered under `-race`, and deliberately so:
  `runSelectorProgram` is excluded by a `!race` build tag because starting a
  real Bubble Tea program trips a data race inside cancelreader, which
  closes an `os.File` while its own goroutine is still reading it. That is a
  dependency's race, not corral's, and it is documented where it is
  excluded.

- **Six documents still described corral as GitHub-only**, three releases
  after cloning worked against six forges. 0.0.32 corrected the README and
  the documentation site; these were missed because nothing links them to
  the README:

  - `server.json`, which is the description the **MCP registry publishes** —
    so the stale wording was the one agents and users read in the listing;
  - `docs-site/ssg.toml`, the site description in every page's `<meta>`;
  - `CONTRIBUTING.md`, `docs/security-model.md` and `docs/packaging.md`;
  - README.md's "When not to use Corral", which still listed five forges and
    said `corralctl <owner>` "only knows those five" — Bitbucket landed in
    0.0.33 and this paragraph was not part of that change;
  - README.md's Architecture section and its flow diagram, which described
    fetching "concurrently from GitHub" through a "GitHub API" node;
  - `corralctl mcp --help`, which promised "the GitHub API is not contacted"
    where it meant no forge API at all;
  - `.bestpractices.json`, which quoted the README's *old* opening sentence
    verbatim as evidence for an OpenSSF criterion. The README had changed
    underneath it, so a public submission was citing a sentence that no
    longer existed anywhere.

  The GitHub repository description and topics were stale in the same way
  and have been updated too, along with the homepage field, which was
  `http://` while `docs/osps-baseline-fillable.md` publicly claims "both
  official channels are HTTPS-only".

- **"…and serve them to AI agents over MCP" described a mechanism, not a
  reason.** It named a protocol and left the reader to work out what they
  would get from it, which is the wrong trade in the one sentence someone
  reads before deciding whether to try the tool.

  The README already had the answer — "your *local mirror* … queryable
  without a round-trip" — so the descriptions now say it: your AI coding
  agent can search across every clone at once, rather than only the project
  it has open, straight from disk. Changed in the three packaging
  descriptions, the documentation site's description and its homepage
  feature card. `docs/ARCHITECTURE.md` keeps the mechanism wording, which is
  the right register for a document explaining how it works.

  The first attempt at this ended "offline, without touching a forge API",
  which contradicted the first half of its own sentence: cloning from six
  forges is exactly a network operation against a forge API. Only the
  *search* is local — `internal/search` reads the clones on disk and the
  read-only MCP server makes no network calls — so that is what the sentence
  claims now. "Offline" as a blanket adjective for a tool whose main job is
  cloning was simply false.

  The feature card also still said "never contacts the GitHub API", which
  had been true of one forge out of six since 0.0.30.
- **The coverage badge had never shown a number.** `.github/workflows/ci.yml`
  delegates testing to a reusable workflow that uploads to Codecov, and that
  workflow *declares* `CODECOV_TOKEN` in its `workflow_call` contract. A
  declared secret is not inherited — the caller has to hand it over — and
  this one handed over nothing.

  The upload step therefore received an empty token, fell back to a tokenless
  upload, and **reported success**. Every CI run showed a green "Upload
  coverage" tick while Codecov received nothing and the badge read "unknown".
  The token itself was configured correctly all along; only the passing of it
  was missing.

  It is passed explicitly rather than with `secrets: inherit`, which would
  also hand `AUR_KEY`, `HOMEBREW_TAP_TOKEN` and `SCORECARD_TOKEN` to a
  workflow in another repository that asks for none of them.

  This is the third time in this release cycle that a step reported success
  without doing its job — after the AUR package and the MCP registry entry.
  The shape is always the same: an absent optional credential treated as
  "nothing to do" rather than as an error.

## [0.0.34] — 2026-09-05

### Added

- **Install snippets are now checked against the released version.** Every
  install path in this project is deliberately version-less — `@latest`,
  `brew install`, `make install` — or interpolates `${VERSION}` from the one
  line `checkVersionedProse` already guards. Nothing enforced that, so a
  hard-coded version could be added to README.md, `docs/`, `pkg/` or
  `examples/` and would then go stale invisibly: the command still runs and
  still succeeds, it just installs the wrong release, and the checksum
  matches the wrong release too. That is worse than a failure, and it is
  what `pkg/VERIFY.md` did at 0.0.29 for two releases before anyone noticed.

  `make manifest-check` now fails on a release-download URL or a
  version-pinned `go install` that names anything other than the newest
  release. CHANGELOG.md is exempt, because every version it names is
  history. Confirmed to fail on a stale pin in both README.md and `docs/`,
  and to stay quiet on a current one.

### Fixed

- **A Homebrew tap permission error cost v0.0.33 its provenance.** The
  cask was committed straight to the tap from inside the release job, and
  the tap protects `main` with `enforce_admins` — so no token can push to
  it, and goreleaser got `409 Changes must be made through a pull
  request`. Because that ran inside goreleaser, the failure aborted
  everything after it: v0.0.33's binaries, SBOMs, checksums and cosign
  bundle were published, but its SLSA provenance
  (`checksums.txt.intoto.jsonl`) was not, the MCP registry stayed on
  0.0.32, and the AUR job never ran at all.

  The AUR publish was already built so that an unreachable package host
  could not cost a release its artefacts, signatures or attestations —
  and the same commit that wrote that reasoning down put Homebrew in the
  one place where it could. The cask now follows the AUR exactly:
  generated by goreleaser under `skip_upload`, published afterwards by a
  job that cannot fail the release.

  That job opens a pull request and merges it immediately. A pull request
  because the protection admits nothing else; merged immediately because
  the tap requires no review and runs no status checks, which is what
  stops seven of them accumulating as they did through v0.0.32. It also
  reads the tap back afterwards and fails if the published cask does not
  name the version just released — reporting success without publishing
  is how both this and the AUR package went stale before.

- **`scripts/publish_aur.sh` described itself incorrectly.** Its header
  said the PKGBUILD "is generated by goreleaser… this fetches that
  generated file from the published release". It never did; the heredoc
  below it has always written the file from checksums the script computes
  itself.

## [0.0.33] — 2026-09-05

### Added

- **Bitbucket Cloud** is the sixth forge corral can clone from:
  `--forge bitbucket`, or `--forge-url https://bitbucket.org`.

  Three things about Bitbucket differ from every other forge here, and
  each was found against the live API rather than reasoned about:

  - It paginates on an absolute `next` URL, so **a short page is not the
    last page** — a filtered listing can be short and still have more.
  - It names its page size `pagelen` and *silently ignores* `per_page`,
    falling back to ten. Not an error, and invisible in the output: just
    ten times the requests.
  - Its API lives on `api.bitbucket.org` while everything a user sees
    lives on `bitbucket.org`, so `--forge-url https://bitbucket.org` —
    a perfectly reasonable input — 404s on every request unless the host
    is rewritten.

  Also: `name` is a display name ("Atlassian Event") and `slug` is the URL
  name ("atlassian-event"), so the slug is what a directory is called;
  a fork is signalled by the presence of a `parent` rather than a boolean;
  and a Mercurial repository, which an old workspace can still list, is
  skipped because it has no git clone URL.

  `prune`, orphan detection and `--protocol ssh` all followed without
  change, which is what the forge abstraction was for. Verified live
  against a real workspace.

  Credentials come from `BITBUCKET_TOKEN`, or `CORRAL_BITBUCKET_TOKEN`
  where both are set.

  **sourcehut is not included.** Its API requires authentication even to
  list public repositories, so there is no anonymous path and no way to
  verify an implementation against the live service without an account.

- **The AUR and Homebrew taps publish again.** Both had silently stopped:
  the Homebrew cask served v0.0.25 while the project shipped v0.0.32,
  because the automation opened a pull request on every release and
  nobody merged them — seven had accumulated. goreleaser has no
  auto-merge option (established by reading the schema, after nearly
  committing a comment claiming otherwise), so the cask is now committed
  directly to the tap: the file is generated, carries `DO NOT EDIT`, and
  its checksums come from the release the same job just signed.

  The AUR package sat at 0.0.13, last touched in July. `skip_upload` was
  set deliberately and for a good reason — aur.archlinux.org being
  unreachable must not cost a release its artefacts, signatures or
  attestations — but the plan it left behind was "generated in dist for
  manual publishing", and manual publishing happened once. **A step
  nobody is scheduled to run is a step that does not happen.** It is now
  a separate job running after the release with `continue-on-error`, so
  the original guarantee holds and the attempt is automatic. A missing
  `AUR_KEY` fails loudly rather than reporting success without
  publishing, which is how both this and the MCP registry entry went
  stale before.

- **`golangci-lint` runs in CI.** It had been in the `Makefile` since the
  beginning and in no workflow. The reusable `go-ci` workflow runs
  `go vet` and `staticcheck` — two of the four linters `.golangci.yml`
  enables — so **`errcheck` and `gosec` were enforced nowhere**, only on
  whichever machine last ran `make lint`. That is a habit, not a gate,
  and habits do not fail pull requests. Pinned at v2.13.2, verified clean
  at both the installed 2.12.2 and the pinned version before being wired
  in.

- **`make contrast`**, which asserts the documentation theme's colour
  pairs at WCAG AAA. `styles.css` had stated since it was written that
  "every pair the theme actually renders is asserted at WCAG AAA by
  `scripts/contrast.py`" — and that script did not exist. The claim was
  load-bearing in the way a claim should not be: it is the reason someone
  would feel safe changing a colour, and nothing behind it would have
  caught them. It checks all three token blocks independently, including
  the `prefers-color-scheme` block that serves readers who have
  expressed no preference, which is the one a recolour forgets. All 45
  pairs pass.

### Fixed

- **The Go Report Card badge had been dead for some time.** The service
  was sunset after ten years; its shields.io endpoint answers
  `404: badge not found`, so the README advertised a code-quality signal
  that no longer existed and nothing noticed, because nothing checked.
  The badge now names golangci-lint, which is the successor Go Report
  Card's own farewell points to, and `make claims-check` fails on any
  link to goreportcard.com so a dead badge cannot return by copy-paste
  from an older README.

- **The documentation site wore the theme's identity rather than its
  own.** Three faults, each shipping for months:

  - The masthead mark was Lucid's placeholder — a rounded square with an
    `L` in it — inlined under a comment explaining that inlining saves a
    request. It does, and it also meant doc.corrallib.com carried another
    project's initial beside the word "Corral".
  - **Both `favicon.ico` files were zero bytes.** `build-docs.sh`
    checked them with `-f`, so a file that existed and contained nothing
    passed a gate written specifically to catch missing assets — while
    the CNAME check two lines below already used `-s` for exactly this
    reason. An empty asset is as broken as a missing one and harder to
    notice, because the browser simply falls back to a blank page icon.
  - The palette was the theme's default blue, beside a coral logo.

  The icon is now generated from the logo's own gradient at 16, 32, 48
  and 64 px, with the small sizes thickened and deepened so the branch
  detail does not wash out to pink in a browser tab, and the palette is
  the logo's coral (`#F87171` → `#F56B5E` → `#9F1239`).

- **A failed `corral_sync_repo` or `corral_clone_repo` returned a wall of
  git internals.** The message put the entire invocation into the error —
  eight internal `-c` flags and an absolute path — ahead of what actually
  went wrong, and the MCP layer then prefixed it a second time:

  ```text
  git pull failed: git -c merge.verifySignatures=false -c rebase.verify…
  ```

  That is noise in a terminal and worse in a tool result, where it
  reaches a model's context in place of the git error. It now names the
  operation and the repository:

  ```text
  git pull failed in alpha: exit status 1: remote: Repository not found.
  ```

- **`make claims-check` could fail at random.** A single low coverage
  reading was observed for `internal/search` and could not be reproduced
  in thirty-seven attempts; the likeliest cause is a worker-pool branch
  that does not always execute under contention. The root cause is not
  claimed to be fixed — but a gate that fails at random is worse than the
  drift it guards against, because people learn to re-run it and then
  re-run it on the day it was right. A disagreement now has to survive a
  second independent measurement, following the same reasoning as
  `scripts/fuzz.sh`.

## [0.0.32] — 2026-09-04

### Fixed

- **The OpenSSF Best Practices submission understated the project.** Five
  entries in `.bestpractices.json` made present-tense claims pinned to
  v0.0.11 — "Statement coverage is 90.2% overall as of v0.0.11", naming
  six packages. Twenty releases later the real figure is 100.0% across
  twelve. Wrong in a public place, and wrong in the direction that makes
  the project look worse than it is.

  The "since v0.0.x" claims elsewhere in that file are untouched: those
  describe when a practice started and stay true.

- **Case-insensitive search reported the wrong column on multi-byte
  text.** It lowercased the line and searched that, then reported the
  offset it found — but lowercasing can change a string's byte length, so
  the offset indexed a string the caller never sees. On a line beginning
  with three `İ` the reported column was 4 where the match was at 7.

  The literal is now compiled to a quoted case-insensitive expression, so
  the search runs against the original line. That also removed a
  per-line allocation that scaled with the line: **4864 B/op on a 4 KB
  line, against 16 B/op now**, and constant regardless of length. No
  timing claim is made — the machine was too noisy for one to mean
  anything; the allocation figures are deterministic.

- **Two version references had gone stale.** `pkg/VERIFY.md` opened with
  `VERSION=0.0.29` and every command below interpolates it, so a packager
  following the page would download the wrong release and get a passing
  checksum for it — worse than a failure. The documentation site's
  version badge sat at v0.0.28, three releases behind. Neither is
  reachable from `server.json` or the `Dockerfile`, so the existing
  version check did not see them; it does now.

- **Prose still described corral as GitHub-only** in the README tagline
  and across the documentation site, two releases after cloning worked
  against five forges.

### Added

- **`make claims-check`**, so the measurable things the project says about
  itself cannot drift from the code. It runs the coverage measurement and
  compares, and fails when:

  - a stated coverage percentage disagrees with the suite;
  - a stated package count disagrees with the packages measured — a claim
    of "100% across all 9 packages" stays true about the percentage while
    silently omitting three packages added since;
  - a package the project publishes performance figures for has no
    benchmark;
  - a program under `examples/` is referenced from no document, and so
    would rot unnoticed because it still compiles.

  Each of the four was confirmed to fail before being wired in.

- **Benchmarks for `internal/search` and `internal/symbols`**, and a
  `make bench` target. Those two packages carry the 6.9s-to-1.3s figure
  and had no benchmark behind it, so a change making extraction three
  times slower would have passed every gate. CI already smoke-ran
  benchmarks; local development had no equivalent. Adding them
  immediately surfaced the allocation defect above.

## [0.0.31] — 2026-09-04

### Fixed

- **Every existing non-GitHub clone failed to sync.** v0.0.30 could clone
  from Codeberg, GitLab, Gitea and Forgejo, and then refused to touch
  what it had cloned: the second run reported
  `origin collision: target has codeberg.org/forgejo/meta, expected
  github.com/forgejo/meta`.

  The identity a listed repository was compared by was built as
  `"github.com/" + FullName`. That only ever agreed with a real remote
  because GitHub's clone URLs happen to take exactly that shape; on any
  other forge it disagreed with every clone, and the origin-collision
  guard reads a disagreement as a collision. Cloning worked. Nothing
  after it did.

  The identity now comes from the clone URL the forge returned, through
  the same function the local side goes through, so the two are
  comparable by construction. Genuine collisions are still caught, and
  now name the right hosts.

  This also fixed a latent version of the same fault in orphan detection,
  where the identity check had been silently degrading to a weaker
  name-matching fallback beside it.

- **`--protocol ssh` could clone from the wrong host.** When a forge
  returns no `ssh_url` — which a Gitea or Forgejo instance with SSH
  disabled does — the URL fell back to
  `git@github.com:<owner>/<name>.git`. The consequence is not a failure
  but something worse: cloning a *different* repository, from a host the
  user never named, that happens to share an owner and name. The SSH
  form is now derived from the HTTPS URL, so the fallback stays on the
  instance the repository actually came from.

- **`topic:` and `language:` were misdiagnosed on other forges.** They are
  GitHub search queries; passed to a forge that lists by owner they were
  read as an owner name, and the 404 that followed said "check the owner
  name" — sending somebody to look for a typo that was not there. They
  are now rejected up front, naming the forge and what to do instead.

- **Test fixtures were disabling the origin check.** Several created a
  bare `.git` directory with no config and a placeholder clone URL, which
  made the identity empty and the guard a no-op — so tests asserting
  "dry run pull" were asserting it without the check having run. The
  fixtures now carry real remotes, which is what surfaced the bug above.

## [0.0.30] — 2026-09-04

### Fixed

- **User-facing text still said GitHub.** `Fetching repositories from
  GitHub...` printed on every run, including `--forge codeberg` — and it
  is the only confirmation a user gets that the flag took effect, so
  naming the wrong service is worse than saying nothing. It now names the
  forge, and the instance too when one was given, since for a self-hosted
  deployment the instance matters more than the software. `--orphans` and
  the retry and timeout flags described themselves as GitHub-only as
  well.

  Per-profile `forge` already worked — `"settings": {"forge": "codeberg"}`
  — but was undocumented and unverified. It is now both, so one config can
  cover owners across several services.

- **`prune` and orphan detection now follow `--forge`.** v0.0.29 made
  cloning work against five hosting services and left the reciprocal
  operation behind: `--forge` was accepted on `prune` and then ignored.

  Three things were wrong, and they cancelled out. `prune` called the
  GitHub client directly whatever `--forge` said. It then built upstream
  identities as `"github.com/" + FullName`, which no Codeberg clone could
  match. And both `prune` and orphan detection matched local clones
  against a hardcoded `github.com/<owner>/` prefix, so non-GitHub clones
  were skipped rather than compared.

  The net effect was a silent no-op: `prune --forge codeberg` looked like
  it ran and did nothing. It failed safe, but fixing any one of the three
  alone would have made it delete — a Codeberg clone compared against a
  GitHub listing is an orphan by construction.

  All three move together. Listings go through the forge; identities come
  from the clone URL the forge returned; and the owner prefix is derived
  from those URLs, which makes it correct for a self-hosted instance
  nobody configured and for GitLab's nested groups, where the owner
  someone types is not the namespace a project lives in. Host scoping is
  preserved — a GitLab clone under the same owner name is still not a
  GitHub orphan, which is what the hardcoded prefix was originally added
  to prevent.

  When nothing can be attributed to an owner — no clone URLs and no host
  to fall back on — `prune` refuses and says why, rather than guessing.

## [0.0.29] — 2026-09-04

### Added

- **Cloning works against five forges**, not one: GitHub, GitLab, Gitea,
  Forgejo and Codeberg. `--forge <name>` selects one; `--forge-url`
  points at a self-hosted instance, and is enough on its own when the
  host is recognisable.

  This closes an asymmetry that had been there from the start. Corral's
  *reading* — the index, the MCP server, symbol lookup, content search —
  never cared where a clone came from, because it operates on
  directories. Only the *cloning* knew one host.

  A forge has to do exactly one thing: given an owner, list their
  repositories. Everything after that is already host-agnostic. So the
  interface is one method, and the differences each forge has are
  collapsed at the edge — GitLab's "internal" visibility becomes Private,
  because the layout has two directories and a third that only one forge
  ever populates would be worse than the lost nuance.

  Gitea, Forgejo and Codeberg are one implementation. Forgejo is a hard
  fork of Gitea that kept the API and Codeberg is a Forgejo instance;
  three copies of one client would be three things that could drift.

  GitHub keeps its existing client, wrapped rather than rewritten: its
  auth ladder, secondary rate-limit handling and search pagination are
  genuinely intricate and not worth re-deriving. GitLab and Gitea get a
  few hundred lines of `net/http` each — no new dependencies, against a
  module that holds eleven direct ones and a hand-maintained SBOM.

  Filters are applied by corral rather than pushed into each API, so
  `--include-forks` means the same thing everywhere instead of however
  each host happens to implement it.

  Verified live: Codeberg, gitlab.com, and GitHub unchanged.

- **Symbol extraction covers Python, TypeScript, JavaScript and Rust.**
  Cross-repository symbol lookup is the thing corral can do that a
  single-repository index cannot, and until now it covered Go and nothing
  else — which made it quietly wrong rather than merely limited. An agent
  asking where `parse_config` is defined in a polyglot workspace did not
  get "no match"; it got a match set missing every Python, TypeScript and
  Rust clone, with nothing to say anything had been left out.

  Go keeps `go/ast`, the compiler's own parser. The rest are read by a line
  scanner, in the tradition ctags established in 1992, because every mature
  parser for them is either CGO (tree-sitter), a port that lags the
  language, or larger than corral itself. Each scanner runs over source
  whose comment and string *contents* have been blanked — offsets and line
  numbers preserved exactly — so a `class` in a docstring, a `function` in
  a template literal and a `struct` in a nested block comment are all
  invisible to it. ADR-0006 carries an amendment recording why this was
  taken over the wazero-hosted tree-sitter path it had anticipated, and
  states plainly what a scanner gives up.

- **The symbol index is cached between runs, and repositories are searched
  concurrently.** A cross-repository symbol lookup on a real
  187-repository workspace took 6.9 seconds. It now takes 1.3.

  Most of that came from a measurement that contradicted the obvious
  assumption. Persisting the parsed symbols was the plan; measured, it
  bought 13% for 53 MB of cache, because extraction is dominated by
  walking the filesystem rather than by parsing — roughly four to one on
  that workspace — and the handler was walking 187 repositories one after
  another, leaving almost all of that I/O wait unoverlapped. Fanning out
  across repositories is what took 6.9 s to 1.5 s; the cache takes it to
  1.3 s, and gzip takes the cache from 53 MB to 3.9 MB.

  A cache hit still walks the repository, because the walk is what
  produces the fingerprint the entry is keyed on — file count, total
  bytes, newest modification time — so an edited clone is never served
  stale. Entries live under `$XDG_CACHE_HOME/corral/symbols`, one file per
  repository so invalidation is per repository and a corrupt file is a
  miss rather than a failure. `--symbol-cache off` disables it.

  Not SQLite: there are no joins, no transactions and no concurrent
  writers to reconcile, and the pure-Go driver is an order of magnitude
  larger than corral itself against a module that holds eleven direct
  dependencies and a hand-maintained SBOM.

- **`corral_search_code`.** `corral_find_symbol` answers where something is
  declared; this answers where it is written — call sites, configuration
  keys, the error string from a ticket. One call searches every clone on
  the machine, which is the part an agent cannot do with a shell, because
  it does not know the clones are there.

  Literal by default, RE2 with `regex`, and narrowable by `repo`,
  `language` or `path_glob`. Only files the file resource would serve are
  searched — otherwise search would be a way to read a refused file one
  line at a time — and the check runs inside the walk, so a denied file is
  never opened. Test files are excluded unless `include_tests` is set. The
  response reports which bound it reached rather than presenting a partial
  answer as a complete one.

  Verified live against the real workspace: 187 repositories and 109,171
  files in one call.

- **Cache hints on the results that carry them** (`ttlMs` / `cacheScope`,
  protocol 2026-07-28). Without them a client re-fetches everything every
  turn, including a tool listing that cannot have changed — the tool set is
  fixed when the process starts.

  Listings get a minute and `public`: they describe this server's own
  surface, which does not depend on who asked. Resource reads get exactly
  the workspace scan's own TTL and `private`: the TTL because promising a
  freshness the server does not itself maintain is a lie a client acts on,
  and the scope because the content is one developer's workspace and an
  intermediary serving it to somebody else is what that flag exists to
  prevent. Tool calls are deliberately left alone — a call can have side
  effects, and there is no TTL at which reusing its answer is safe.

- **The MCP server can serve over HTTP.** `corralctl mcp --http
  127.0.0.1:7777` runs the Streamable HTTP transport instead of stdio, for a
  client that connects to a server somebody else started rather than
  launching one itself. The transport is stateless, so any instance can serve
  any request and there is no session state to leak between clients.

  The address is required to be on loopback. This server has no
  authentication and exposes every repository under its root — with mutations
  enabled, it can change them — so binding it to a routable interface
  publishes all of that. `--http :7777`, which is the form typed by somebody
  thinking about the port and not about the empty host, binds every interface
  and is refused with a message naming the alternative. `--allow-remote`
  overrides the refusal for an operator who has put their own authentication
  in front of it.

### Documentation

- **Migration guides** (`docs/migrating/`) for the four places people
  actually arrive from: ghq, a hand-written clone script, a
  single-repository code index, and an unsorted `~/src`. Each says what
  carries over, what is genuinely different, and — the section that is
  usually missing — what corral will not do, so a migration does not end
  in disappointment. None of them requires re-cloning anything.

- **`pkg/`**, one directory per distribution format, so a packager can
  find the recipe without reading the release pipeline. The recipes are
  not duplicated there: each page points at the file that actually
  produces the artefact, because a copy would be a second place to
  change and a first place to forget. `pkg/VERIFY.md` covers checksums,
  keyless cosign signatures, SLSA provenance and the SBOM.

  `make pkg-check` asserts the directory matches what the pipeline
  builds, in both directions — a format with no page, and a page naming
  no format, both fail. A directory of prose cannot notice that a new
  format shipped last month, which is exactly how this kind of directory
  rots.

- **An evaluation suite for the MCP server** (`make eval`). Every other
  test asks whether a handler is correct; none asked the question that
  decides whether the server is useful — given a real question, does the
  right tool answer it with something somebody could act on?

  Eleven scenarios, each phrased as a person actually asked it, against a
  polyglot fixture workspace with a fork, a private repository and a
  credential file that must never surface in an answer. A second gate
  measures *discriminability*: that each tool's description says something
  its nearest sibling's does not, which is what a model reads when
  choosing between them. Selection itself needs a model in the loop and
  is out of scope; what this does is make the inputs to that decision
  testable, so a selection failure downstream is predictable rather than
  mysterious.

  It earned its place immediately. A scenario — "we have a retry limit in
  three services, find them all" — returned two of three, because Go
  writes `MaxAttempts` and Python writes `MAX_ATTEMPTS` and no amount of
  case-insensitivity bridges the underscore. `corral_search_code`'s
  description now says to reach for a regex when a name is spelled
  differently per language, and both the literal and the regex forms are
  pinned as scenarios.

- **A committed fuzz seed corpus** for all four fuzz targets. The inline
  `f.Add` seeds cover the readable cases; these cover the ones that are
  unreadable as Go string literals — NUL bytes, invalid UTF-8,
  bidirectional overrides, 8 KiB paths, `ext::` remotes, a URL with an
  embedded newline and a fake system prompt. They run on every `go test`,
  not only under `-fuzz`, and give a future finding somewhere to live.

### Security

- **Deletion stages the clone aside before removing it (SEC-5).** Every
  safety check ran against a path any other process on the machine could
  still write to, and the window between the last check and the `rm` was
  real. The clone is now renamed within its own parent directory first —
  same-directory, so the rename is atomic and never a cross-device copy —
  after which nothing can reach it under the name a writer knew: a
  concurrent `git commit` either fails or writes into a directory it has
  just recreated, which is not the one being deleted.

  The whole refusal cascade then runs a second time against the staged
  copy, so work that landed between the first check and the rename is seen
  *before* anything is destroyed rather than after. Losing that race costs
  nothing: the clone is renamed back and the deletion refuses, saying when
  the problem was detected. If the restore itself fails — the only outcome
  that leaves a clone under a name nobody would look for — the error names
  the staged path so it can be moved back by hand.

- **Deletion now needs a person, not just a flag.** Until now the only gate
  on `corral_delete_repo` was `--enable-destructive-mutations`, decided once
  when the process started; after that every call was authorised purely by
  having been registered.

  The refusal cascade bounds *mistakes* — it declines when a clone holds
  uncommitted, unpushed, stashed, gitignored or submodule work. It bounds
  nothing about intent: an agent that has been talked into deleting the one
  clone with no unpublished work passes every check, because the request is
  well-formed and each check genuinely succeeds. That is the gap the 2026 MCP
  guidance describes when it says access control belongs at the execution
  layer rather than in prompt text.

  Each individual deletion is now put to a person over MCP elicitation before
  it runs, and only an explicit accept proceeds — a dismissed prompt is not
  consent. A client that cannot ask its user anything cannot delete, and a
  confirmation that cannot be obtained refuses rather than proceeding. The
  question is asked only once the deletion would otherwise have gone ahead,
  so a refusal the server can reach on its own never reaches a person; a
  prompt that mostly appears for operations that were going to fail anyway is
  a prompt people learn to click through. Refusals and unobtainable
  confirmations are both written to the audit log.

  This matters more over HTTP than it ever did over stdio, where "the user
  launched this process" was a reasonable proxy for "the user wants this
  deletion". With concurrent sessions it is not.

  `--no-confirm-deletes` restores the previous behaviour, and is documented
  as appropriate only for an unattended workspace you are willing to lose.

- **Deletions are serialised per repository.** One session's safety checks
  could previously be invalidated by another session acting on the same clone
  between the check and the removal. Unrelated repositories still delete in
  parallel.

### Fixed

- **Confirmation is implemented as a multi round-trip request, not a
  server-initiated one.** Protocol 2026-07-28 (SEP-2322 / SEP-2575) forbids a
  server from issuing an elicitation request while it is serving a call, and
  the SDK enforces it. A first implementation called `ServerSession.Elicit`
  from inside the tool handler; a test driving a real client caught that it
  fails on every conforming client, which would have refused every deletion
  for the wrong reason. The handler now returns an input request and is
  invoked again with the answer, so every safety check re-runs against the
  clone as it stands *after* the person decided. Clients too old for the
  round-trip flow are served by the SDK's compatibility middleware.

## [0.0.28] — 2026-09-03

### Security

- **The MCP file resource served eight classes of credential file.** The
  content policy was a denylist, which put the burden on the list to have
  anticipated every credential filename in advance. An audit drove the
  compiled binary over real MCP stdio JSON-RPC and read back, in full:
  `.kube/config`, `kubeconfig`, `credentials.json`, `.pgpass`,
  `terraform.tfvars`, `.htpasswd`, `.yarnrc.yml` and `deploy.ppk`. The
  near-misses show the shape of the gap — `credentials` was refused but
  `credentials.json` was not, `terraform.tfstate` was refused but the
  `.tfvars` where the secrets are actually typed was not, `.npmrc` was
  refused but `.yarnrc.yml` and its `npmAuthToken` was not.

  The policy is now an allowlist with the denylist kept behind it. Source,
  documentation and non-secret configuration are served by extension, plus
  the conventional project files (`Makefile`, `Dockerfile`, `LICENSE`,
  `go.mod`); everything else is refused with a message naming
  `--allow-file-ext`, which widens the allowlist and cannot re-enable a
  denylisted file. The denylist still matters, because some credential
  stores wear an allowed extension. All eight paths are pinned as
  regression tests.

- **Repository names and origin URLs reached agent context verbatim.** The
  MCP server's job is reporting what is on disk, and nearly every string it
  reports is chosen by someone else — a directory name comes from the
  repository's owner, and `corralctl topic:…` clones repositories the user
  never named. A repository called
  `SYSTEM-ignore-prior-instructions-…` is a legal GitHub name, and was
  reproduced reaching a client's context in full, with no bound and no
  framing. This is the runtime half of the trust gap in the 2026 MCP
  security work: tool descriptions are reviewed once at connect time, tool
  responses never are.

  New `internal/sanitize` strips the mechanisms that make injected text
  invisible or unbounded — C0/C1 controls including the ESC that begins
  every ANSI sequence, bidirectional overrides and isolates, zero-width
  characters, the BOM, and invalid UTF-8 — and bounds each field. It is
  applied on the way out, never at construction: `RepoEntry.Path` is what
  `SafeMutationPath` resolves and what git is handed, so the stored value
  stays byte-exact. Sanitising cannot remove plain-language injection
  without breaking the tool, so the server instructions now tell the model
  that every returned value is untrusted data describing the workspace,
  never an instruction.

- **`corral_clone_repo` accepted any URL the agent supplied.** Not
  exploitable today — git refuses `ext::` by default, verified against git
  2.55 — but that guarantee lived in configuration corral does not own, on
  a machine corral does not control, for a parameter an agent chooses. The
  tool now validates the transport itself against an allowlist of `https`,
  `ssh` and `git`, rejecting remote-helper syntax (`transport::address`),
  `file://`, and anything beginning with `-`.

### Added

- **A Nix flake**: `nix develop` for a shell with every tool the CI gates
  need, `nix build` for the package with its manpages and completions, and
  `nix run` to try it without installing.

  The dev shell exists because of a specific failure. The devcontainer had
  installed markdownlint, codespell and pre-commit with `pip install` and
  `npm install -g`; Scorecard flagged those as unpinnable by hash, and the
  resolution was to delete them — which removed capability rather than
  securing it. Nix pins them by construction, so they are back without the
  finding. `flake.lock` is committed.

  Verified by building it in the `nixos/nix` container rather than by
  inspection: the package installs the binary, all eight manpages and
  bash/zsh/fish completions under the names their shells look up, and its
  check phase runs the whole test suite — all ten packages — in a hermetic
  sandbox. `.nix` files are now covered by the SPDX gate, proven by
  removing the header and watching the gate fail.

- **Cross-repository symbol lookup.** `corral_find_symbol` resolves a
  function, method, type, interface, constant or variable to its file and
  line across **every** clone in the workspace, not just one. That is the
  thing corral can do that a single-repository code index cannot, and it is
  what the rest of the index exists to make possible.

  Filter by kind, scope to a repository, match by substring, or restrict to
  the exported surface. Methods are found by their bare name or as
  `Receiver.Name`. Test declarations are excluded by default — on a
  well-tested repository they outnumber everything else — and can be
  included on request. Results are ranked exported-first and paginated, and
  any repository whose index hit a bound is named in the response, because
  a caller cannot otherwise tell a missing symbol from an absent one.

- **`corral_repo_overview`** summarises one repository in a single call:
  origin, file count, declaration counts by kind, and its most significant
  exported types and functions. Cheaper and far smaller than listing the
  tree and reading files.

- **`internal/symbols`**, a declaration extractor behind an `Extractor`
  interface. **Go only for now**: tree-sitter is the 2026 default for this
  and was rejected, because its Go binding is CGO and corral builds
  `CGO_ENABLED=0` — measured, three of four release targets fail to
  cross-compile with it. `go/ast` costs no dependency, no CGO, and is the
  same parser the compiler uses. Recorded as
  [ADR-0006](docs/adr/0006-symbol-extraction-without-cgo.md) with what
  would reopen it.

- **`manifest_check` gained a fourth rule**, guarding the OSPS document's
  internal consistency: every justification in the tables must be
  byte-identical to the one carried in the prefilled form link for the same
  criterion, in both directions. The tables are what a reviewer reads; the
  links are what actually reaches bestpractices.dev. Nothing but discipline
  kept them in step, and discipline is what failed when `SECURITY.md` was
  rewritten. Verified against a diverging justification, an orphaned table row
  and an orphaned link.

- **Manpages and shell completions**, generated from the cobra command tree
  by `scripts/gen_docs.go` and packaged into every archive, `.deb`, `.rpm`
  and `make install`. Eight section-1 pages (`man corralctl`,
  `man corralctl-mcp`, one per subcommand) plus bash, zsh, fish and
  PowerShell completions. Generated, never committed: a hand-written `.1`
  drifts from `--help` the first time a flag changes and nothing catches
  it. CI renders every page with `groff -ww`.

- **The Unix install contract.** `PREFIX` now defaults to `/usr/local` (was
  `$HOME/.local`) with `DESTDIR` staging, and `make uninstall` removes
  exactly what `make install` placed. Binaries, manpages, completions and
  docs land at FHS paths. `make install-smoke` stages the tree on a clean
  runner and asserts its shape, as a CI gate.

- **Windows binaries.** `windows/amd64` and `windows/arm64` are built and
  published as `.zip` archives alongside the existing Linux and macOS
  targets.

- **A release dry-run.** The Release workflow accepts `workflow_dispatch`
  with `dry_run: true`, building and packaging every artefact but stopping
  before publish, sign and attest — so new release machinery can be proven
  before its first real use rather than during it.

- **Docs Lint workflow** — markdownlint, codespell and an offline link
  check with fragment resolution, plus an SPDX licence-header gate over the
  whole tree.

- **Documentation the repository was missing**: `DEVELOPMENT.md` (toolchain
  and the local equivalent of every CI gate), `docs/ARCHITECTURE.md`,
  `docs/packaging.md` for distribution maintainers, `SUPPORT.md`,
  `AGENTS.md` (invariants for AI-assisted contributors), `CITATION.cff`,
  and five architecture decision records under `docs/adr/`.

- **`.pre-commit-config.yaml` and `.devcontainer/`**, mirroring the cheap CI
  gates locally and booting a Codespace to a working `make`.

- **README sections** the project had no home for: a unified documentation
  link block, an honest "when not to use Corral", the minimum-toolchain
  *policy* rather than just the number, stability guarantees stating the
  breaking axis as behaviour rather than signatures, and a security and
  hardening section.

### Changed

- **The workspace scan no longer trades small workspaces for large ones.**
  The previous change split discovery from enrichment into two passes,
  which threw away walk-time locality: a ten-repository workspace got
  ~0.4ms slower even with the fan-out disabled, and that was shipped as an
  accepted trade. It should not have been.

  Enrichment is now pipelined into the walk — each repository is handed to
  the pool the moment it is found, so a worker opens its `.git/config`
  while the directory is still hot from the walk that just stat'd it. The
  pool seeds at four workers and grows only under queue backpressure:
  starting at the full two dozen made a small workspace pay for goroutines
  it could not use, and starting at one left it enriching serially.

  Measured with `benchstat`, 12 runs against the pre-optimisation
  implementation. **Every size is now faster than it has ever been:**

  | Workspace | before any of this | now | |
  |---|---|---|---|
  | 10 repos | 543.8 µs | **462.7 µs** | **-14.9%** |
  | 100 repos | 4.674 ms | **2.502 ms** | **-46.5%** |
  | 1,000 repos | 55.11 ms | **23.93 ms** | **-56.6%** |

  The cost is memory: +5% to +8% bytes and +4% to +8% allocations, from the
  queue and the workers' result slices. At 1,000 repositories that is
  ~525 KB against 31 ms saved.

- **The MCP workspace scan is roughly three times faster.** A CPU profile
  put 97% of its samples in syscalls and essentially none in user code:
  every repository cost two file opens, taken one after another. Discovery
  and enrichment are now separate phases, and the enrichment fans out
  across a bounded worker pool — oversubscribed deliberately, because the
  work is waiting rather than computing.

  Measured with `benchstat`, 20 runs against the previous implementation:
  1,000 repositories go from **103ms to 33ms (-67%)**, 100 from 8.1ms to
  5.7ms (-29%), and the spread collapses from ±19% with a tail to 3.1ms
  down to ±3%. Below 16 repositories enrichment stays serial, which costs
  a very small workspace about 0.4ms of walk-time locality — a trade
  recorded in the code with its numbers.

- **Repository lookup no longer allocates per repository.** `Index.Find` is
  case-insensitive and lowercased every entry inside the loop: 999
  allocations to answer one query on a 1,000-repository workspace, on a
  path three of the eight tools reach. The keys are computed once during
  the scan that already touches every repository. **-92% time, 999
  allocations to 1.**

- **A small `--limit` no longer fetches every page.** `corralctl bigorg
  --limit 10` against a 5,000-repository organisation issued all 50 page
  requests to keep 10 repositories, on every run — 49 wasted round-trips
  against a rate limit. The concurrent fetch now stops once the limit can
  no longer consume another page, and maps each page to corral's trimmed
  `Repo` inside the worker rather than holding every page of go-github's
  ~100-field `Repository` live at once.

- **`--api-timeout` is split into `--api-request-timeout` and
  `--api-total-timeout`.** It was documented as "GitHub API request
  deadline" and applied as *both* the per-request deadline and the deadline
  for the entire paginated fetch. An organisation large enough to need 50
  pages could not be listed at all, and the help text gave nobody a reason
  to raise the value, because they believed it governed one request.

  The two are now separate, defaulting to 30s per request and 10 minutes
  for the whole fetch. `--api-timeout` keeps working for at least one minor
  release, supplying both halves so behaviour is unchanged for anyone who
  set it deliberately, and warns on stderr — never stdout, which carries
  the selected output format.

- **doc.corrallib.com is now built by `ssg` through the Lucid theme.** The site
  was a single hand-written `public/index.html` emitted by
  `scripts/generate_docs.go`, carrying its own dark palette, its own layout and
  a Google Fonts link, none of which had been through an accessibility gate.
  It is now five pages — Home, Installation, Usage, MCP Server and the
  generated Package Reference — rendered through the Lucid documentation theme
  vendored at `docs-site/_layouts`, which holds every WCAG 2.2 AAA criterion a
  theme can determine on its own. Measured across the five pages in both
  colour schemes: AAA contrast, 44px targets, heading order, one `h1` per page.

- **The OSPS self-assessment is rewritten against criteria v2026.02.19.** The
  criterion *identifiers* were reused when the standard moved, but several of
  the *questions* changed underneath them — so roughly half the Level 1
  answers argued for something the criterion no longer asked.
  `OSPS-BR-01.01` had become "sanitize untrusted CI metadata" while the answer
  still described commit signing; `OSPS-QA-02.01` had become "provide a
  dependency list" while the answer quoted test coverage; `OSPS-QA-01.01` had
  become "is the repository publicly readable" while the answer described the
  CI test suite.

  Nothing had been submitted, so no false attestation was ever published. All
  64 criteria across the three levels are now answered against the current
  questions, each linking to the file or setting that backs it: 55 Met, 2 N/A,
  6 Unmet, 1 left unanswered.

  `OSPS-AC-01.01` is deliberately unanswered. It asks whether MFA guards
  sensitive resources, which is a property of the maintainer's GitHub account
  that nothing in this repository can establish.

  Five of the six Unmet criteria are the same shape — the practice exists but
  is not written down: a secrets policy covering rotation (`BR-07.02`), a VEX
  document (`VM-04.02`), and stated remediation thresholds for dependency and
  static-analysis findings (`VM-05.01`, `VM-05.02`, `VM-06.01`). The sixth,
  `QA-07.01`, requires a non-author reviewer and needs a second maintainer
  rather than a document.

- **Release notes are a descriptive log again.** GoReleaser was emitting a
  list of merge-commit subjects and SHAs, which names branches rather than
  changes — so `OSPS-BR-04.01` could not honestly be claimed. Merge commits
  are now filtered out, the remainder grouped into Features, Fixes, and
  Security and dependencies, and the release header links to the changelog
  entry and gives the one command that verifies the download.

- **GitHub Discussions enabled.** The issue-template chooser added in v0.0.26
  linked to Discussions for anything that is not a defect or a feature
  request. Discussions were not enabled, so that link 404'd.

- **Release builds are reproducible and no longer carry the build
  machine's paths.** The goreleaser build set `-s -w` but not `-trimpath`,
  so a release shipped 1,142 strings rooted at the maintainer's home
  directory and two builds of the same commit were not byte-identical —
  which sits oddly beside SLSA provenance, since reproducibility is what
  lets a third party check provenance rather than trust it. Added
  `-trimpath` and a commit-pinned `mod_timestamp`.

### Fixed

- **The rate-limit retry could never complete a wait.** On a 403 with
  `X-RateLimit-Remaining: 0`, the transport computes the wait from
  `X-RateLimit-Reset` — which GitHub sets up to an hour out — and then
  raced it against a context that descended from the same 30s budget. Any
  reset further out than the remaining time was guaranteed to lose, so the
  most carefully written branch in the retry logic was unreachable in
  production, and secondary rate limits failed rather than backing off.

  With the budget split it is reachable. A wait that still cannot fit now
  reports immediately as a `RetryBudgetError` naming the delay, the
  remaining budget and the flag to raise, instead of sleeping until the
  deadline and surfacing a bare "context deadline exceeded" that named no
  cause.

- **A private repository could be filed under `Public/`.** `mapRepository`
  read only the API's `visibility` field, so a response carrying `private`
  but omitting `visibility` — which some endpoints and older API versions
  do — fell through to `Public`. Visibility decides the on-disk Collection
  and the value the MCP tools report, so the mistake was durable and
  visible. `private` is now authoritative and `visibility` refines it.

- **`--type sponsored` could never match anything.** `CanBeSponsored` is
  hardcoded `false`, because sponsorship status is not carried by the REST
  listing endpoints, and the filter required it to be true. The flag
  validated, ran, hit the API and returned nothing — indistinguishable from
  a correct empty result. It is now refused at flag validation with the
  reason and the list of values that do work. A filter that cannot be
  honoured is an error, never silence.

- **Orphan detection matched the owner by substring.** `findOrphans` used
  `strings.Contains(url, "/"+owner+"/")`, which matched any host — a
  `gitlab.com` clone under a same-named owner counted as a GitHub orphan —
  and matched unrelated path segments. It now uses `git.CanonicalRemote`,
  the same identity `migrateLegacy`, `originMismatch` and `prune` already
  agree on.

- **Three git subprocesses could block forever.** `CurrentBranch`,
  `IsEmpty` and `RemoteOrigin` used `exec.Command` with no context and no
  deadline, so a stale NFS mount, an unresponsive FUSE filesystem or a
  wedged index lock hung them indefinitely — and `CurrentBranch` sits on
  the sync decision path for every repository in a run. All three now take
  a context, bounded at 30s, and unwind on cancellation like the rest of
  the run.

- **Every "on this page" link pointed at nothing.** `ssg` renders Markdown with
  pulldown-cmark configured for HTML output only, which emits no heading ids,
  and its `{#id}` attribute syntax is not enabled either — it renders as
  literal text. Nothing catches that on its own: a link checker reads
  `/page/#thing` as a request for `/page/`, which is a 200, and axe has no rule
  for it. `scripts/anchor_headings.py` now derives ids from the heading text
  after the build and fails on a dangling fragment, verified by breaking one.

- **Two duplicated changelog headings**, `## [Unreleased]` and `## [0.0.25]`,
  each present twice from an earlier merge.

- **The documentation site at doc.corrallib.com documented the wrong things.**
  Four defects, all visible on the published page:

  | Defect | Detail |
  |---|---|
  | Uncompilable imports | Every package card printed `import ".../internal/github"`. Go forbids importing an `internal/` path across module boundaries, so each of the five import statements on the page was something no reader could use. |
  | Mostly private | `doc.AllDecls` published **173 of 233** declarations that were unexported helpers — `levenshtein`, `envToken`, `acquirePageSlot` — several with no doc comment, rendering as empty paragraphs. |
  | Stale package list | The generator hardcoded five paths. The module has eight. `internal/diag` shipped in v0.0.26 and never reached the site, while the documentation-coverage gate counted it — the gate checked eight packages and the site showed five. |
  | Unnavigable | 233 entries on one page with **zero** links: no contents, no anchors, no way back to the source. |

  The site now discovers packages instead of listing them, so it cannot go
  stale; publishes only exported declarations (66, down from 233); replaces
  the uncompilable import lines with a note saying why the package cannot be
  imported and a link to its source; and carries a package index with an
  anchor on every declaration. It is retitled **Corral Package Reference**,
  because it documents packages that are deliberately not an API — the
  interfaces Corral actually offers are its command line and its MCP server.

- **The site is now built in CI on every pull request**, not only on push to
  main by the deploy workflow — so a change that breaks the generator is
  caught before it merges. The job additionally fails if any package
  `go list` reports is absent from the page, or if an unexported declaration
  reaches it. Both failure modes were verified by reintroducing them.

- **Five OSPS baseline justifications asserted things `SECURITY.md` does not
  say.** Found while preparing the bestpractices.dev submission, which is the
  point at which these stop being documentation and become a public
  self-attestation:

  | Criterion | Claimed | Actual |
  |---|---|---|
  | `OSPS-VM-02.01` | 90-day coordinated disclosure timeline | not in SECURITY.md |
  | `OSPS-VM-01.01` | private disclosure channel *(email)* | GitHub private vulnerability reporting |
  | `OSPS-DO-04.01` | disclosure email + 90-day timeline | neither present |
  | `OSPS-VM-05.02` | follow-up PR within 7 days | not in SECURITY.md |
  | `OSPS-BR-07.02` | secrets rotated on personnel change | not in SECURITY.md |

  Two of these predate the v0.0.26 rewrite of `SECURITY.md`; the rest became
  false when it was rewritten. Each criterion is still genuinely met — there
  *is* a private reporting channel, a supported-versions policy and a secrets
  practice — so the fix is to describe what the file says rather than what
  someone hoped it said.

  The 90-day disclosure timeline and the 7-day dependency SLA are not
  documented anywhere, and adding them would be committing the maintainer to
  a promise rather than recording a fact, so they are left out.

- **`spdx_sweep` only ever covered `.go` files**, so twenty-one workflow and
  configuration files carried no licence header — the tree was not
  machine-readable for REUSE-style tooling. It now handles `#`-comment file
  types (preserving shebangs), gained a `-check` mode, and that mode is a
  CI gate. All 113 covered files now carry a header.

- **The README's "Back to Top" link had never worked.** It targeted
  `#corral`, and GitHub does not generate anchors for raw HTML headings.

- **Two duplicated subsections in the `[0.0.28]` changelog entry**, `###
  Changed` and `### Fixed` each appearing twice from a merge, now merged.

## [0.0.27] — 2026-08-30

### Added

- **Dependabot pull requests inside a narrow policy now auto-merge.** `main`
  requires branches to be up to date before merging — a real correctness
  gate, and one that makes every Dependabot pull request go stale the moment
  anything else lands. #96 (alpine 3.20 → 3.24) had to be rebased twice for
  that reason, once because merging #97 moved `main` underneath it. With
  weekly grouped updates that is recurring toil, and toil on a
  security-update path is how security updates end up sitting.

  Auto-merge bypasses nothing: all thirteen required checks, the signature
  requirement and the up-to-date gate still have to pass first. It removes
  the click, not the gate. "Always suggest updating pull request branches"
  is enabled alongside it so GitHub can clear the up-to-date requirement
  without a manual rebase.

  The policy is an allowlist, deliberately: `github-actions` and `docker`
  updates of any size, because both are pinned by SHA or digest here and the
  diff is a single reference; Go module *patch* and *minor* bumps. Major Go
  bumps and anything whose update type is unknown — which is what a grouped
  pull request reports — fall through to review. A denylist would have
  failed open on exactly the grouped case that most needs a human.

  The trade-off, stated plainly: a dependency passing CodeQL, gosec,
  govulncheck, Dependency Review, gitleaks and a 100%-coverage suite on three
  platforms would merge without anyone reading it. On a repository with no
  second reviewer, the alternative is not review — it is the same maintainer
  clicking merge without reading it either. Delete the workflow to go back to
  that.

### Fixed

- **A base-image bump silently falsified an attestation.** Dependabot moved
  the Dockerfile from `alpine:3.20` to `3.24` — the first bump the new Docker
  ecosystem entry produced. `docs/osps-baseline-fillable.md` quoted
  `alpine:3.20@sha256:d9e853…` as its `OSPS-BR-03.02` justification, so the
  moment that merged, a document submitted to bestpractices.dev as an
  attestation became false. Nobody edits that file during a dependency bump,
  which is exactly why it drifted — the same shape as `go-github` v74 sitting
  in `SBOM.md` for six releases.

  The prose no longer names a version: the Dockerfile's `FROM` line is the
  single source of truth. `scripts/manifest_check.go` gained a third rule
  that fails CI if any prose file names a base image the Dockerfile does not
  build on, so a concrete version cannot be reintroduced and left to rot. The
  rule was verified against both directions: reintroducing the stale claim
  fails, and naming the correct version passes.

## [0.0.26] — 2026-08-29

A hardening release. No behaviour changes for existing invocations; one new
flag, and a large amount of work making the repository's own claims about
itself true and mechanically checked.

### Added

- **`--log-level` (and `CORRAL_LOG_LEVEL`)** selects diagnostic verbosity on
  stderr: `error`, `warn`, `info` or `debug`. Results still go to stdout in
  whatever `--output` selects, so `--output json --log-level debug` stays
  pipeable while giving a bug report something to attach. The default,
  `info`, prints exactly what corral printed before. Diagnostics now run
  through a new `internal/diag` package rather than the standard logger; an
  unknown level name is rejected rather than silently ignored.

- **Secret scanning.** `SECURITY.md` claimed *"Gitleaks scans run on every
  push and pull request"*. No job in this repository ran gitleaks, and none
  in the reusable pipelines it calls did either. The claim is now true:
  `.github/workflows/secret-scan.yml` scans the full commit history on every
  push, every pull request and weekly, using a checksum-verified upstream
  binary rather than a mutable action. The one historical finding — a fixture
  of deliberately fake credential filenames proving the MCP reader refuses
  them — is recorded by fingerprint in `.gitleaksignore`, not by muting a
  rule.

- **A manifest drift check**, `make sbom-check`, run in CI. It fails if
  `SBOM.md` gains, loses or misstates a direct dependency in either
  direction, and if `server.json`'s version does not match the newest
  `CHANGELOG.md` release or its own OCI image tag. Both files had drifted
  silently before: `SBOM.md` carried `go-github/v74` for six releases after
  `go.mod` moved to v90 and omitted three direct dependencies while
  `SECURITY.md` linked to it as the *full* bill of materials; `server.json`
  sat at 0.0.13 through five releases.

- **A fuzz target for the MCP path sandbox.** `Index.SafePath` is the
  boundary that keeps an agent inside the workspace root — a defect there is
  arbitrary file access, not a wrong answer — and it was the one
  security-critical function with no fuzz target. The invariant under test is
  absolute: whatever the sandbox returns is inside the root.

- **Benchmarks** for the workspace scan, index lookup, sandbox check, layout
  evaluation and existing-clone discovery. CI compiles and briefly runs them
  on every push so they cannot rot.

- **Audit log rotation.** The MCP mutation log grew without limit; a
  long-lived server would fill the disk. It now rotates at 8 MiB keeping
  three generations, before the write rather than after, so the bound is a
  real bound.

- **`CODEOWNERS`, a pull request template and issue templates.**

- **Dependabot now watches the Dockerfile base image.** The Alpine base is
  pinned by digest, which is correct — and meant nothing was watching it.

### Changed

- **Provenance is published as a release asset.** SLSA build provenance was
  attested into GitHub's attestation store, which is not the release: anyone
  auditing the download page saw signatures with no provenance beside them.
  Releases now carry `checksums.txt.intoto.jsonl`.

- **Every pull request must target the default branch.** On 2026-08-19, PR
  #91 was opened against `feat/v0.0.24` rather than `main`. Nothing here ran
  for it — every workflow filters on `pull_request: branches: [main]`, so a
  pull request aimed anywhere else is invisible to CI — and when #90 merged,
  GitHub marked #91 merged too and collapsed its commit range to nothing.
  OpenSSF Scorecard reads a merged PR's check suites through
  `associatedPullRequests { commits(last: 1) { … checkSuites } }`, so an
  empty commit range yields zero suites, and `parseCheckRuns` caches that
  empty answer under the head SHA, defeating the REST fallback that would
  have found the eleven suites GitHub does hold. #91 was scored as a merged
  pull request with no CI test at all, which is code scanning alert 37.

  The new `PR Base` workflow fails any pull request whose base is not the
  default branch. It carries no `branches` filter of its own, because a pull
  request aimed at the wrong base is precisely what a filtered workflow can
  never see.

- **`SECURITY.md` rewritten against what is actually enforced.** Every claim
  now names the workflow or file that enforces it, and the commit-signing
  claim states the one historical exception rather than asserting an absolute
  that is 99.6% true.

- **`internal/engine` is usable as a library.** `Run` called `os.Exit` on six
  validation paths and returned nothing, so the package could not be embedded
  despite `examples/engine_run.go` presenting it that way. `RunE` now returns
  an error — an `*ExitError` when the failure maps to a specific exit status
  — and `Run` is the thin wrapper that turns that back into a process exit
  for the CLI. Behaviour and output are unchanged.

- **The three longest functions are decomposed.** `engine.Run` (307 lines),
  `FetchReposWithClientOptions` (195) and `processRepo` (182) are now
  pipelines of named, individually testable steps. `selectorModel.Update`
  (128) is split by keypress. No behaviour changed; the whole existing test
  suite passed throughout without modification.

- **Dependencies refreshed.** Every direct and reachable indirect module is
  at its latest version; `govulncheck` reports none.

### Documentation

- **The examples now compile in CI.** They carry `//go:build ignore`, so
  `go build ./...` skipped them and nothing else touched them — they could
  have referenced a renamed function indefinitely without a single job
  noticing. `make example-check` compiles all four against the real module.
  `examples/engine_run.go` now demonstrates `RunE` and `*ExitError`, which is
  the point of making the engine embeddable.

- **README corrections.** The MCP section's `--audit-log` flag was
  undocumented; the deletion refusal list omitted gitignored content and
  submodules holding unpublished commits, both of which the cascade actually
  checks; the "no network calls" claim did not distinguish the read-only
  default from `--enable-mutations`, where `git` does reach the network; and
  an install cross-reference pointed at a `go install` section that did not
  exist. That section now exists, and records that a `go install` build
  reports `version dev` because `-ldflags` are applied only at release.

- **`docs/security-model.md`** claims C2, C3 and C4 updated: C2 cites the new
  path-sandbox fuzz target, C3 records provenance as a release asset, and C4's
  refusal list and evidence now match the code.

- **`docs/osps-baseline-fillable.md`** refreshed from a v0.0.11 snapshot. It
  claimed 90.2% coverage (now 100%), 56/56 documented symbols (now 93/93),
  gitleaks running on every PR (it was not, until this release), and
  `OSPS-SA-03.02` unmet with a threat model as a "candidate for a future
  security-model.md" — which exists. The prefilled bestpractices.dev form
  links were rewritten alongside the tables so the two cannot disagree.

### Testing

- **Coverage is 100% of statements**, up from 97.6%, across all eight
  packages. The gaps that closed matter more than the number: the least
  covered function in the repository was `submodulesHaveUnpublishedWork` at
  18.2% — the guard that stops a clone with unpushed submodule commits from
  being deleted. It is now exercised against real git repositories with real
  submodules, as are the ignored-content and origin-mismatch guards beside
  it.

- Error paths that could not previously be reached are reachable and tested:
  every audit-write failure arm of every mutation tool, the concurrent
  page-fetch cancellation path, the config write and re-encode failures, and
  the clamps on `defaultConcurrency`. Where that required a seam, the seam
  says in its comment why the branch was otherwise untestable.

## [0.0.25] — 2026-08-20

### Fixed

- **`corralctl config --init` wrote a file the tool could not read back.** The
  starter config documents itself with `"//"` keys — a block at the top level
  and `"//<flag>"` notes beside each setting — but the config decoder runs with
  `DisallowUnknownFields`, so loading it failed immediately:

  ```console
  corralctl: decode config ~/.config/corral/config.json: json: unknown field "//"
  ```

  Round-trip was broken out of the box: after `--init`, every subsequent
  `config --explain`, `plan` or `profile` aborted. Reported as #92.

  The strictness is worth keeping — a misspelled `concurrancy` should be an
  error, not a setting that silently does nothing — so the fix is not to relax
  the decoder. Comment keys are stripped from every object in the document
  first, at any depth, and the strict pass then validates what remains.

  Pinned by a test covering all three properties: the starter file loads, real
  settings alongside comments still apply, and a misspelled key is still
  rejected. A key beginning with a single `/` is a typo rather than a comment
  and is still an error, so the prefix check cannot over-match.

## [0.0.24] — 2026-08-19

### Changed

- **MCP server migrated from `mark3labs/mcp-go` to the official
  `modelcontextprotocol/go-sdk` v1.7.0.** corral's MCP surface was built on a
  third-party implementation that tracked the specification at its own pace;
  the official SDK is maintained alongside the spec itself. The dependency is
  gone from `go.mod` entirely.

  Every tool now takes a typed Go input struct, and the SDK derives each tool's
  JSON Schema from that struct's fields and `jsonschema` tags. Previously the
  schemas were hand-written alongside hand-written argument parsing, so the two
  could disagree — a schema could advertise a parameter the handler never read.
  They are now the same declaration, and a test asserts the generated schemas
  reach the wire.

  One behaviour worth knowing: over the legacy `initialize` handshake a client
  negotiates protocol `2025-11-25`, not `2026-07-28`. That is the SDK's
  deliberate cap — `initialize` is deprecated in the 2026-07-28 specification,
  which negotiates versions by a different path — and not a corral limitation.

- **`maxTreeEntries` is a variable rather than a constant**, matching the
  existing `maxIndexRepos`, so the tree-truncation bound is reachable from a
  test without materialising 2,000 files. The truncation notice now reports the
  actual bound instead of a hard-coded "2000", which would have become wrong
  the moment the bound changed.

### Added

- **A protocol-level test harness** driving a real client against a real server
  over the SDK's in-memory transport. The previous tests called handler
  functions directly, so they could not have caught a tool that was registered
  wrongly, a schema that failed to generate, or an annotation that never
  reached the wire — all of which are exactly what a migration puts at risk.

- **Mutation coverage against the seams**: every failure path in
  `corral_sync_repo`, `corral_clone_repo` and `corral_delete_repo` — including
  each case where the operation fails *and* its audit record cannot be written,
  which must tell the caller both things.

### Fixed

- **A delete-guard test that passed without testing anything.** It aimed a
  delete at a directory with no `.git`, but the scanner never indexes such a
  directory, so the lookup failed first and the `IsRepository` guard it existed
  to cover was never reached. It now reproduces the race the guard actually
  defends: a repository that is indexed, then stops being a repository before
  the delete acts on it. Verified by coverage that the guard line now executes.

- **Stale dependency and tooling references** to `mark3labs/mcp-go` and
  `go-github v74` in `.bestpractices.json` and `.goreleaser.yaml`.

- **A flaky `-race` failure in `internal/tui`.** `TestRemainingSelectorStates`
  started a real Bubble Tea program to cover `runSelectorProgram`, the one-line
  adapter over `tea.NewProgram(...).Run()`. Bubble Tea's `Program.shutdown()`
  calls `cancelreader`'s `Close()`, which closes the underlying `os.File` while
  that reader's own goroutine is still using it — a data race inside the
  dependencies, not in corral. Both `cancelreader` backends are affected
  (kqueue when stdin is a terminal, select when it is not), so redirecting
  stdin does not avoid it; it only changes which one races, and in fact makes
  it far more frequent.

  The real-runner assertion is now built only under `!race`, so it still runs —
  and still covers that line — in ordinary and coverage runs, while
  `go test -race` skips just that one call rather than the surrounding test.
  Reproduced 22 times in 40 terminal runs before the change and 0 in 40 after.

- **`corral prune` printed nothing when there was nothing to prune.** Text
  mode emitted one line per pruned repository and no other output, so a run
  that found no orphans was byte-for-byte identical to a run that never
  reached the reporting step at all. On the one subcommand whose job is
  deleting directories, silence is the answer a user cannot safely interpret —
  it reads equally as "your clones are all accounted for" and as "the owner
  lookup quietly returned nothing". It now states the result and names the
  owner: `No prunable repositories found for <owner>.` JSON mode was already
  unambiguous (it emits `[]`) and is unchanged; a test pins both.

- **A new MCP test that could only pass on Unix.** `TestFileResourceReportsUnreadableFile`
  made a file unreadable with `chmod 0o000` and asserted the read failed. On
  Windows `os.Chmod` only toggles the read-only attribute, which does not stop a
  read, so the file stayed readable and the assertion failed on
  `windows-latest` while passing on macOS and Linux. The open-failure branch is
  now covered through the `openResource` seam, which behaves identically on
  every platform, and the real-filesystem permission check is kept — and skipped
  on Windows with the reason — as proof the seam stands in for something that
  actually happens.

## [0.0.23] — 2026-08-18

### Changed

- **`google/go-github` upgraded v74 → v90**, sixteen major versions in one step.
  The surface corral uses is small — twelve symbols in a single non-test file —
  so a staged walk through each major would have been ceremony rather than
  safety. Two breaking changes had to be adapted:
  - `gh.NewClient` is now a variadic options constructor returning an error,
    and `WithAuthToken` moved from a chained client method to a
    `ClientOptionsFunc`.
  - `Client.BaseURL` is a read-only accessor; the base URL is set at
    construction through the `WithURLs` option. This only affected the test
    client.

### Added

- **Field-mapping tests covering every value corral reads from go-github**,
  decoded from a realistic API payload through go-github's own struct tags.
  Sixteen majors of a generated API client is exactly where a renamed JSON tag
  starts silently yielding zero values, and most such regressions would not
  fail a build: corral would simply report every repository as language
  "Other", visibility "Public", or with a zero `pushed_at` — which disables
  smart-sync by making every repository look never-synced. A full sync of
  everything looks like working software, so this needed an explicit
  assertion rather than an end-to-end smoke test. Confirmed to fail against a
  simulated mapping regression.

## [0.0.22] — 2026-08-18

### Fixed

- **The MCP registry publish reported success while publishing nothing.** The
  step was gated on an `MCP_REGISTRY_TOKEN` secret, but it authenticates with
  `mcp-publisher login github-oidc`, which uses the GitHub Actions OIDC token
  the job already holds via `id-token: write` and needs no secret at all. With
  the secret unset the step took its skip branch and exited 0, so the registry
  entry stayed at 0.0.13 through v0.0.21 — including the release where the
  publisher itself was finally working. Gate removed.

## [0.0.21] — 2026-08-18

Usability release. The v0.0.20 work made corral safe; this makes it
configurable.

### Fixed

- **28 of 31 flags were unreachable from the commands that consumed them.**
  `plan`, `prune` and `profile` all build their options from the same variables
  as the root command, but the flags were registered on the root command only:

```console
$ corralctl plan acme --limit 5
unknown flag: --limit
```

  So `corralctl plan` always ran at limit=1000, concurrency=1, visibility=all —
  unconfigurable and unstated. The fetch/filter and clone/sync groups are now
  shared flag sets registered on each command that acts on them. `prune`
  deliberately gets only the fetch group, since it removes clones and never
  creates one.

- **The config file covered 5 settings of 31, and only `corralctl profile` read
  it.** Settings are now keyed by flag name — `"concurrency": 8` is exactly
  `--concurrency 8` — so the file covers the whole surface and picks up new
  flags automatically, and it applies to every command. Precedence, highest
  first: an explicit flag, the selected profile, the defaults block, the flag's
  own default. Configs written before this release keep working: the old
  snake_case profile fields are still parsed.
- **A mistyped setting used to be ignored silently.** An unknown key is now an
  error naming the key, because a setting the user believes took effect and did
  not is worse than one that fails. A key naming a flag owned by a *different*
  command is not an error — one file serves the whole CLI.
- **Config values were validated differently from typed ones.** A profile's
  settings went through a hand-written allow-list covering five fields. Every
  value now goes through the flag's own parser and the shared
  `validateCommonFlags()`, so `"protocol": "carrier-pigeon"` is rejected with
  the same message as `--protocol carrier-pigeon`. `plan`, `prune` and
  `profile` run that validation too; previously a bad value reached the engine,
  which printed an error and terminated the process.
- **`--concurrency` defaulted to 1**, so the documented concurrency feature was
  off unless asked for and the README's "10x-50x faster" claim rested entirely
  on `pushed_at` caching. It now sizes from the host, bounded to 4–8.
- **`go install` produced a binary named `corral`, not `corralctl`**, because
  the module basename is `corral`. `main.go` moved to `cmd/corralctl/`. This is
  the same repo-vs-binary mismatch that makes mise's `ubi:` backend install an
  unusable `corral` binary; `github:` is the backend that works.
- **The README advertised "Homebrew (macOS / Linux)"** while shipping a cask,
  which is a macOS-only mechanism — `brew install` on Linux refuses it. The
  README now says macOS and points Linux users at the .deb/.rpm packages and
  tarballs from the same release. (The cask stays: goreleaser has deprecated
  `brews:`.)

### Changed

- **MCP tool responses are bounded.** `corral_list_repos` and
  `corral_workspace_index` returned every match, indented, at ~526 bytes per
  repository: ~55,000 tokens for 500 repositories against a 25,000-token client
  budget, i.e. over budget from roughly 190 repositories. Both now page
  (`limit`, `offset`, `next_offset`; default 50, max 200) and default to a
  concise projection, with `response_format: "detailed"` for the full entry.
  Measured on a synthetic 500-repository workspace: **~55,000 tokens → ~1,957**.
- `corral_workspace_index`'s description no longer invites the most expensive
  call it can make; it points at `corral_list_repos` for filtered work.
- `Index.Truncated` is finally surfaced in tool payloads. It was set when a
  workspace exceeded the scan cap and never reported, so an over-cap workspace
  looked complete to the caller.
- `corral_list_repos` renames `count` to `total_matched`. With a page window a
  bare count is ambiguous between matched and returned.

### Added

- `corralctl config --init` writes a commented starter config documenting the
  real flag surface, and refuses to overwrite an existing file.
- `corralctl config --explain` reports each effective setting and where it came
  from. A layered config is only debuggable if you can ask it why a value is
  what it is.

## [0.0.20] — 2026-08-17

Safety release. Everything below was found by an audit of the v0.0.19 tree;
several of these were reachable from a single mistyped argument.

### Fixed

- **The sync sidecar no longer dirties every clone.** `.corral-state.json` was
  written into each clone's working tree, so `git status` reported every
  corral-managed repository as modified. Because `corralctl status`,
  `corralctl prune` and the MCP delete tool all refuse to act on a repository
  with local changes, `prune` could never prune anything and `status` reported
  every repo dirty. The sidecar now lives at `<gitdir>/corral-state.json`,
  which is outside the working tree by construction. The old location is still
  read, so existing clones keep their smart-sync state, and it is removed on
  the first write.
- **A typo'd subcommand no longer starts a live run.** `corralctl statuss` was
  a valid invocation meaning "owner=statuss" and began fetching from GitHub and
  cloning into `$HOME/Code`. Arguments within edit distance 2 of a real
  subcommand are now rejected with a suggestion; `corralctl [flags] -- <owner>`
  forces the owner reading.
- **Positional arguments no longer swallow `base_dir`.** Ten ordinary directory
  names — `forks`, `stars`, `name`, `public`, `private`, `templates` and others
  — were consumed as filter keywords, and the target directory silently fell
  back to `$HOME/Code`. Repository type and sort are now `--type` and `--sort`,
  and the positional grammar is the documented `<owner> [base_dir] [limit]`.
- **The preflight confirmation is no longer a no-op off-TTY.** It returned true
  whenever stdin was not a terminal, so it protected nobody in scripts, pipes,
  cron or CI. Creating a new target directory without a TTY now refuses, with a
  non-zero exit so callers can tell "did nothing" from "succeeded".
- **Files in subdirectories are readable over MCP.** The file resource used RFC
  6570 simple expansion, which does not match `/`, so nothing below a
  repository's top level resolved at all.
- **MCP file reads no longer expose credentials.** `.git` was hidden from the
  tree listing but not the file reader, so `.git/config` — and `.env`, `.npmrc`,
  private keys — were readable. Now denied, alongside `.ssh`, `.aws` and
  `.gnupg`.
- **A workspace root that is itself a repository no longer collapses the
  index.** The scan matched the root, appended it as the only entry and aborted,
  and `corral_delete_repo` could then resolve that entry back to the root.
- **Detached-HEAD commits block deletion.** The guard counted
  `rev-list --branches`, which covers `refs/heads/**` only, so work committed in
  detached HEAD was invisible and the delete proceeded. Widened to `--all`.
- **Gitignored content blocks deletion.** `git status --porcelain` excludes
  ignored files, so local `.env` files and databases — the least recoverable
  content in a clone — were destroyed silently.
- **Submodules with unpushed commits block deletion.**
- **`corralctl prune` refuses a truncated upstream listing.** It compared
  against at most `--limit` repositories, so for an owner with more than that,
  every repository past the cap looked like an orphan and was deleted.
- **Non-clone directories are no longer relocated.** Legacy migration treated a
  matching *name* as sufficient grounds for `os.Rename`, so an unrelated folder
  sharing a name with one of the owner's repositories was moved — unprompted,
  and invisible in `--dry-run`. Migration now requires a `.git` directory and a
  matching origin remote.
- **Errors print once.** Cobra printed them and then `ExecuteContext` printed
  them again, inside a full usage dump: 52 lines with the message duplicated at
  both ends, now 3.
- **A malformed `--layout` fails immediately** rather than after a full
  paginated GitHub fetch.
- **`server.json` is published.** Nothing shipped it, so the MCP registry entry
  sat at 0.0.13 across five releases, advertising a stale image tag. The release
  workflow now publishes it and fails if the file and the tag disagree.

### Changed

- **MCP tool annotations are set.** mcp-go's zero value serialises as
  `destructiveHint: true`, so all five read-only tools advertised themselves as
  destructive — and `corral_delete_repo` carried the identical annotation,
  making the signal worthless. Clients use these to decide whether to
  auto-approve, so reads were paying a confirmation tax while deletion gave no
  warning.
- **The MCP server sends `instructions`**, describing the on-disk layout, which
  tool to start with, and whether writes are enabled.
- Resource subscriptions are no longer advertised. The capability was announced
  and never implemented, so a subscribing client waited forever.
- Go toolchain 1.26.1 → 1.26.6 in `go.mod` and all four workflows.
- `mcp.json` replaced by `examples/mcp-client-config.json`. It was stale at
  0.0.8, invalid against the schema it declared, and used the filename
  convention of a *client* config — so copying it into `.cursor/mcp.json`
  produced a non-functional file.

### Added

- Machine-readable SPDX SBOMs per release archive. `SBOM.md` was
  hand-maintained and had already drifted, omitting `mcp-go` — a direct
  dependency powering the entire MCP server.
- A keyless cosign Sigstore bundle over `checksums.txt`
  (`checksums.txt.sigstore.json`), attached as a release asset, so a consumer
  who downloads a tarball has something next to it to verify against. Until now
  SLSA provenance lived only in GitHub's attestation store and the cosign
  signatures covered only the OCI images — neither is a release asset.
  Verify with `cosign verify-blob --bundle checksums.txt.sigstore.json ...`
  (see `.goreleaser.yaml` for the full command). Note this is the modern bundle
  format rather than a separate `.sig`/`.pem` pair, because cosign v3 removed
  `--output-signature`/`--output-certificate` from `sign-blob`; whether OpenSSF
  Scorecard's Signed-Releases check credits a `.sigstore.json` bundle has not
  been verified.
- `go test -race -shuffle=on`, fixed shuffle seeds, and `govulncheck` in CI.
- **Seam-binding tests.** The suite reported 99.8% coverage with seven packages
  at 100%, yet 18 of 33 injected mutants survived — every one a default
  indirection binding. Replacing `main`'s `executeContext` with a no-op turned
  the whole binary into a program that does nothing and the suite stayed green.
  Four tests now pin 30 seams to their production implementations.

### Security

- The two MCP fixes above are a pair: repairing the `{+path}` routing without
  the denylist would have converted an unreachable resource into a working
  credential-exfiltration primitive for any prompt-injected agent. Verified
  locally — with routing fixed and no denylist, reading `.git/config` returned a
  token.

## [0.0.19] — 2026-08-16

### Changed

- Dependency refresh: `github.com/mark3labs/mcp-go` 0.57.0 → 0.58.0 and
  `golang.org/x/sys` 0.46.0 → 0.47.0.
- Pinned GitHub Actions refreshed, including `github/codeql-action/upload-sarif`
  to v4.37.7 and `hadolint/hadolint-action` to v3.4.0. Dependabot now groups
  `github-actions` updates into a single pull request.

### Fixed

- The Dockerfile runtime user is created with an explicit numeric uid/gid
  (`65532:65532`) so Kubernetes `runAsNonRoot` can verify the container is not
  root, and hadolint 2.15.0's DL3066 is satisfied.
- `server.json` now tracks the released version. It had been stale at 0.0.13
  since that release, so the MCP registry entry advertised an outdated
  `ghcr.io/sebastienrousseau/corral:0.0.13` image tag.

## [0.0.18] — 2026-08-02

### Changed

- Release container tooling now uses the Node.js 24-compatible Docker QEMU,
  Buildx, and registry login actions pinned to immutable release commits.

## [0.0.17] — 2026-08-01

### Added

- Native macOS Finder Tags for repository lifecycle, visibility, ecosystem,
  ownership, and repository type while preserving user-managed tags.
- Canonical `Public`, `Private`, `Forks`, and `Work` collection folders.

### Changed

- The default layout now uses Finder-facing ecosystem buckets such as `Go`,
  `Rust`, `Python`, and `Web`. Forks are separated under `Forks` and archived
  repositories are included by default so they can be tagged `On Hold`.

### Fixed

- Repositories whose names end in `.github.io` are always organized under the
  `Web` bucket, regardless of GitHub's detected primary language.
- Repository discovery reports every duplicate clone location instead of
  silently retaining whichever matching remote was encountered first.
- Dry runs no longer perform legacy migrations, case normalization, collection
  creation, or empty-folder cleanup.

### Performance

- Local discovery prunes dependency, build, cache, and virtual-environment
  trees, reducing a 185-repository layout audit from roughly 90 seconds to
  about 2.5 seconds on the reference workspace.

## [0.0.16] — 2026-08-01

### Fixed

- Case-only path aliases on case-insensitive filesystems no longer produce
  false target-collision errors. Corral now compares filesystem identity before
  deciding whether an existing clone and desired target are distinct.

## [0.0.15] — 2026-08-01

### Fixed

- Homebrew cask updates now open pull requests against the protected tap.
- AUR availability no longer blocks GitHub artifacts, checksums, container
  images, or build-provenance attestations.

## [0.0.14] — 2026-08-01

Security hardening, operational resilience, and complete unit coverage.

### Added

- Native `make install` support, installing `corralctl` under
  `~/.local/bin` by default with `PREFIX` and `DESTDIR` overrides.
- Direct mise installation through the GitHub release backend.
- Bounded repository discovery and MCP indexing, cancellation-aware git
  execution, mutation audit durability, and stricter workspace containment.
- Full tests for command, engine, git, GitHub, MCP, TUI, and entry-point paths,
  bringing project statement coverage to 100%.

### Security

- Repository paths, clone URLs, redirects, response bodies, git output, audit
  records, and concurrent work are now validated or explicitly bounded.
- Unsafe Git transports, credential-bearing URLs, symlink escapes, ambiguous
  remotes, insecure API origins, and option-like repository arguments fail
  closed before network or filesystem mutation.
- Release tags are verified as semantic versions pointing at commits on
  `main`, and release source is tested before packaging.

### Maintenance

- GitHub Actions and Go dependencies were updated to their current pinned
  releases.
- Governance, assurance-case, DCO, SPDX, and fuzzing coverage were expanded.

### Governance

- **Per-file SPDX headers** — every `.go` file now carries
  `SPDX-FileCopyrightText` and `SPDX-License-Identifier: GPL-3.0-only`
  headers at the top (after any `//go:build` constraint). Applied via
  a one-shot codegen tool committed at `scripts/spdx_sweep.go`, safe
  to re-run on new files. Satisfies CII Best Practices Silver
  `copyright_per_file` and `license_per_file` criteria.
- **DCO enforcement** — every PR commit must carry a matching
  `Signed-off-by:` trailer, checked by a new
  `.github/workflows/dco.yml`. Contributor flow (`git commit -s`,
  `git rebase --signoff`) documented in `CONTRIBUTING.md`. Satisfies
  the CII Best Practices Silver `dco` criterion.
- **Formal assurance case** at `docs/security-model.md` — trust
  boundaries, security claims C1–C5 with linked source evidence,
  threats considered vs out of scope, assumptions, and compensating
  controls for the single-maintainer bus factor. Satisfies CII Silver
  `assurance_case` and OSPS Baseline `OSPS-SA-03.02`.
- **`MAINTAINERS.md`** cataloguing every load-bearing external
  service (repo, ghcr.io, Homebrew tap, AUR, MCP Registry, docs DNS,
  SSH signing key, Sigstore) with the specific configuration file a
  successor must edit, plus voluntary hand-off, community-fork, and
  emergency compromise procedures. Referenced from `GOVERNANCE.md`.

### Changed

- `GOVERNANCE.md` succession section now points at `MAINTAINERS.md`
  for the detailed catalogue and at `docs/security-model.md` for the
  assurance-case perspective, so hand-over context is not tribal.
- `.bestpractices.json` refined: `dco` and `assurance_case` (both
  Silver) flipped to Met with evidence links; `bus_factor`,
  `two_person_review`, `contributors_unassociated` remain honestly
  Unmet with strengthened compensating-controls justifications rather
  than misrepresenting the solo-maintainer reality.

### Dependencies

- Bumped 10 indirect dependencies to latest: `go-udiff`,
  `bits-and-blooms/bitset`, `charmbracelet/x/exp/golden`,
  `cpuguy83/go-md2man/v2`, `dlclark/regexp2`, `rogpeppe/go-internal`,
  `sahilm/fuzzy`, `golang.org/x/exp`, `golang.org/x/mod`,
  `golang.org/x/tools`, plus direct bumps of `google/jsonschema-go` and
  `spf13/cast`. Full test suite green (unit + `-race`).

## [0.0.13] — 2026-07-01

Preflight visibility + real-world sync robustness.

### Fixed

- **Empty upstream repositories no longer surface as sync errors.**
  When a GitHub repo is created but never pushed to, its local clone
  has an unborn HEAD and `git pull` bails with `no such ref was fetched`.
  Corral now detects that state locally via a cheap
  `git rev-parse --verify HEAD^{commit}` before calling pull and returns
  `SKIP: empty repository (no commits yet)` instead of `ERROR`.
  Verified against six real cases on `sebastienrousseau` — all now
  clean-skip instead of erroring at the tail of the run.

### Added

- **Preflight banner + confirmation** in front of every non-interactive
  run. Prints `Owner: <owner>` and `Target: <absolute base_dir>` so an
  arg typo like `corralctl i sebastienrousseau` (owner=`i`,
  base_dir=`sebastienrousseau`) is visible before any GitHub API call
  or clone. When the base directory does not yet exist and stdin is a
  TTY, additionally prompts `Continue? [y/N]` and aborts on anything
  other than `y` / `yes`. Skipped when `--yes` / `-y` is passed, when
  `--dry-run` is set (no side effects to warn about), and in
  `--interactive` mode (the TUI has its own /exit confirmation).
- **`internal/git.IsEmpty(targetDir)`** helper — a cheap local
  `git rev-parse --verify HEAD^{commit}` check that the engine now
  calls before every pull. Exposed for reuse.

### Stats

- 7 packages, `-race -count=1` green across Ubuntu + macOS + Windows.
- Doc coverage: 64/64 (100 %).
- Adds 5 new tests (2 engine, 3 git) + tightens the shared
  `withGitPullStub` helper so pre-v0.0.13 tests continue to exercise
  the pull path.

## [0.0.12] — 2026-07-01

Write tools + prompts + container security scanning.

### Added

- **Three MCP write tools**, gated behind `--enable-mutations`:
  - **`corral_sync_repo`** — runs `git pull --rebase --autostash` against
    one clone. Reuses the existing non-interactive git environment and
    smart-sync sidecar semantics.
  - **`corral_clone_repo`** — clones a URL into a caller-provided
    target path relative to the sandbox root. Supports optional
    `depth` / `blobless`. Refuses when the target already exists or
    escapes the sandbox.
  - **`corral_delete_repo`** — removes a clone. Additionally gated by
    `--enable-destructive-mutations`. Refuses when uncommitted
    changes exist, unpushed commits exist, or the target is not a git
    repository.
- **Mutation audit log** — every mutation attempt (successful or
  refused) is appended as a JSONL record to
  `$XDG_STATE_HOME/corral/mutations.log` (or
  `~/.local/state/corral/mutations.log` per XDG spec) capturing
  timestamp, tool, target, args, result, and any error message.
  Path is overridable with `--audit-log`.
- **Two MCP prompts**:
  - **`explain_workspace`** — pre-canned instructions asking the agent
    to survey the workspace via read-only tools and produce a
    human-readable summary.
  - **`identify_stale_repos`** — pre-canned instructions asking the
    agent to find clones whose `.corral-state.json` says they haven't
    been synced in more than `threshold_days` days (default 30).
- **Container security workflow** at `.github/workflows/container-scan.yml`:
  - **hadolint** static-lints the Dockerfile on every PR that touches
    it; results uploaded as SARIF to the Code Scanning surface.
  - **Trivy** CVE-scans the published multi-arch OCI image after each
    release; also uploaded as SARIF. Both jobs are advisory
    initially (findings do not block the merge/release).
- **OpenSSF Best Practices Passing badge earned** (project #13455). All 67
  Passing-tier criteria answered via `.bestpractices.json` and accepted
  by the badge app. Badge is displayed in the README badge row.

### Changed

- **MCP server** advertises `prompts` capability. `server.json`
  updated to reflect the new capability + tool inventory.

### Stats

- 7 packages, `-race -count=1` green.
- Doc coverage: 63/63 (100 %).
- `internal/mcp` gains 15 new tests covering mutation happy paths,
  refusal cascades, and the audit log.

## [0.0.11] — 2026-07-01

Supply-chain hardening + coverage lifts.

### Added

- **SLSA v1.0 build provenance for every release artifact.**
  `actions/attest-build-provenance` runs after goreleaser and attests
  the contents of `dist/checksums.txt` — so every `.tar.gz`, `.deb`,
  `.rpm`, and the checksums file itself carries a cryptographic
  attestation that binds it to this exact commit and workflow run.
  Users verify with `gh attestation verify <file> --owner sebastienrousseau`.
- **Keyless cosign signing of Docker images.** `docker_signs:` in
  `.goreleaser.yaml` signs both the per-arch images and the
  `docker_manifests:` fan-out to `:{version}` + `:latest`. Uses the
  Actions OIDC token — no long-lived signing key. Users verify with
  `cosign verify` against the workflow identity documented in the
  goreleaser config.

### Changed

- **OpenSSF Scorecard Signed-Releases check.** Both mechanisms above
  feed the check; expect a jump from 0/10 → 10/10 on the next scan.
- **Test coverage** for the two remaining gaps flagged in the v0.0.10
  post-release audit:
  - `cmd/mcp.go` `runMCP`: 0 % → 90 % (all validation, wiring, and
    error-propagation paths).
  - `internal/github` `matchesFilters`: 55.9 % → 100 % (17 subtests
    covering every `opts.Type` branch — sources / forks / archived /
    mirrors / templates / sponsored / public / private / unknown).
  - Project total: 88.9 % → 90.2 %.

## [0.0.10] — 2026-07-01

MCP hardening pass — four v0 quality issues surfaced by post-release
review, all closed in one PR.

### Fixed

- **Nested-namespace URI resolution.** `corral://repo/{owner}/{name}/…`
  now resolves when `{owner}` matches **any** namespace segment in the
  origin URL, not only the direct parent. Self-hosted GitLab / Gitea
  layouts like `https://git.example.com/parent/subgroup/team/repo.git`
  are queryable via `parent`, `subgroup`, *or* `team` as the owner
  argument. `parseOwnerFromURL` return type changed from `string` to
  `[]string` accordingly (internal, non-breaking to MCP clients).
- **Silent git diagnostics.** `currentBranch` and `readState` used to
  swallow errors, hiding detached-HEAD, corrupted-refs, and
  permission-denied cases behind an empty-string result. Both now log
  to `stderr` (never `stdout` — that's the JSON-RPC protocol stream)
  with the repo path and underlying error, while preserving the same
  return contract so tool results stay backward compatible.
- **Docker permission failures under strict host mounts.** The README
  Docker snippet now includes `--user 1000:1000` (documented as "replace
  with `$(id -u):$(id -g)`") plus notes on the read-only `:ro` mount
  and the `--root /workspace` sandbox. Without this, the containerised
  scanner ran as a system UID and hit `permission denied` on any
  workspace directory made group- or user-private on the host.

### Changed

- **In-memory scan cache with a 5-second TTL.** `Server.scan()` now
  amortises filesystem walks across a burst of tool/resource calls in
  a single client session — critical for workspaces with hundreds of
  clones where an agent typically fires 5–10 tool calls in quick
  succession. Cache is invalidated after `scanTTL` (5s) so a
  just-cloned repo appears on the next call the agent makes.
  `invalidateScanCache()` gives tests deterministic control without
  needing time.Sleep.

## [0.0.9] — 2026-07-01

Docker distribution + MCP Registry submission.

### Added

- **Docker image published to `ghcr.io/sebastienrousseau/corral`** on
  every release. Multi-arch (linux/amd64 + linux/arm64) with the
  `io.modelcontextprotocol.server.name=io.github.sebastienrousseau/corral`
  ownership label required by the MCP Registry for OCI verification.
  Tags: `:<version>` (e.g. `:0.0.9`) and `:latest`.
- **`server.json`** at the repo root — the manifest consumed by the
  official `mcp-publisher` CLI (schema `2025-12-11`). Registers Corral
  in the MCP Registry under `io.github.sebastienrousseau/corral`.
- **README install-via-Docker snippet** for editors that cannot easily
  install a Go binary but can shell out to `docker run`.
- **`corral_find_repo` and resource-URI resolution now consult the
  remote origin URL**, so `corral://repo/{owner}/{name}/…` works when
  `{owner}` matches the GitHub org from `.git/config`'s origin URL —
  not only the layout's visibility directory. New
  `TestResolveURIRepoWithOwner` covers both HTTPS and SSH remote URL
  forms.

### Changed

- **`.goreleaser.yaml`** gains `dockers:` and `docker_manifests:`
  sections. `.github/workflows/release.yml` gains `packages: write`
  permission and SHA-pinned `docker/{login,setup-buildx,setup-qemu}-action`
  steps so goreleaser can push to ghcr.io during the release job.
- **`mcp.json` removed** — it was a speculative artifact that the
  registry does not consume. `server.json` is the canonical manifest.

## [0.0.8] — 2026-07-01

The MCP release. Corral becomes the canonical local index for AI coding
agents, alongside cron-grade cancellation visibility, a docs migration
to native GitHub Pages publishing on a custom domain, and every
GitHub-owned Action SHA-pinned to close 7 open OpenSSF Scorecard alerts.

### Added

- **`corralctl mcp` subcommand** — a Model Context Protocol server on
  stdio that exposes the local Corral-organised workspace to AI coding
  agents (Claude Code, Cursor, Cline, Codex CLI, Aider). Read-only in
  v0; ships five tools (`corral_list_repos`, `corral_find_repo`,
  `corral_get_repo_metadata`, `corral_status_summary`,
  `corral_workspace_index`) and four resources
  (`corral://workspace/index`, `corral://repo/{owner}/{name}/state`,
  `corral://repo/{owner}/{name}/tree`,
  `corral://repo/{owner}/{name}/file/{path}`). Sandboxes to a
  configurable `--root` (defaults to `--base-dir`); the file resource
  is bounded at 1 MiB with path-traversal defence canonicalising both
  the root and the candidate via `EvalSymlinks`. Reserved
  `--enable-mutations` flag is a placeholder for the Phase-3 write
  tools planned in v0.0.9.
- **`mcp.json` registry manifest** at the repo root for submission to
  `registry.modelcontextprotocol.io`.
- **Cancellation visibility for scripted callers.** When a run is
  interrupted by SIGINT/SIGTERM, the JSON output payload now carries
  `summary.canceled: true`, NDJSON emits a terminal
  `{"action":"CANCELED",...}` record, and the non-TTY text path logs a
  single `operation canceled (…)` line. The interactive TUI path stays
  silent (no regression of the existing UX). Exit code on cancellation
  is now `130` (POSIX 128+SIGINT) instead of `0`, so scripts can
  distinguish an aborted run from a clean one.

### Changed

- **Docs site** now publishes via the native GitHub Pages workflow
  (`actions/upload-pages-artifact` + `actions/deploy-pages`) instead of
  `peaceiris/actions-gh-pages`. The legacy `gh-pages` branch has been
  deleted.
- **Documentation URL** moved to <https://doc.corrallib.com> with HTTPS
  enforced via Let's Encrypt-issued cert.
- **Orphan detection is skipped on cancellation.** A mid-run abort can
  leave the local tree in a partial state where orphan reporting would
  be misleading.

### Security

- **All GitHub-owned Actions are now SHA-pinned** in `ci.yml` and
  `docs.yml`, closing the 7 open `PinnedDependenciesID` OpenSSF
  Scorecard / CodeQL alerts (#11, #12, #17, #21, #22, #23, #24).
  Convention matches `release.yml` and `scorecard.yml`: immutable SHA
  followed by `# vX.Y.Z` comment.

### Stats

- 7 packages, 100 % doc coverage (56 / 56 exported symbols).
- `internal/mcp` ships at 88.4 % statement coverage with 26 new tests
  covering scan / find / SafePath traversal defence / every tool and
  every resource.

## [0.0.7] — 2026-06-30

The first release after the binary rename to `corralctl`. Smart sync,
interactive TUI, `exec` subcommand, layout templating, and a complete
cron-safety overhaul.

### Added

- **Smart sync** — every clone now carries a `.corral-state.json` sidecar
  recording the last-observed upstream `pushed_at`. Subsequent runs skip
  the `git pull` round-trip when nothing has changed upstream, delivering
  10×–50× faster syncs on read-mostly workspaces.
- **`--force-sync`** flag to bypass the sidecar cache and pull regardless.
- **`--ignore-submodule-failures`** flag — with `--recurse-submodules`,
  swallow submodule update errors so a single inaccessible nested repo
  doesn't block the parent sync.
- **`--layout`** flag — text/template path renderer with vars `{{.Owner}}`,
  `{{.Name}}`, `{{.Visibility}}`, `{{.Language}}`, `{{.Fork}}`,
  `{{.Archived}}`. Default preserves `Visibility/Language/Name`.
- **`corralctl exec`** — concurrent batch executor for arbitrary shell
  commands across all (or a filtered subset of) cloned repos. Supports
  `--languages`, `--exclude-languages`, `--visibility`, `--concurrency`,
  and `--dry-run`.
- **Interactive TUI selector** (`--select`) with slash commands
  (`/help`, `/exit`, `/all`, `/none`, `/sort name|language|visibility`,
  `/sort public|private`, `/sort <language>`), Tab autocomplete,
  `topic:` / `language:` search queries, default-select-all, brand
  footer, and AltScreen mode (no scrollback pollution).
- **Concurrent GitHub API pagination** — pages 2…N are fetched in
  parallel (max 5 in-flight) once the first response advertises
  `resp.LastPage`. Sequential fallback for endpoints that don't report
  it. Substantial speed-up on accounts/orgs with hundreds of repos.
- **`git` binary pre-resolution** — `exec.LookPath("git")` runs once at
  startup; a missing `git` exits 1 with a clear error instead of failing
  mid-clone with a noisier message.
- **Subprocess-free orphan detection** — `RemoteOriginFromConfig` parses
  `.git/config` directly, ~5–15 ms saved per repo over spawning
  `git remote get-url origin`.
- **Documentation coverage CI gate** at 100 % (40 / 40 exported symbols)
  via `scripts/doc_coverage.go`.
- **GitHub Pages site** (`https://sebastienrousseau.github.io/corral/`)
  generated from `scripts/generate_docs.go` and deployed via
  `peaceiris/actions-gh-pages` on every push to `main`.
- **Animated terminal demo** (`demo.gif`) embedded in the README.
- **README architecture diagram** restored (mermaid) covering the full
  flow from API fetch through worker pool to summary.
- **CHANGELOG.md** — this file.

### Changed

- **One-time language-directory case normalisation** — on case-insensitive
  filesystems (APFS, HFS+, NTFS), pre-existing title-case folders like
  `Public/JavaScript/` are renamed to the documented lowercase form
  (`Public/javascript/`) on the next run. Unrelated dirs (e.g.
  `Public/Configurations/`) are untouched. Idempotent.
- **Strict non-interactive `git` environment** — every clone/pull now
  sets `GIT_TERMINAL_PROMPT=0`, `GIT_ASKPASS=/bin/true`,
  `SSH_ASKPASS=/bin/true`, `GCM_INTERACTIVE=Never`, and the rebase replay
  overrides `commit.gpgsign=false` + `gpg.format=openpgp`. Cron jobs can
  no longer hang on a credential prompt, SSH passphrase, or GPG/SSH
  signing pinentry, even when the user has `commit.gpgsign=true` set
  globally.
- **Version is now `-ldflags` injected** in both `Makefile` (via
  `git describe --tags --always --dirty`) and `.goreleaser.yaml`, into
  both `cmd.Version` *and* `internal/tui.Version`. The hard-coded
  fallback is now `"dev"` so an un-injected build is obvious instead of
  pretending to be `0.0.6`.
- **README rewritten** to a flatter, scannable layout (Quick Start →
  Features → Architecture → TUI → Layouts → Smart Sync → Exec → Flags →
  Examples → Troubleshooting → FAQ).
- **`Pull` signature** is now `Pull(ctx, dir, PullOptions)` instead of
  `Pull(ctx, dir, recurseSubmodules bool)`. **Breaking** for direct
  callers of `internal/git`; the engine layer is unaffected.
- **`internal/github.Repo`** carries a `PushedAt time.Time` field
  populated from the API response.
- **Default binary name** is `corralctl` (was `corral`, renamed in v0.0.6
  to avoid clashing with the `corral` formula in `homebrew-core`).
  Project name and import path are unchanged.
- **SBOM** refreshed: `go-github` v60 → v74 (matches `go.mod`); removed
  stale `golang.org/x/oauth2` reference (auth uses go-github's
  `WithAuthToken` helper now); Go toolchain pin 1.21 → 1.26.

### Fixed

- **`.corral-state.json` and `public/index.html` leaks** — both were
  accidentally tracked in version control. Now in `.gitignore`.
- **README absolute filesystem links** (`file:///Users/seb/...`) replaced
  with relative paths.
- **`runExecCommands` test coverage** lifted from 0 % to 91 % — the
  flagship `exec` path is now exercised under the race detector,
  including success / non-zero exit / pre-cancelled context / empty
  input / no-matching-repos branches.
- **`tui.go:57`** double-slash comment typo (`// // Init …`).
- **Layout `--orphans` walk** now uses `.git/config` parsing instead of
  per-repo `git remote get-url origin` subprocess spawns.

### Security

- All commits and merge commits are cryptographically signed
  (ED25519 / GPG); CI verifies signatures.
- CI actions remain pinned to immutable SHAs.
- Dependency Review, CodeQL, OpenSSF Scorecard, and gosec checks gate
  every PR.

### Stats

- 6 packages, 88.9 % statement coverage (up from 86.2 % mid-cycle),
  100 % doc coverage.
- All tests green under `-race -count=1`.

[Unreleased]: https://github.com/sebastienrousseau/corralctl/compare/v0.0.37...HEAD
[0.0.37]: https://github.com/sebastienrousseau/corralctl/compare/v0.0.36...v0.0.37
[0.0.36]: https://github.com/sebastienrousseau/corral/compare/v0.0.35...v0.0.36
[0.0.35]: https://github.com/sebastienrousseau/corral/compare/v0.0.34...v0.0.35
[0.0.34]: https://github.com/sebastienrousseau/corral/compare/v0.0.33...v0.0.34
[0.0.33]: https://github.com/sebastienrousseau/corral/compare/v0.0.32...v0.0.33
[0.0.32]: https://github.com/sebastienrousseau/corral/compare/v0.0.31...v0.0.32
[0.0.31]: https://github.com/sebastienrousseau/corral/compare/v0.0.30...v0.0.31
[0.0.30]: https://github.com/sebastienrousseau/corral/compare/v0.0.29...v0.0.30
[0.0.29]: https://github.com/sebastienrousseau/corral/compare/v0.0.28...v0.0.29
[0.0.28]: https://github.com/sebastienrousseau/corral/compare/v0.0.27...v0.0.28
[0.0.27]: https://github.com/sebastienrousseau/corral/compare/v0.0.26...v0.0.27
[0.0.26]: https://github.com/sebastienrousseau/corral/compare/v0.0.25...v0.0.26
[0.0.25]: https://github.com/sebastienrousseau/corral/compare/v0.0.24...v0.0.25
[0.0.24]: https://github.com/sebastienrousseau/corral/compare/v0.0.23...v0.0.24
[0.0.23]: https://github.com/sebastienrousseau/corral/compare/v0.0.22...v0.0.23
[0.0.22]: https://github.com/sebastienrousseau/corral/compare/v0.0.21...v0.0.22
[0.0.21]: https://github.com/sebastienrousseau/corral/compare/v0.0.20...v0.0.21
[0.0.20]: https://github.com/sebastienrousseau/corral/compare/v0.0.19...v0.0.20
[0.0.19]: https://github.com/sebastienrousseau/corral/compare/v0.0.18...v0.0.19
[0.0.18]: https://github.com/sebastienrousseau/corral/compare/v0.0.17...v0.0.18
[0.0.17]: https://github.com/sebastienrousseau/corral/compare/v0.0.16...v0.0.17
[0.0.16]: https://github.com/sebastienrousseau/corral/compare/v0.0.15...v0.0.16
[0.0.15]: https://github.com/sebastienrousseau/corral/compare/v0.0.14...v0.0.15
[0.0.14]: https://github.com/sebastienrousseau/corral/compare/v0.0.13...v0.0.14
[0.0.13]: https://github.com/sebastienrousseau/corral/compare/v0.0.12...v0.0.13
[0.0.12]: https://github.com/sebastienrousseau/corral/compare/v0.0.11...v0.0.12
[0.0.11]: https://github.com/sebastienrousseau/corral/compare/v0.0.10...v0.0.11
[0.0.10]: https://github.com/sebastienrousseau/corral/compare/v0.0.9...v0.0.10
[0.0.9]: https://github.com/sebastienrousseau/corral/compare/v0.0.8...v0.0.9
[0.0.8]: https://github.com/sebastienrousseau/corral/compare/v0.0.7...v0.0.8
[0.0.7]: https://github.com/sebastienrousseau/corral/compare/v0.0.6...v0.0.7
