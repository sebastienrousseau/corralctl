// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"context"
	"errors"
	"testing"

	"github.com/sebastienrousseau/corralctl/internal/discover"
	"github.com/sebastienrousseau/corralctl/internal/engine"
	"github.com/sebastienrousseau/corralctl/internal/forge"
	"github.com/sebastienrousseau/corralctl/internal/github"
)

type dummyForge struct{}

func (d *dummyForge) Name() string    { return "dummy" }
func (d *dummyForge) Hosts() []string { return []string{"dummy.com"} }
func (d *dummyForge) List(_ context.Context, _ string, _ forge.Options) ([]forge.Repo, error) {
	return nil, nil
}

type dummyTargetForge struct {
	dummyForge
}

func (d *dummyTargetForge) Whoami(_ context.Context, _ forge.TargetOptions) (string, error) {
	return "test", nil
}

func (d *dummyTargetForge) EnsureRepo(_ context.Context, _ string, _ forge.RepoSpec, _ forge.TargetOptions) (forge.Remote, error) {
	return forge.Remote{HTTPSURL: "https://dummy.com/repo.git"}, nil
}

func (d *dummyTargetForge) PushAuth(_ forge.TargetOptions) (string, string) {
	return "user", "pass"
}

func resetAdoptFlags(t *testing.T) {
	origInteractive := adoptInteractive
	origOutput := adoptOutput
	origForge := adoptForge
	origForgeURL := adoptForgeURL
	origOwner := adoptOwner
	origVisibility := adoptVisibility
	origRelocate := adoptRelocate
	origTargetDir := adoptTargetDir
	origProtocol := adoptProtocol
	origUntrackedOnly := adoptUntrackedOnly
	origFinderTags := adoptFinderTags
	origTagOnly := adoptTagOnly
	origCollection := adoptCollection
	origMaxDepth := adoptMaxDepth
	origDryRun := dryRun

	adoptInteractive = false
	adoptOutput = "text"
	adoptForge = ""
	adoptForgeURL = ""
	adoptOwner = ""
	adoptVisibility = "private"
	adoptRelocate = false
	adoptTargetDir = ""
	adoptProtocol = "https"
	adoptUntrackedOnly = false
	adoptFinderTags = false
	adoptTagOnly = false
	adoptCollection = ""
	adoptMaxDepth = 0
	dryRun = false

	t.Cleanup(func() {
		adoptInteractive = origInteractive
		adoptOutput = origOutput
		adoptForge = origForge
		adoptForgeURL = origForgeURL
		adoptOwner = origOwner
		adoptVisibility = origVisibility
		adoptRelocate = origRelocate
		adoptTargetDir = origTargetDir
		adoptProtocol = origProtocol
		adoptUntrackedOnly = origUntrackedOnly
		adoptFinderTags = origFinderTags
		adoptTagOnly = origTagOnly
		adoptCollection = origCollection
		adoptMaxDepth = origMaxDepth
		dryRun = origDryRun
	})
}

func TestAdoptPreRunE(t *testing.T) {
	resetAdoptFlags(t)
	tests := []struct {
		proto   string
		vis     string
		output  string
		wantErr bool
	}{
		{"https", "private", "text", false},
		{"ssh", "public", "json", false},
		{"ftp", "private", "text", true},
		{"https", "internal", "text", true},
		{"https", "private", "yaml", true},
	}

	for _, tt := range tests {
		adoptProtocol = tt.proto
		adoptVisibility = tt.vis
		adoptOutput = tt.output
		err := adoptCmd.PreRunE(adoptCmd, nil)
		if (err != nil) != tt.wantErr {
			t.Errorf("proto=%s, vis=%s, output=%s: got error %v, wantErr %v", tt.proto, tt.vis, tt.output, err, tt.wantErr)
		}
	}
}

