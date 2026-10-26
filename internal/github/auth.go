// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package github

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

var (
	// runGitHubCLIAuthToken asks the gh CLI for its token for host, or for
	// its default host when host is empty. --hostname is what makes gh
	// answer with the Enterprise Server credential rather than the
	// github.com one.
	runGitHubCLIAuthToken = func(ctx context.Context, host string) (string, error) {
		args := []string{"auth", "token"}
		if host != "" {
			args = append(args, "--hostname", host)
		}
		// #nosec G204 -- fixed binary; host comes from a parsed URL and
		// follows a flag, so it cannot be read as one.
		cmd := exec.CommandContext(ctx, "gh", args...)
		out, err := cmd.Output()
		if err != nil {
			return "", fmt.Errorf("gh auth token failed: %w", err)
		}
		token := strings.TrimSpace(string(out))
		if token == "" {
			return "", errors.New("gh auth token returned an empty token")
		}
		return token, nil
	}
)

// envToken reads the token for host from the environment. github.com
// (host "") uses GITHUB_TOKEN or GH_TOKEN; an Enterprise Server uses
// GH_ENTERPRISE_TOKEN or GITHUB_ENTERPRISE_TOKEN, the names the gh CLI
// reads for the same purpose, so a github.com token is never presented
// to another host.
func envToken(host string) string {
	names := []string{"GITHUB_TOKEN", "GH_TOKEN"}
	if host != "" {
		names = []string{"GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN"}
	}
	for _, n := range names {
		if token := strings.TrimSpace(os.Getenv(n)); token != "" {
			return token
		}
	}
	return ""
}

// envTokenNames names the variables envToken reads for host, for an error
// message.
func envTokenNames(host string) string {
	if host != "" {
		return "GH_ENTERPRISE_TOKEN (or GITHUB_ENTERPRISE_TOKEN)"
	}
	return "GITHUB_TOKEN (or GH_TOKEN)"
}

// Token resolves a GitHub token for the given auth mode, returning an empty
// string when none can be obtained. It lets the git package authenticate HTTPS
// clones and pulls of private repositories with the same credential as the API.
func Token(ctx context.Context, authMode AuthMode) string {
	return TokenFor(ctx, authMode, "")
}

// TokenFor is Token for the GitHub instance baseURL names: github.com when
// it is empty or a github.com address, otherwise that Enterprise Server's
// own credential. It returns an empty string when none can be obtained or
// baseURL is unusable.
func TokenFor(ctx context.Context, authMode AuthMode, baseURL string) string {
	_, host, err := enterpriseBase(baseURL)
	if err != nil {
		return ""
	}
	tok, err := resolveTokenFor(ctx, authMode, host)
	if err != nil {
		return ""
	}
	return tok
}

func resolveToken(ctx context.Context, authMode AuthMode) (string, error) {
	return resolveTokenFor(ctx, authMode, "")
}

// resolveTokenFor resolves the token for host, "" meaning github.com.
func resolveTokenFor(ctx context.Context, authMode AuthMode, host string) (string, error) {
	switch normalizeAuthMode(authMode) {
	case AuthModeToken:
		token := envToken(host)
		if token == "" {
			return "", fmt.Errorf("%s environment variable not set", envTokenNames(host))
		}
		return token, nil
	case AuthModeGH:
		return runGitHubCLIAuthToken(ctx, host)
	default:
		token := envToken(host)
		if token != "" {
			return token, nil
		}
		token, err := runGitHubCLIAuthToken(ctx, host)
		if err != nil {
			return "", fmt.Errorf("failed to resolve GitHub token (auto mode): %w", err)
		}
		return token, nil
	}
}

func normalizeAuthMode(authMode AuthMode) AuthMode {
	switch AuthMode(strings.ToLower(strings.TrimSpace(string(authMode)))) {
	case AuthModeToken:
		return AuthModeToken
	case AuthModeGH:
		return AuthModeGH
	default:
		return AuthModeAuto
	}
}
