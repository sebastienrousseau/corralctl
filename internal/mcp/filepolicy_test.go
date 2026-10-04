// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"strings"
	"testing"
)

// TestFileAllowedServesWork asserts the allowlist does not break the thing
// the resource exists for: reading source and documentation.
func TestFileAllowedServesWork(t *testing.T) {
	for _, path := range []string{
		"main.go", "src/lib.rs", "app/models/user.rb", "index.ts",
		"README.md", "docs/guide.rst", "CHANGELOG", "LICENSE",
		"Makefile", "Dockerfile", "go.mod", "go.sum", "Cargo.lock",
		"config.yaml", "package.json", "pyproject.toml", ".gitignore",
		"deploy/chart/values.yaml", "web/styles/main.scss",
		"MAKEFILE", "ReadMe", // basename matching is case-insensitive
	} {
		if reason, ok := fileAllowed(path, nil); !ok {
			t.Errorf("fileAllowed(%q) refused a legitimate file: %s", path, reason)
		}
	}
}

// TestFileAllowedRefusesUnknownShapes is the point of the inversion: a file
// the policy has never heard of is refused rather than served.
func TestFileAllowedRefusesUnknownShapes(t *testing.T) {
	for _, path := range []string{
		"kubeconfig",             // no extension, not a project file
		"backup.sqlite",          // unknown extension
		"dump.tar.gz",            // unknown extension
		"deploy/deploy.ppk",      // private key
		"infra/terraform.tfvars", // secrets by convention
		".pgpass",                // dotfile, not a project file
		".htpasswd",              // dotfile, not a project file
		"id_rsa",                 // no extension
		"vault.kdbx",             // unknown extension
		"snapshot.bin",           // unknown extension
	} {
		if _, ok := fileAllowed(path, nil); ok {
			t.Errorf("fileAllowed(%q) served a file with no recognised shape", path)
		}
	}
}

func TestFileAllowedMessagesNameTheEscapeHatch(t *testing.T) {
	// A refusal has to tell the caller how to proceed, or an agent retries
	// the same call forever.
	// #nosec G101 -- these are filenames and a flag name, not credentials
	cases := map[string]string{
		"notes.xyz":  "--allow-file-ext",
		"kubeconfig": "--allow-file-ext",
		".pgpass":    "--allow-file-ext",
	}
	for path, want := range cases {
		reason, ok := fileAllowed(path, nil)
		if ok {
			t.Fatalf("fileAllowed(%q) unexpectedly allowed", path)
		}
		if !strings.Contains(reason, want) {
			t.Errorf("fileAllowed(%q) reason %q does not mention %q", path, reason, want)
		}
	}
}

func TestFileAllowedEmptyPath(t *testing.T) {
	for _, p := range []string{"", "."} {
		if reason, ok := fileAllowed(p, nil); ok || reason != "not a file" {
			t.Errorf("fileAllowed(%q) = (%q, %v), want (\"not a file\", false)", p, reason, ok)
		}
	}
}

func TestFileAllowedHonoursExtraExtensions(t *testing.T) {
	extra := normalizeExtraExts([]string{"tpl", ".hbs"})
	for _, path := range []string{"page.tpl", "email.hbs", "DEEP/nested.TPL"} {
		if reason, ok := fileAllowed(path, extra); !ok {
			t.Errorf("fileAllowed(%q, extra) refused: %s", path, reason)
		}
	}
	// The escape hatch widens the allowlist; it must not re-enable a
	// denylisted credential file, which is refused before this runs.
	if _, blocked := blockedRepoFile("credentials.json"); !blocked {
		t.Error("credentials.json must stay denylisted regardless of --allow-file-ext")
	}
}

func TestNormalizeExtraExts(t *testing.T) {
	got := normalizeExtraExts([]string{"go", ".RS", "  py  ", "", "   ", "."})
	want := []string{".go", ".rs", ".py"}
	if len(got) != len(want) {
		t.Fatalf("normalizeExtraExts returned %d entries, want %d: %v", len(got), len(want), got)
	}
	for _, w := range want {
		if _, ok := got[w]; !ok {
			t.Errorf("normalizeExtraExts missing %q, got %v", w, got)
		}
	}
	if normalizeExtraExts(nil) != nil {
		t.Error("nil input should produce a nil map")
	}
	if normalizeExtraExts([]string{"", " ", "."}) != nil {
		t.Error("input with nothing usable should produce a nil map")
	}
}

