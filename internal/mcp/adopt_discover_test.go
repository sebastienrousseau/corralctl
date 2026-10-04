// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/discover"
	"github.com/sebastienrousseau/corralctl/internal/engine"
	"github.com/sebastienrousseau/corralctl/internal/forge"
	"github.com/sebastienrousseau/corralctl/internal/github"
)

type dummyMCPForge struct{}

func (d *dummyMCPForge) Name() string    { return "dummy" }
func (d *dummyMCPForge) Hosts() []string { return []string{"dummy.com"} }
func (d *dummyMCPForge) List(_ context.Context, _ string, _ forge.Options) ([]forge.Repo, error) {
	return nil, nil
}

type dummyMCPTarget struct {
	dummyMCPForge
}

func (d *dummyMCPTarget) Whoami(_ context.Context, _ forge.TargetOptions) (string, error) {
	return "test", nil
}

func (d *dummyMCPTarget) EnsureRepo(_ context.Context, _ string, _ forge.RepoSpec, _ forge.TargetOptions) (forge.Remote, error) {
	return forge.Remote{HTTPSURL: "https://dummy.com/repo.git"}, nil
}

func (d *dummyMCPTarget) PushAuth(_ forge.TargetOptions) (string, string) {
	return "user", "pass"
}

func TestDiscoverReposTool(t *testing.T) {
	base := t.TempDir()
	h := newHarness(t, ServerOptions{Root: base})
	origDiscover := discoverReposFunc
	defer func() { discoverReposFunc = origDiscover }()

	// Unscannable root
	hUnscannable := newHarness(t, ServerOptions{Root: filepath.Join(t.TempDir(), "does-not-exist")})
	_, isErr := hUnscannable.callTool("corral_discover_repos", map[string]any{})
	if !isErr {
		t.Fatal("expected error on unscannable root")
	}

	// Invalid baseDir escaping Root
	_, isErr = h.callTool("corral_discover_repos", map[string]any{"base_dir": "../outside"})
	if !isErr {
		t.Fatal("expected error for escaping base_dir")
	}

	// Discovery error
	discoverReposFunc = func(ctx context.Context, opts discover.Options) ([]discover.Candidate, error) {
		return nil, errors.New("discovery failure")
	}
	_, isErr = h.callTool("corral_discover_repos", map[string]any{})
	if !isErr {
		t.Fatal("expected error on discovery failure")
	}

	// Discovery success
	discoverReposFunc = func(ctx context.Context, opts discover.Options) ([]discover.Candidate, error) {
		return []discover.Candidate{{Name: "repo1", Path: filepath.Join(opts.BaseDir, "repo1")}}, nil
	}
	res, isErr := h.callTool("corral_discover_repos", map[string]any{"base_dir": base})
	if isErr {
		t.Fatalf("unexpected discovery error: %s", res)
	}
}

