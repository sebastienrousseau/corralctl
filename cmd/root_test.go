// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sebastienrousseau/corralctl/internal/engine"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func TestDefaultBaseDir(t *testing.T) {
	old := userHomeDir
	defer func() { userHomeDir = old }()

	userHomeDir = func() (string, error) { return "/home/example", nil }
	if got, want := defaultBaseDir(), filepath.Join("/home/example", "Code"); got != want {
		t.Errorf("defaultBaseDir() = %q, want %q", got, want)
	}

	userHomeDir = func() (string, error) { return "", fmt.Errorf("no home") }
	if got, want := defaultBaseDir(), filepath.Join(".", "Code"); got != want {
		t.Errorf("defaultBaseDir() fallback = %q, want %q", got, want)
	}
}

func TestRootRunParsesTypeSortBaseAndLimit(t *testing.T) {
	oldRun, oldYes, oldInteractive, oldExit := engineRun, assumeYes, interactive, osExit
	oldTimeout := apiTimeout
	t.Cleanup(func() {
		engineRun, assumeYes, interactive, osExit = oldRun, oldYes, oldInteractive, oldExit
		apiTimeout = oldTimeout
	})
	assumeYes, interactive = true, false
	apiTimeout = time.Second
	osExit = func(code int) { t.Fatalf("unexpected exit %d", code) }
	oldType, oldSort := repoType, repoSort
	t.Cleanup(func() { repoType, repoSort = oldType, oldSort })
	var got engine.RunOptions
	engineRun = func(ctx context.Context, opts engine.RunOptions) { got = opts }
	base := t.TempDir()

	// The positional grammar is exactly what `Use` and the README document:
	// <owner> [base_dir] [limit]. Type and sort are flags, so no ordinary
	// directory name can be mistaken for a filter.
	repoType, repoSort = "forks", "stars"
	rootCmd.Run(rootCmd, []string{"owner", base, "5"})
	if got.Owner != "owner" || got.BaseDir != base || got.Fetch.Limit != 5 ||
		got.Fetch.Type != "forks" || got.Fetch.Sort != "stars" {
		t.Fatalf("parsed options = %+v", got)
	}

	repoType, repoSort = "", "updated"
	rootCmd.Run(rootCmd, []string{"owner", base, "6"})
	if got.Fetch.Type != "" || got.Fetch.Sort != "updated" || got.Fetch.Limit != 6 {
		t.Fatalf("sort-only options = %+v", got.Fetch)
	}

	// A directory whose name collides with a former positional keyword is
	// now honoured as base_dir rather than silently swallowed as a filter.
	repoType, repoSort = "", ""
	forksDir := filepath.Join(base, "forks")
	if err := os.MkdirAll(forksDir, 0o750); err != nil {
		t.Fatal(err)
	}
	rootCmd.Run(rootCmd, []string{"owner", forksDir})
	if got.BaseDir != forksDir {
		t.Fatalf("base_dir named 'forks' was not honoured: %q", got.BaseDir)
	}
	if got.Fetch.Type != "" {
		t.Fatalf("base_dir must not be reinterpreted as --type: %q", got.Fetch.Type)
	}

	interactive = true
	rootCmd.Run(rootCmd, []string{"owner"})
	if !got.Interactive {
		t.Fatal("interactive option not propagated")
	}
}

// TestValidateRootArgsRejectsSubcommandTypo is the regression test for the
// catch-all-subcommand bug: `corralctl statuss` was a syntactically valid
// invocation meaning "owner=statuss", so a one-character typo started a live
// GitHub fetch and cloned into $HOME/Code.
func TestValidateRootArgsRejectsSubcommandTypo(t *testing.T) {
	for _, typo := range []string{"statuss", "stat", "prunee", "pln", "exe"} {
		err := validateRootArgs(rootCmd, []string{typo})
		if err == nil {
			t.Errorf("%q: expected rejection, got nil", typo)
			continue
		}
		if !strings.Contains(err.Error(), "Did you mean") {
			t.Errorf("%q: expected a suggestion, got: %v", typo, err)
		}
		if !strings.Contains(err.Error(), "--") {
			t.Errorf("%q: expected the force hint, got: %v", typo, err)
		}
	}
}

