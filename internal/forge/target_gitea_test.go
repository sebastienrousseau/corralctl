// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package forge

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestGiteaDefaultBranch(t *testing.T) {
	ctx := context.Background()
	tg := mustTarget(t, "gitea")

	withDefault := strings.Replace(giteaRepoJSON, `"private":true`, `"private":true,"default_branch":"feat/old"`, 1)
	base := serve(t, newJSONHandler(t).on("GET /api/v1/repos/me/repo", 200, withDefault))
	if got, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo", Private: true}, targetOpts(base)); err != nil || got.DefaultBranch != "feat/old" {
		t.Fatalf("default branch = %+v, %v", got, err)
	}
	// An empty repository names a branch it does not have yet.
	empty := strings.Replace(withDefault, `"private":true`, `"private":true,"empty":true`, 1)
	base = serve(t, newJSONHandler(t).on("GET /api/v1/repos/me/repo", 200, empty))
	if got, err := tg.EnsureRepo(ctx, "me", RepoSpec{Name: "repo", Private: true}, targetOpts(base)); err != nil || got.DefaultBranch != "" {
		t.Fatalf("empty repository default = %+v, %v", got, err)
	}

	setter, ok := tg.(DefaultBranchSetter)
	if !ok {
		t.Fatal("gitea cannot set a default branch")
	}
	h := newJSONHandler(t).on("PATCH /api/v1/repos/org/repo", 200, giteaRepoJSON)
	base = serve(t, h)
	if err := setter.SetDefaultBranch(ctx, "org", "repo", "main", targetOpts(base)); err != nil {
		t.Fatal(err)
	}
	if b := h.bodies["PATCH /api/v1/repos/org/repo"]; len(b) != 1 || b["default_branch"] != "main" {
		t.Fatalf("payload = %v", b)
	}
	base = serve(t, newJSONHandler(t).on("PATCH /api/v1/repos/org/repo", 404, `no branch`))
	if err := setter.SetDefaultBranch(ctx, "org", "repo", "gone", targetOpts(base)); !statusIs(err, 404) {
		t.Fatalf("patch failure = %v", err)
	}
	if err := setter.SetDefaultBranch(ctx, "org", "repo", "main", TargetOptions{BaseURL: base}); !errors.Is(err, ErrNoToken) {
		t.Fatalf("no token = %v", err)
	}
}
