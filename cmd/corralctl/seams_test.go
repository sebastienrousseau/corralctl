// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package main

import (
	"os"
	"reflect"
	"testing"

	"github.com/sebastienrousseau/corralctl/cmd"
	"github.com/sebastienrousseau/corralctl/internal/git"
)

// TestMainSeamsBindToRealImplementations is the smallest and most important of
// the seam tests.
//
// Mutation testing found that replacing executeContext with a no-op turned the
// entire binary into a program that does nothing — and the suite still reported
// success at 100% coverage. If this file had existed, that mutant would have
// died immediately.
func TestMainSeamsBindToRealImplementations(t *testing.T) {
	cases := []struct {
		name string
		got  any
		want any
	}{
		{"resolveGitBinary", resolveGitBinary, git.ResolveGitBinary},
		{"executeContext", executeContext, cmd.ExecuteContext},
		{"exitMain", exitMain, os.Exit},
	}
	for _, tc := range cases {
		if reflect.ValueOf(tc.got).Pointer() != reflect.ValueOf(tc.want).Pointer() {
			t.Errorf("%s is not bound to its production implementation "+
				"(a stub leaked out of a test, or the default was changed)", tc.name)
		}
	}
}
