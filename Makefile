# SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
# SPDX-License-Identifier: GPL-3.0-only

BINARY_NAME=corralctl

# The Unix install contract. PREFIX defaults to /usr/local per the FHS and
# the convention every packager expects; DESTDIR stages the tree elsewhere
# without changing the paths compiled into it, which is how deb, rpm, AUR
# and Homebrew all build.
#
# Installing to /usr/local needs root. For a home-directory install:
#   make install PREFIX=$$HOME/.local
PREFIX ?= /usr/local
DESTDIR ?=
BINDIR = $(DESTDIR)$(PREFIX)/bin
MANDIR = $(DESTDIR)$(PREFIX)/share/man/man1
DOCDIR = $(DESTDIR)$(PREFIX)/share/doc/corralctl
BASHCOMPDIR = $(DESTDIR)$(PREFIX)/share/bash-completion/completions
ZSHCOMPDIR = $(DESTDIR)$(PREFIX)/share/zsh/site-functions
FISHCOMPDIR = $(DESTDIR)$(PREFIX)/share/fish/vendor_completions.d

# Where generated artefacts land. Never committed: manpages and completions
# are derived from the cobra command tree, so a checked-in copy could only
# ever be out of date with `--help`.
# Not dist/: goreleaser owns that name and cleans it after its before-hooks
# run, which would delete these before packaging.
DIST ?= build

# Build-time version. Resolved from `git describe` so local builds carry a
# meaningful version (matching whatever tag/commit you built from) rather than
# the "dev" fallback baked into the source. Overridden by goreleaser at
# release time.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
VERSION_PKG = github.com/sebastienrousseau/corralctl
LDFLAGS = -s -w \
	-X $(VERSION_PKG)/cmd.Version=$(VERSION) \
	-X $(VERSION_PKG)/internal/tui.Version=$(VERSION)

.PHONY: all build docs install uninstall install-smoke test test-race vet lint \
        clean format sbom-check example-check doc-check spdx-check pkg-check eval staticcheck race-hard bench claims-check \
        docs-lint help

all: format vet staticcheck spdx-check sbom-check pkg-check claims-check example-check test test-race build

## build: compile the binary with version metadata
build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o $(BINARY_NAME) ./cmd/corralctl

## docs: generate manpages and shell completions from the command tree
docs:
	go run scripts/gen_docs.go $(DIST)

