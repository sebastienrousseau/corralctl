// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package git

import (
	"path/filepath"
	"strings"
)

// DeletionStagePrefix marks a clone renamed aside for removal by the MCP
// delete tool (.corral-deleting-<name>-<hex>, beside the original).
//
// It lives here, in the package every walker already imports, because
// three places must agree on it: the delete that creates the name, the
// mirror walk that must not take a leftover for a repository, and doctor,
// which reports one so a person can decide what to do with it. A delete
// that crashes between the rename and the removal leaves the directory
// behind with its .git intact.
const DeletionStagePrefix = ".corral-deleting-"

// IsDeletionStaging reports whether path names a directory staged for
// deletion.
func IsDeletionStaging(path string) bool {
	return strings.HasPrefix(filepath.Base(path), DeletionStagePrefix)
}