// A real owner that happens to look nothing like a subcommand passes straight
// through — the guard must not block ordinary use.
func TestValidateRootArgsAcceptsOwners(t *testing.T) {
	for _, owner := range []string{
		"sebastienrousseau", "acme-corp", "torvalds",
		"topic:cli", "language:go",
	} {
		if err := validateRootArgs(rootCmd, []string{owner}); err != nil {
			t.Errorf("%q: unexpected rejection: %v", owner, err)
		}
	}
}

// TestValidateRootArgsRejectsLegacyPositionalKeyword covers the other half of
// the same bug: `corralctl acme forks` used to eat "forks" as a type filter and
// silently fall back to $HOME/Code instead of the directory the user named.
func TestValidateRootArgsRejectsLegacyPositionalKeyword(t *testing.T) {
	for _, kw := range []string{"forks", "stars", "name", "public", "templates", "updated"} {
		err := validateRootArgs(rootCmd, []string{"acme", kw})
		if err == nil {
			t.Errorf("%q: expected rejection as an ambiguous base_dir, got nil", kw)
			continue
		}
		if !strings.Contains(err.Error(), "./"+kw) {
			t.Errorf("%q: error must show how to mean the directory, got: %v", kw, err)
		}
	}
	// Qualified forms are unambiguous and accepted.
	if err := validateRootArgs(rootCmd, []string{"acme", "./forks"}); err != nil {
		t.Errorf("qualified ./forks must be accepted: %v", err)
	}
}

func TestValidateRootArgsArity(t *testing.T) {
	if err := validateRootArgs(rootCmd, nil); err == nil {
		t.Error("expected an error with no owner")
	}
	if err := validateRootArgs(rootCmd, []string{"a", "b", "c", "d"}); err == nil {
		t.Error("expected an error with 4 positionals")
	}
}

func TestRootLayoutValidatedBeforeNetwork(t *testing.T) {
	old := layout
	t.Cleanup(func() { layout = old })
	layout = "{{.Nope"
	err := rootCmd.PreRunE(rootCmd, []string{"owner"})
	if err == nil {
		t.Fatal("a malformed --layout must fail flag validation, not after the fetch")
	}
	if !strings.Contains(err.Error(), "--layout") {
		t.Errorf("error should name the flag, got: %v", err)
	}
}

func TestRootTypeSortValidation(t *testing.T) {
	oldType, oldSort := repoType, repoSort
	t.Cleanup(func() { repoType, repoSort = oldType, oldSort })

	repoType, repoSort = "not-a-type", ""
	if err := rootCmd.PreRunE(rootCmd, []string{"owner"}); err == nil {
		t.Error("expected --type validation error")
	}
	repoType, repoSort = "", "not-a-sort"
	if err := rootCmd.PreRunE(rootCmd, []string{"owner"}); err == nil {
		t.Error("expected --sort validation error")
	}
	repoType, repoSort = "FORKS", "Stars"
	if err := rootCmd.PreRunE(rootCmd, []string{"owner"}); err != nil {
		t.Errorf("values should be case-insensitive: %v", err)
	}
}

func TestRootAPITimeoutValidation(t *testing.T) {
	oldReq, oldTotal, oldDep := apiRequestTimeout, apiTotalTimeout, apiTimeout
	t.Cleanup(func() {
		apiRequestTimeout, apiTotalTimeout, apiTimeout = oldReq, oldTotal, oldDep
	})

	// A non-positive request timeout is still rejected.
	apiTimeout = 0
	apiTotalTimeout = 10 * time.Minute
	apiRequestTimeout = 0
	if err := rootCmd.PreRunE(rootCmd, []string{"owner"}); err == nil {
		t.Fatal("expected an error for a non-positive --api-request-timeout")
	}

	// So is a non-positive total.
	apiRequestTimeout = 30 * time.Second
	apiTotalTimeout = 0
	if err := rootCmd.PreRunE(rootCmd, []string{"owner"}); err == nil {
		t.Fatal("expected an error for a non-positive --api-total-timeout")
	}

	// A zero deprecated flag means "not set", not "invalid": it is the
	// default now that the knob it replaced has been split in two.
	apiRequestTimeout, apiTotalTimeout, apiTimeout = 30*time.Second, 10*time.Minute, 0
	if err := rootCmd.PreRunE(rootCmd, []string{"owner"}); err != nil {
		t.Fatalf("an unset --api-timeout must not be an error: %v", err)
	}
}

