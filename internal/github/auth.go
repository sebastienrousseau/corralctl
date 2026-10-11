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
	runGitHubCLIAuthToken = func(ctx context.Context) (string, error) {
		cmd := exec.CommandContext(ctx, "gh", "auth", "token")
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

func envToken() string {
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		return token
	}
	if token := strings.TrimSpace(os.Getenv("GH_TOKEN")); token != "" {
		return token
	}
	return ""
}

// Token resolves a GitHub token for the given auth mode, returning an empty
// string when none can be obtained. It lets the git package authenticate HTTPS
// clones and pulls of private repositories with the same credential as the API.
func Token(ctx context.Context, authMode AuthMode) string {
	tok, err := resolveToken(ctx, authMode)
	if err != nil {
		return ""
	}
	return tok
}

func resolveToken(ctx context.Context, authMode AuthMode) (string, error) {
	switch normalizeAuthMode(authMode) {
	case AuthModeToken:
		token := envToken()
		if token == "" {
			return "", errors.New("GITHUB_TOKEN (or GH_TOKEN) environment variable not set")
		}
		return token, nil
	case AuthModeGH:
		return runGitHubCLIAuthToken(ctx)
	default:
		token := envToken()
		if token != "" {
			return token, nil
		}
		token, err := runGitHubCLIAuthToken(ctx)
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
