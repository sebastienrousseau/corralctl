// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package mcp

import "testing"

// FuzzParseOwnerFromURL checks that parseOwnerFromURL is robust and does not
// panic when processing arbitrary malformed remote URL inputs.
func FuzzParseOwnerFromURL(f *testing.F) {
	for _, seed := range []string{
		"https://github.com/sebastienrousseau/corralctl.git",
		"git@github.com:sebastienrousseau/corralctl.git",
		"https://git.company.com/parent/sub/owner/repo.git",
		"git@github-personal:owner/repo.git",
		"",
		"http://",
		"://",
		"a/b/c",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, url string) {
		_ = parseOwnerFromURL(url)
	})
}