func TestRootRunPreflightAbort(t *testing.T) {
	oldPreflight, oldRun, oldExit, oldInteractive := preflightRunner, engineRun, osExit, interactive
	t.Cleanup(func() {
		preflightRunner, engineRun, osExit, interactive = oldPreflight, oldRun, oldExit, oldInteractive
	})
	preflightRunner = func(string, string) (bool, error) { return false, nil }
	engineRun = func(context.Context, engine.RunOptions) { t.Fatal("engine must not run after abort") }
	exitCode := -1
	osExit = func(code int) { exitCode = code }
	interactive = false
	rootCmd.Run(rootCmd, []string{"owner"})
	if exitCode != 0 {
		t.Fatalf("abort exit = %d", exitCode)
	}
}

// TestRootRunPreflightRefusalExitsNonZero separates the two stop conditions: a
// user answering "n" chose to stop and exits 0, but a refusal — corral could
// not ask, so it did nothing — must exit non-zero or a calling script cannot
// tell it from success.
func TestRootRunPreflightRefusalExitsNonZero(t *testing.T) {
	oldPreflight, oldRun, oldExit, oldInteractive := preflightRunner, engineRun, osExit, interactive
	t.Cleanup(func() {
		preflightRunner, engineRun, osExit, interactive = oldPreflight, oldRun, oldExit, oldInteractive
	})
	preflightRunner = func(string, string) (bool, error) {
		return false, fmt.Errorf("refusing to create a new target directory without confirmation")
	}
	engineRun = func(context.Context, engine.RunOptions) { t.Fatal("engine must not run after a refusal") }
	exitCode := -1
	osExit = func(code int) { exitCode = code }
	interactive = false

	oldStderr := os.Stderr
	os.Stderr = mustDevNull(t)
	t.Cleanup(func() { os.Stderr = oldStderr })

	rootCmd.Run(rootCmd, []string{"owner"})
	if exitCode != 1 {
		t.Fatalf("refusal exit = %d, want 1", exitCode)
	}
}

