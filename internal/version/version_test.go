// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package version

import "testing"

func TestDisplay(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		// The three real producers.
		{"goreleaser strips the v", "0.0.37", "v0.0.37"},
		{"git describe keeps it", "v0.0.37-25-gdf4b427", "v0.0.37-25-gdf4b427"},
		{"no ldflags at all", "dev", "dev"},

		// The bug this package exists for: a v-prefixed version must not gain
		// a second one.
		{"already prefixed", "v1.2.3", "v1.2.3"},

		// A version is never dressed up as one when there isn't one.
		{"empty", "", "dev"},
		{"whitespace only", "   ", "dev"},
		{"padded dev", "  dev  ", "dev"},

		// Ordinary shapes.
		{"padded release", "  1.2.3  ", "v1.2.3"},
		{"prerelease", "1.2.3-rc.1", "v1.2.3-rc.1"},
		{"build metadata", "1.2.3+darwin.arm64", "v1.2.3+darwin.arm64"},

		// A version that merely starts with the letter v is left alone rather
		// than guessed at: "v" is the prefix this package knows about, and
		// anything already carrying it is taken at its word.
		{"vanity string", "vNext", "vNext"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Display(tc.in); got != tc.want {
				t.Errorf("Display(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestDisplayIsIdempotent is the property that actually failed in production:
// formatting an already-formatted version must not change it, because the
// value can arrive having been through this once already.
func TestDisplayIsIdempotent(t *testing.T) {
	for _, in := range []string{"", "dev", "0.0.37", "v0.0.37", "1.2.3-rc.1", "  0.1.0  "} {
		once := Display(in)
		if twice := Display(once); twice != once {
			t.Errorf("Display(Display(%q)) = %q, want %q", in, twice, once)
		}
	}
}