func TestAdoptRepoTool(t *testing.T) {
	base := t.TempDir()
	localRepo := filepath.Join(base, "repo1")
	h, auditPath := mutationHarness(t, base, false)

	origExec := adoptExecFunc
	origResolve := adoptForgeResolve
	origAsTarget := adoptForgeAsTarget
	origToken := adoptForgeToken
	defer func() {
		adoptExecFunc = origExec
		adoptForgeResolve = origResolve
		adoptForgeAsTarget = origAsTarget
		adoptForgeToken = origToken
	}()

	// Unscannable root
	hUnscannable, _ := mutationHarness(t, filepath.Join(t.TempDir(), "does-not-exist"), false)
	_, isErr := hUnscannable.callTool("corral_adopt_repo", map[string]any{
		"repo_name":  "r",
		"local_path": "repo1",
	})
	if !isErr {
		t.Fatal("expected error on unscannable root")
	}

	// Empty repo_name
	_, isErr = h.callTool("corral_adopt_repo", map[string]any{"repo_name": "", "local_path": localRepo})
	if !isErr {
		t.Fatal("expected error for empty repo_name")
	}

	// LocalPath escapes root
	_, isErr = h.callTool("corral_adopt_repo", map[string]any{"repo_name": "r", "local_path": "../outside"})
	if !isErr {
		t.Fatal("expected error for escaping local_path")
	}

	// NewTargetDir escapes root
	_, isErr = h.callTool("corral_adopt_repo", map[string]any{
		"repo_name":      "r",
		"local_path":     localRepo,
		"relocate_local": true,
		"new_target_dir": "../outside",
	})
	if !isErr {
		t.Fatal("expected error for escaping new_target_dir")
	}

	// Forge resolve error
	adoptForgeResolve = func(name, baseURL string) (forge.Forge, error) {
		return nil, errors.New("forge error")
	}
	_, isErr = h.callTool("corral_adopt_repo", map[string]any{
		"repo_name":  "r",
		"local_path": localRepo,
		"forge":      "bad",
	})
	if !isErr {
		t.Fatal("expected error for bad forge")
	}

	// Forge not a target
	adoptForgeResolve = func(name, baseURL string) (forge.Forge, error) {
		return &dummyMCPForge{}, nil
	}
	adoptForgeAsTarget = func(f forge.Forge) (forge.Target, bool) {
		return nil, false
	}
	_, isErr = h.callTool("corral_adopt_repo", map[string]any{
		"repo_name":  "r",
		"local_path": localRepo,
		"forge":      "notarget",
	})
	if !isErr {
		t.Fatal("expected error for non-target forge")
	}

	// Adopt execution error
	adoptForgeAsTarget = func(f forge.Forge) (forge.Target, bool) {
		return &dummyMCPTarget{}, true
	}
	adoptForgeToken = func(ctx context.Context, name string, mode github.AuthMode) string {
		return "token"
	}
	adoptExecFunc = func(ctx context.Context, opts engine.AdoptOptions) (engine.AdoptResult, error) {
		return engine.AdoptResult{}, errors.New("execution error")
	}
	_, isErr = h.callTool("corral_adopt_repo", map[string]any{
		"repo_name":  "r",
		"local_path": localRepo,
		"forge":      "github",
	})
	if !isErr {
		t.Fatal("expected error for adopt execution failure")
	}

	// Successful adoption without relocate
	adoptExecFunc = func(ctx context.Context, opts engine.AdoptOptions) (engine.AdoptResult, error) {
		return engine.AdoptResult{
			RepoName:  opts.RepoName,
			FinalPath: opts.LocalPath,
			Action:    "ADOPTED",
		}, nil
	}
	res, isErr := h.callTool("corral_adopt_repo", map[string]any{
		"repo_name":  "r",
		"local_path": localRepo,
		"forge":      "github",
	})
	if isErr {
		t.Fatalf("unexpected adopt error: %s", res)
	}

	// Successful adoption with relocate
	res, isErr = h.callTool("corral_adopt_repo", map[string]any{
		"repo_name":      "r",
		"local_path":     localRepo,
		"relocate_local": true,
		"new_target_dir": filepath.Join(base, "relocated"),
		"forge":          "github",
	})
	if isErr {
		t.Fatalf("unexpected adopt error with relocate: %s", res)
	}

	// Begin mutation failure
	t.Run("audit_intent_failure", func(t *testing.T) {
		failAuditAfter(t, 1)
		_, isErr := h.callTool("corral_adopt_repo", map[string]any{
			"repo_name":  "r",
			"local_path": localRepo,
		})
		if !isErr {
			t.Fatal("expected error on beginMutation failure")
		}
	})

	// Complete mutation failure
	t.Run("audit_completion_failure", func(t *testing.T) {
		failAuditAfter(t, 2)
		_, isErr := h.callTool("corral_adopt_repo", map[string]any{
			"repo_name":  "r",
			"local_path": localRepo,
		})
		if !isErr {
			t.Fatal("expected error on completeMutation failure")
		}
	})

	// Verify audit log has entries
	recs := auditRecords(t, auditPath)
	if len(recs) == 0 {
		t.Fatal("expected audit records")
	}
}
