// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package adopt

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/forge"
	"github.com/sebastienrousseau/corralctl/internal/git"
)

type mockAdoptTarget struct {
	ensureFn func(ctx context.Context, owner string, spec forge.RepoSpec, opts forge.TargetOptions) (forge.Remote, error)
	pushAuth func(opts forge.TargetOptions) (string, string)
}

func (m *mockAdoptTarget) Whoami(_ context.Context, _ forge.TargetOptions) (string, error) {
	return "testuser", nil
}

func (m *mockAdoptTarget) EnsureRepo(ctx context.Context, owner string, spec forge.RepoSpec, opts forge.TargetOptions) (forge.Remote, error) {
	if m.ensureFn != nil {
		return m.ensureFn(ctx, owner, spec, opts)
	}
	return forge.Remote{
		HTTPSURL: "https://example.com/" + owner + "/" + spec.Name + ".git",
		SSHURL:   "git@example.com:" + owner + "/" + spec.Name + ".git",
	}, nil
}

func (m *mockAdoptTarget) PushAuth(opts forge.TargetOptions) (string, string) {
	if m.pushAuth != nil {
		return m.pushAuth(opts)
	}
	return "oauth2", opts.Token
}

func TestExecuteAdoptionValidation(t *testing.T) {
	ctx := context.Background()

	// Empty repo name
	if _, err := Execute(ctx, Options{LocalPath: "/tmp/repo"}); err == nil {
		t.Fatal("expected error for empty repo name")
	}

	// Empty local path
	if _, err := Execute(ctx, Options{RepoName: "repo"}); err == nil {
		t.Fatal("expected error for empty local path")
	}
}

func TestExecuteAdoptionRelocation(t *testing.T) {
	ctx := context.Background()
	origMkdir := adoptMkdirAll
	origRename := adoptRename
	defer func() {
		adoptMkdirAll = origMkdir
		adoptRename = origRename
	}()

	// Dry run relocation
	res, err := Execute(ctx, Options{
		RepoName:      "testrepo",
		LocalPath:     "/old/path",
		NewTargetDir:  "/new/path",
		RelocateLocal: true,
		DryRun:        true,
	})
	if err != nil || res.FinalPath != filepath.Clean("/new/path") {
		t.Fatalf("expected dry-run final path /new/path, got %+v, err: %v", res, err)
	}

	// Mkdir failure
	adoptMkdirAll = func(path string, perm os.FileMode) error {
		return errors.New("mkdir failed")
	}
	_, err = Execute(ctx, Options{
		RepoName:      "testrepo",
		LocalPath:     "/old/path",
		NewTargetDir:  "/new/path",
		RelocateLocal: true,
	})
	if err == nil {
		t.Fatal("expected mkdir error")
	}

	// Rename failure
	adoptMkdirAll = func(path string, perm os.FileMode) error { return nil }
	adoptRename = func(oldpath, newpath string) error {
		return errors.New("rename failed")
	}
	_, err = Execute(ctx, Options{
		RepoName:      "testrepo",
		LocalPath:     "/old/path",
		NewTargetDir:  "/new/path",
		RelocateLocal: true,
	})
	if err == nil {
		t.Fatal("expected rename error")
	}

	// Successful relocation without remote
	adoptRename = func(oldpath, newpath string) error { return nil }
	res, err = Execute(ctx, Options{
		RepoName:      "testrepo",
		LocalPath:     "/old/path",
		NewTargetDir:  "/new/path",
		RelocateLocal: true,
	})
	if err != nil || res.Action != "RELOCATED" || res.FinalPath != filepath.Clean("/new/path") {
		t.Fatalf("expected RELOCATED, got %+v, err: %v", res, err)
	}

	// EXDEV with copyDir failure
	origCopyDir := adoptCopyDir
	origRemoveAll := adoptRemoveAll
	defer func() {
		adoptCopyDir = origCopyDir
		adoptRemoveAll = origRemoveAll
	}()
	adoptRename = func(oldpath, newpath string) error { return syscall.EXDEV }
	adoptCopyDir = func(src, dst string) error { return errors.New("copy failed") }
	_, err = Execute(ctx, Options{
		RepoName:      "testrepo",
		LocalPath:     "/old/path",
		NewTargetDir:  "/new/path",
		RelocateLocal: true,
	})
	if err == nil {
		t.Fatal("expected EXDEV copy error")
	}

	// EXDEV with copyDir success
	removed := false
	adoptCopyDir = func(src, dst string) error { return nil }
	adoptRemoveAll = func(path string) error {
		removed = true
		return nil
	}
	res, err = Execute(ctx, Options{
		RepoName:      "testrepo",
		LocalPath:     "/old/path",
		NewTargetDir:  "/new/path",
		RelocateLocal: true,
	})
	if err != nil || !removed || res.FinalPath != filepath.Clean("/new/path") {
		t.Fatalf("expected EXDEV success, got %+v, removed=%v, err: %v", res, removed, err)
	}
}

