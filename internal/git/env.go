// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package git

import (
	"os"
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
