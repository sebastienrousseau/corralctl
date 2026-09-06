//go:build ignore

// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/sebastienrousseau/corralctl/internal/engine"
	"github.com/sebastienrousseau/corralctl/internal/github"
)

func main() {
	ctx := context.Background()

	opts := engine.RunOptions{
		Owner:       "sebastienrousseau",
		BaseDir:     "./my_local_mirror",
		Concurrency: 4,
		DryRun:      true,
		Protocol:    "https",
		DoSync:      true,
		Output:      engine.OutputText,
		Layout:      "{{.Visibility}}/{{.Language}}/{{.Name}}",
		Fetch: github.FetchOptions{
			Limit:      10,
			Visibility: "public",
			Type:       "sources",
			Sort:       "stars",
			AuthMode:   github.AuthModeAuto,
		},
	}

	fmt.Println("Running Corral organization engine in dry-run mode...")

	// RunE, not Run. Run is the CLI's wrapper: it prints the failure and
	// terminates the process, which is right for a command and wrong for a
	// library. RunE hands the error back so an embedder decides what a
	// failure means.
	//
	// A run that finished with per-repository failures returns an
	// *engine.ExitError carrying the exit status the CLI would have used,
	// which lets a caller tell "corral could not start" apart from "corral
	// ran and some repositories failed".
	if err := engine.RunE(ctx, opts); err != nil {
		var exit *engine.ExitError
		if errors.As(err, &exit) {
			log.Fatalf("run finished with exit status %d: %v", exit.Code, err)
		}
		log.Fatalf("run could not start: %v", err)
	}

	fmt.Println("\nDry run completed successfully.")
}
