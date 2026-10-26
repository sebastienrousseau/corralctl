// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"context"
	"testing"
)

// A GitHub Enterprise Server destination must be given that host's token.
// The github.com one belongs to github.com, and presenting it to another
// host hands it to whoever runs that host.
func TestSyncNeverSendsTheGitHubDotComTokenToAnEnterpriseServer(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "dotcom")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GH_ENTERPRISE_TOKEN", "ghes")
	t.Setenv("GITHUB_ENTERPRISE_TOKEN", "")
	old := authMode
	authMode = "token"
	t.Cleanup(func() { authMode = old })

	specs, err := parseDestinations([]string{"github:acme@https://ghe.corp"})
	if err != nil {
		t.Fatal(err)
	}
	dests, err := buildDestinations(context.Background(), specs)
	if err != nil {
		t.Fatal(err)
	}
	if got := dests[0].Options.Token; got != "ghes" {
		t.Fatalf("token for https://ghe.corp = %q, want the enterprise token", got)
	}

	specs, _ = parseDestinations([]string{"github:acme"})
	dests, err = buildDestinations(context.Background(), specs)
	if err != nil || dests[0].Options.Token != "dotcom" {
		t.Fatalf("github.com token = %+v, %v", dests, err)
	}
}
