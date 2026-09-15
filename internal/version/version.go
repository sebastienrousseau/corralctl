// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package version formats a build's version string for display.
//
// It exists because the version reaches the binary from more than one place and
// they disagree about the leading "v". GoReleaser substitutes {{.Version}},
// which is the tag with the "v" stripped ("0.0.37"). A developer building by
// hand usually passes `git describe --tags`, which keeps it
// ("v0.0.37-25-gdf4b427"). A build with neither gets the "dev" placeholder.
//
// Call sites used to write "v%s" and get all three wrong in different ways:
// "v0.0.37" (right), "vv0.0.37-25-gdf4b427" (a doubled v, seen in the MCP
// startup banner), and "vdev" (a v on something that is not a version).
package version

import "strings"

// Display renders a version for humans: exactly one leading "v" on a real
// version, and no "v" at all on the placeholder a build without one carries.
//
// Callers should print the result as-is — "%s", never "v%s".
func Display(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == "dev" {
		return "dev"
	}
	if strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}