func TestExecute(t *testing.T) {
	resetRootCmdState(t)
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	defer func() {
		os.Stdout = oldStdout
		os.Stderr = oldStderr
	}()
	os.Stdout = mustDevNull(t)
	os.Stderr = mustDevNull(t)

	var exitCode int
	oldOsExit := osExit
	defer func() { osExit = oldOsExit }()
	osExit = func(code int) {
		exitCode = code
	}

	rootCmd.SetArgs([]string{})
	err := rootCmd.Execute()
	if err == nil {
		t.Errorf("Expected error for missing args, got nil")
	}

	oldEngineRun := engineRun
	defer func() { engineRun = oldEngineRun }()
	engineRun = func(ctx context.Context, opts engine.RunOptions) {}

	rootCmd.SetArgs([]string{"owner", "basedir", "10"})
	_ = rootCmd.Execute()

	oldConcurrency := concurrency
	oldLimit := limit
	oldProtocol := protocol
	oldOutput := output
	oldAuthMode := authMode
	oldVisibility := visibility
	oldCloneDepth := cloneDepth
	oldRetryMax := retryMax
	oldRetryMin := retryMinBackoff
	oldRetryMaxB := retryMaxBackoff
	defer func() {
		concurrency = oldConcurrency
		limit = oldLimit
		protocol = oldProtocol
		output = oldOutput
		authMode = oldAuthMode
		visibility = oldVisibility
		cloneDepth = oldCloneDepth
		retryMax = oldRetryMax
		retryMinBackoff = oldRetryMin
		retryMaxBackoff = oldRetryMaxB
	}()

	exitCode = 0
	rootCmd.SetArgs([]string{"owner", "basedir", "abc"})
	_ = rootCmd.Execute()
	if exitCode != 1 {
		t.Errorf("Expected exit code 1 for invalid limit argument, got %d", exitCode)
	}

	rootCmd.SetArgs([]string{"owner"})
	concurrency = 0
	err = rootCmd.Execute()
	if err == nil {
		t.Errorf("Expected error for invalid concurrency")
	}

	concurrency = 1
	limit = -1
	err = rootCmd.Execute()
	if err == nil {
		t.Errorf("Expected error for negative limit")
	}

	limit = 1000
	protocol = "ftp"
	err = rootCmd.Execute()
	if err == nil {
		t.Errorf("Expected error for invalid protocol")
	}

	protocol = "https"
	output = "xml"
	err = rootCmd.Execute()
	if err == nil {
		t.Errorf("Expected error for invalid output")
	}

	output = "text"
	authMode = "bad"
	err = rootCmd.Execute()
	if err == nil {
		t.Errorf("Expected error for invalid auth mode")
	}

	authMode = "auto"
	visibility = "secret"
	err = rootCmd.Execute()
	if err == nil {
		t.Errorf("Expected error for invalid visibility")
	}

	visibility = "all"
	cloneDepth = -1
	err = rootCmd.Execute()
	if err == nil {
		t.Errorf("Expected error for invalid clone depth")
	}

	cloneDepth = 0
	retryMax = -1
	err = rootCmd.Execute()
	if err == nil {
		t.Errorf("Expected error for invalid retry max")
	}

	retryMax = 1
	retryMinBackoff = 0
	err = rootCmd.Execute()
	if err == nil {
		t.Errorf("Expected error for invalid retry min backoff")
	}

	retryMinBackoff = oldRetryMin
	retryMaxBackoff = oldRetryMin / 2
	err = rootCmd.Execute()
	if err == nil {
		t.Errorf("Expected error for invalid retry backoff ordering")
	}

	// Cover the retryMaxBackoff <= 0 branch.
	retryMinBackoff = oldRetryMin
	retryMaxBackoff = 0
	err = rootCmd.Execute()
	if err == nil {
		t.Errorf("Expected error for non-positive retry max backoff")
	}
	retryMaxBackoff = oldRetryMaxB

	// Cover the Run branch where the positional limit argument is negative.
	exitCode = 0
	rootCmd.SetArgs([]string{"owner", "basedir", "--", "-5"})
	_ = rootCmd.Execute()
	if exitCode != 1 {
		t.Errorf("Expected exit code 1 for negative limit argument, got %d", exitCode)
	}

	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()
	os.Args = []string{"corralctl"}
	rootCmd.SetArgs([]string{})
	exitCode = 0
	Execute()
	if exitCode != 1 {
		t.Errorf("Expected Execute() to exit with code 1 when no args provided, got %d", exitCode)
	}

	os.Args = []string{"corralctl", "-h"}
	rootCmd.SetArgs([]string{"-h"})
	exitCode = 0
	Execute()
	if exitCode != 0 {
		t.Errorf("Expected Execute() to succeed with code 0 when help provided, got %d", exitCode)
	}
}

func TestCmdContext(t *testing.T) {
	// nil command returns context.Background().
	if got := cmdContext(nil); got == nil {
		t.Fatal("cmdContext(nil) returned nil context")
	}

	// Command with an explicitly set context returns that context.
	type ctxKey string
	const key ctxKey = "k"
	want := context.WithValue(context.Background(), key, "v")
	c := &cobra.Command{}
	c.SetContext(want)
	if got := cmdContext(c); got.Value(key) != "v" {
		t.Fatalf("cmdContext did not return the command's context")
	}

	// Command whose Context() returns context.Background by default.
	fresh := &cobra.Command{}
	if got := cmdContext(fresh); got == nil {
		t.Fatal("cmdContext(fresh) returned nil context")
	}
}

