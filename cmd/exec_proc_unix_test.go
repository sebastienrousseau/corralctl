// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build unix

package cmd

import (
	"context"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestRunExecCommandsTimeoutKillsChildren checks that a deadline stops
// the processes the shell started, not only the shell: a timed-out
// `make test` must not keep running in the background.
func TestRunExecCommandsTimeoutKillsChildren(t *testing.T) {
	repo := t.TempDir()
	opts := execOptions{
		Command:     "sleep 30 & echo $! > child.pid; wait",
		Concurrency: 1,
		Timeout:     300 * time.Millisecond,
	}
	_ = captureStdout(t, func() { runExecCommands(context.Background(), []string{repo}, opts) })
	raw, err := fs.ReadFile(os.DirFS(repo), "child.pid")
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("child %d outlived the timeout", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