func TestExecuteAdoptionForgeProvisioning(t *testing.T) {
	ctx := context.Background()

	mockTgt := &mockAdoptTarget{}

	// Dry run with forge
	res, err := Execute(ctx, Options{
		RepoName:     "dryrepo",
		LocalPath:    "/tmp/dryrepo",
		Forge:        mockTgt,
		ForgeOptions: forge.TargetOptions{BaseURL: "git.example.com"},
		Owner:        "org",
		DryRun:       true,
	})
	if err != nil || res.Action != "DRY-RUN" {
		t.Fatalf("expected DRY-RUN, got %+v, err: %v", res, err)
	}

	// EnsureRepo failure
	mockTgt.ensureFn = func(ctx context.Context, owner string, spec forge.RepoSpec, opts forge.TargetOptions) (forge.Remote, error) {
		return forge.Remote{}, errors.New("remote error")
	}
	_, err = Execute(ctx, Options{
		RepoName:  "failrepo",
		LocalPath: "/tmp/failrepo",
		Forge:     mockTgt,
	})
	if err == nil {
		t.Fatal("expected EnsureRepo error")
	}

	// Add remote failure
	mockTgt.ensureFn = nil
	origAddRemote := adoptAddRemote
	defer func() { adoptAddRemote = origAddRemote }()
	adoptAddRemote = func(ctx context.Context, targetDir, name, url string) error {
		return errors.New("remote add failure")
	}
	_, err = Execute(ctx, Options{
		RepoName:  "remotefail",
		LocalPath: "/tmp/remotefail",
		Forge:     mockTgt,
	})
	if err == nil {
		t.Fatal("expected remote add error")
	}
}

func TestExecuteAdoptionPushFlow(t *testing.T) {
	ctx := context.Background()
	mockTgt := &mockAdoptTarget{}

	origAddRemote := adoptAddRemote
	origIsEmpty := adoptIsEmpty
	origBranch := adoptCurrentBranch
	origPush := adoptPushUpstream
	defer func() {
		adoptAddRemote = origAddRemote
		adoptIsEmpty = origIsEmpty
		adoptCurrentBranch = origBranch
		adoptPushUpstream = origPush
	}()

	adoptAddRemote = func(ctx context.Context, targetDir, name, url string) error { return nil }

	// Empty repository skips push
	adoptIsEmpty = func(ctx context.Context, dir string) bool { return true }
	res, err := Execute(ctx, Options{
		RepoName:  "emptyrepo",
		LocalPath: "/tmp/emptyrepo",
		Forge:     mockTgt,
	})
	if err != nil || res.Action != "ADOPTED" {
		t.Fatalf("expected ADOPTED for empty repo, got %+v, err: %v", res, err)
	}

	// Non-empty repository: branch error falls back to main and push fails
	adoptIsEmpty = func(ctx context.Context, dir string) bool { return false }
	adoptCurrentBranch = func(ctx context.Context, dir string) (string, error) {
		return "", errors.New("no branch")
	}
	adoptPushUpstream = func(ctx context.Context, dir, remote, branch, pushURL string, cred *git.PushCredential) error {
		return errors.New("push rejected")
	}
	_, err = Execute(ctx, Options{
		RepoName:  "pushfail",
		LocalPath: "/tmp/pushfail",
		Forge:     mockTgt,
	})
	if err == nil {
		t.Fatal("expected push error")
	}

	// Successful HTTPS push with credentials
	adoptPushUpstream = func(ctx context.Context, dir, remote, branch, pushURL string, cred *git.PushCredential) error {
		if cred == nil || cred.Secret != "secret-token" {
			t.Fatalf("expected credential with secret-token, got %+v", cred)
		}
		return nil
	}
	adoptCurrentBranch = func(ctx context.Context, dir string) (string, error) { return "trunk", nil }
	res, err = Execute(ctx, Options{
		RepoName:     "successepoch",
		LocalPath:    "/tmp/successepoch",
		Forge:        mockTgt,
		ForgeOptions: forge.TargetOptions{Token: "secret-token"},
		Protocol:     "https",
	})
	if err != nil || res.Action != "ADOPTED" {
		t.Fatalf("expected ADOPTED, got %+v, err: %v", res, err)
	}

	// Successful SSH push (SSH URL selected, nil credential passed)
	adoptPushUpstream = func(ctx context.Context, dir, remote, branch, pushURL string, cred *git.PushCredential) error {
		if cred != nil {
			t.Fatalf("expected nil credential for ssh push, got %+v", cred)
		}
		if pushURL != "git@example.com:testuser/sshsuccess.git" {
			t.Fatalf("expected ssh url, got %s", pushURL)
		}
		return nil
	}
	res, err = Execute(ctx, Options{
		RepoName:  "sshsuccess",
		LocalPath: "/tmp/sshsuccess",
		Forge:     mockTgt,
		Protocol:  "ssh",
		Owner:     "testuser",
	})
	if err != nil || res.Action != "ADOPTED" || res.RemoteURL != "git@example.com:testuser/sshsuccess.git" {
		t.Fatalf("expected ADOPTED with SSH URL, got %+v, err: %v", res, err)
	}
}

func TestCopyDirRecursive(t *testing.T) {
	src := t.TempDir()
	dst := filepath.Join(t.TempDir(), "target")

	subDir := filepath.Join(src, "sub")
	if err := os.MkdirAll(subDir, 0o750); err != nil {
		t.Fatal(err)
	}
	testFile := filepath.Join(subDir, "hello.txt")
	if err := os.WriteFile(testFile, []byte("world"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := copyDirRecursive(src, dst); err != nil {
		t.Fatalf("copyDirRecursive failed: %v", err)
	}

	if err := copyDirRecursive("/nonexistent/source", dst); err == nil {
		t.Fatal("expected error for nonexistent source")
	}

	// Read error
	origReadFile := adoptReadFile
	defer func() { adoptReadFile = origReadFile }()
	adoptReadFile = func(name string) ([]byte, error) {
		return nil, errors.New("read error")
	}
	if err := copyDirRecursive(src, filepath.Join(t.TempDir(), "target2")); err == nil {
		t.Fatal("expected error on read failure")
	}
	adoptReadFile = origReadFile

	copiedData, err := os.ReadFile(filepath.Join(dst, "sub", "hello.txt")) // #nosec G304 -- test file path constructed from TempDir
	if err != nil || string(copiedData) != "world" {
		t.Fatalf("copied content mismatch: %v, %s", err, string(copiedData))
	}
}
