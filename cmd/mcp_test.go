// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/mcp"
)

// stubMCPServer is the mcpServer implementation the tests inject via
// mcpNewServer. It records the constructor options and controls the
// ServeStdio outcome so runMCP's branches are all reachable without a
// real stdio loop.
type stubMCPServer struct {
	root           string
	mutations      bool
	audit          string
	serveErr       error
	serveCallCount int

	httpErr       error
	httpCallCount int
	httpAddr      string
}

func (s *stubMCPServer) Root() string           { return s.root }
func (s *stubMCPServer) MutationsEnabled() bool { return s.mutations }
func (s *stubMCPServer) AuditLogPath() string   { return s.audit }
func (s *stubMCPServer) ServeStdio() error {
	s.serveCallCount++
	return s.serveErr
}

func (s *stubMCPServer) ServeHTTP(_ context.Context, addr string) error {
	s.httpCallCount++
	s.httpAddr = addr
	return s.httpErr
}

func TestRunMCPAdditionalPathBranches(t *testing.T) {
	resetMCPFlags(t)
	oldBase, oldAbs, oldStat := baseDir, absMCPPath, statMCP
	t.Cleanup(func() { baseDir, absMCPPath, statMCP = oldBase, oldAbs, oldStat })
	baseDir = ""
	home := t.TempDir()
	oldHome := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = oldHome })
	defaultRoot := filepath.Join(home, "Code")
	if err := os.Mkdir(defaultRoot, 0o750); err != nil {
		t.Fatal(err)
	}
	stub := &stubMCPServer{root: defaultRoot, audit: "/tmp/audit.log"}
	withStubServer(t, stub, nil)
	if err := runMCP(nil, nil); err != nil {
		t.Fatal(err)
	}

	absMCPPath = func(string) (string, error) { return "", errors.New("abs failed") }
	if err := runMCP(nil, nil); err == nil || !strings.Contains(err.Error(), "resolving root") {
		t.Fatalf("absolute path error = %v", err)
	}
	absMCPPath = filepath.Abs
	statMCP = func(string) (os.FileInfo, error) { return nil, errors.New("stat failed") }
	if err := runMCP(nil, nil); err == nil || !strings.Contains(err.Error(), "not accessible") {
		t.Fatalf("stat error = %v", err)
	}
}

func TestMCPNewServerFactory(t *testing.T) {
	srv, err := mcpNewServer(mcp.ServerOptions{Root: t.TempDir()})
	if err != nil || srv == nil {
		t.Fatalf("factory result = %v, %v", srv, err)
	}
}

// withStubServer swaps in the given stubMCPServer and restores the
// production constructor at test teardown. Returns a pointer to the
// recorded ServerOptions so the test can assert what runMCP handed to
// the constructor.
func withStubServer(t *testing.T, stub *stubMCPServer, ctorErr error) *mcp.ServerOptions {
	t.Helper()
	captured := &mcp.ServerOptions{}
	old := mcpNewServer
	mcpNewServer = func(opts mcp.ServerOptions) (mcpServer, error) {
		*captured = opts
		if ctorErr != nil {
			return nil, ctorErr
		}
		return stub, nil
	}
	t.Cleanup(func() { mcpNewServer = old })
	return captured
}

// resetMCPFlags restores the package-level flag vars to their defaults
// so tests don't leak state into each other via cobra's global flag
// registry.
func resetMCPFlags(t *testing.T) {
	t.Helper()
	oldRoot, oldMut := mcpRoot, mcpEnableMutations
	oldHTTP, oldRemote, oldNoConfirm := mcpHTTP, mcpAllowRemote, mcpNoConfirmDeletes
	mcpRoot = ""
	mcpEnableMutations = false
	mcpHTTP = ""
	mcpAllowRemote = false
	mcpNoConfirmDeletes = false
	t.Cleanup(func() {
		mcpRoot, mcpEnableMutations = oldRoot, oldMut
		mcpHTTP, mcpAllowRemote, mcpNoConfirmDeletes = oldHTTP, oldRemote, oldNoConfirm
	})
}