## install: install binary, manpages and completions under PREFIX
install: build docs
	install -d $(BINDIR)
	install -m 0755 $(BINARY_NAME) $(BINDIR)/$(BINARY_NAME)
	install -d $(MANDIR)
	install -m 0644 $(DIST)/man/*.1 $(MANDIR)/
	install -d $(BASHCOMPDIR) $(ZSHCOMPDIR) $(FISHCOMPDIR)
	install -m 0644 $(DIST)/completions/corralctl.bash $(BASHCOMPDIR)/$(BINARY_NAME)
	install -m 0644 $(DIST)/completions/corralctl.zsh  $(ZSHCOMPDIR)/_$(BINARY_NAME)
	install -m 0644 $(DIST)/completions/corralctl.fish $(FISHCOMPDIR)/$(BINARY_NAME).fish
	install -d $(DOCDIR)
	install -m 0644 README.md CHANGELOG.md LICENSE SECURITY.md $(DOCDIR)/

## uninstall: remove everything install put in place
uninstall:
	rm -f $(BINDIR)/$(BINARY_NAME)
	rm -f $(MANDIR)/corralctl.1 $(MANDIR)/corralctl-*.1
	rm -f $(BASHCOMPDIR)/$(BINARY_NAME)
	rm -f $(ZSHCOMPDIR)/_$(BINARY_NAME)
	rm -f $(FISHCOMPDIR)/$(BINARY_NAME).fish
	rm -rf $(DOCDIR)

## install-smoke: stage an install into a temp tree and assert its shape
install-smoke:
	@rm -rf /tmp/corral-stage
	@$(MAKE) --no-print-directory install DESTDIR=/tmp/corral-stage PREFIX=/usr
	@set -e; \
	for f in usr/bin/$(BINARY_NAME) \
	         usr/share/man/man1/corralctl.1 \
	         usr/share/man/man1/corralctl-mcp.1 \
	         usr/share/bash-completion/completions/$(BINARY_NAME) \
	         usr/share/zsh/site-functions/_$(BINARY_NAME) \
	         usr/share/fish/vendor_completions.d/$(BINARY_NAME).fish \
	         usr/share/doc/corralctl/README.md; do \
	  test -f "/tmp/corral-stage/$$f" || { echo "MISSING: $$f" >&2; exit 1; }; \
	done; \
	test -x /tmp/corral-stage/usr/bin/$(BINARY_NAME) || { echo "binary not executable" >&2; exit 1; }
	@echo "install-smoke: staged tree is correct"
	@rm -rf /tmp/corral-stage

## test: run the test suite
test:
	go test ./...

## test-race: run the suite under the race detector with shuffled order
#
# -count=1 because a race gate must never be answered from the test cache.
test-race:
	go test -race -shuffle=on -count=1 ./...

## bench: smoke-run every benchmark, as CI does
#
# CI runs this and local development had no equivalent, which is how a
# performance claim ends up with no benchmark behind it: internal/search
# and internal/symbols carry the 6.9s-to-1.3s figure and had none until a
# review asked whether they did.
bench:
	go test -run '^$$' -bench . -benchtime 1x ./...

## race-hard: hammer the concurrent packages under the race detector
#
# One pass of `test-race` is weak evidence. A real race in
# internal/mcp — a plain int incremented by the concurrent repository
# fan-out — was missed by the full shuffled suite on every local run and
# caught by CI, then reproduced 10 times out of 10 by running that one
# test on its own. Whole-suite timing simply hides some interleavings.
#
# So the packages that actually run goroutines get repeated, focused runs.
# RACE_RUNS overrides the count for a longer soak.
RACE_RUNS ?= 10
race-hard:
	go test -race -count=$(RACE_RUNS) ./internal/mcp/ ./internal/symbols/ ./internal/search/ ./internal/engine/

## vet: run go vet
vet:
	go vet ./...

## staticcheck: run staticcheck standalone, the way CI does
#
# Separate from `lint` on purpose. golangci-lint embeds staticcheck but
# honours //nolint directives; the standalone binary CI runs honours only
# //lint:ignore. A suppression that satisfies one and not the other passes
# locally and fails in CI, which has now happened twice.
staticcheck:
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...

## lint: run golangci-lint
lint:
	golangci-lint run ./...

## sbom-check: verify SBOM.md and server.json against go.mod and CHANGELOG
sbom-check:
	go run scripts/manifest_check.go

## example-check: compile every program under examples/
example-check:
	go run scripts/example_check.go

## doc-check: enforce 100% documentation coverage on exported declarations
doc-check:
	go run scripts/doc_coverage.go

## eval: run the MCP evaluation suite and write a report
# The path is absolute because `go test` runs each package in its own
# directory, so a relative one would land under internal/mcp.
eval:
	@mkdir -p $(DIST)
	CORRAL_EVAL_REPORT=$(CURDIR)/$(DIST)/eval.json \
	  go test ./internal/mcp/ -count=1 -v \
	  -run 'TestEval|TestEveryReadTool|TestToolDescriptions|TestEveryToolIsDescribed|TestServerInstructionsOrient'
	@echo "eval report: $(DIST)/eval.json"

## claims-check: verify the project's measurable claims about itself
#
# Coverage percentages, package counts, benchmark presence and example
# references all go stale silently — nothing fails when a number in prose
# stops matching the code. .bestpractices.json claimed 90.2% coverage
# "as of v0.0.11" through twenty releases, by which point it was 100%
# across twelve packages.
claims-check:
	go run scripts/claims_check.go

## pkg-check: verify pkg/ documents every distribution format built
pkg-check:
	go run scripts/pkg_check.go

## spdx-check: verify every source file carries an SPDX header
spdx-check:
	go run scripts/spdx_sweep.go -check

# codespell's skip list and accepted words, kept identical to
# .github/workflows/docs-lint.yml. They had drifted: the Makefile passed
# neither the ignore list nor half the skip paths, so running it locally
# reported findings CI does not — which is how a target ends up being run
# with `|| true` and stops meaning anything.
#
#   intoto      the in-toto attestation format, spelled correctly
#   statuss     a deliberate typo in the "did you mean" suggestion tests
#   confg       likewise, a deliberate typo fixture
#   gitub       a deliberate misspelling of "github", used to test the
#               unknown-forge error message
#   repositor   the stem in `"repositor" + plural(n)`
#   unparseable accepted variant spelling used throughout
CODESPELL_SKIP ?= ./.git,./docs-site,./public,./go.sum,./dist,./build,./node_modules
CODESPELL_IGNORE ?= intoto,statuss,confg,gitub,repositor,unparseable

## release-preflight: prove a release can publish, before the tag exists
release-preflight:
	python3 scripts/release_preflight.py

## contrast: assert the docs theme's colour pairs at WCAG AAA
contrast:
	python3 scripts/contrast.py

## docs-lint: markdownlint + codespell over the prose
#
# npx is the fallback rather than a hard requirement on a global install,
# because a silent skip is worse than either. This target printed
# "markdownlint not installed; skipping" and exited 0 on a machine that had
# no markdownlint, so a broken table passed locally and failed in CI — a
# gate that cannot run must say so loudly, not report success.
docs-lint:
	@if command -v markdownlint-cli2 >/dev/null 2>&1; then \
	  markdownlint-cli2 '**/*.md'; \
	elif command -v markdownlint >/dev/null 2>&1; then \
	  markdownlint '**/*.md' --ignore node_modules --ignore docs-site; \
	elif command -v npx >/dev/null 2>&1; then \
	  npx --yes markdownlint-cli2@0.23.2 '**/*.md'; \
	else \
	  echo "!! GATE DID NOT RUN: markdownlint is unavailable and npx is missing." >&2; \
	  echo "!! CI will still run it. Install node, or expect a surprise." >&2; \
	  exit 1; \
	fi
	@if command -v codespell >/dev/null 2>&1; then \
	  codespell --skip='$(CODESPELL_SKIP)' --ignore-words-list='$(CODESPELL_IGNORE)'; \
	else \
	  echo "!! GATE DID NOT RUN: codespell is unavailable." >&2; \
	  echo "!! CI will still run it. Install it, or expect a surprise." >&2; \
	  exit 1; \
	fi

## format: gofmt the tree
format:
	go fmt ./...

## clean: remove build output
clean:
	go clean
	rm -f $(BINARY_NAME)
	rm -rf $(DIST)

## help: list the targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /' | sort
