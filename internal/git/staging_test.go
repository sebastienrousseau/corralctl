// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package git

import "testing"

func TestIsDeletionStaging(t *testing.T) {
	for path, want := range map[string]bool{
		"/w/Public/.corral-deleting-repo-00ff": true,
		".corral-deleting-x":                   true,
		"/w/Public/repo":                       false,
		"/w/.corral-deleting-a/repo":           false,
		"/w/corral-deleting-repo":              false,
	} {
		if got := IsDeletionStaging(path); got != want {
			t.Errorf("IsDeletionStaging(%q) = %v, want %v", path, got, want)
		}
	}
}
