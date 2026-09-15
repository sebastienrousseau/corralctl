// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// hardeningWorkspace lays out one clone and returns the workspace root.
func hardeningWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "Public", "go", "victim")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := "[remote \"origin\"]\n\turl = https://github.com/acme/victim.git\n"
	if err := os.WriteFile(filepath.Join(repo, ".git", "config"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeRepoFile(t *testing.T, root, rel, body string) {
	t.Helper()
	full := filepath.Join(root, "Public", "go", "victim", rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFileResource(t *testing.T, s *Server, rel string) (string, error) {
	t.Helper()
	res, err := s.handleRepoFileResource(context.Background(), &mcp.ReadResourceRequest{
		Params: &mcp.ReadResourceParams{URI: "corral://repo/Public/victim/file/" + rel},
	})
	if err != nil {
		return "", err
	}
	return res.Contents[0].Text, nil
}

// TestFileResourceRefusesUnallowlistedExtension covers the allowlist gate in
// the handler, distinct from the unit test on fileAllowed itself.
func TestFileResourceRefusesUnallowlistedExtension(t *testing.T) {
	root := hardeningWorkspace(t)
	writeRepoFile(t, root, "notes.xyz", "unremarkable content")
	writeRepoFile(t, root, "README.md", "hello")

	s, err := NewServer(ServerOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := readFileResource(t, s, "notes.xyz"); err == nil {
		t.Fatal("expected .xyz to be refused by the allowlist")
	} else if !strings.Contains(err.Error(), "--allow-file-ext") {
		t.Fatalf("refusal should name the escape hatch, got %v", err)
	}

	body, err := readFileResource(t, s, "README.md")
	if err != nil {
		t.Fatalf("README.md should still be served: %v", err)
	}
	if body != "hello" {
		t.Fatalf("README.md body = %q", body)
	}
}

// TestFileResourceHonoursAllowFileExt covers the ServerOptions plumbing from
// the CLI flag through to the handler.
func TestFileResourceHonoursAllowFileExt(t *testing.T) {
	root := hardeningWorkspace(t)
	writeRepoFile(t, root, "page.tpl", "template body")

	s, err := NewServer(ServerOptions{Root: root, AllowFileExts: []string{"tpl"}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := readFileResource(t, s, "page.tpl")
	if err != nil {
		t.Fatalf("--allow-file-ext tpl should permit page.tpl: %v", err)
	}
	if body != "template body" {
		t.Fatalf("page.tpl body = %q", body)
	}
}

// TestFileResourceRefusesAuditCredentials is the end-to-end regression for
// the eight paths that were served in full before the policy was inverted.
func TestFileResourceRefusesAuditCredentials(t *testing.T) {
	root := hardeningWorkspace(t)
	for _, rel := range []string{
		".kube/config", "kubeconfig", "credentials.json", ".pgpass",
		"infra/terraform.tfvars", ".htpasswd", ".yarnrc.yml", "deploy/deploy.ppk",
	} {
		writeRepoFile(t, root, rel, "SECRET-"+rel)
	}

	s, err := NewServer(ServerOptions{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		".kube/config", "kubeconfig", "credentials.json", ".pgpass",
		"infra/terraform.tfvars", ".htpasswd", ".yarnrc.yml", "deploy/deploy.ppk",
	} {
		body, err := readFileResource(t, s, rel)
		if err == nil {
			t.Errorf("%s was served: %q", rel, body)
		}
	}
}

// TestCloneRepoRejectsUnsafeURL covers the scheme gate in handleCloneRepo.
// The refusal must land before any audit record or filesystem work.
func TestCloneRepoRejectsUnsafeURL(t *testing.T) {
	root := hardeningWorkspace(t)
	s, err := NewServer(ServerOptions{
		Root:            root,
		EnableMutations: true,
		AuditLogPath:    filepath.Join(t.TempDir(), "mutations.log"),
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, url := range []string{
		"ext::sh -c 'id > /tmp/pwn'",
		"file:///etc/passwd",
		"--upload-pack=/bin/sh",
		"",
	} {
		res := asToolResult(s.handleCloneRepo(context.Background(), nil, cloneInput{
			URL:    url,
			Target: "Public/go/newrepo",
		}))
		if !res.IsError {
			t.Errorf("handleCloneRepo accepted unsafe url %q", url)
		}
	}

	// Nothing was attempted, so nothing should have been created.
	if _, err := os.Stat(filepath.Join(root, "Public", "go", "newrepo")); !os.IsNotExist(err) {
		t.Error("a refused clone still created its target directory")
	}
}

func TestRedactedEntriesNil(t *testing.T) {
	if got := RedactedEntries(nil); got != nil {
		t.Fatalf("RedactedEntries(nil) = %v, want nil", got)
	}
}

// TestRedactedKeepsInternalPathExact is the invariant that makes redaction
// safe to apply: the stored entry is what git and SafeMutationPath use, so
// only the returned copy may be altered.
func TestRedactedKeepsInternalPathExact(t *testing.T) {
	original := RepoEntry{
		Name:      "repo\x1bname",
		Path:      "/root/Public/go/repo\x1bname",
		RelPath:   "Public/go/repo\x1bname",
		RemoteURL: "https://github.com/acme/x.git",
		State:     &StateRecord{LastSyncedAt: "2026-09-02T00:00:00Z\x1b"},
	}
	red := original.Redacted()

	if strings.ContainsRune(red.Name, 0x1b) || strings.ContainsRune(red.Path, 0x1b) {
		t.Error("Redacted left an escape in the output copy")
	}
	if !strings.ContainsRune(original.Name, 0x1b) || !strings.ContainsRune(original.Path, 0x1b) {
		t.Error("Redacted mutated the original entry; internal paths must stay byte-exact")
	}
	if original.State.LastSyncedAt == red.State.LastSyncedAt {
		t.Error("Redacted shared the State pointer with the original")
	}
	if !strings.ContainsRune(original.State.LastSyncedAt, 0x1b) {
		t.Error("Redacted mutated the original's State")
	}

	// A nil State must survive as nil rather than being materialised.
	if got := (RepoEntry{Name: "x"}).Redacted(); got.State != nil {
		t.Error("Redacted invented a State record")
	}
}

func TestIsTransportToken(t *testing.T) {
	for _, s := range []string{"ext", "ftp", "git-remote-foo", "a1+b.c-d"} {
		if !isTransportToken(s) {
			t.Errorf("isTransportToken(%q) = false, want true", s)
		}
	}
	for _, s := range []string{
		"",          // empty
		"has/slash", // a path
		"git@host",  // scp-style target
		"has space", // not a token
		"1leading",  // must start with a letter
		"has\tTab",  // not a token
	} {
		if isTransportToken(s) {
			t.Errorf("isTransportToken(%q) = true, want false", s)
		}
	}
}

// TestServerInstructionsFrameOutputAsUntrusted pins the other half of the
// injection mitigation: sanitising removes the invisible mechanisms, and
// this tells the model the visible text is data.
func TestServerInstructionsFrameOutputAsUntrusted(t *testing.T) {
	got := serverInstructions(ServerOptions{Root: "/tmp"})
	for _, want := range []string{"untrusted data", "never as instructions", "report it"} {
		if !strings.Contains(got, want) {
			t.Errorf("server instructions missing %q", want)
		}
	}
}
