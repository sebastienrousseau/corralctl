// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/sebastienrousseau/corralctl/internal/diag"
	"github.com/sebastienrousseau/corralctl/internal/github"
)

// withConfigPath points the package-level configPath at a temporary file and
// restores it afterwards, so one test's config cannot leak into the next.
func withConfigPath(t *testing.T, path string) {
	t.Helper()
	original := configPath
	configPath = path
	t.Cleanup(func() { configPath = original })
}

// ---------------------------------------------------------------------------
// flags
// ---------------------------------------------------------------------------

func TestDefaultConcurrencyClampsToUsefulRange(t *testing.T) {
	original := numCPU
	t.Cleanup(func() { numCPU = original })

	cases := []struct {
		name  string
		cores int
		want  int
		why   string
	}{
		{"below the floor", 1, minDefaultConcurrency, "a single-core host still benefits from overlapping network waits"},
		{"at the floor", minDefaultConcurrency, minDefaultConcurrency, ""},
		{"inside the range", 6, 6, ""},
		{"at the ceiling", maxDefaultConcurrency, maxDefaultConcurrency, ""},
		{"above the ceiling", 64, maxDefaultConcurrency, "more parallel clones than this is hostile to the GitHub API"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			numCPU = func() int { return tc.cores }
			if got := defaultConcurrency(); got != tc.want {
				t.Fatalf("defaultConcurrency() with %d cores = %d, want %d. %s", tc.cores, got, tc.want, tc.why)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// settings
// ---------------------------------------------------------------------------

func TestSettingToStringHandlesRemainingScalarTypes(t *testing.T) {
	// int64 does not arrive from encoding/json, but it does arrive from a
	// caller building a settings map in Go, and silently failing on it
	// would be a confusing error a long way from its cause.
	got, err := settingToString(int64(42))
	if err != nil {
		t.Fatalf("int64 rejected: %v", err)
	}
	if got != "42" {
		t.Fatalf("settingToString(int64(42)) = %q, want %q", got, "42")
	}

	// A list containing an unsupported element must report the element's
	// problem, not a vague failure about the list.
	if _, err := settingToString([]any{"go", map[string]any{}}); err == nil {
		t.Fatal("a list with an unsupported element was accepted")
	}
}

func TestApplySettingsRejectsUnrenderableValue(t *testing.T) {
	c := newSettingsCmd(t)
	_, err := applySettings(c, map[string]any{"protocol": map[string]any{"nested": true}}, "test source")
	if err == nil {
		t.Fatal("a value that cannot be rendered as a flag string was accepted")
	}
	if !strings.Contains(err.Error(), "test source") || !strings.Contains(err.Error(), "protocol") {
		t.Fatalf("error names neither the source nor the setting: %v", err)
	}
}

func TestErrorCauseStopsAtNilUnwrap(t *testing.T) {
	// An error whose Unwrap returns nil must terminate the walk at itself
	// rather than returning nil, which would make os.IsNotExist panic-free
	// but always false.
	sentinel := &nilUnwrapError{}
	if got := errorCause(sentinel); got != error(sentinel) {
		t.Fatalf("errorCause returned %v, want the error itself", got)
	}
}

// nilUnwrapError implements Unwrap but has nothing to unwrap to, which is
// legal and does occur with hand-rolled error types.
type nilUnwrapError struct{}

func (e *nilUnwrapError) Error() string { return "no cause" }
func (e *nilUnwrapError) Unwrap() error { return nil }

// ---------------------------------------------------------------------------
// config file loading
// ---------------------------------------------------------------------------

func TestConfiguredDefaultsSurfacesLoadFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	withConfigPath(t, path)

	if _, err := configuredDefaults(newSettingsCmd(t)); err == nil {
		t.Fatal("a malformed config file was accepted")
	}
}

func TestConfiguredDefaultsAppliesDefaultsBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"defaults":{"protocol":"ssh"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	withConfigPath(t, path)

	c := newSettingsCmd(t)
	applied, err := configuredDefaults(c)
	if err != nil {
		t.Fatalf("valid defaults block rejected: %v", err)
	}
	if len(applied) != 1 || applied[0].Key != "protocol" || applied[0].Value != "ssh" {
		t.Fatalf("defaults were not applied: %+v", applied)
	}
}

func TestLoadConfigOptionalDistinguishesExplicitMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.json")

	// Asked for explicitly: a missing file is an error, because the user
	// named a path that is not there.
	if _, err := loadConfigOptional(missing); err == nil {
		t.Fatal("an explicitly named missing config file was accepted")
	}

	// Not named: corral is usable with no config at all, so the default
	// path being absent is the normal case.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	cfg, err := loadConfigOptional("")
	if err != nil {
		t.Fatalf("a missing default config should not be an error: %v", err)
	}
	if len(cfg.Defaults) != 0 || len(cfg.Profiles) != 0 {
		t.Fatalf("a missing config produced settings: %+v", cfg)
	}
}

func TestLoadConfigReportsUnreadableAndMalformedFiles(t *testing.T) {
	dir := t.TempDir()

	// A directory where a file is expected: the read fails.
	if _, err := loadConfig(dir); err == nil {
		t.Fatal("reading a directory as a config file should fail")
	} else if !strings.Contains(err.Error(), "read config") {
		t.Fatalf("error does not name the cause: %v", err)
	}

	malformed := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(malformed, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(malformed); err == nil {
		t.Fatal("a malformed config file was accepted")
	} else if !strings.Contains(err.Error(), "decode config") {
		t.Fatalf("error does not name the cause: %v", err)
	}

	// Valid JSON, but a value of the wrong shape for the strict decoder.
	wrongShape := filepath.Join(dir, "shape.json")
	if err := os.WriteFile(wrongShape, []byte(`{"profiles": "not an object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadConfig(wrongShape); err == nil {
		t.Fatal("a config with a wrongly typed section was accepted")
	} else if !strings.Contains(err.Error(), "decode config") {
		t.Fatalf("error does not name the cause: %v", err)
	}
}

// ---------------------------------------------------------------------------
// config template
// ---------------------------------------------------------------------------

func TestDefaultConfigTemplateOmitsHelpAndConfigFlags(t *testing.T) {
	// Cobra adds --help lazily, at Execute time, so a bare rootCmd has no
	// help flag and the exclusion below would look exercised without ever
	// having a --help to exclude.
	rootCmd.InitDefaultHelpFlag()
	body := defaultConfigTemplate(rootCmd)
	if strings.Contains(body, `"//help"`) {
		t.Fatal("the template documents --help, which is not a setting")
	}
	if strings.Contains(body, `"//config"`) {
		t.Fatal("the template documents --config, which selects the file it is written into")
	}
	if !strings.Contains(body, `"//concurrency"`) {
		t.Fatalf("the template omits a real setting:\n%s", body)
	}
}

func TestWriteConfigTemplateUsesDefaultPathAndReportsFailures(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)

	out := captureStdout(t, func() {
		if err := writeConfigTemplate("", "{}\n"); err != nil {
			t.Errorf("writing to the default path failed: %v", err)
		}
	})
	if !strings.Contains(out, "Wrote ") {
		t.Fatalf("the written path was not reported: %q", out)
	}
	written := defaultConfigPath()
	if _, err := os.Stat(written); err != nil {
		t.Fatalf("no config at the default path: %v", err)
	}

	// Second run must refuse rather than overwrite: this file holds the
	// user's settings.
	if err := writeConfigTemplate("", "{}\n"); err == nil {
		t.Fatal("an existing config was overwritten")
	}

	// A parent path occupied by a regular file: directory creation fails.
	blocked := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeConfigTemplate(filepath.Join(blocked, "config.json"), "{}\n"); err == nil {
		t.Fatal("writing under a regular file was accepted")
	} else if !strings.Contains(err.Error(), "create config directory") {
		t.Fatalf("error does not name the cause: %v", err)
	}

	// The write itself failing, after the directory was created. Driven
	// through the seam rather than through permission bits, which behave
	// differently on the three platforms this is tested on.
	original := writeConfigFile
	t.Cleanup(func() { writeConfigFile = original })
	writeConfigFile = func(string, []byte, os.FileMode) error {
		return errors.New("disk full")
	}
	if err := writeConfigTemplate(filepath.Join(t.TempDir(), "config.json"), "{}\n"); err == nil {
		t.Fatal("a failed write was reported as success")
	} else if !strings.Contains(err.Error(), "write config") {
		t.Fatalf("error does not name the cause: %v", err)
	}
}

// ---------------------------------------------------------------------------
// commands
// ---------------------------------------------------------------------------

func TestPruneRefusesTruncatedUpstreamListing(t *testing.T) {
	// A listing that hits --limit hides every repository past the cap, and
	// prune's answer to a repository it cannot see upstream is rm -rf.
	original := opsFetchRepos
	t.Cleanup(func() { opsFetchRepos = original })
	opsFetchRepos = func(context.Context, string, github.FetchOptions) ([]github.Repo, error) {
		return []github.Repo{
			{Name: "a", Owner: "acme", FullName: "acme/a"},
			{Name: "b", Owner: "acme", FullName: "acme/b"},
		}, nil
	}

	originalLimit, originalYes := limit, assumeYes
	t.Cleanup(func() { limit, assumeYes = originalLimit, originalYes })
	limit, assumeYes = 2, true

	err := pruneCmd.RunE(pruneCmd, []string{"acme"})
	if err == nil {
		t.Fatal("prune proceeded on a truncated listing")
	}
	if !strings.Contains(err.Error(), "refusing to prune") {
		t.Fatalf("error does not name the cause: %v", err)
	}
}

func TestConfigCommandInitWritesTemplate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	withConfigPath(t, path)

	originalInit := configInit
	t.Cleanup(func() { configInit = originalInit })
	configInit = true

	out := captureStdout(t, func() {
		if err := configCmd.RunE(configCmd, nil); err != nil {
			t.Errorf("config --init failed: %v", err)
		}
	})
	if !strings.Contains(out, "Wrote ") {
		t.Fatalf("the written path was not reported: %q", out)
	}

	// The file `config --init` writes must load back through the strict
	// decoder. It did not, once, and every later config command aborted.
	if _, err := loadConfig(path); err != nil {
		t.Fatalf("the generated config does not load: %v", err)
	}
}

func TestConfigCommandExplainReportsProvenance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"defaults":{"protocol":"ssh"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	withConfigPath(t, path)

	originalExplain, originalProtocol := configExplain, protocol
	t.Cleanup(func() { configExplain, protocol = originalExplain, originalProtocol })
	configExplain = true

	out := captureStdout(t, func() {
		if err := configCmd.RunE(configCmd, nil); err != nil {
			t.Errorf("config --explain failed: %v", err)
		}
	})

	var report map[string]any
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("--explain did not emit JSON: %v\n%s", err, out)
	}
	if report["config_path"] != path {
		t.Fatalf("report names the wrong config: %v", report["config_path"])
	}
	applied, _ := report["applied"].([]any)
	if len(applied) != 1 {
		t.Fatalf("expected one applied setting, got %v", report["applied"])
	}
}

func TestConfigCommandExplainNotesAnEmptyDefaultsBlock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"profiles":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	withConfigPath(t, path)

	originalExplain := configExplain
	t.Cleanup(func() { configExplain = originalExplain })
	configExplain = true

	out := captureStdout(t, func() {
		if err := configCmd.RunE(configCmd, nil); err != nil {
			t.Errorf("config --explain failed: %v", err)
		}
	})
	if !strings.Contains(out, "no defaults block") {
		t.Fatalf("an empty defaults block was reported without explanation:\n%s", out)
	}
}

func TestConfigCommandExplainSurfacesBadDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"defaults":{"protocol":null}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	withConfigPath(t, path)

	originalExplain := configExplain
	t.Cleanup(func() { configExplain = originalExplain })
	configExplain = true

	if err := configCmd.RunE(configCmd, nil); err == nil {
		t.Fatal("a defaults block that cannot be applied was reported as fine")
	}
}

func TestPersistentPreRunSkipsConfigInit(t *testing.T) {
	// `config --init` writes the file this hook would otherwise read, so
	// the hook must not fail on its absence.
	withConfigPath(t, filepath.Join(t.TempDir(), "absent.json"))
	originalInit := configInit
	t.Cleanup(func() { configInit = originalInit })

	configInit = true
	if err := rootCmd.PersistentPreRunE(configCmd, nil); err != nil {
		t.Fatalf("the hook ran despite --init: %v", err)
	}

	configInit = false
	if err := rootCmd.PersistentPreRunE(configCmd, nil); err == nil {
		t.Fatal("an explicitly named missing config was accepted")
	}
}

func TestProfileCommandSurfacesUnappliableSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	body := `{"profiles":{"work":{"owners":["acme"],"settings":{"concurrency":null}}}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	withConfigPath(t, path)

	err := profileCmd.RunE(profileCmd, []string{"work"})
	if err == nil {
		t.Fatal("a profile with an unrenderable setting was accepted")
	}
	if !strings.Contains(err.Error(), `profile "work"`) {
		t.Fatalf("error does not name the profile: %v", err)
	}
}

func TestNearestSubcommandIgnoresExactAndHiddenMatches(t *testing.T) {
	if got := nearestSubcommand(rootCmd, "config"); got != "" {
		t.Fatalf("an exact match was reported as a typo: %q", got)
	}
	if got := nearestSubcommand(rootCmd, "confg"); got != "config" {
		t.Fatalf("nearestSubcommand(%q) = %q, want %q", "confg", got, "config")
	}
	if got := nearestSubcommand(rootCmd, "totallyunrelated"); got != "" {
		t.Fatalf("an unrelated word was matched to %q", got)
	}
}

func TestLoadConfigSurfacesReencodeFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"defaults":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	original := marshalConfig
	t.Cleanup(func() { marshalConfig = original })
	marshalConfig = func(any) ([]byte, error) { return nil, errors.New("re-encode exploded") }

	if _, err := loadConfig(path); err == nil {
		t.Fatal("a failed re-encode was reported as a valid config")
	} else if !strings.Contains(err.Error(), "decode config") {
		t.Fatalf("error does not name the cause: %v", err)
	}
}

func TestConfigCommandExplainFallsBackToDefaultPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", home)
	withConfigPath(t, "") // no --config: the default path must be used

	target := defaultConfigPath()
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(`{"defaults":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	originalExplain := configExplain
	t.Cleanup(func() { configExplain = originalExplain })
	configExplain = true

	out := captureStdout(t, func() {
		if err := configCmd.RunE(configCmd, nil); err != nil {
			t.Errorf("config --explain failed: %v", err)
		}
	})

	var report map[string]any
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("--explain did not emit JSON: %v\n%s", err, out)
	}
	if report["config_path"] != target {
		t.Fatalf("report names %v, want the default path %s", report["config_path"], target)
	}
}

func TestNearestSubcommandSkipsHiddenCommands(t *testing.T) {
	// A hidden command is not something a user can be trying to type, so
	// suggesting it would send them somewhere undocumented.
	hidden := &cobra.Command{Use: "zzsecret", Hidden: true, Run: func(*cobra.Command, []string) {}}
	rootCmd.AddCommand(hidden)
	t.Cleanup(func() { rootCmd.RemoveCommand(hidden) })

	if got := nearestSubcommand(rootCmd, "zzsecrat"); got != "" {
		t.Fatalf("a hidden command was suggested: %q", got)
	}
}

// ---------------------------------------------------------------------------
// diagnostic verbosity
// ---------------------------------------------------------------------------

func TestEnvLogLevelHonoursValidValuesOnly(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want string
		why  string
	}{
		{"unset", "", "info", "the default must reproduce corral's historical output"},
		{"valid", "debug", "debug", "an exported level applies to the whole shell session"},
		{"mixed case", "DEBUG", "debug", "level names are not case-sensitive"},
		{"invalid", "chatty", "info", "an unusable value must not silence or flood diagnostics"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CORRAL_LOG_LEVEL", tc.env)
			if got := envLogLevel(); got != tc.want {
				t.Fatalf("envLogLevel() with CORRAL_LOG_LEVEL=%q = %q, want %q. %s", tc.env, got, tc.want, tc.why)
			}
		})
	}
}

func TestApplyLogLevelRejectsUnknownNames(t *testing.T) {
	originalFlag, originalLevel := logLevel, diag.CurrentLevel()
	t.Cleanup(func() { logLevel = originalFlag; diag.SetLevel(originalLevel) })

	logLevel = "debug"
	if err := applyLogLevel(); err != nil {
		t.Fatalf("a valid level was rejected: %v", err)
	}
	if diag.CurrentLevel() != diag.LevelDebug {
		t.Fatalf("level is %v, want debug", diag.CurrentLevel())
	}

	logLevel = "chatty"
	if err := applyLogLevel(); err == nil {
		t.Fatal("an unknown level was accepted, so a typo would silently do nothing")
	}
	if diag.CurrentLevel() != diag.LevelDebug {
		t.Fatal("a rejected level changed the verbosity anyway")
	}
}

func TestPersistentPreRunAppliesLogLevel(t *testing.T) {
	originalFlag, originalLevel, originalInit := logLevel, diag.CurrentLevel(), configInit
	t.Cleanup(func() {
		logLevel, configInit = originalFlag, originalInit
		diag.SetLevel(originalLevel)
	})

	// With --init the config is not read, but the level still must apply:
	// `config --init --log-level debug` should be debuggable.
	withConfigPath(t, filepath.Join(t.TempDir(), "absent.json"))
	configInit = true
	logLevel = "warn"
	if err := rootCmd.PersistentPreRunE(configCmd, nil); err != nil {
		t.Fatalf("hook failed under --init: %v", err)
	}
	if diag.CurrentLevel() != diag.LevelWarn {
		t.Fatalf("level is %v, want warn", diag.CurrentLevel())
	}

	configInit = true
	logLevel = "chatty"
	if err := rootCmd.PersistentPreRunE(configCmd, nil); err == nil {
		t.Fatal("an unknown level was accepted under --init")
	}

	// Without --init, the config loads first so a level set in the defaults
	// block behaves like the flag; a bad flag value still fails.
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"defaults":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	withConfigPath(t, path)
	configInit = false
	logLevel = "error"
	if err := rootCmd.PersistentPreRunE(configCmd, nil); err != nil {
		t.Fatalf("hook failed: %v", err)
	}
	if diag.CurrentLevel() != diag.LevelError {
		t.Fatalf("level is %v, want error", diag.CurrentLevel())
	}

	logLevel = "chatty"
	if err := rootCmd.PersistentPreRunE(configCmd, nil); err == nil {
		t.Fatal("an unknown level was accepted")
	}
}
