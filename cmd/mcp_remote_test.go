// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureMCPStderr runs fn with os.Stderr redirected and returns what it
// wrote.
func captureMCPStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = old }()
	fn()
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

// TestRunMCPTokenSources: the token comes from --token-file, from
// CORRAL_MCP_TOKEN, or (with a warning, since argv is world-readable)
// from --token.
func TestRunMCPTokenSources(t *testing.T) {
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenPath, []byte("  file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	emptyPath := filepath.Join(dir, "empty")
	if err := os.WriteFile(emptyPath, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("file", func(t *testing.T) {
		resetMCPFlags(t)
		mcpRoot, mcpTokenFile = dir, tokenPath
		t.Setenv("CORRAL_MCP_TOKEN", "env-secret")
		captured := withStubServer(t, &stubMCPServer{root: dir}, nil)
		if err := runMCP(nil, nil); err != nil {
			t.Fatal(err)
		}
		if captured.AuthToken != "file-secret" {
			t.Errorf("AuthToken = %q, want the trimmed file contents", captured.AuthToken)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		resetMCPFlags(t)
		mcpRoot, mcpTokenFile = dir, filepath.Join(dir, "absent")
		withStubServer(t, &stubMCPServer{root: dir}, nil)
		if err := runMCP(nil, nil); err == nil || !strings.Contains(err.Error(), "--token-file") {
			t.Fatalf("an unreadable token file should be refused by flag name: %v", err)
		}
	})
	t.Run("empty file", func(t *testing.T) {
		resetMCPFlags(t)
		mcpRoot, mcpTokenFile = dir, emptyPath
		withStubServer(t, &stubMCPServer{root: dir}, nil)
		if err := runMCP(nil, nil); err == nil || !strings.Contains(err.Error(), "empty") {
			t.Fatalf("an empty token file should be refused, not run without a token: %v", err)
		}
	})
	t.Run("both flags", func(t *testing.T) {
		resetMCPFlags(t)
		mcpRoot, mcpTokenFile, mcpToken = dir, tokenPath, "argv-secret"
		withStubServer(t, &stubMCPServer{root: dir}, nil)
		if err := runMCP(nil, nil); err == nil || !strings.Contains(err.Error(), "--token-file") {
			t.Fatalf("--token with --token-file should be refused: %v", err)
		}
	})
	t.Run("argv warns", func(t *testing.T) {
		resetMCPFlags(t)
		mcpRoot, mcpToken = dir, "argv-secret"
		captured := withStubServer(t, &stubMCPServer{root: dir}, nil)
		stderr := captureMCPStderr(t, func() {
			if err := runMCP(nil, nil); err != nil {
				t.Fatal(err)
			}
		})
		if captured.AuthToken != "argv-secret" {
			t.Errorf("AuthToken = %q, want argv-secret", captured.AuthToken)
		}
		if !strings.Contains(stderr, "--token is deprecated") {
			t.Errorf("--token should warn that argv is visible, got: %s", stderr)
		}
	})
}

// TestRunMCPAllowsARoutableBindWhenAsked covers the escape hatch, which
// takes --allow-remote and a bearer token together.
func TestRunMCPAllowsARoutableBindWhenAsked(t *testing.T) {
	resetMCPFlags(t)
	dir := t.TempDir()
	mcpRoot = dir
	mcpHTTP = "0.0.0.0:7777"
	mcpAllowRemote = true
	t.Setenv("CORRAL_MCP_TOKEN", "remote-secret")

	stub := &stubMCPServer{root: dir}
	withStubServer(t, stub, nil)

	if err := runMCP(nil, nil); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if stub.httpAddr != "0.0.0.0:7777" {
		t.Errorf("served on %q, want the address that was asked for", stub.httpAddr)
	}
}

// TestRunMCPRefusesARoutableHost: the loopback guard covers --host as it
// covers --http, for both listening transports.
func TestRunMCPRefusesARoutableHost(t *testing.T) {
	for _, transport := range []string{"streamable-http", "sse"} {
		t.Run(transport, func(t *testing.T) {
			resetMCPFlags(t)
			dir := t.TempDir()
			mcpRoot = dir
			mcpTransport, mcpHost = transport, "0.0.0.0"

			stub := &stubMCPServer{root: dir}
			withStubServer(t, stub, nil)
			err := runMCP(nil, nil)
			if err == nil || !strings.Contains(err.Error(), "--allow-remote") {
				t.Fatalf("a bind on every interface should be refused and name the override: %v", err)
			}
			if stub.httpCallCount+stub.sseCallCount+stub.serveCallCount != 0 {
				t.Error("the server was started despite the refusal")
			}

			// --allow-remote alone is not enough: a routable listener
			// without a token would serve the workspace to anyone.
			mcpAllowRemote = true
			err = runMCP(nil, nil)
			if err == nil || !strings.Contains(err.Error(), "CORRAL_MCP_TOKEN") {
				t.Fatalf("--allow-remote without a token should be refused and name the fix: %v", err)
			}
			if stub.httpCallCount+stub.sseCallCount+stub.serveCallCount != 0 {
				t.Error("the server was started without a token")
			}

			t.Setenv("CORRAL_MCP_TOKEN", "remote-secret")
			stderr := captureMCPStderr(t, func() {
				if err := runMCP(nil, nil); err != nil {
					t.Fatalf("--allow-remote with a token should lift the refusal: %v", err)
				}
			})
			if !strings.Contains(stderr, "TLS") {
				t.Errorf("a routable plain-HTTP listener should say it needs TLS in front, got: %s", stderr)
			}
		})
	}
}

func TestRunMCPMetricsFlags(t *testing.T) {
	resetMCPFlags(t)
	dir := t.TempDir()
	mcpRoot = dir
	stub := &stubMCPServer{root: dir}

	// 1. Explicit --metrics-addr
	mcpMetricsAddr = "127.0.0.1:9095"
	captured := withStubServer(t, stub, nil)
	if err := runMCP(nil, nil); err != nil {
		t.Fatalf("unexpected error with --metrics-addr: %v", err)
	}
	if captured.MetricsAddr != "127.0.0.1:9095" {
		t.Errorf("expected MetricsAddr 127.0.0.1:9095, got %q", captured.MetricsAddr)
	}

	// 2. Synthesized from --metrics-port
	resetMCPFlags(t)
	mcpRoot = dir
	mcpMetricsPort = 9096
	captured = withStubServer(t, stub, nil)
	if err := runMCP(nil, nil); err != nil {
		t.Fatalf("unexpected error with --metrics-port: %v", err)
	}
	if captured.MetricsAddr != "127.0.0.1:9096" {
		t.Errorf("expected MetricsAddr 127.0.0.1:9096, got %q", captured.MetricsAddr)
	}

	// 3. Non-loopback metrics address rejected without allow-remote
	resetMCPFlags(t)
	mcpRoot = dir
	mcpMetricsAddr = "192.168.1.50:9097"
	withStubServer(t, stub, nil)
	err := runMCP(nil, nil)
	if err == nil || !strings.Contains(err.Error(), "must be on loopback") {
		t.Fatalf("expected loopback refusal for metrics addr, got: %v", err)
	}

	// 4. Non-loopback with allow-remote still needs a token
	mcpAllowRemote = true
	withStubServer(t, stub, nil)
	err = runMCP(nil, nil)
	if err == nil || !strings.Contains(err.Error(), "token") {
		t.Fatalf("a routable metrics listener without a token should be refused: %v", err)
	}

	// 5. Non-loopback permitted with allow-remote and a token
	t.Setenv("CORRAL_MCP_TOKEN", "metrics-secret")
	captured = withStubServer(t, stub, nil)
	if err := runMCP(nil, nil); err != nil {
		t.Fatalf("expected allow-remote to permit routable metrics addr: %v", err)
	}
	if captured.MetricsAddr != "192.168.1.50:9097" {
		t.Errorf("expected MetricsAddr 192.168.1.50:9097, got %q", captured.MetricsAddr)
	}
}
