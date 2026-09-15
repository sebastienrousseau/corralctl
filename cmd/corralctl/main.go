// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package main is the entry point for the Corral CLI application.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/sebastienrousseau/corralctl/cmd"
	"github.com/sebastienrousseau/corralctl/internal/git"
)

var (
	resolveGitBinary = git.ResolveGitBinary
	executeContext   = cmd.ExecuteContext
	exitMain         = os.Exit
)

// main invokes the Cobra CLI execution.
func main() {
	if err := resolveGitBinary(); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		exitMain(1)
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	executeContext(ctx)
}
