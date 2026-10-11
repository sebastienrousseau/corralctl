// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var (
	configPath    string
	configInit    bool
	configExplain bool
)

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Validate, print, or create the configuration",
	Long: `Validate, print, or create the corral configuration.

Settings are named after flags, so "concurrency": 8 in the file is the same as
passing --concurrency 8. Precedence, highest first: an explicitly-passed flag,
then the selected profile, then the defaults block, then the flag's own default.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if configInit {
			return writeConfigTemplate(configPath, defaultConfigTemplate(rootCmd))
		}

		cfg, err := loadConfig(configPath)
		if err != nil {
			return err
		}
		for name, configured := range cfg.Profiles {
			if err := validateProfile(name, configured); err != nil {
				return err
			}
		}

		if configExplain {
			// Show where each effective value comes from. A layered config is
			// only debuggable if you can ask it why a value is what it is.
			path := configPath
			if path == "" {
				path = defaultConfigPath()
			}
			applied, err := applySettings(rootCmd, cfg.Defaults, "config defaults")
			if err != nil {
				return err
			}
			report := map[string]any{"config_path": path, "applied": applied}
			if len(applied) == 0 {
				report["note"] = "no defaults block, or every setting was overridden by an explicit flag"
			}
			return writeJSON(os.Stdout, report)
		}

		return writeJSON(os.Stdout, cfg)
	},
}

// configCommentPrefix marks keys that `config --init` writes as documentation
// rather than settings: a top-level "//" block, and per-setting "//<flag>"
// entries inside "defaults".
const configCommentPrefix = "//"

// stripConfigComments removes documentation keys from every object in a decoded
// JSON document, at any depth.
//
// The decoder is deliberately strict, and should stay that way: a typo like
// "concurrancy" must be an error rather than a setting that silently does
// nothing. But `config --init` writes "//" keys itself, so strictness rejected
// corral's own starter file — the config the tool had just written could not be
// read back, and every later `config --explain`, `plan` or `profile` failed
// with `unknown field "//"`. Stripping comment keys before the strict pass
// keeps typo detection while honouring the convention corral documents and
// emits.
func stripConfigComments(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			if strings.HasPrefix(key, configCommentPrefix) {
				delete(typed, key)
				continue
			}
			typed[key] = stripConfigComments(nested)
		}
		return typed
	case []any:
		for i := range typed {
			typed[i] = stripConfigComments(typed[i])
		}
		return typed
	}
	return value
}

func loadConfig(path string) (configFile, error) {
	if path == "" {
		path = defaultConfigPath()
	}
	raw, err := readConfigFile(path)
	if err != nil {
		return configFile{}, err
	}

	// Two passes: a permissive one to find and drop the comment keys, then the
	// strict one that actually validates the settings.
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return configFile{}, fmt.Errorf("decode config %s: %w", path, err)
	}
	cleaned, err := marshalConfig(stripConfigComments(document))
	if err != nil {
		return configFile{}, fmt.Errorf("decode config %s: %w", path, err)
	}

	var cfg configFile
	decoder := json.NewDecoder(bytes.NewReader(cleaned))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return configFile{}, fmt.Errorf("decode config %s: %w", path, err)
	}
	return cfg, nil
}

// marshalConfig re-encodes the comment-stripped document for the strict
// decoding pass. A seam because the re-encode cannot fail on any input the
// permissive pass accepts, which would leave its error check permanently
// unexercised — and an untested error path is one nobody knows is wrong.
var marshalConfig = json.Marshal

// readConfigFile is a seam so tests can exercise an unreadable config without
// depending on permission bits, which do not behave the same on every platform.
var readConfigFile = func(path string) ([]byte, error) {
	f, err := os.Open(path) // #nosec G304 -- path is explicitly selected by the local user
	if err != nil {
		return nil, fmt.Errorf("open config %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	return raw, nil
}

func defaultConfigPath() string {
	if configured := os.Getenv("XDG_CONFIG_HOME"); configured != "" {
		return filepath.Join(configured, "corral", "config.json")
	}
	home, err := userHomeDir()
	if err != nil {
		return filepath.Join(".", ".config", "corral", "config.json")
	}
	return filepath.Join(home, ".config", "corral", "config.json")
}

func validateProfile(name string, configured profile) error {
	if len(configured.Owners) == 0 {
		return fmt.Errorf("profile %q must contain at least one owner", name)
	}
	for _, owner := range configured.Owners {
		if strings.TrimSpace(owner) == "" {
			return fmt.Errorf("profile %q contains an empty owner", name)
		}
	}
	// Per-setting validity is enforced where it belongs: applySettings pushes
	// each value through the flag's own parser, and the commands' PreRunE
	// checks the semantic ranges. Duplicating a protocol allow-list here meant
	// only the five hand-mapped fields were ever checked, and it drifted from
	// the flag definitions.
	for key := range configured.effectiveSettings() {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("profile %q contains an empty setting name", name)
		}
	}
	return nil
}

func init() {
	rootCmd.PersistentFlags().StringVar(&configPath, "config", "", "profile configuration file")
	configCmd.Flags().BoolVar(&configInit, "init", false, "write a commented starter config and exit")
	configCmd.Flags().BoolVar(&configExplain, "explain", false, "show each effective setting and where it came from")
	rootCmd.AddCommand(configCmd)
}
