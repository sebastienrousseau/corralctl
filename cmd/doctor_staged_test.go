// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitutil "github.com/sebastienrousseau/corralctl/internal/git"
	corralmcp "github.com/sebastienrousseau/corralctl/internal/mcp"
)

// TestDoctorSurfacesStagedDeletions reproduces D-03: a delete that crashed
// after renaming the clone aside left a .corral-deleting-* directory that
// doctor counted as an ordinary, healthy repository. It is now listed on
// its own, left in place, and the workspace is not reported healthy.
func TestDoctorSurfacesStagedDeletions(t *testing.T) {
	oldScan, oldLocal, oldUnpub := doctorScan, doctorHasLocalChanges, doctorHasUnpublishedWork
	oldWorktrees, oldDirSize, oldCacheDir, oldStat := doctorListWorktrees, doctorDirSize, doctorUserCacheDir, doctorStat
	t.Cleanup(func() {
		doctorScan, doctorHasLocalChanges, doctorHasUnpublishedWork = oldScan, oldLocal, oldUnpub
		doctorListWorktrees, doctorDirSize, doctorUserCacheDir, doctorStat = oldWorktrees, oldDirSize, oldCacheDir, oldStat
	})

	dir := t.TempDir()
	staged := filepath.Join(dir, "Public", ".corral-deleting-beta-00112233aabbccdd")
	doctorScan = func(string) (*corralmcp.Index, error) {
		return &corralmcp.Index{Repos: []corralmcp.RepoEntry{
			{Name: "alpha", Path: filepath.Join(dir, "Public", "alpha")},
			{Name: filepath.Base(staged), Path: staged},
		}}, nil
	}
	doctorHasLocalChanges = func(context.Context, string) (bool, string) { return false, "" }
	doctorHasUnpublishedWork = func(context.Context, string) (bool, string) { return false, "" }
	doctorListWorktrees = func(context.Context, string) ([]gitutil.WorktreeInfo, error) { return nil, nil }
	doctorDirSize = func(string) int64 { return 0 }
	doctorUserCacheDir = func() (string, error) { return "", errors.New("no cache dir") }
	doctorStat = func(string) (fs.FileInfo, error) { return nil, os.ErrNotExist }

	report, err := collectDoctorReport(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if report.TotalRepos != 1 || len(report.StagedDeletions) != 1 || report.StagedDeletions[0] != staged {
		t.Fatalf("repos=%d staged=%v, want 1 repo and the staged path", report.TotalRepos, report.StagedDeletions)
	}
	if report.Healthy {
		t.Error("a workspace with an interrupted deletion was reported healthy")
	}

	r, w, _ := os.Pipe()
	stdout := os.Stdout
	os.Stdout = w
	printDoctorText(report)
	_ = w.Close()
	os.Stdout = stdout
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	for _, want := range []string{"Interrupted Deletions: 1", staged, "attention required for 1 issues"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("text report lacks %q:\n%s", want, buf.String())
		}
	}
}
