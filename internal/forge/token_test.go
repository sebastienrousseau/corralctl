// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package forge

import (
	"context"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/github"
)

func TestTokenForSync(t *testing.T) {
	// The exported resolver is what `corralctl sync` uses. It differs from
	// the listing path in one way: GitHub's token is resolved here, because
	// the target does not carry the listing client's auth ladder.
	t.Setenv("GITHUB_TOKEN", "gh-env")
	t.Setenv("GH_TOKEN", "")
	if got := Token(context.Background(), "github", github.AuthModeToken); got != "gh-env" {
		t.Errorf("github sync token = %q", got)
	}
	t.Setenv("CORRAL_GITLAB_TOKEN", "")
	t.Setenv("GITLAB_TOKEN", "gl-env")
	if got := Token(context.Background(), "gitlab", github.AuthModeAuto); got != "gl-env" {
		t.Errorf("gitlab sync token = %q", got)
	}
}

func TestEnvTokenSources(t *testing.T) {
	// GitHub's credential is resolved inside its own client, which knows
	// the ladder from an explicit token through the environment to the gh
	// CLI. Supplying one here would bypass all of that.
	t.Setenv("GITHUB_TOKEN", "gh-env")
	if got := EnvToken("github"); got != "" {
		t.Errorf("github token = %q, want empty: its client resolves its own", got)
	}

	t.Setenv("CORRAL_GITLAB_TOKEN", "")
	t.Setenv("GITLAB_TOKEN", "from-gitlab-env")
	t.Setenv("CI_JOB_TOKEN", "")
	if got := EnvToken("gitlab"); got != "from-gitlab-env" {
		t.Errorf("gitlab token = %q", got)
	}
	// A corral-specific name wins, so somebody can point corral at a
	// different instance than their shell is already configured for.
	t.Setenv("CORRAL_GITLAB_TOKEN", "from-corral")
	if got := EnvToken("gitlab"); got != "from-corral" {
		t.Errorf("gitlab token = %q, want the corral-specific variable to win", got)
	}

	t.Setenv("CORRAL_BITBUCKET_TOKEN", "")
	t.Setenv("BITBUCKET_TOKEN", "bb-env")
	if got := EnvToken("bitbucket"); got != "bb-env" {
		t.Errorf("bitbucket token = %q", got)
	}
	t.Setenv("CORRAL_BITBUCKET_TOKEN", "bb-corral")
	if got := EnvToken("bitbucket"); got != "bb-corral" {
		t.Errorf("bitbucket token = %q, want the corral-specific variable to win", got)
	}

	t.Setenv("CORRAL_FORGE_TOKEN", "")
	t.Setenv("FORGEJO_TOKEN", "")
	t.Setenv("CODEBERG_TOKEN", "")
	t.Setenv("GITEA_TOKEN", "gitea-env")
	for _, name := range []string{"gitea", "forgejo", "codeberg"} {
		if got := EnvToken(name); got != "gitea-env" {
			t.Errorf("%s token = %q", name, got)
		}
	}
}

func TestFirstEnv(t *testing.T) {
	t.Setenv("CORRAL_TEST_A", "")
	t.Setenv("CORRAL_TEST_B", "   ")
	t.Setenv("CORRAL_TEST_C", "value")
	if got := firstEnv("CORRAL_TEST_A", "CORRAL_TEST_B", "CORRAL_TEST_C"); got != "value" {
		t.Errorf("firstEnv = %q, want value: empty and blank should be skipped", got)
	}
	if got := firstEnv("CORRAL_TEST_MISSING"); got != "" {
		t.Errorf("firstEnv = %q, want empty", got)
	}
	if got := firstEnv(); got != "" {
		t.Errorf("firstEnv() = %q, want empty", got)
	}
}
