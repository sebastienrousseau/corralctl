# SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
# SPDX-License-Identifier: GPL-3.0-only
#
# corralctl as a Nix flake: a development shell, and the package itself.
#
# The dev shell exists because of a specific failure. The devcontainer used
# to install markdownlint, codespell and pre-commit with `pip install` and
# `npm install -g`, which OpenSSF Scorecard flagged as unpinnable by hash —
# an installer that resolves at build time and then runs with the
# developer's credentials. The resolution was to delete those tools, which
# removed capability rather than securing it. Nix pins every one of them by
# hash by construction, so they come back without the finding.
#
#   nix develop          # every tool the CI gates need, pinned
#   nix build            # the corralctl package, with manpages + completions
#   nix run . -- --help  # run it without installing
{
  description = "corralctl: a local repository index for AI coding agents";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };

        # Read the version from CHANGELOG.md's newest release heading, so
        # the flake cannot drift from the manifest gate that already keeps
        # CHANGELOG, server.json and the container tag in agreement.
        # Extracted by splitting rather than by regex. Nix's regex engine
        # rejects "\\[" as an escape, so matching a bracketed heading needs
        # either a bracket expression or no regex at all; the latter reads
        # better and cannot be broken by the next engine change.
        version =
          let
            lines = pkgs.lib.splitString "\n" (builtins.readFile ./CHANGELOG.md);
            isRelease = l:
              pkgs.lib.hasPrefix "## [" l && !(pkgs.lib.hasPrefix "## [Unreleased]" l);
            releases = builtins.filter isRelease lines;
            # "## [0.0.28] — 2026-09-02"  ->  "0.0.28"
            extract = l:
              builtins.head (pkgs.lib.splitString "]" (builtins.elemAt (pkgs.lib.splitString "[" l) 1));
          in
          if releases == [ ] then "0.0.0" else extract (builtins.head releases);

        corralctl = pkgs.buildGoModule {
          pname = "corralctl";
          inherit version;
          src = ./.;

          # Never guessed: `nix build` reports the expected value when
          # go.mod changes, and this is what it reported.
          vendorHash = "sha256-uV/CdmXRyUhU1Xd2NGK7vwSmbm48QDxhyLbffIjelkA=";

          subPackages = [ "cmd/corralctl" ];

          # Matches what the Makefile and goreleaser do: no CGO, paths
          # trimmed, version injected in both places that report it.
          env.CGO_ENABLED = 0;
          ldflags = [
            "-s"
            "-w"
            "-X github.com/sebastienrousseau/corralctl/cmd.Version=${version}"
            "-X github.com/sebastienrousseau/corralctl/internal/tui.Version=${version}"
          ];

          nativeBuildInputs = [ pkgs.installShellFiles ];

          # Manpages and completions are generated from the cobra command
          # tree, never committed, so they are generated here too rather
          # than assumed to exist.
          postInstall = ''
            ${pkgs.go}/bin/go run scripts/gen_docs.go "$TMPDIR/artifacts"
            installManPage "$TMPDIR"/artifacts/man/*.1
            # --name is given per shell rather than relying on the source
            # filename. bash-completion and zsh look completions up by
            # command name, so "corralctl.bash" and "corralctl.zsh" would
            # be installed and never found. (--cmd does not cover this: it
            # only applies when a filename cannot be inferred at all.)
            installShellCompletion --bash --name corralctl \
              "$TMPDIR/artifacts/completions/corralctl.bash"
            installShellCompletion --zsh --name _corralctl \
              "$TMPDIR/artifacts/completions/corralctl.zsh"
            installShellCompletion --fish --name corralctl.fish \
              "$TMPDIR/artifacts/completions/corralctl.fish"
            install -Dm644 README.md   "$out/share/doc/corralctl/README.md"
            install -Dm644 CHANGELOG.md "$out/share/doc/corralctl/CHANGELOG.md"
            install -Dm644 LICENSE      "$out/share/doc/corralctl/LICENSE"
          '';

          # The suite makes no network calls — the GitHub API is exercised
          # through an httptest server — so it runs in the sandbox as-is.
          # git is required because corral shells out to it for every clone,
          # pull and inspection.
          nativeCheckInputs = [ pkgs.git ];

          # subPackages narrows the build to the one binary worth
          # installing, and it narrows checkPhase with it — which silently
          # tested 1 package of 10. The whole module is tested instead, so
          # `nix build` means what it appears to mean.
          checkPhase = ''
            runHook preCheck
            go test ./...
            runHook postCheck
          '';

          meta = with pkgs.lib; {
            description = "Clone and organise repositories, and serve them to AI coding agents over MCP";
            homepage = "https://github.com/sebastienrousseau/corralctl";
            license = licenses.gpl3Only;
            mainProgram = "corralctl";
            maintainers = [ ];
          };
        };
      in
      {
        packages = {
          default = corralctl;
          corralctl = corralctl;
        };

        apps.default = flake-utils.lib.mkApp { drv = corralctl; };

        # Every tool DEVELOPMENT.md lists, pinned by the flake lock rather
        # than resolved at install time.
        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            golangci-lint
            go-tools # staticcheck, which CI runs standalone
            gotools

            git
            gnumake
            groff # renders the generated manpages in the Install Contract gate

            # The prose gates. These are the ones the devcontainer had to
            # drop for being unpinnable; here they are hash-pinned.
            markdownlint-cli2
            codespell
            lychee
            pre-commit

            # Release and supply-chain tooling.
            goreleaser
            syft
            cosign
            gh
            actionlint
          ];

          shellHook = ''
            echo "corral dev shell — go $(go version | awk '{print $3}')"
            echo
            echo "  make            format, vet, checks, tests, build"
            echo "  make test-race  race detector, shuffled order"
            echo "  make docs-lint  markdownlint + codespell"
            echo "  make help       every target"
            echo
            echo "See DEVELOPMENT.md for the local equivalent of every CI gate."
          '';
        };

        formatter = pkgs.nixpkgs-fmt;
      });
}
