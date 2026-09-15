// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/diag"
)

// Unit tests for helpers that do not touch the MCP protocol. Carried over
// unchanged from the mark3labs-era test files during the official-SDK
// migration: these assert behaviour of corral's own code, so the SDK swap must
// not change what they check.

func TestAuditDefaultsAndFailures(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", filepath.Join(t.TempDir(), "state"))
	if got := DefaultAuditLogPath(); !strings.HasSuffix(got, filepath.Join("state", "corral", "mutations.log")) {
		t.Fatalf("XDG audit path = %q", got)
	}
	oldHome := auditUserHomeDir
	t.Cleanup(func() { auditUserHomeDir = oldHome })
	_ = os.Unsetenv("XDG_STATE_HOME")
	homeDir := filepath.Join(string(filepath.Separator), "home", "test")
	auditUserHomeDir = func() (string, error) { return homeDir, nil }
	if got := DefaultAuditLogPath(); got != filepath.Join(homeDir, ".local", "state", "corral", "mutations.log") {
		t.Fatalf("home audit path = %q", got)
	}
	auditUserHomeDir = func() (string, error) { return "", errors.New("no home") }
	if got := DefaultAuditLogPath(); !strings.HasSuffix(got, filepath.Join("corral", "mutations.log")) {
		t.Fatalf("fallback audit path = %q", got)
	}

	auditor := NewAuditor(filepath.Join(t.TempDir(), "audit.log"))
	if err := auditor.Write(AuditRecord{Args: map[string]any{"bad": make(chan int)}}); err == nil || !strings.Contains(err.Error(), "marshal") {
		t.Fatalf("expected marshal failure, got %v", err)
	}
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NewAuditor(filepath.Join(blocked, "audit.log")).Write(AuditRecord{Tool: "x"}); err == nil || !strings.Contains(err.Error(), "create audit dir") {
		t.Fatalf("expected mkdir failure, got %v", err)
	}
	dirPath := t.TempDir()
	if err := NewAuditor(dirPath).Write(AuditRecord{Tool: "x"}); err == nil || !strings.Contains(err.Error(), "open audit log") {
		t.Fatalf("expected open failure, got %v", err)
	}
}

func TestIndexInjectedFailuresAndBounds(t *testing.T) {
	oldAbs, oldStat, oldWalk, oldRel, oldMax := absIndex, statIndex, walkIndex, relIndex, maxIndexRepos
	t.Cleanup(func() {
		absIndex, statIndex, walkIndex, relIndex, maxIndexRepos = oldAbs, oldStat, oldWalk, oldRel, oldMax
	})
	absIndex = func(string) (string, error) { return "", errors.New("abs failed") }
	if _, err := Scan("x"); err == nil || !strings.Contains(err.Error(), "resolving root") {
		t.Fatalf("absolute root error = %v", err)
	}
	absIndex = filepath.Abs
	statIndex = func(string) (os.FileInfo, error) { return nil, errors.New("stat failed") }
	if _, err := Scan("x"); err == nil || !strings.Contains(err.Error(), "stat root") {
		t.Fatalf("stat root error = %v", err)
	}
	statIndex = os.Stat

	root := t.TempDir()
	dir := filepath.Join(root, "dir")
	file := filepath.Join(root, "file")
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var dirEntry, fileEntry os.DirEntry
	for _, entry := range entries {
		if entry.IsDir() {
			dirEntry = entry
		} else {
			fileEntry = entry
		}
	}
	walkIndex = func(base string, fn fs.WalkDirFunc) error {
		_ = fn(file, fileEntry, nil)
		_ = fn(dir, dirEntry, errors.New("dir unreadable"))
		_ = fn(file, nil, errors.New("file unreadable"))
		return nil
	}
	if _, err := Scan(root); err != nil {
		t.Fatal(err)
	}

	walkIndex = func(base string, fn fs.WalkDirFunc) error { _ = fn(dir, dirEntry, nil); return nil }
	relIndex = func(string, string) (string, error) { return "", errors.New("rel failed") }
	if _, err := Scan(root); err != nil {
		t.Fatal(err)
	}
	relIndex = filepath.Rel
	deep := filepath.Join(root, "a", "b", "c", "d", "e")
	walkIndex = func(base string, fn fs.WalkDirFunc) error { _ = fn(deep, dirEntry, nil); return nil }
	if _, err := Scan(root); err != nil {
		t.Fatal(err)
	}

	maxIndexRepos = 0
	walkIndex = func(base string, fn fs.WalkDirFunc) error {
		if got := fn(root, dirEntry, nil); got != fs.SkipAll {
			t.Fatalf("cap callback = %v", got)
		}
		return nil
	}
	idx, err := Scan(root)
	if err != nil || !idx.Truncated {
		t.Fatalf("truncated index = %+v, %v", idx, err)
	}
}

