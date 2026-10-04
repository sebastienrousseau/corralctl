// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package symbols

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestMapCtagsKind(t *testing.T) {
	cases := []struct {
		raw  string
		want Kind
	}{
		{"f", KindFunc},
		{"func", KindFunc},
		{"function", KindFunc},
		{"sub", KindFunc},
		{"subroutine", KindFunc},
		{"procedure", KindFunc},
		{"m", KindMethod},
		{"method", KindMethod},
		{"c", KindType},
		{"class", KindType},
		{"s", KindType},
		{"struct", KindType},
		{"e", KindType},
		{"enum", KindType},
		{"t", KindType},
		{"type", KindType},
		{"typedef", KindType},
		{"module", KindType},
		{"record", KindType},
		{"i", KindInterface},
		{"interface", KindInterface},
		{"protocol", KindInterface},
		{"trait", KindInterface},
		{"d", KindConst},
		{"macro", KindConst},
		{"define", KindConst},
		{"constant", KindConst},
		{"const", KindConst},
		{"v", KindVar},
		{"variable", KindVar},
		{"var", KindVar},
		{"field", KindVar},
		{"property", KindVar},
		{"member", KindVar},
		{"unknown_kind", KindFunc},
	}
	for _, tc := range cases {
		if got := MapCtagsKind(tc.raw); got != tc.want {
			t.Errorf("MapCtagsKind(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestParseCtagsJSON(t *testing.T) {
	input := `
{"_type": "tag", "name": "User", "path": "models/user.rb", "line": 10, "kind": "class", "language": "Ruby"}
{"_type": "tag", "name": "find_by_id", "path": "models/user.rb", "line": 25, "kind": "method", "scope": "User", "scopeKind": "class"}
{"_type": "tag", "name": "_", "path": "ignore.rb", "line": 1, "kind": "var"}
{"_type": "tag", "name": "", "path": "empty.rb", "line": 1}
invalid-json-line
{"_type": "tag", "name": "GLOBAL_LIMIT", "path": "consts.php", "line": 0, "kind": "const"}
`
	syms, err := ParseCtagsJSON(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseCtagsJSON error: %v", err)
	}
	if len(syms) != 3 {
		t.Fatalf("expected 3 symbols, got %d: %+v", len(syms), syms)
	}

	if syms[0].Name != "User" || syms[0].Kind != KindType || syms[0].Language != "ruby" || syms[0].Line != 10 {
		t.Errorf("unexpected syms[0]: %+v", syms[0])
	}
	if syms[1].Name != "find_by_id" || syms[1].Kind != KindMethod || syms[1].Receiver != "User" || syms[1].Line != 25 {
		t.Errorf("unexpected syms[1]: %+v", syms[1])
	}
	if syms[2].Name != "GLOBAL_LIMIT" || syms[2].Kind != KindConst || syms[2].Language != "php" || syms[2].Line != 1 {
		t.Errorf("unexpected syms[2]: %+v", syms[2])
	}
}

func TestParseCtagsTags(t *testing.T) {
	input := `!_TAG_FILE_FORMAT	2	/extended format/
!_TAG_FILE_SORTED	1	/0=unsorted, 1=sorted/

User	models/user.rb	15;"	c
find_by_id	models/user.rb	30;"	m	class:User
login	auth/session.rb	/^def login$/;"	f
MAX_ATTEMPTS	config/limits.rb	5;"	kind:constant
Counter	stats.rb	20;"	struct:Metrics
shortline	onlytwotabs
`
	syms, err := ParseCtagsTags(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseCtagsTags error: %v", err)
	}
	if len(syms) != 5 {
		t.Fatalf("expected 5 symbols, got %d: %+v", len(syms), syms)
	}

	if syms[0].Name != "User" || syms[0].Kind != KindType || syms[0].Line != 15 || syms[0].File != "models/user.rb" {
		t.Errorf("unexpected tag[0]: %+v", syms[0])
	}
	if syms[1].Name != "find_by_id" || syms[1].Kind != KindMethod || syms[1].Receiver != "User" {
		t.Errorf("unexpected tag[1]: %+v", syms[1])
	}
	if syms[2].Name != "login" || syms[2].Kind != KindFunc || syms[2].Line != 1 {
		t.Errorf("unexpected tag[2]: %+v", syms[2])
	}
	if syms[3].Name != "MAX_ATTEMPTS" || syms[3].Kind != KindConst || syms[3].Line != 5 {
		t.Errorf("unexpected tag[3]: %+v", syms[3])
	}
	if syms[4].Name != "Counter" || syms[4].Receiver != "Metrics" {
		t.Errorf("unexpected tag[4]: %+v", syms[4])
	}

	// No extension file
	noExtInput := "RunTask\truntask\t10;\"\tf\n"
	noExtSyms, err := ParseCtagsTags(strings.NewReader(noExtInput))
	if err != nil || len(noExtSyms) != 1 || noExtSyms[0].Language != "ctags" {
		t.Fatalf("unexpected noExtSyms: %v, err: %v", noExtSyms, err)
	}
}

func TestCtagsExtractor(t *testing.T) {
	origExec := execCtags
	defer func() { execCtags = origExec }()

	ext := CtagsExtractor{}
	if ext.Language() != "ctags" {
		t.Errorf("Language() = %q, want ctags", ext.Language())
	}
	if ext.Extensions() != nil {
		t.Errorf("Extensions() = %v, want nil", ext.Extensions())
	}

	// 1. Success execution with output
	execCtags = func(ctx context.Context, cmd, path string, src []byte) ([]byte, error) {
		if cmd != "ctags" {
			t.Errorf("cmd = %q, want ctags", cmd)
		}
		jsonOutput := `{"_type": "tag", "name": "helper", "path": "-", "line": 4, "kind": "func"}`
		return []byte(jsonOutput), nil
	}

	syms, err := ext.Extract("lib/helper.rb", []byte("def helper; end"))
	if err != nil {
		t.Fatalf("Extract error: %v", err)
	}
	if len(syms) != 1 || syms[0].Name != "helper" || syms[0].File != "lib/helper.rb" {
		t.Fatalf("unexpected syms: %+v", syms)
	}

	// 2. Command execution failure degrades gracefully
	execCtags = func(ctx context.Context, cmd, path string, src []byte) ([]byte, error) {
		return nil, errors.New("ctags not installed")
	}
	syms, err = ext.Extract("lib/helper.rb", []byte("def helper; end"))
	if err != nil || len(syms) != 0 {
		t.Fatalf("expected nil symbols on exec error, got %v, err %v", syms, err)
	}

	// 3. Test default execCtags with a non-existent command
	if _, err := origExec(context.Background(), "nonexistent-ctags-binary-xyz", "foo", []byte("src")); err == nil {
		t.Fatal("expected exec error for nonexistent command, got nil")
	}
}

func TestFallbackExtractorRegistration(t *testing.T) {
	defer func() {
		SetFallbackExtractor(nil)
	}()

	if fb := GetFallbackExtractor(); fb != nil {
		t.Fatalf("expected initial nil fallback, got %v", fb)
	}

	// Unregistered extension returns false initially
	if _, ok := ExtractorFor("test.custom"); ok {
		t.Fatal("expected test.custom to have no extractor")
	}

	customExt := CtagsExtractor{Command: "custom-tags"}
	SetFallbackExtractor(customExt)

	if fb := GetFallbackExtractor(); fb == nil || fb.Language() != "ctags" {
		t.Fatalf("expected fallback extractor registered, got %v", fb)
	}

	// Now unregistered extension resolves to fallback
	e, ok := ExtractorFor("test.custom")
	if !ok || e.Language() != "ctags" {
		t.Fatalf("expected test.custom to resolve to fallback, got %v, ok %v", e, ok)
	}

	// Built-in .go still resolves to goExtractor
	eGo, ok := ExtractorFor("main.go")
	if !ok || eGo.Language() != "go" {
		t.Fatalf("expected main.go to resolve to go, got %v, ok %v", eGo, ok)
	}

	// Reset fallback
	SetFallbackExtractor(nil)
	if _, ok := ExtractorFor("test.custom"); ok {
		t.Fatal("expected test.custom to have no extractor after reset")
	}
}