func TestAdoptRunEDiscoveryFlows(t *testing.T) {
	resetAdoptFlags(t)
	origDiscover := adoptDiscoverRun
	origAbs := adoptFilepathAbs
	origExec := adoptExecute
	defer func() {
		adoptDiscoverRun = origDiscover
		adoptExecute = origExec
		adoptFilepathAbs = origAbs
	}()

	// Abs failure
	adoptFilepathAbs = func(path string) (string, error) {
		return "", errors.New("abs failure")
	}
	if err := adoptCmd.RunE(adoptCmd, []string{"/tmp/test"}); err == nil {
		t.Fatal("expected abs error")
	}
	adoptFilepathAbs = origAbs

	// Discovery failure
	adoptDiscoverRun = func(ctx context.Context, opts discover.Options) ([]discover.Candidate, error) {
		return nil, errors.New("disk fault")
	}
	adoptProtocol = "https"
	adoptVisibility = "private"
	adoptOutput = "text"
	if err := adoptCmd.RunE(adoptCmd, []string{"/tmp/test"}); err == nil {
		t.Fatal("expected discovery error")
	}

	// Zero candidates
	adoptDiscoverRun = func(ctx context.Context, opts discover.Options) ([]discover.Candidate, error) {
		return nil, nil
	}
	if err := adoptCmd.RunE(adoptCmd, nil); err != nil {
		t.Fatalf("expected nil on zero candidates, got %v", err)
	}
}

func TestAdoptRunEForgeValidation(t *testing.T) {
	resetAdoptFlags(t)
	origDiscover := adoptDiscoverRun
	origResolve := adoptResolveForge
	origAsTarget := adoptAsTarget
	origToken := adoptForgeToken
	defer func() {
		adoptDiscoverRun = origDiscover
		adoptResolveForge = origResolve
		adoptAsTarget = origAsTarget
		adoptForgeToken = origToken
	}()

	candidates := []discover.Candidate{{Name: "repo1", Path: "/tmp/repo1"}}
	adoptDiscoverRun = func(ctx context.Context, opts discover.Options) ([]discover.Candidate, error) {
		return candidates, nil
	}

	// Forge resolve error
	adoptForge = "invalidforge"
	adoptResolveForge = func(name, baseURL string) (forge.Forge, error) {
		return nil, errors.New("unknown forge")
	}
	if err := adoptCmd.RunE(adoptCmd, nil); err == nil {
		t.Fatal("expected forge resolve error")
	}

	// Forge not a target
	adoptResolveForge = func(name, baseURL string) (forge.Forge, error) {
		return &dummyForge{}, nil
	}
	adoptAsTarget = func(f forge.Forge) (forge.Target, bool) {
		return nil, false
	}
	if err := adoptCmd.RunE(adoptCmd, nil); err == nil {
		t.Fatal("expected forge not a target error")
	}

	// Missing token when not dry run
	adoptAsTarget = func(f forge.Forge) (forge.Target, bool) {
		return &dummyTargetForge{}, true
	}
	adoptForgeToken = func(ctx context.Context, name string, mode github.AuthMode) string {
		return ""
	}
	dryRun = false
	if err := adoptCmd.RunE(adoptCmd, nil); err == nil {
		t.Fatal("expected missing token error")
	}
}

