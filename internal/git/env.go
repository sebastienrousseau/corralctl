// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package git

import (
	"os"
	"strconv"
	"strings"
)

// repoRedirectEnv names the variables that tell git which repository,
// work tree, index or object store to use, or which refs to read.
//
// git exports several of them to hooks and aliases. When corralctl runs
// from one, an inherited GIT_DIR outranks `-C targetDir`, so every
// command, including the guards that decide whether a clone holds
// unpublished work, would inspect the repository GIT_DIR names instead.
// The list follows git's own local_repo_env (the set git clears when it
// moves into another repository, such as a submodule) without the config
// variables, which carry the user's settings and are kept, plus
// GIT_NAMESPACE and GIT_QUARANTINE_PATH.
var repoRedirectEnv = map[string]bool{
	"GIT_DIR":                          true,
	"GIT_WORK_TREE":                    true,
	"GIT_IMPLICIT_WORK_TREE":           true,
	"GIT_INDEX_FILE":                   true,
	"GIT_OBJECT_DIRECTORY":             true,
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true,
	"GIT_COMMON_DIR":                   true,
	"GIT_GRAFT_FILE":                   true,
	"GIT_SHALLOW_FILE":                 true,
	"GIT_NO_REPLACE_OBJECTS":           true,
	"GIT_REPLACE_REF_BASE":             true,
	"GIT_PREFIX":                       true,
	"GIT_NAMESPACE":                    true,
	"GIT_QUARANTINE_PATH":              true,
}

// childEnv is the environment every git child starts from: the process
// environment without the repository-redirecting variables, plus the
// non-interactive guards.
func childEnv() []string {
	inherited := os.Environ()
	env := make([]string, 0, len(inherited)+len(nonInteractiveEnv()))
	for _, kv := range inherited {
		name, _, _ := strings.Cut(kv, "=")
		// Upper-cased because Windows environment names are
		// case-insensitive.
		if repoRedirectEnv[strings.ToUpper(name)] {
			continue
		}
		env = append(env, kv)
	}
	return append(env, nonInteractiveEnv()...)
}

// configPair is one git config entry passed through the environment.
type configPair struct {
	key, value string
}

// withConfig adds pairs to env as GIT_CONFIG_KEY_n/GIT_CONFIG_VALUE_n,
// numbered after the entries env already declares, and raises
// GIT_CONFIG_COUNT to cover both.
//
// Writing GIT_CONFIG_COUNT=1 and KEY_0 instead, as corralctl once did,
// silently replaced a user's own environment config (a proxy, a CA
// bundle) whenever a credential was attached. os/exec keeps the last
// duplicate of a variable, so the new count appended here wins.
func withConfig(env []string, pairs ...configPair) []string {
	if len(pairs) == 0 {
		return env
	}
	base := inheritedConfigCount(env)
	for i, p := range pairs {
		n := strconv.Itoa(base + i)
		env = append(env, "GIT_CONFIG_KEY_"+n+"="+p.key, "GIT_CONFIG_VALUE_"+n+"="+p.value)
	}
	return append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(base+len(pairs)))
}

// inheritedConfigCount reads the last GIT_CONFIG_COUNT in env. A value git
// itself would reject as bogus counts as zero, which is what corralctl did
// before it preserved the user's entries.
func inheritedConfigCount(env []string) int {
	count := 0
	for _, kv := range env {
		v, ok := strings.CutPrefix(kv, "GIT_CONFIG_COUNT=")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 0 {
			n = 0
		}
		count = n
	}
	return count
}

// hardeningArgs are the -c overrides every git invocation in this package
// carries, ahead of its own arguments. Command-line config outranks the
// repository's, the user's and the environment's, so none of them can
// switch these back on.
//
//   - core.hooksPath=/dev/null: no repository hook runs.
//   - core.fsmonitor=false: no fsmonitor command named in a clone's
//     config runs; it executes on every status, like a hook.
//   - protocol.ext.allow=never: no ext:: transport, which runs a command.
//   - protocol.file.allow=user: a submodule URL cannot read a local path
//     (CVE-2022-39253). git defaults to this from 2.30.6, 2.31.5 and
//     2.38.1, so stating it covers older patch levels and a user who set
//     "always" globally.
var hardeningArgs = []string{
	"-c", "core.hooksPath=/dev/null",
	"-c", "core.fsmonitor=false",
	"-c", "protocol.ext.allow=never",
	"-c", "protocol.file.allow=user",
}

// hardened prefixes args with hardeningArgs.
func hardened(args ...string) []string {
	return append(append(make([]string, 0, len(hardeningArgs)+len(args)), hardeningArgs...), args...)
}
