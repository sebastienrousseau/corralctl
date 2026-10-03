// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package git

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestWithMetadataTimeoutNilContext covers the defensive nil branch: these
// helpers are exported, so an embedder can reach them with a nil context,
// and context.WithTimeout panics on nil.
func TestWithMetadataTimeoutNilContext(t *testing.T) {
	// SA1012 forbids a literal nil Context, which is the entire point of
	// this test: withMetadataTimeout is reached from exported functions an
	// embedder can call with nil, and context.WithTimeout panics on one.
	//
	// The directive is staticcheck's own `//lint:ignore`, not golangci-lint's
	// `//nolint`. CI runs staticcheck standalone, which does not honour
	// //nolint — so a //nolint here passes golangci-lint locally and fails
	// CI, which is exactly what happened.
	//lint:ignore SA1012 passing nil is the behaviour under test
	ctx, cancel := withMetadataTimeout(nil)
	defer cancel()
	if ctx == nil {
		t.Fatal("withMetadataTimeout(nil) returned a nil context")
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("withMetadataTimeout did not set a deadline")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > metadataTimeout {
		t.Fatalf("deadline %v is not within (0, %v]", remaining, metadataTimeout)
	}
}

// TestWithMetadataTimeoutPreservesCancellation asserts the derived context
// still unwinds when the caller's run is cancelled, which is the reason
// these calls take a context at all.
func TestWithMetadataTimeoutPreservesCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	ctx, cancel := withMetadataTimeout(parent)
	defer cancel()

	cancelParent()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling the parent did not cancel the metadata context")
	}
}

// TestMetadataCallsRespectCancellation covers the three helpers that used to
// run with no context and could block forever on an unresponsive filesystem.
func TestMetadataCallsRespectCancellation(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	if _, err := CurrentBranch(ctx, dir); err == nil {
		t.Error("CurrentBranch ignored a cancelled context")
	}
	if _, err := RemoteOrigin(ctx, dir); err == nil {
		t.Error("RemoteOrigin ignored a cancelled context")
	}
	if !IsEmpty(ctx, dir) {
		t.Error("IsEmpty should report true when the command cannot run")
	}
}

// TestGitHooksAreDisarmedDuringOperations proves that repository hooks
// are disarmed and cannot execute during Clone or Pull.
func TestGitHooksAreDisarmedDuringOperations(t *testing.T) {
	bareDir, workDir := setupTestRepo(t)
	defer cleanup(t, bareDir)
	defer cleanup(t, workDir)

	targetDir := t.TempDir()
	if err := Clone(context.Background(), bareDir, targetDir, CloneOptions{}); err != nil {
		t.Fatalf("Clone failed: %v", err)
	}

	marker := filepath.Join(targetDir, "hook_ran.marker")
	hookScript := "#!/bin/sh\ntouch \"" + marker + "\"\n"
	hooksDir := filepath.Join(targetDir, ".git", "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, hook := range []string{"post-merge", "post-checkout", "pre-rebase"} {
		path := filepath.Join(hooksDir, hook)
		if err := os.WriteFile(path, []byte(hookScript), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	newFile := filepath.Join(workDir, "new.txt")
	if err := os.WriteFile(newFile, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	run(t, "git", "-C", workDir, "add", "new.txt")
	run(t, "git", "-C", workDir, "commit", "-m", "update")
	run(t, "git", "-C", workDir, "push", "origin", "main")

	if err := Pull(context.Background(), targetDir, PullOptions{}); err != nil {
		t.Fatalf("Pull failed: %v", err)
	}

	if _, err := os.Stat(marker); err == nil {
		t.Errorf("hook executed during Pull despite core.hooksPath=/dev/null hardening")
	}
}
