// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"testing"

	corralmcp "github.com/sebastienrousseau/corralctl/internal/mcp"
)

func TestRefreshWorkspaceFinderTagsEmpty(t *testing.T) {
	if count := refreshWorkspaceFinderTags(nil); count != 0 {
		t.Fatalf("expected 0 refreshed tags for empty list, got %d", count)
	}
}

func TestRefreshWorkspaceFinderTags(t *testing.T) {
	oldRefresh := doctorRefreshFinderTags
	defer func() { doctorRefreshFinderTags = oldRefresh }()

	var (
		mu             sync.Mutex
		refreshedPaths []string
	)
	doctorRefreshFinderTags = func(path string) error {
		if strings.Contains(path, "fail") {
			return errors.New("simulated failure")
		}
		mu.Lock()
		refreshedPaths = append(refreshedPaths, path)
		mu.Unlock()
		return nil
	}

	repos := []corralmcp.RepoEntry{
		{Name: "repo1", Path: "/workspace/repo1"},
		{Name: "repo2", Path: "/workspace/fail-repo"},
		{Name: "repo3", Path: "/workspace/repo3"},
	}

	count := refreshWorkspaceFinderTags(repos)
	if count != 2 {
		t.Fatalf("expected 2 successful refreshes, got %d", count)
	}
}

func TestDoctorRefreshTagsFlag(t *testing.T) {
	oldScan := doctorScan
	oldRefresh := doctorRefreshFinderTags
	oldOutput := doctorOutput
	oldRefreshFlag := doctorRefreshTags
	defer func() {
		doctorScan = oldScan
		doctorRefreshFinderTags = oldRefresh
		doctorOutput = oldOutput
		doctorRefreshTags = oldRefreshFlag
	}()

	doctorScan = func(root string) (*corralmcp.Index, error) {
		return &corralmcp.Index{
			Repos: []corralmcp.RepoEntry{
				{Name: "alpha", Path: "/tmp/alpha"},
			},
		}, nil
	}
	doctorRefreshFinderTags = func(path string) error {
		return nil
	}

	// 1. Text mode
	doctorOutput = "text"
	doctorRefreshTags = true

	rPipe, wPipe, _ := os.Pipe()
	oldStdout := os.Stdout
	os.Stdout = wPipe

	err := doctorCmd.RunE(doctorCmd, []string{t.TempDir()})
	_ = wPipe.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("unexpected error running doctor: %v", err)
	}

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, rPipe)
	_ = rPipe.Close()

	out := buf.String()
	if !strings.Contains(out, "Finder Tags:") || !strings.Contains(out, "Refreshed: 1 repositories") {
		t.Fatalf("expected Finder Tags refresh output, got:\n%s", out)
	}

	// 2. JSON mode
	doctorOutput = "json"
	rPipe, wPipe, _ = os.Pipe()
	os.Stdout = wPipe

	err = doctorCmd.RunE(doctorCmd, []string{t.TempDir()})
	_ = wPipe.Close()
	os.Stdout = oldStdout

	if err != nil {
		t.Fatalf("unexpected error running doctor json: %v", err)
	}

	buf.Reset()
	_, _ = io.Copy(&buf, rPipe)
	_ = rPipe.Close()

	jsonOut := buf.String()
	if !strings.Contains(jsonOut, `"refreshed_tags": 1`) {
		t.Fatalf("expected refreshed_tags: 1 in json output, got:\n%s", jsonOut)
	}
}