func TestExecuteContext(t *testing.T) {
	resetRootCmdState(t)
	oldStdout := os.Stdout
	oldStderr := os.Stderr
	defer func() {
		os.Stdout = oldStdout
		os.Stderr = oldStderr
	}()
	os.Stdout = mustDevNull(t)
	os.Stderr = mustDevNull(t)

	var exitCode int
	oldOsExit := osExit
	defer func() { osExit = oldOsExit }()
	osExit = func(code int) { exitCode = code }

	oldEngineRun := engineRun
	defer func() { engineRun = oldEngineRun }()
	engineRun = func(ctx context.Context, opts engine.RunOptions) {}

	// Error path: missing required args makes Execute return an error,
	// triggering osExit(1). Reset the help flag in case a prior test left it
	// set, which would otherwise short-circuit into help output.
	if hf := rootCmd.Flags().Lookup("help"); hf != nil {
		if err := hf.Value.Set("false"); err != nil {
			t.Fatalf("failed to reset help flag: %v", err)
		}
		hf.Changed = false
	}
	rootCmd.SetArgs([]string{})
	exitCode = 0
	ExecuteContext(context.Background())
	if exitCode != 1 {
		t.Errorf("Expected exit code 1 for missing args, got %d", exitCode)
	}

	// nil context path: ExecuteContext should substitute context.Background()
	// and execute successfully. The nil is held in a variable so static
	// analysis does not flag the intentional nil-context argument.
	var nilCtx context.Context
	rootCmd.SetArgs([]string{"-h"})
	exitCode = 0
	ExecuteContext(nilCtx)
	if exitCode != 0 {
		t.Errorf("Expected exit code 0 for help with nil context, got %d", exitCode)
	}
}

func TestParseCSV(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"go", 1},
		{"go, rust", 2},
		{"go, , rust", 2},
	}
	for _, tc := range cases {
		got := parseCSV(tc.in)
		if len(got) != tc.want {
			t.Fatalf("parseCSV(%q) len=%d want=%d", tc.in, len(got), tc.want)
		}
	}
}

// mustDevNull opens /dev/null for writing and closes it when the test ends.
//
// Tests previously did `os.NewFile(0, os.DevNull)`, which does not open
// anything: it wraps *file descriptor 0* — stdin — and merely names it
// "/dev/null". When that *os.File was garbage collected its finalizer closed
// fd 0, so an unrelated later test could fail with EBADF on a fresh file. That
// is what made the suite order-dependent under -shuffle.
func mustDevNull(t *testing.T) *os.File {
	t.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = f.Close() })
	return f
}

// resetRootCmdState restores the mutable state of the package-level rootCmd
// when the test finishes.
//
// rootCmd is a global, and cobra mutates it: SetArgs stores the argv, and
// parsing `-h` sets the persistent `help` flag to true and marks it Changed.
// A test that ends on `-h` therefore leaves every subsequent test executing
// rootCmd short-circuited into help output — so those tests pass while
// asserting nothing. That is why the suite failed under -shuffle on most
// seeds despite reporting 100% coverage, and it is why `go test -shuffle=on`
// now runs in CI.
func resetRootCmdState(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SilenceErrors = false
		rootCmd.SilenceUsage = false
		for _, fs := range []*pflag.FlagSet{rootCmd.Flags(), rootCmd.PersistentFlags()} {
			if f := fs.Lookup("help"); f != nil {
				_ = f.Value.Set("false")
				f.Changed = false
			}
		}
	})
}

// TestRootCmdHelpFlagDoesNotLeak pins the invariant directly, so a future test
// that leaves rootCmd in a help state fails here rather than corrupting an
// unrelated test three seeds later.
func TestRootCmdHelpFlagDoesNotLeak(t *testing.T) {
	if f := rootCmd.Flags().Lookup("help"); f != nil && f.Value.String() == "true" {
		t.Fatal("rootCmd's help flag is set at test start: a prior test leaked state")
	}
	if err := rootCmd.PreRunE(rootCmd, []string{"owner"}); err != nil {
		t.Fatalf("validation must actually run, not short-circuit into help: %v", err)
	}
}
