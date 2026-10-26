// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package forge

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// GitHub refuses to delete a repository's default branch, as git itself
// does (receive.denyDeleteCurrent; TestPushRefUnblocksAPruneOfTheRemoteDefault
// in internal/git shows the refusal and the way through). So a GitHub
// destination whose default was deleted upstream failed every sync run,
// the same failure 519cd5a fixed for the Gitea family, until it reported
// its default and could repoint it.
func TestGitHubDefaultBranch(t *testing.T) {
	ctx := context.Background()
	tg := mustTarget(t, "github")

	withDefault := strings.Replace(githubRepoJSON, `"private":true`, `"private":true,"default_branch":"feat/old"`, 1)
	base := serve(t, newJSONHandler(t).on("GET /api/v3/repos/me/repo", 200, withDefault))
	if got, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo", Private: true}, targetOpts(base)); err != nil || got.DefaultBranch != "feat/old" {
		t.Fatalf("default branch = %+v, %v", got, err)
	}
	// A repository this run created has nothing a push could prune.
	h := newJSONHandler(t).on("GET /api/v3/repos/me/repo", 404, `{}`).on("POST /api/v3/user/repos", 201, withDefault)
	if got, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo", Private: true}, targetOpts(serve(t, h))); err != nil || got.DefaultBranch != "" {
		t.Fatalf("created repository default = %+v, %v", got, err)
	}

	setter, ok := tg.(DefaultBranchSetter)
	if !ok {
		t.Fatal("github cannot set a default branch")
	}
	h = newJSONHandler(t).on("PATCH /api/v3/repos/org/repo", 200, githubRepoJSON)
	if err := setter.SetDefaultBranch(ctx, "org", "repo", "main", targetOpts(serve(t, h))); err != nil {
		t.Fatal(err)
	}
	if b := h.bodies["PATCH /api/v3/repos/org/repo"]; len(b) != 1 || b["default_branch"] != "main" {
		t.Fatalf("payload = %v", b)
	}
	base = serve(t, newJSONHandler(t).on("PATCH /api/v3/repos/org/repo", 422, `{"message":"Validation Failed"}`))
	if err := setter.SetDefaultBranch(ctx, "org", "repo", "gone", targetOpts(base)); !githubStatusIs(err, 422) {
		t.Fatalf("patch failure = %v", err)
	}
	if err := setter.SetDefaultBranch(ctx, "org", "repo", "main", TargetOptions{BaseURL: base}); !errors.Is(err, ErrNoToken) {
		t.Fatalf("no token = %v", err)
	}
}