func TestStateInjectedIOFailures(t *testing.T) {
	oldRead, oldCreate, oldRename := readStateFile, createSyncTemp, renameSyncFile
	t.Cleanup(func() { readStateFile, createSyncTemp, renameSyncFile = oldRead, oldCreate, oldRename })
	readStateFile = func(string) ([]byte, error) { return nil, errors.New("read failed") }
	if state, ok := readState(t.TempDir()); ok || state != nil {
		t.Fatalf("read failure = %+v %v", state, ok)
	}
	readStateFile = oldRead
	dir := t.TempDir()
	// markStateSynced resolves the git directory before creating a temp file.
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o750); err != nil {
		t.Fatal(err)
	}
	createSyncTemp = func(string, string) (syncTempFile, error) { return nil, errors.New("create failed") }
	if err := markStateSynced(dir); err == nil || !strings.Contains(err.Error(), "create sync") {
		t.Fatalf("create error = %v", err)
	}
	createSyncTemp = func(string, string) (syncTempFile, error) {
		return &fakeSyncFile{name: filepath.Join(dir, "tmp"), writeErr: errors.New("write failed")}, nil
	}
	if err := markStateSynced(dir); err == nil || !strings.Contains(err.Error(), "write sync") {
		t.Fatalf("write error = %v", err)
	}
	createSyncTemp = func(string, string) (syncTempFile, error) {
		return &fakeSyncFile{name: filepath.Join(dir, "tmp"), closeErr: errors.New("close failed")}, nil
	}
	if err := markStateSynced(dir); err == nil || !strings.Contains(err.Error(), "close sync") {
		t.Fatalf("close error = %v", err)
	}
	createSyncTemp = oldCreate
	renameSyncFile = func(string, string) error { return errors.New("rename failed") }
	if err := markStateSynced(dir); err == nil || !strings.Contains(err.Error(), "replace sync") {
		t.Fatalf("rename error = %v", err)
	}
}

func TestSafePathInjectedErrorsAndCanonicalFallback(t *testing.T) {
	oldAbs, oldRel, oldEval := absSafePath, relSafePath, evalSafePath
	t.Cleanup(func() { absSafePath, relSafePath, evalSafePath = oldAbs, oldRel, oldEval })
	idx := &Index{Root: t.TempDir()}
	absSafePath = func(string) (string, error) { return "", errors.New("abs failed") }
	if _, err := idx.SafePath("x"); err == nil || !strings.Contains(err.Error(), "resolving path") {
		t.Fatalf("abs error = %v", err)
	}
	absSafePath = filepath.Abs
	relSafePath = func(string, string) (string, error) { return "", errors.New("rel failed") }
	if _, err := idx.SafePath("x"); err == nil || !strings.Contains(err.Error(), "escapes root") {
		t.Fatalf("rel error = %v", err)
	}
	evalSafePath = func(string) (string, error) { return "", errors.New("eval failed") }
	if got := canonicalizeExistingPrefix("/"); got != "/" {
		t.Fatalf("canonical root fallback = %q", got)
	}
}