func TestAdoptRunEExecutionAndOutputs(t *testing.T) {
	resetAdoptFlags(t)
	origDiscover := adoptDiscoverRun
	origResolve := adoptResolveForge
	origAsTarget := adoptAsTarget
	origToken := adoptForgeToken
	origExec := adoptExecute
	defer func() {
		adoptDiscoverRun = origDiscover
		adoptResolveForge = origResolve
		adoptAsTarget = origAsTarget
		adoptForgeToken = origToken
		adoptExecute = origExec
	}()

	candidates := []discover.Candidate{
		{Name: "repo1", Path: "/tmp/repo1"},
		{Name: "repo2", Path: "/tmp/repo2"},
	}
	adoptDiscoverRun = func(ctx context.Context, opts discover.Options) ([]discover.Candidate, error) {
		return candidates, nil
	}
	adoptForge = "github"
	adoptResolveForge = func(name, baseURL string) (forge.Forge, error) {
		return &dummyTargetForge{}, nil
	}
	adoptAsTarget = func(f forge.Forge) (forge.Target, bool) {
		return &dummyTargetForge{}, true
	}
	adoptForgeToken = func(ctx context.Context, name string, mode github.AuthMode) string {
		return "dummytoken"
	}

	adoptExecute = func(ctx context.Context, opts engine.AdoptOptions) (engine.AdoptResult, error) {
		if opts.RepoName == "repo1" {
			return engine.AdoptResult{
				RepoName:  "repo1",
				FinalPath: opts.NewTargetDir,
				Action:    "ADOPTED",
				RemoteURL: "https://dummy.com/repo1.git",
			}, nil
		}
		return engine.AdoptResult{}, errors.New("execution failure")
	}

	// Text output with relocation
	adoptRelocate = true
	adoptTargetDir = "/workspace"
	adoptOutput = "text"
	dryRun = true
	if err := adoptCmd.RunE(adoptCmd, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// NDJSON output
	adoptOutput = "ndjson"
	adoptRelocate = false
	if err := adoptCmd.RunE(adoptCmd, nil); err != nil {
		t.Fatalf("unexpected ndjson error: %v", err)
	}

	// JSON output
	adoptOutput = "json"
	if err := adoptCmd.RunE(adoptCmd, nil); err != nil {
		t.Fatalf("unexpected json error: %v", err)
	}
}

func TestAdoptRunEInteractiveWizard(t *testing.T) {
	resetAdoptFlags(t)
	origDiscover := adoptDiscoverRun
	origWizard := adoptWizardRun
	origExec := adoptExecute
	defer func() {
		adoptDiscoverRun = origDiscover
		adoptWizardRun = origWizard
		adoptExecute = origExec
	}()

	candidates := []discover.Candidate{{Name: "repo1", Path: "/tmp/repo1"}}
	adoptDiscoverRun = func(ctx context.Context, opts discover.Options) ([]discover.Candidate, error) {
		return candidates, nil
	}
	adoptInteractive = true
	adoptOutput = "text"
	adoptForge = ""

	// Wizard error
	adoptWizardRun = func(ctx context.Context, c []discover.Candidate) ([]discover.Candidate, bool, error) {
		return nil, false, errors.New("wizard error")
	}
	if err := adoptCmd.RunE(adoptCmd, nil); err == nil {
		t.Fatal("expected wizard error")
	}

	// Wizard canceled
	adoptWizardRun = func(ctx context.Context, c []discover.Candidate) ([]discover.Candidate, bool, error) {
		return nil, false, nil
	}
	if err := adoptCmd.RunE(adoptCmd, nil); err != nil {
		t.Fatalf("expected nil on canceled wizard, got %v", err)
	}

	// Wizard empty selection
	adoptWizardRun = func(ctx context.Context, c []discover.Candidate) ([]discover.Candidate, bool, error) {
		return []discover.Candidate{}, true, nil
	}
	if err := adoptCmd.RunE(adoptCmd, nil); err != nil {
		t.Fatalf("expected nil on empty wizard selection, got %v", err)
	}

	// Wizard success
	adoptWizardRun = func(ctx context.Context, c []discover.Candidate) ([]discover.Candidate, bool, error) {
		return c, true, nil
	}
	adoptExecute = func(ctx context.Context, opts engine.AdoptOptions) (engine.AdoptResult, error) {
		return engine.AdoptResult{Action: "ADOPTED", RepoName: opts.RepoName}, nil
	}
	adoptOutput = "text"
	if err := adoptCmd.RunE(adoptCmd, nil); err != nil {
		t.Fatalf("unexpected wizard success error: %v", err)
	}
}

func TestAdoptFinderTagsAndTagOnly(t *testing.T) {
	resetAdoptFlags(t)
	origDiscover := adoptDiscoverRun
	origTag := adoptApplyCandidateTags
	defer func() {
		adoptDiscoverRun = origDiscover
		adoptApplyCandidateTags = origTag
	}()

	candidates := []discover.Candidate{
		{Name: "untracked", Path: "/tmp/untracked", HasRemote: false},
		{Name: "foreign", Path: "/tmp/foreign", HasRemote: true, RemoteURL: "https://github.com/other/foreign.git"},
		{Name: "mine", Path: "/tmp/mine", HasRemote: true, RemoteURL: "https://github.com/myuser/mine.git"},
	}
	adoptDiscoverRun = func(ctx context.Context, opts discover.Options) ([]discover.Candidate, error) {
		return candidates, nil
	}

	tagged := make(map[string]bool)
	adoptApplyCandidateTags = func(path string, isNew bool, ecosystem string) error {
		tagged[path] = isNew
		return nil
	}

	adoptOwner = "myuser"
	adoptFinderTags = true
	adoptTagOnly = true

	if err := adoptCmd.RunE(adoptCmd, nil); err != nil {
		t.Fatalf("unexpected error with tag-only: %v", err)
	}

	if !tagged["/tmp/untracked"] {
		t.Errorf("expected /tmp/untracked to be flagged isNew=true")
	}
	if !tagged["/tmp/foreign"] {
		t.Errorf("expected /tmp/foreign to be flagged isNew=true (foreign owner)")
	}
	if tagged["/tmp/mine"] {
		t.Errorf("expected /tmp/mine to be flagged isNew=false (owned by myuser)")
	}
}

func TestAdoptCollectionAndRelocate(t *testing.T) {
	resetAdoptFlags(t)
	origDiscover := adoptDiscoverRun
	origExecute := adoptExecute
	defer func() {
		adoptDiscoverRun = origDiscover
		adoptExecute = origExecute
	}()

	candidates := []discover.Candidate{
		{Name: "my-service", Path: "/tmp/legacy/my-service", DetectedLang: "Go"},
	}
	adoptDiscoverRun = func(ctx context.Context, opts discover.Options) ([]discover.Candidate, error) {
		return candidates, nil
	}

	var capturedOpts engine.AdoptOptions
	adoptExecute = func(ctx context.Context, opts engine.AdoptOptions) (engine.AdoptResult, error) {
		capturedOpts = opts
		return engine.AdoptResult{Action: "ADOPTED", RepoName: opts.RepoName, FinalPath: opts.NewTargetDir}, nil
	}

	// 1. With explicit collection: Forks
	adoptRelocate = true
	adoptTargetDir = "/target"
	adoptCollection = "Forks"
	if err := adoptCmd.RunE(adoptCmd, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expected := engine.ResolveLayoutPath("/target", "Forks", "Go", "my-service")
	if capturedOpts.NewTargetDir != expected {
		t.Errorf("expected target dir %q, got %q", expected, capturedOpts.NewTargetDir)
	}

	// 2. Without collection, default public
	adoptCollection = ""
	adoptVisibility = "public"
	if err := adoptCmd.RunE(adoptCmd, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedPublic := engine.ResolveLayoutPath("/target", "Public", "Go", "my-service")
	if capturedOpts.NewTargetDir != expectedPublic {
		t.Errorf("expected target dir %q, got %q", expectedPublic, capturedOpts.NewTargetDir)
	}

	// 3. Without collection, private visibility
	adoptVisibility = "private"
	if err := adoptCmd.RunE(adoptCmd, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedPrivate := engine.ResolveLayoutPath("/target", "Private", "Go", "my-service")
	if capturedOpts.NewTargetDir != expectedPrivate {
		t.Errorf("expected target dir %q, got %q", expectedPrivate, capturedOpts.NewTargetDir)
	}

	// 4. Test max-depth option passed to discover
	var capturedDiscoverOpts discover.Options
	adoptDiscoverRun = func(ctx context.Context, opts discover.Options) ([]discover.Candidate, error) {
		capturedDiscoverOpts = opts
		return candidates, nil
	}
	adoptMaxDepth = 3
	if err := adoptCmd.RunE(adoptCmd, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedDiscoverOpts.MaxDepth != 3 {
		t.Errorf("expected MaxDepth 3, got %d", capturedDiscoverOpts.MaxDepth)
	}
}
