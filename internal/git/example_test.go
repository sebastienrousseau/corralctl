// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package git_test

import (
	"context"
	"log"

	"github.com/sebastienrousseau/corralctl/internal/git"
)

// ExampleClone demonstrates a shallow, single-branch, blobless partial clone —
// the combination Corral uses to minimise bandwidth and disk for large mirrors.
func ExampleClone() {
	ctx := context.Background()
	opts := git.CloneOptions{
		SingleBranch: true,
		Blobless:     true,
		Depth:        1,
	}
	if err := git.Clone(ctx, "https://github.com/sebastienrousseau/corralctl.git", "/tmp/corral", opts); err != nil {
		log.Printf("clone failed: %v", err)
	}
}

// ExamplePull demonstrates updating an existing clone with rebase + autostash,
// recursing into submodules but tolerating submodule failures so a single
// inaccessible nested repo doesn't block the parent sync.
func ExamplePull() {
	ctx := context.Background()
	opts := git.PullOptions{
		RecurseSubmodules:       true,
		IgnoreSubmoduleFailures: true,
	}
	if err := git.Pull(ctx, "/tmp/corral", opts); err != nil {
		log.Printf("pull failed: %v", err)
	}
}