func TestAuditWriteFailure(t *testing.T) {
	oldOpen := openAuditFile
	t.Cleanup(func() { openAuditFile = oldOpen })
	openAuditFile = func(string, int, os.FileMode) (auditFile, error) {
		return &fakeAuditWriter{writeErr: errors.New("write failed")}, nil
	}
	if err := NewAuditor(filepath.Join(t.TempDir(), "audit.log")).Write(AuditRecord{Tool: "x"}); err == nil || !strings.Contains(err.Error(), "write audit") {
		t.Fatalf("audit write error = %v", err)
	}
}

func TestBuildEntryTwoLevelAndMarshalStateFailure(t *testing.T) {
	root := t.TempDir()
	repo := makeFakeRepo(t, root, "", "go", "repo", "", "")
	entry := buildEntry(root, repo)
	if entry.Language != "go" || entry.Visibility != "" {
		t.Fatalf("two-level entry = %+v", entry)
	}
	oldMarshal := marshalSyncState
	t.Cleanup(func() { marshalSyncState = oldMarshal })
	marshalSyncState = func(any, string, string) ([]byte, error) { return nil, errors.New("marshal failed") }
	if err := markStateSynced(repo); err == nil || !strings.Contains(err.Error(), "marshal sync") {
		t.Fatalf("marshal state error = %v", err)
	}
}

func TestHasDirtyWorkingTreeDefault(t *testing.T) {
	if dirty, _ := hasDirtyWorkingTree(context.Background(), t.TempDir()); !dirty {
		t.Fatal("invalid repository must fail closed")
	}
	dir := t.TempDir()
	cmd := exec.Command("git", "init", dir) // #nosec G204 -- fixed executable with a test-owned destination
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if dirty, detail := hasDirtyWorkingTree(context.Background(), dir); dirty || detail != "" {
		t.Fatalf("clean tree = %v %q", dirty, detail)
	}
	if err := os.WriteFile(filepath.Join(dir, "new"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if dirty, detail := hasDirtyWorkingTree(context.Background(), dir); !dirty || detail == "" {
		t.Fatalf("dirty tree = %v %q", dirty, detail)
	}
}

func TestAuditorWritesJSONL(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "sub", "mutations.log")
	a := NewAuditor(logPath)
	if err := a.Write(AuditRecord{Tool: "t1", Target: "a", Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Write(AuditRecord{Tool: "t2", Target: "b", Result: "refused", Message: "why"}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(logPath) // #nosec G304 -- test-owned temporary path
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), string(body))
	}
	for i, line := range lines {
		var rec AuditRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("line %d not JSON: %v", i, err)
		}
		if rec.Timestamp == "" {
			t.Errorf("expected timestamp on line %d", i)
		}
	}
}

func TestDefaultAuditLogPathHonoursXDG(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)
	got := DefaultAuditLogPath()
	want := filepath.Join(tmp, "corral", "mutations.log")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// smoke test that the server registers the expected tool set based
// on flags. Uses a fresh MCP GetToolsRequest? mcp-go doesn't expose
// that publicly either, so we fall back to counting the registered
// tools via the constructor observing side-effects.

