// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrPullRefused marks a pull corralctl declined to start because the
// clone holds state an unattended `git pull --rebase --autostash` could
// rewrite or tangle. The wrapped message names the reason.
var ErrPullRefused = errors.New("pull refused")

// inProgressMarkers are the files and directories git leaves in a
// worktree's git directory while an operation waits for the user.
//
// git pull refuses some of these itself, with messages that do not say
// what to do, and proceeds through others: a revert staged with
// --no-commit was autostashed, HEAD moved, and REVERT_HEAD was left
// pointing at the old base.
var inProgressMarkers = []struct{ name, what string }{
	{"rebase-merge", "a rebase"},
	{"rebase-apply", "a rebase or git am"},
	{"MERGE_HEAD", "a merge"},
	{"CHERRY_PICK_HEAD", "a cherry-pick"},
	{"REVERT_HEAD", "a revert"},
	{"sequencer", "a multi-commit cherry-pick or revert"},
	{"BISECT_LOG", "a bisect"},
}

// pullRefusal reports why Pull must not run in targetDir, or "" when it
// may. It fails closed: a state it cannot read is a reason to refuse.
//
// Local commits ahead of the upstream are refused rather than rebased.
// The rebase rewrites them, with signing disabled so it cannot block on
// a passphrase, so a signed local commit came back unsigned; and a
// conflict left the clone mid-rebase for the user to find later. corral
// does not rewrite commits it did not make: push them, then sync.
func pullRefusal(ctx context.Context, targetDir string) string {
	gitDir, err := runGitOutput(ctx, targetDir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return fmt.Sprintf("cannot locate the git directory: %v", err)
	}
	if reason := inProgressReason(strings.TrimSpace(gitDir)); reason != "" {
		return reason
	}
	ahead, err := runGitOutput(ctx, targetDir, "rev-list", "--count", "@{upstream}..HEAD")
	if err != nil {
		return "the branch has no readable upstream to compare with"
	}
	if n := strings.TrimSpace(ahead); n != "0" {
		return n + " local commit(s) not on the upstream branch; push them, then sync"
	}
	return ""
}

// inProgressReason names the first in-progress operation found in gitDir.
func inProgressReason(gitDir string) string {
	for _, m := range inProgressMarkers {
		if _, err := os.Lstat(filepath.Join(gitDir, m.name)); err == nil {
			return fmt.Sprintf("%s is in progress (%s); finish or abort it, then sync", m.what, m.name)
		}
	}
	return ""
}
