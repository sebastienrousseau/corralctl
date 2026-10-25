// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package symbols

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The symbol index must not read through a symlink a clone commits: its
// target can be any file on disk, and the extracted names and lines would
// carry its contents out to the agent.
func TestExtractRepoDoesNotFollowSymlinks(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.go"), []byte("package x\n\nfunc OutsideRootSecret() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := writeRepo(t, map[string]string{"real.go": "package x\n\nfunc Real() {}\n"})
	if err := os.Symlink(filepath.Join(outside, "secret.go"), filepath.Join(root, "leak.go")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	res, err := ExtractRepo(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range res.Symbols {
		if s.Name == "OutsideRootSecret" {
			t.Fatalf("symbol index read through a symlink: %+v", s)
		}
	}
	if got := parseOne(root, "leak.go"); got != nil {
		t.Fatalf("parseOne read through a symlink: %+v", got)
	}
}