func TestRunMCPHappyPath(t *testing.T) {
	resetMCPFlags(t)
	dir := t.TempDir()
	mcpRoot = dir

	stub := &stubMCPServer{root: dir}
	captured := withStubServer(t, stub, nil)

	if err := runMCP(nil, nil); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if stub.serveCallCount != 1 {
		t.Errorf("expected exactly one ServeStdio call, got %d", stub.serveCallCount)
	}

	// Absolute path arrived at the constructor.
	if abs, _ := filepath.Abs(dir); captured.Root != abs {
		t.Errorf("constructor root = %q, want %q", captured.Root, abs)
	}
}

func TestRunMCPDefaultsToBaseDir(t *testing.T) {
	resetMCPFlags(t)
	dir := t.TempDir()

	// mcpRoot unset → runMCP must fall back to the shared baseDir var.
	oldBase := baseDir
	baseDir = dir
	t.Cleanup(func() { baseDir = oldBase })

	stub := &stubMCPServer{root: dir}
	captured := withStubServer(t, stub, nil)

	if err := runMCP(nil, nil); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if abs, _ := filepath.Abs(dir); captured.Root != abs {
		t.Errorf("expected fallback root %q, got %q", abs, captured.Root)
	}
}

func TestRunMCPMutationsFlagPropagates(t *testing.T) {
	resetMCPFlags(t)
	dir := t.TempDir()
	mcpRoot = dir
	mcpEnableMutations = true

	stub := &stubMCPServer{root: dir, mutations: true}
	captured := withStubServer(t, stub, nil)

	if err := runMCP(nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !captured.EnableMutations {
		t.Error("expected --enable-mutations to reach ServerOptions")
	}
}

func TestRunMCPRejectsMissingRoot(t *testing.T) {
	resetMCPFlags(t)
	mcpRoot = "/definitely/not/a/directory/at/all"

	stub := &stubMCPServer{}
	withStubServer(t, stub, nil)

	err := runMCP(nil, nil)
	if err == nil {
		t.Fatal("expected error for missing root")
	}
	if !strings.Contains(err.Error(), "not accessible") {
		t.Errorf("expected 'not accessible', got %v", err)
	}
	if stub.serveCallCount != 0 {
		t.Errorf("ServeStdio should not run when root is invalid; called %d times", stub.serveCallCount)
	}
}

func TestRunMCPRejectsFileRoot(t *testing.T) {
	resetMCPFlags(t)
	f, err := os.CreateTemp("", "mcp_root_file_*")
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	t.Cleanup(func() { _ = os.Remove(f.Name()) })
	mcpRoot = f.Name()

	stub := &stubMCPServer{}
	withStubServer(t, stub, nil)

	err = runMCP(nil, nil)
	if err == nil {
		t.Fatal("expected error for non-directory root")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("expected 'not a directory', got %v", err)
	}
}

func TestRunMCPPropagatesConstructorError(t *testing.T) {
	resetMCPFlags(t)
	dir := t.TempDir()
	mcpRoot = dir

	ctorErr := errors.New("boom-from-ctor")
	withStubServer(t, nil, ctorErr)

	err := runMCP(nil, nil)
	if err == nil || !strings.Contains(err.Error(), "constructing mcp server") {
		t.Errorf("expected wrapped constructor error, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "boom-from-ctor") {
		t.Errorf("expected inner error preserved in wrap, got %v", err)
	}
}

func TestRunMCPPropagatesServeError(t *testing.T) {
	resetMCPFlags(t)
	dir := t.TempDir()
	mcpRoot = dir

	serveErr := errors.New("stdio-blew-up")
	stub := &stubMCPServer{root: dir, serveErr: serveErr}
	withStubServer(t, stub, nil)

	err := runMCP(nil, nil)
	if err == nil || !strings.Contains(err.Error(), "mcp server") {
		t.Errorf("expected wrapped serve error, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "stdio-blew-up") {
		t.Errorf("expected inner error preserved, got %v", err)
	}
}

// TestLoopbackOnly is the classification the --http guard rests on.
//
// The case that matters is ":7777" — an empty host binds every interface,
// and it is the form somebody types when they are thinking about the port
// and not about the host.
func TestLoopbackOnly(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:7777", true},
		{"localhost:7777", true},
		{"[::1]:7777", true},
		{"127.0.0.2:7777", true}, // the whole 127/8 block is loopback

		{":7777", false},
		{"0.0.0.0:7777", false},
		{"[::]:7777", false},
		{"192.168.1.10:7777", false},
		{"example.com:7777", false}, // a name that is not localhost
		{"7777", false},             // no port separator at all
		{"", false},
	} {
		if got := loopbackOnly(tc.addr); got != tc.want {
			t.Errorf("loopbackOnly(%q) = %t, want %t", tc.addr, got, tc.want)
		}
	}
}

// TestRunMCPRefusesARoutableBind: the server has no authentication and
// serves a developer's whole workspace, so publishing it has to be asked
// for rather than arrived at.
func TestRunMCPRefusesARoutableBind(t *testing.T) {
	resetMCPFlags(t)
	dir := t.TempDir()
	mcpRoot = dir
	mcpHTTP = ":7777"

	stub := &stubMCPServer{root: dir}
	withStubServer(t, stub, nil)

	err := runMCP(nil, nil)
	if err == nil {
		t.Fatal("a bind on every interface should be refused")
	}
	if !strings.Contains(err.Error(), "--allow-remote") {
		t.Errorf("the refusal should name the override: %v", err)
	}
	// Refused before anything was constructed, let alone served.
	if stub.httpCallCount != 0 || stub.serveCallCount != 0 {
		t.Error("the server was started despite the refusal")
	}
}

func TestRunMCPServesOverHTTP(t *testing.T) {
	resetMCPFlags(t)
	dir := t.TempDir()
	mcpRoot = dir
	mcpHTTP = "127.0.0.1:7777"

	stub := &stubMCPServer{root: dir}
	withStubServer(t, stub, nil)

	if err := runMCP(nil, nil); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if stub.httpCallCount != 1 {
		t.Errorf("ServeHTTP called %d times, want 1", stub.httpCallCount)
	}
	if stub.httpAddr != "127.0.0.1:7777" {
		t.Errorf("served on %q, want the address that was asked for", stub.httpAddr)
	}
	if stub.serveCallCount != 0 {
		t.Error("--http must not also start the stdio loop")
	}
}

// TestRunMCPAllowsARoutableBindWhenAsked covers the escape hatch, which
// exists for somebody who has put their own authentication in front.
func TestRunMCPAllowsARoutableBindWhenAsked(t *testing.T) {
	resetMCPFlags(t)
	dir := t.TempDir()
	mcpRoot = dir
	mcpHTTP = "0.0.0.0:7777"
	mcpAllowRemote = true

	stub := &stubMCPServer{root: dir}
	withStubServer(t, stub, nil)

	if err := runMCP(nil, nil); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if stub.httpAddr != "0.0.0.0:7777" {
		t.Errorf("served on %q, want the address that was asked for", stub.httpAddr)
	}
}

func TestRunMCPReportsAnHTTPFailure(t *testing.T) {
	resetMCPFlags(t)
	dir := t.TempDir()
	mcpRoot = dir
	mcpHTTP = "127.0.0.1:7777"

	stub := &stubMCPServer{root: dir, httpErr: errors.New("address already in use")}
	withStubServer(t, stub, nil)

	err := runMCP(nil, nil)
	if err == nil {
		t.Fatal("a failed listener should be reported")
	}
	if !strings.Contains(err.Error(), "address already in use") {
		t.Errorf("the cause should survive: %v", err)
	}
}

// TestConfirmDeletesDefaultsOn: the flag is a negative, so the default has
// to be asserted rather than assumed. A default that silently flipped would
// remove the confirmation from every existing configuration.
func TestConfirmDeletesDefaultsOn(t *testing.T) {
	resetMCPFlags(t)
	dir := t.TempDir()
	mcpRoot = dir

	captured := withStubServer(t, &stubMCPServer{root: dir}, nil)
	if err := runMCP(nil, nil); err != nil {
		t.Fatal(err)
	}
	if !captured.ConfirmDeletes {
		t.Error("deletions should require confirmation unless --no-confirm-deletes is passed")
	}

	mcpNoConfirmDeletes = true
	if err := runMCP(nil, nil); err != nil {
		t.Fatal(err)
	}
	if captured.ConfirmDeletes {
		t.Error("--no-confirm-deletes should switch the confirmation off")
	}
}
