// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mirror

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWalkSkipsClonesStagedForDeletion reproduces D-03: a delete that
// crashed after its rename leaves .corral-deleting-<name>-<hex>, which
// still holds .git. The walk took it for a repository and `sync` would
// have created and pushed a destination repository under that name.
func TestWalkSkipsClonesStagedForDeletion(t *testing.T) {
	base := t.TempDir()
	for _, p := range []string{
		filepath.Join(base, "Public", "Go", "alpha", ".git"),
		filepath.Join(base, "Public", "Go", ".corral-deleting-beta-00112233aabbccdd", ".git"),
	} {
		if err := os.MkdirAll(p, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Walk(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "alpha" {
		t.Fatalf("repositories = %+v, want only alpha", got)
	}
}
