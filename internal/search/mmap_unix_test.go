// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build unix

package search

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMapFileReportsAnUnmappableTarget(t *testing.T) {
	// A directory opens cleanly and mmap refuses it — which is the shape of
	// every real mmap failure this code can meet. It needs an entry: btrfs
	// reports an empty directory's size as 0, which would take the "empty"
	// branch instead of reaching mmap. Unix only: NTFS reports 0 for every
	// directory, so on Windows no directory reaches the mapping call.
	d, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if err := os.WriteFile(filepath.Join(d.Name(), "entry"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = mapFile(d)
	if err == nil || !strings.HasPrefix(err.Error(), "mmap:") {
		t.Errorf("err = %v, want the mmap failure", err)
	}
}
