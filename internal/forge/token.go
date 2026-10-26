// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package forge

import (
	"context"
	"os"
	"strings"

	"github.com/sebastienrousseau/corralctl/internal/github"
)

// EnvToken resolves the credential for a non-GitHub forge from the
// environment variable its own tooling names, so a developer who already
// has it exported does not have to learn a corral-specific name. The
// corral-specific name is tried first, so somebody can point corral at a
// different instance than their shell is configured for.
//
// It returns "" for "github": GitHub's credential is resolved by its own
// client, which knows the ladder from an explicit token through the
// environment to the gh CLI. Use [Token] when that ladder is wanted here.
func EnvToken(name string) string {
	switch name {
	case "github":
		return ""
	case "gitlab":
		return firstEnv("CORRAL_GITLAB_TOKEN", "GITLAB_TOKEN", "CI_JOB_TOKEN")
	case "bitbucket":
		return firstEnv("CORRAL_BITBUCKET_TOKEN", "BITBUCKET_TOKEN")
	default:
		return firstEnv("CORRAL_FORGE_TOKEN", "FORGEJO_TOKEN", "GITEA_TOKEN", "CODEBERG_TOKEN")
	}
}

// Token resolves the credential `corralctl sync` and `adopt` present to a
// destination forge, through the same ladder `corralctl clone` uses to
// list from one: GitHub through its own client, every other forge from
// the environment variable its own tooling names ([EnvToken]).
func Token(ctx context.Context, name string, mode github.AuthMode) string {
	if name == "github" {
		return github.Token(ctx, mode)
	}
	return EnvToken(name)
}

// firstEnv returns the first of these variables that is set and non-empty.
func firstEnv(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}
