// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package deps

import "testing"

func TestClassifyNamesHowFarAVersionMoves(t *testing.T) {
	cases := []struct {
		from, to string
		want     Jump
	}{
		{"4.165.0", "4.166.0", JumpMinor},
		{"6.21.4", "7.1.1", JumpMajor},
		{"2.90.6", "2.90.8", JumpPatch},
		{"v10.3.3", "v10.4.0", JumpMinor},
		{"1.2.1+spec-1.1.0", "1.2.2+spec-1.1.0", JumpPatch},
		{"10.36.0.15337", "10.37.0.15768", JumpMinor},
		{"5.29.6", "7.36.2", JumpMajor},
		{"4", "5", JumpMajor},
		// Pre-1.0: a minor move may break, so it is handled as major.
		{"0.6.15", "0.12.1", JumpMajor},
		{"0.0.8", "0.0.9", JumpPatch},
		{"0.17.3", "0.17.6", JumpPatch},
		{"2.0.0-beta.1", "2.0.0", JumpUnknown},
		{"3d3c42e5aac5ba805825da76410c181273ba90b1", "4d101475d8b20a2381f78447822ac1eab6504dd8", JumpDigest},
	}
	for _, c := range cases {
		if got := Classify(c.from, c.to); got != c.want {
			t.Errorf("Classify(%q, %q) = %q, want %q", c.from, c.to, got, c.want)
		}
	}
}

func TestClassifyCallsADowngradeOrNonVersionUnknown(t *testing.T) {
	cases := [][2]string{
		{"7.1.1", "6.21.4"}, // major down
		{"1.5.0", "1.4.9"},  // minor down
		{"0.12.0", "0.6.0"}, // pre-1.0 minor down
		{"1.2.3", "1.2.3"},  // no move
		{"1.2.4", "1.2.3"},  // patch down
		{"latest", "1.2.3"}, // not a version
		{"1.2.3", ""},       // missing
		{"1.x.3", "1.2.3"},  // not numeric
	}
	for _, c := range cases {
		if got := Classify(c[0], c[1]); got != JumpUnknown {
			t.Errorf("Classify(%q, %q) = %q, want unknown", c[0], c[1], got)
		}
	}
}