func TestAllowedExtensionListIsSortedAndNonEmpty(t *testing.T) {
	list := allowedExtensionList()
	if list == "" {
		t.Fatal("allowedExtensionList is empty; the resource description would tell the model nothing")
	}
	parts := strings.Fields(list)
	if len(parts) != len(allowedFileExts) {
		t.Fatalf("allowedExtensionList has %d entries, allowlist has %d", len(parts), len(allowedFileExts))
	}
	for i := 1; i < len(parts); i++ {
		if parts[i-1] >= parts[i] {
			t.Fatalf("allowedExtensionList is not sorted at %q/%q", parts[i-1], parts[i])
		}
	}
}

// TestCredentialFilesFoundByAudit pins the eight paths a security audit
// found being served in full by the previous denylist-only policy. Each is
// a regression test for a real leak, not a hypothetical.
func TestCredentialFilesFoundByAudit(t *testing.T) {
	for _, path := range []string{
		".kube/config",
		"kubeconfig",
		"credentials.json",
		".pgpass",
		"infra/terraform.tfvars",
		".htpasswd",
		".yarnrc.yml",
		"deploy/deploy.ppk",
	} {
		_, denied := blockedRepoFile(path)
		_, allowed := fileAllowed(path, nil)
		if !denied && allowed {
			t.Errorf("%q is served: denylist=%v allowlist=%v", path, denied, allowed)
		}
	}
}

func TestBlockedRepoFileCredentialDirectories(t *testing.T) {
	for _, path := range []string{
		".ssh/id_ed25519", ".aws/config", ".gnupg/secring.gpg",
		".kube/config", ".gcloud/creds.db", ".docker/config.json",
		"nested/.kube/config",
	} {
		reason, blocked := blockedRepoFile(path)
		if !blocked {
			t.Errorf("blockedRepoFile(%q) allowed a credential directory", path)
			continue
		}
		if reason == "" {
			t.Errorf("blockedRepoFile(%q) blocked without a reason", path)
		}
	}
}

func TestBlockedRepoFileNamesWithAllowedExtensions(t *testing.T) {
	// These wear an extension the allowlist serves, so only the denylist
	// can refuse them — the reason the denylist is kept.
	for name, wantSubstr := range map[string]string{
		"credentials.json":   "credentials",
		"client_secret.json": "secret",
		"kubeconfig.yaml":    "kubeconfig",
		"kubeconfig.yml":     "kubeconfig",
		"secrets.json":       "secrets",
		".yarnrc.yml":        "yarnrc",
	} {
		reason, blocked := blockedRepoFile(name)
		if !blocked {
			t.Errorf("blockedRepoFile(%q) allowed a credential store", name)
			continue
		}
		if !strings.Contains(strings.ToLower(reason), wantSubstr) {
			t.Errorf("blockedRepoFile(%q) reason %q lacks %q", name, reason, wantSubstr)
		}
	}
}

func TestValidateCloneURL(t *testing.T) {
	ok := []string{
		"https://github.com/acme/api.git",
		"HTTPS://github.com/acme/api.git",
		"ssh://git@github.com/acme/api.git",
		"git://github.com/acme/api.git",
		"git@github.com:acme/api.git",
		"git@git.example.com:group/sub/api.git",
	}
	for _, u := range ok {
		if err := validateCloneURL(u); err != nil {
			t.Errorf("validateCloneURL(%q) rejected a valid url: %v", u, err)
		}
	}

	bad := map[string]string{
		"":                           "must not be empty",
		"   ":                        "must not be empty",
		"ext::sh -c 'id > /tmp/pwn'": "not permitted",
		"file:///etc/passwd":         "not permitted",
		"/local/path/repo":           "no scheme",
		"--upload-pack=/bin/sh":      "must not begin",
		"-oProxyCommand=id":          "must not begin",
		"ftp://example.com/repo.git": "not permitted",
		"user@hostnocolon":           "no scheme",
	}
	for u, wantSubstr := range bad {
		err := validateCloneURL(u)
		if err == nil {
			t.Errorf("validateCloneURL(%q) accepted an unsafe url", u)
			continue
		}
		if !strings.Contains(err.Error(), wantSubstr) {
			t.Errorf("validateCloneURL(%q) error %q lacks %q", u, err, wantSubstr)
		}
	}
}

func TestExportedFileAllowed(t *testing.T) {
	if !FileAllowed("main.go") {
		t.Error("expected main.go to be allowed")
	}
	if FileAllowed(".env") {
		t.Error("expected .env to be refused")
	}
}
