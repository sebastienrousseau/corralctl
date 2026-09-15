//go:build ignore

// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/sebastienrousseau/corralctl/internal/git"
)

func main() {
	ctx := context.Background()

	repoURL := "https://github.com/sebastienrousseau/corralctl.git"
	targetDir := "./tmp_corral_clone"

	defer func() {
		_ = os.RemoveAll(targetDir)
	}()

	fmt.Printf("Cloning %s into %s...\n", repoURL, targetDir)

	opts := git.CloneOptions{
		SingleBranch: true,
		Depth:        1,
	}
	err := git.Clone(ctx, repoURL, targetDir, opts)
	if err != nil {
		log.Fatalf("Git clone failed: %v", err)
	}

	branch, err := git.CurrentBranch(ctx, targetDir)
	if err != nil {
		log.Fatalf("Failed to query branch: %v", err)
	}

	remote, err := git.RemoteOrigin(ctx, targetDir)
	if err != nil {
		log.Fatalf("Failed to query remote: %v", err)
	}

	fmt.Printf("\nSuccess!\n")
	fmt.Printf(" - Active Branch: %s\n", branch)
	fmt.Printf(" - Origin URL: %s\n", remote)
}