func TestExtractFilePath(t *testing.T) {
	cases := map[string]struct {
		want    string
		wantErr bool
	}{
		"corral://repo/o/n/file/main.go":       {want: "main.go"},
		"corral://repo/o/n/file/sub%2Fmain.go": {want: "sub/main.go"},
		"corral://repo/o/n/state":              {wantErr: true},
		"corral://repo/o/n/file/":              {wantErr: true},
	}
	for uri, tc := range cases {
		got, err := extractFilePath(uri)
		if tc.wantErr {
			if err == nil {
				t.Errorf("extractFilePath(%q) expected error, got %q", uri, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("extractFilePath(%q) unexpected error %v", uri, err)
		}
		if got != tc.want {
			t.Errorf("extractFilePath(%q) = %q, want %q", uri, got, tc.want)
		}
	}
}

func TestGuessMIME(t *testing.T) {
	cases := map[string]string{
		"foo.md":       mimeMarkdown,
		"foo.markdown": mimeMarkdown,
		"foo.json":     mimeJSON,
		"foo.go":       mimePlain,
		"NOEXT":        mimePlain,
	}
	for in, want := range cases {
		if got := guessMIME(in); got != want {
			t.Errorf("guessMIME(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFirstSegment(t *testing.T) {
	if firstSegment("a/b/c") != "a" {
		t.Error("expected first segment 'a'")
	}
	if firstSegment("solo") != "solo" {
		t.Error("expected solo to return itself")
	}
}

// writeFile mkdir-p's the parent and writes body. Test-only helper.

func TestParseOwnerFromURLReturnsSegments(t *testing.T) {
	cases := map[string][]string{
		"":                           nil,
		"https://github.com/o/r.git": {"o"},
		"https://gitlab.com/g/sub/r": {"g", "sub"},
		"https://git.example.com/parent/sub/team/r.git": {"parent", "sub", "team"},
		"git@github.com:o/r.git":                        {"o"},
		"git@gitea.corp:group/subgroup/r":               {"group", "subgroup"},
		"malformed":                                     nil,
	}
	for in, want := range cases {
		got := parseOwnerFromURL(in)
		if len(got) != len(want) {
			t.Errorf("parseOwnerFromURL(%q) = %v, want %v", in, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("parseOwnerFromURL(%q)[%d] = %q, want %q", in, i, got[i], want[i])
			}
		}
	}
}

func TestBlockedRepoFile(t *testing.T) {
	blocked := []string{
		".git/config", ".git/HEAD", "sub/.git/config", ".GIT/config",
		".env", ".env.local", ".ENV", ".envrc", ".netrc", "_netrc",
		".npmrc", ".pypirc", ".git-credentials", "credentials",
		"id_rsa", "id_ed25519", ".dockercfg", "terraform.tfstate",
		"secrets.yml", "secrets.yaml", "service-account.json",
		"a/b/tls.pem", "server.key", "store.p12", "store.jks", "vault.kdbx",
		".ssh/known_hosts", ".aws/credentials", ".gnupg/secring.gpg",
		"", ".",
	}
	for _, rel := range blocked {
		if reason, ok := blockedRepoFile(rel); !ok {
			t.Errorf("%q should be blocked", rel)
		} else if reason == "" {
			t.Errorf("%q blocked without a reason", rel)
		}
	}

	allowed := []string{
		"README.md", "main.go", "src/deep/x.txt", "Makefile",
		"docs/environment.md", // contains "environment" but is not .env
		"keyboard.go",         // ends in neither .key nor a blocked name
		"pemberton.txt",
		".github/workflows/ci.yml",
		"credentials.md", // documentation about credentials, not a store
	}
	for _, rel := range allowed {
		if _, ok := blockedRepoFile(rel); ok {
			t.Errorf("%q should be allowed", rel)
		}
	}
}

func TestCurrentBranchLogsOnFailure(t *testing.T) {
	// Diagnostics moved from the stdlib logger to internal/diag, which is
	// where verbosity is now decided; this failure is reported at warn, so
	// it is visible at the default level.
	buf := captureDiag(t, diag.LevelWarn)

	// Point at a definitely-not-a-git-repo path so rev-parse exits nonzero.
	got := currentBranch(context.Background(), "/dev/null")
	if got != "" {
		t.Errorf("expected empty branch on failure, got %q", got)
	}
	if !strings.Contains(buf.String(), "git rev-parse") {
		t.Errorf("expected log line to mention 'git rev-parse', got: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "/dev/null") {
		t.Errorf("expected log line to include repoPath, got: %q", buf.String())
	}
}

func TestSortedLangCountsOrdering(t *testing.T) {
	m := map[string]int{"go": 3, "rust": 3, "python": 2}
	out := sortedLangCounts(m)
	// Sorted by count desc, then alpha. go and rust both have 3 → go first.
	if out[0]["language"] != "go" || out[1]["language"] != "rust" || out[2]["language"] != "python" {
		t.Errorf("unexpected ordering: %+v", out)
	}
}

// TestToolAnnotationsOnTheWire asserts the annotations clients actually receive
// from tools/list.
//
// This matters more than it looks. mcp-go's zero-value Annotations serialise as
// readOnlyHint:false, destructiveHint:true — so before this was set, all five
// read-only tools advertised themselves to clients as destructive, and
// corral_delete_repo carried the *identical* annotation. Clients use these to
// decide whether to auto-approve a call, so corral was simultaneously paying a
// confirmation tax on a directory listing and giving no signal at all on the one
// tool that removes data.

func TestServerInstructionsDescribeLayoutAndMode(t *testing.T) {
	ro := serverInstructions(ServerOptions{Root: "/w"})
	for _, want := range []string{"Visibility", "Language", "corral_status_summary", "read-only"} {
		if !strings.Contains(ro, want) {
			t.Errorf("read-only instructions missing %q:\n%s", want, ro)
		}
	}
	if strings.Contains(ro, "corral_delete_repo") {
		t.Error("read-only instructions must not advertise the delete tool")
	}

	full := serverInstructions(ServerOptions{
		Root: "/w", EnableMutations: true, EnableDestructiveMutations: true,
	})
	if !strings.Contains(full, "corral_delete_repo") {
		t.Errorf("destructive-mode instructions should name the delete tool:\n%s", full)
	}
	if !strings.Contains(full, "audit") {
		t.Errorf("destructive-mode instructions should mention the audit log:\n%s", full)
	}
}

type fakeSyncFile struct {
	name     string
	writeErr error
	closeErr error
}

type fakeAuditWriter struct{ writeErr error }

func (f *fakeSyncFile) Write([]byte) (int, error) { return 0, f.writeErr }

func (f *fakeSyncFile) Close() error { return f.closeErr }

func (f *fakeSyncFile) Name() string { return f.name }

func (f *fakeAuditWriter) Write(p []byte) (int, error) { return len(p), f.writeErr }

func (f *fakeAuditWriter) Close() error { return nil }

// Credentials embedded in a clone URL must never reach the audit log or a tool
// response, in any of the forms a caller might pass.
func TestRedactCloneURLStripsSecrets(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"token in userinfo", "https://ghp_secret@github.com/o/r.git", "https://REDACTED@github.com/o/r.git"},
		{"user and password", "https://user:pass@github.com/o/r.git", "https://REDACTED@github.com/o/r.git"},
		{"query string dropped", "https://github.com/o/r.git?token=secret", "https://github.com/o/r.git"},
		{"fragment dropped", "https://github.com/o/r.git#secret", "https://github.com/o/r.git"},
		{"scp form", "git@github.com:o/r.git", "REDACTED@github.com:o/r.git"},
		{"no credentials", "https://github.com/o/r.git", "https://github.com/o/r.git"},
		{"plain path", "/local/path/r", "/local/path/r"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactCloneURL(tc.in)
			if got != tc.want {
				t.Errorf("redactCloneURL(%q) = %q, want %q", tc.in, got, tc.want)
			}
			for _, secret := range []string{"ghp_secret", "pass", "token=secret"} {
				if strings.Contains(got, secret) {
					t.Errorf("redaction leaked %q in %q", secret, got)
				}
			}
		})
	}
}

func TestMutationIDsAreUnique(t *testing.T) {
	seen := make(map[string]bool, 100)
	for range 100 {
		id := mutationID()
		if seen[id] {
			t.Fatalf("mutationID repeated %q — audit records would collide", id)
		}
		seen[id] = true
	}
}
