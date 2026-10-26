// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package syncguard

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/github"
)

func TestRemoteIdentity(t *testing.T) {
	cases := map[string]github.Repo{
		"gitlab.com/o/r": {CloneURL: "https://gitlab.com/O/r.git", FullName: "x/y"},
		"github.com/a/b": {FullName: "A/b"},
		"github.com/c/d": {Owner: "c", Name: "D"},
		"":               {Name: "only-a-name"},
	}
	for want, repo := range cases {
		if got := RemoteIdentity(repo); got != want {
			t.Errorf("RemoteIdentity(%+v) = %q, want %q", repo, got, want)
		}
	}
}

func originReader(url string, err error) Reader {
	return Reader{RemoteOrigin: func(string) (string, error) { return url, err }}
}

func TestOriginMismatch(t *testing.T) {
	want := github.Repo{CloneURL: "https://github.com/o/r.git"}
	if _, bad := originReader("", errors.New("unused")).OriginMismatch(github.Repo{}, "/d"); bad {
		t.Error("a repository with no identity cannot mismatch")
	}
	if msg, bad := originReader("", errors.New("no config")).OriginMismatch(want, "/d"); !bad || !strings.Contains(msg, "cannot verify") {
		t.Errorf("unreadable origin: %q %t", msg, bad)
	}
	if msg, bad := originReader("git@github.com:x/y.git", nil).OriginMismatch(want, "/d"); !bad || !strings.Contains(msg, "origin collision") {
		t.Errorf("other origin: %q %t", msg, bad)
	}
	if _, bad := originReader("git@github.com:o/r.git", nil).OriginMismatch(want, "/d"); bad {
		t.Error("the same repository over ssh is not a mismatch")
	}
}

func TestBranchRefusal(t *testing.T) {
	ctx := context.Background()
	reader := func(empty bool, branch string, err error) Reader {
		return Reader{
			IsEmpty:       func(context.Context, string) bool { return empty },
			CurrentBranch: func(context.Context, string) (string, error) { return branch, err },
		}
	}
	if msg, refuse := reader(true, "main", nil).BranchRefusal(ctx, "/d", "main"); !refuse || !strings.Contains(msg, "empty") {
		t.Errorf("empty: %q %t", msg, refuse)
	}
	if msg, refuse := reader(false, "feature", nil).BranchRefusal(ctx, "/d", "main"); !refuse || msg != "on branch feature" {
		t.Errorf("feature: %q %t", msg, refuse)
	}
	if _, refuse := reader(false, "main", nil).BranchRefusal(ctx, "/d", "main"); refuse {
		t.Error("the default branch was refused")
	}
	if _, refuse := reader(false, "", errors.New("unreadable")).BranchRefusal(ctx, "/d", "main"); refuse {
		t.Error("an unreadable branch is left to git, as before")
	}
}

func TestDefaultReadsThroughGit(t *testing.T) {
	if Default.RemoteOrigin == nil || Default.IsEmpty == nil || Default.CurrentBranch == nil {
		t.Fatal("Default must read every field through git")
	}
}
