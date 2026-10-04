// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package symbols

import (
	"fmt"
	"strings"
	"testing"
)

// Tests for the line-scanned languages.
//
// Each is driven with source shaped the way real source is shaped —
// decorators, docstrings, template literals, attribute macros — because the
// failure mode of a line scanner is never the tidy case. Every fixture
// therefore carries at least one trap: a keyword inside a comment or a
// string, which must not become a symbol.

// found looks a symbol up by qualified name and asserts everything about
// it at once. Returning the symbol lets a caller make extra assertions
// without a second lookup.
func found(t *testing.T, syms []Symbol, qualified string, kind Kind, line int) Symbol {
	t.Helper()
	for _, s := range syms {
		if s.Qualified() == qualified {
			if s.Kind != kind {
				t.Errorf("%s: kind = %q, want %q", qualified, s.Kind, kind)
			}
			if s.Line != line {
				t.Errorf("%s: line = %d, want %d", qualified, s.Line, line)
			}
			return s
		}
	}
	t.Errorf("%s not found; got %s", qualified, render(syms))
	return Symbol{}
}

// absent asserts a name was not extracted. This is the assertion that
// matters most for a scanner: a false positive is a confident lie, and
// every fixture plants one.
func absent(t *testing.T, syms []Symbol, name string) {
	t.Helper()
	for _, s := range syms {
		if s.Name == name {
			t.Errorf("%q should not have been extracted (line %d, kind %s)", name, s.Line, s.Kind)
		}
	}
}

func render(syms []Symbol) string {
	var b strings.Builder
	for _, s := range syms {
		fmt.Fprintf(&b, "\n  %d %-9s %s", s.Line, s.Kind, s.Qualified())
	}
	if b.Len() == 0 {
		return " (nothing)"
	}
	return b.String()
}

func extract(t *testing.T, path, src string) []Symbol {
	t.Helper()
	e, ok := ExtractorFor(path)
	if !ok {
		t.Fatalf("no extractor claims %q", path)
	}
	syms, err := e.Extract(path, []byte(src))
	if err != nil {
		t.Fatalf("extract %s: %v", path, err)
	}
	return syms
}

// ---------------------------------------------------------------------------
// Python
// ---------------------------------------------------------------------------

const pythonSource = `"""Module docstring.

class NotAClass:
    def not_a_method(self): pass
"""
import os

MAX_RETRIES = 3
_private_default = None
timeout: float = 1.5


class Client:
    """A client.

    def also_not_a_method(self): ...
    """

    DEFAULT_PORT = 443

    def __init__(self, host):
        self.host = host
        local_only = 1

    @property
    def host_name(self):
        return self.host

    async def fetch(self, path):
        return await self._get(path)

    class Nested:
        def inner(self):
            pass


class Reader(Protocol):
    def read(self, n: int) -> bytes: ...


def module_level(a, b):
    # def commented_out(): pass
    label = "class StringClass:"
    return a + b


async def amain():
    pass
`

func TestPythonExtractor(t *testing.T) {
	syms := extract(t, "client.py", pythonSource)

	found(t, syms, "MAX_RETRIES", KindConst, 8)
	found(t, syms, "_private_default", KindVar, 9)
	found(t, syms, "timeout", KindVar, 10)

	found(t, syms, "Client", KindType, 13)
	found(t, syms, "Client.DEFAULT_PORT", KindConst, 19)
	found(t, syms, "Client.__init__", KindMethod, 21)
	found(t, syms, "Client.host_name", KindMethod, 26)
	found(t, syms, "Client.fetch", KindMethod, 29)

	// A class nested in a class owns its own methods.
	found(t, syms, "Nested", KindType, 32)
	found(t, syms, "Nested.inner", KindMethod, 33)

	// A Protocol is an interface, because that is the question people ask.
	found(t, syms, "Reader", KindInterface, 37)
	found(t, syms, "Reader.read", KindMethod, 38)

	found(t, syms, "module_level", KindFunc, 41)
	found(t, syms, "amain", KindFunc, 47)

	// The traps: a docstring, a comment, and a string literal.
	absent(t, syms, "NotAClass")
	absent(t, syms, "not_a_method")
	absent(t, syms, "also_not_a_method")
	absent(t, syms, "commented_out")
	absent(t, syms, "StringClass")
	// A local inside a function body is not a workspace-level symbol.
	absent(t, syms, "local_only")
	// `self.host = host` is an attribute assignment, not a declaration; it
	// has a dot before the `=`, so the identifier scan stops at `self`.
	absent(t, syms, "self")
}

func TestPythonExportedFollowsTheUnderscoreConvention(t *testing.T) {
	syms := extract(t, "m.py", "def public():\n    pass\n\ndef _private():\n    pass\n")
	if !found(t, syms, "public", KindFunc, 1).Exported {
		t.Error("a name without a leading underscore is the public surface")
	}
	if found(t, syms, "_private", KindFunc, 4).Exported {
		t.Error("a leading underscore means private, by the convention Python actually uses")
	}
}

func TestPythonTestDetection(t *testing.T) {
	for _, path := range []string{"test_client.py", "client_test.py", "tests/helpers.py", "a/test/b.py"} {
		syms := extract(t, path, "def f():\n    pass\n")
		if len(syms) != 1 || !syms[0].Test {
			t.Errorf("%s should be recognised as a test module", path)
		}
	}
	syms := extract(t, "src/client.py", "def f():\n    pass\n")
	if syms[0].Test {
		t.Error("src/client.py is not a test module")
	}
}

func TestPythonStubsAreIndexed(t *testing.T) {
	// A typed library's only readable declarations are often its stubs.
	syms := extract(t, "lib.pyi", "class Config:\n    def load(self) -> None: ...\n")
	found(t, syms, "Config", KindType, 1)
	found(t, syms, "Config.load", KindMethod, 2)
}

// ---------------------------------------------------------------------------
// TypeScript / JavaScript
// ---------------------------------------------------------------------------

const tsSource = "/*\n" +
	" * class NotAClass {}\n" +
	" * export function notAFunction() {}\n" +
	" */\n" +
	"import { thing } from './thing';\n" +
	"\n" +
	"export const MAX = 10;\n" +
	"let counter = 0;\n" +
	"const template = `\n" +
	"  export class TemplateClass {}\n" +
	"  function templateFunction() {}\n" +
	"`;\n" +
	"\n" +
	"export interface Options {\n" +
	"  retries: number;\n" +
	"}\n" +
	"\n" +
	"export type Result<T> = T | Error;\n" +
	"\n" +
	"export enum Level {\n" +
	"  Low,\n" +
	"}\n" +
	"\n" +
	"export function parse(input: string): Options {\n" +
	"  // function commentedOut() {}\n" +
	"  const label = 'class StringClass {}';\n" +
	"  return JSON.parse(input);\n" +
	"}\n" +
	"\n" +
	"export const handler = async (req) => {\n" +
	"  return null;\n" +
	"};\n" +
	"\n" +
	"const legacy = function () {};\n" +
	"\n" +
	"export default class Client {\n" +
	"  private secret: string;\n" +
	"\n" +
	"  constructor(host: string) {\n" +
	"    this.host = host;\n" +
	"  }\n" +
	"\n" +
	"  get host(): string {\n" +
	"    return this._host;\n" +
	"  }\n" +
	"\n" +
	"  async fetch<T>(path: string): Promise<T> {\n" +
	"    if (path) {\n" +
	"      return null;\n" +
	"    }\n" +
	"    for (let i = 0; i < 1; i++) {}\n" +
	"    return null;\n" +
	"  }\n" +
	"\n" +
	"  static create(): Client {\n" +
	"    return new Client('');\n" +
	"  }\n" +
	"}\n"

func TestTypeScriptExtractor(t *testing.T) {
	syms := extract(t, "src/client.ts", tsSource)

	found(t, syms, "MAX", KindConst, 7)
	found(t, syms, "counter", KindVar, 8)
	found(t, syms, "template", KindConst, 9)

	found(t, syms, "Options", KindInterface, 14)
	found(t, syms, "Result", KindType, 18)
	found(t, syms, "Level", KindType, 20)
	found(t, syms, "parse", KindFunc, 24)

	// An arrow function bound to a const is a function, because that is
	// what anyone looking for it calls it.
	found(t, syms, "handler", KindFunc, 30)
	found(t, syms, "legacy", KindFunc, 34)

	found(t, syms, "Client", KindType, 36)
	found(t, syms, "Client.host", KindMethod, 43)
	found(t, syms, "Client.fetch", KindMethod, 47)
	found(t, syms, "Client.create", KindMethod, 55)

	// The traps.
	absent(t, syms, "NotAClass")
	absent(t, syms, "notAFunction")
	absent(t, syms, "commentedOut")
	absent(t, syms, "StringClass")
	// A template literal spans lines and is full of plausible code.
	absent(t, syms, "TemplateClass")
	absent(t, syms, "templateFunction")
	// Control flow inside a method body has a method's exact shape.
	absent(t, syms, "if")
	absent(t, syms, "for")
	// Every class has one and nobody searches a workspace for it.
	absent(t, syms, "constructor")
}

func TestTypeScriptExportIsTheVisibleSurface(t *testing.T) {
	syms := extract(t, "m.ts",
		"export function shown() {}\nfunction hidden() {}\nexport default function alsoShown() {}\n")
	if !found(t, syms, "shown", KindFunc, 1).Exported {
		t.Error("an exported function is part of the visible surface")
	}
	if found(t, syms, "hidden", KindFunc, 2).Exported {
		t.Error("a module-private function is not exported")
	}
	if !found(t, syms, "alsoShown", KindFunc, 3).Exported {
		t.Error("export default is still an export")
	}
}

func TestTypeScriptPrivateMembersAreNotExported(t *testing.T) {
	syms := extract(t, "m.ts", "class C {\n  private hide() {}\n  public show() {}\n}\n")
	if found(t, syms, "C.hide", KindMethod, 2).Exported {
		t.Error("a private member is not the visible surface")
	}
	if !found(t, syms, "C.show", KindMethod, 3).Exported {
		t.Error("a public member is")
	}
}

func TestJavaScriptReportsItsOwnLanguage(t *testing.T) {
	for path, want := range map[string]string{
		"a.js": "javascript", "a.mjs": "javascript", "a.cjs": "javascript", "a.jsx": "javascript",
		"a.ts": "typescript", "a.tsx": "typescript", "a.mts": "typescript",
	} {
		syms := extract(t, path, "function f() {}\n")
		if len(syms) != 1 {
			t.Fatalf("%s: got %d symbols", path, len(syms))
		}
		if syms[0].Language != want {
			t.Errorf("%s: language = %q, want %q", path, syms[0].Language, want)
		}
	}
}

func TestTypeScriptTestDetection(t *testing.T) {
	for _, path := range []string{"a.test.ts", "b.spec.js", "__tests__/c.ts", "test/d.ts"} {
		syms := extract(t, path, "function f() {}\n")
		if !syms[0].Test {
			t.Errorf("%s should be recognised as a test file", path)
		}
	}
	if extract(t, "src/app.ts", "function f() {}\n")[0].Test {
		t.Error("src/app.ts is not a test file")
	}
}

func TestTypeScriptTypeKeywordNeedsAnAssignment(t *testing.T) {
	// `type` is not a reserved word; a variable may be called it, and a
	// property may be named it. Neither declares a type alias.
	syms := extract(t, "m.ts", "const type = 1;\nlet x = { type: 'a' };\ntype Real = string;\n")
	found(t, syms, "Real", KindType, 3)
	if len(syms) != 3 {
		t.Errorf("expected exactly three symbols, got %s", render(syms))
	}
}

// ---------------------------------------------------------------------------
// Rust
// ---------------------------------------------------------------------------

const rustSource = `//! Crate docs.
//! struct NotAStruct;

/* fn not_a_function() {}
   /* nested, and still a comment */
   struct AlsoNot;
*/

use std::fmt;

pub const MAX_RETRIES: usize = 3;
static GLOBAL: &str = "struct StringStruct;";

pub struct Client {
    host: String,
}

pub enum Level {
    Low,
}

pub trait Fetch {
    fn fetch(&self, path: &str) -> String;
}

pub type Alias = Client;

impl Client {
    pub fn new(host: String) -> Self {
        Client { host }
    }

    pub const fn port(&self) -> u16 {
        443
    }
}

impl<T> Fetch for Client {
    fn fetch(&self, path: &str) -> String {
        // fn commented_out() {}
        String::new()
    }
}

pub async unsafe fn danger() {}

macro_rules! shout {
    () => {};
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn covers_new() {}
}

pub fn after_the_test_module() {}
`

func TestRustExtractor(t *testing.T) {
	syms := extract(t, "src/lib.rs", rustSource)

	found(t, syms, "MAX_RETRIES", KindConst, 11)
	found(t, syms, "GLOBAL", KindConst, 12)
	found(t, syms, "Client", KindType, 14)
	found(t, syms, "Level", KindType, 18)
	found(t, syms, "Fetch", KindInterface, 22)
	found(t, syms, "Alias", KindType, 26)

	// An inherent impl gives its functions a receiver.
	found(t, syms, "Client.new", KindMethod, 29)
	// `const fn` is a function, not a constant.
	found(t, syms, "Client.port", KindMethod, 33)
	// A trait impl belongs to the type, not the trait.
	found(t, syms, "Client.fetch", KindMethod, 39)

	found(t, syms, "danger", KindFunc, 45)
	// A macro is invoked like a function and looked for like one.
	found(t, syms, "shout", KindFunc, 47)

	// The traps.
	absent(t, syms, "NotAStruct")
	absent(t, syms, "not_a_function")
	absent(t, syms, "AlsoNot")
	absent(t, syms, "StringStruct")
	absent(t, syms, "commented_out")
}

// TestRustInlineTestModuleIsMarked is the case a filename cannot reveal:
// Rust's unit tests live inside the file they test.
func TestRustInlineTestModuleIsMarked(t *testing.T) {
	syms := extract(t, "src/lib.rs", rustSource)

	if !found(t, syms, "covers_new", KindFunc, 56).Test {
		t.Error("a fn inside #[cfg(test)] mod is test code")
	}
	if found(t, syms, "Client.new", KindMethod, 29).Test {
		t.Error("a fn before the test module is not")
	}
	// And the marking stops when the module does, rather than tainting the
	// rest of the file.
	if found(t, syms, "after_the_test_module", KindFunc, 59).Test {
		t.Error("the test module's scope should have closed")
	}
}

func TestRustPubIsTheVisibleSurface(t *testing.T) {
	syms := extract(t, "src/lib.rs",
		"pub fn shown() {}\nfn hidden() {}\npub(crate) fn narrower() {}\n")
	if !found(t, syms, "shown", KindFunc, 1).Exported {
		t.Error("pub is exported")
	}
	if found(t, syms, "hidden", KindFunc, 2).Exported {
		t.Error("a private fn is not")
	}
	if !found(t, syms, "narrower", KindFunc, 3).Exported {
		t.Error("pub(crate) is still a declared surface, not a private detail")
	}
}

func TestRustImplTargetStripsGenericsAndPaths(t *testing.T) {
	for _, tc := range []struct{ line, want string }{
		{"impl Client {", "Client"},
		{"impl<T> Client<T> {", "Client"},
		{"impl<T: Debug> fmt::Display for Client<T> {", "Client"},
		{"impl Fetch for crate::client::Client {", "Client"},
		{"impl<'a> Parser<'a> where T: Send {", "Parser"},
	} {
		src := tc.line + "\n    fn m(&self) {}\n}\n"
		syms := extract(t, "src/lib.rs", src)
		got := found(t, syms, tc.want+".m", KindMethod, 2)
		if got.Receiver != tc.want {
			t.Errorf("%q: receiver = %q, want %q", tc.line, got.Receiver, tc.want)
		}
	}
}

func TestRustIntegrationTestsAreMarked(t *testing.T) {
	if !extract(t, "tests/api.rs", "fn helper() {}\n")[0].Test {
		t.Error("tests/ holds integration tests")
	}
	if !extract(t, "benches/speed.rs", "fn bench() {}\n")[0].Test {
		t.Error("benches/ is not production code either")
	}
	if extract(t, "src/lib.rs", "fn f() {}\n")[0].Test {
		t.Error("src/lib.rs is not a test")
	}
}

// ---------------------------------------------------------------------------
// the blanking pass, which every scanner above depends on
// ---------------------------------------------------------------------------

// TestBlankNonCodePreservesPositions is the invariant that makes every
// reported line number trustworthy: blanking may change bytes, never
// offsets.
func TestBlankNonCodePreservesPositions(t *testing.T) {
	for name, tc := range map[string]struct {
		src   string
		style commentStyle
	}{
		"python":     {pythonSource, pythonStyle},
		"typescript": {tsSource, tsStyle},
		"rust":       {rustSource, rustStyle},
	} {
		t.Run(name, func(t *testing.T) {
			got := blankNonCode([]byte(tc.src), tc.style)
			if len(got) != len(tc.src) {
				t.Fatalf("length changed: %d, want %d", len(got), len(tc.src))
			}
			if a, b := strings.Count(string(got), "\n"), strings.Count(tc.src, "\n"); a != b {
				t.Fatalf("line count changed: %d, want %d", a, b)
			}
		})
	}
}

func TestBlankNonCodeHandlesEscapesAndUnterminatedStrings(t *testing.T) {
	// A backslash-escaped quote does not close the string, so `class Real`
	// after it is still inside and must stay hidden.
	src := `const a = "he said \"class Hidden\"";
class Real {}
`
	syms := extract(t, "m.ts", src)
	absent(t, syms, "Hidden")
	found(t, syms, "Real", KindType, 2)

	// An unterminated string is a syntax error, and must not blank the
	// rest of the file — source in a workspace is frequently mid-edit.
	syms = extract(t, "m2.ts", "const broken = \"oops\nclass StillFound {}\n")
	found(t, syms, "StillFound", KindType, 2)
}

func TestBlankNonCodeNestsRustBlockComments(t *testing.T) {
	// The inner close must not end the outer comment.
	syms := extract(t, "l.rs", "/* outer /* inner */ struct Hidden; */\nstruct Real;\n")
	absent(t, syms, "Hidden")
	found(t, syms, "Real", KindType, 2)
}

// TestScannersSurviveGarbage pins the same tolerance the Go extractor has:
// no input may produce an error, because one unparseable file must never
// blank a repository's index.
func TestScannersSurviveGarbage(t *testing.T) {
	garbage := []string{
		"", "\n\n\n", "\x00\x01\x02", strings.Repeat("{", 5000),
		"def", "class", "fn", "function", "impl",
		"def (", "class :", "\"unterminated", "'''unterminated",
		"/*", "`", strings.Repeat("def f():\n", 2000),
	}
	for _, path := range []string{"g.py", "g.ts", "g.js", "g.rs"} {
		e, ok := ExtractorFor(path)
		if !ok {
			t.Fatalf("no extractor for %s", path)
		}
		for i, src := range garbage {
			syms, err := e.Extract(path, []byte(src))
			if err != nil {
				t.Errorf("%s garbage[%d]: unexpected error %v", path, i, err)
			}
			for _, s := range syms {
				if s.Line < 1 {
					t.Errorf("%s garbage[%d]: symbol %q at line %d", path, i, s.Name, s.Line)
				}
			}
		}
	}
}

// TestEveryScannedLanguageIsRegistered guards the registry against a new
// extractor being written and never wired up — the failure mode is silence,
// not an error.
func TestEveryScannedLanguageIsRegistered(t *testing.T) {
	want := map[string]bool{
		"c": true, "cpp": true, "go": true, "java": true,
		"javascript": true, "kotlin": true, "protobuf": true,
		"python": true, "rust": true, "swift": true, "typescript": true,
	}
	got := map[string]bool{}
	for _, l := range Languages() {
		got[l] = true
	}
	for l := range want {
		if !got[l] {
			t.Errorf("%s is not registered; Languages() = %v", l, Languages())
		}
	}
}

// ---------------------------------------------------------------------------
// the edges each scanner has to get right
// ---------------------------------------------------------------------------

func TestPythonAssignmentEdges(t *testing.T) {
	syms := extract(t, "m.py", strings.Join([]string{
		"NAME = 1",           // a plain binding
		"ANNOTATED: int = 2", // annotated with a value
		"BARE: int",          // annotated with none
		"if NAME == 1:",      // a comparison, not a binding
		"    pass",
		"a, b = 1, 2",    // tuple unpacking, deliberately not handled
		"result",         // an expression statement
		"obj.attr = 3",   // an attribute, not a declaration
		"lower_case = 4", // a variable by convention
		"MixedCase = 5",  // not SCREAMING_CASE, so a variable
		"_1 = 6",         // no letters at all
	}, "\n")+"\n")

	found(t, syms, "NAME", KindConst, 1)
	found(t, syms, "ANNOTATED", KindConst, 2)
	found(t, syms, "BARE", KindConst, 3)
	found(t, syms, "lower_case", KindVar, 9)
	found(t, syms, "MixedCase", KindVar, 10)
	found(t, syms, "_1", KindVar, 11)

	// `NAME == 1` is a comparison. Reading it as a binding would declare a
	// symbol on a line that declares nothing.
	for _, s := range syms {
		if s.Name == "NAME" && s.Line != 1 {
			t.Errorf("NAME was extracted twice; the second at line %d is a comparison", s.Line)
		}
	}
	absent(t, syms, "obj")
	absent(t, syms, "result")
	// Tuple unpacking is knowingly out of scope; this pins that it fails
	// closed rather than picking an arbitrary name out of it.
	absent(t, syms, "a")
	absent(t, syms, "b")
}

func TestPythonProtocolDetection(t *testing.T) {
	for _, tc := range []struct {
		decl string
		want Kind
	}{
		{"class R(Protocol):", KindInterface},
		{"class R(ABC):", KindInterface},
		{"class R(metaclass=ABCMeta):", KindInterface},
		{"class R(Base):", KindType},
		{"class R:", KindType},
	} {
		syms := extract(t, "m.py", tc.decl+"\n    pass\n")
		found(t, syms, "R", tc.want, 1)
	}
}

func TestTypeScriptFunctionBindingEdges(t *testing.T) {
	syms := extract(t, "m.ts", strings.Join([]string{
		"const arrow = () => {};",
		"const asyncArrow = async () => {};",
		"const expr = function () {};",
		"const asyncExpr = async function () {};",
		"const plain = 42;",
		"const obj = { key: 'value' };",
		"let mutable = compute();",
		"var old = 1;",
		"const generator = function* () {};",
	}, "\n")+"\n")

	// Every one of these is a function to anybody looking for it, however
	// it happens to be spelled.
	for name, line := range map[string]int{
		"arrow": 1, "asyncArrow": 2, "expr": 3, "asyncExpr": 4, "generator": 9,
	} {
		found(t, syms, name, KindFunc, line)
	}
	found(t, syms, "plain", KindConst, 5)
	found(t, syms, "obj", KindConst, 6)
	found(t, syms, "mutable", KindVar, 7)
	found(t, syms, "old", KindVar, 8)
}

func TestTypeScriptGeneratorAndStaticShapes(t *testing.T) {
	syms := extract(t, "m.ts", "export function* gen() {}\nclass C {\n  static async *stream() {}\n}\n")
	found(t, syms, "gen", KindFunc, 1)
	found(t, syms, "C", KindType, 2)
}

func TestRustBareTypeNameEdges(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"Client", "Client"},
		{"&Client", "Client"},
		{"&mut Client", "Client"},
		{"crate::a::b::Client", "Client"},
		{"Client<T, U>", "Client"},
		{"  Client  ", "Client"},
		{"", ""},
		{"<T>", ""},
	} {
		if got := rustBareTypeName(tc.in); got != tc.want {
			t.Errorf("rustBareTypeName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRustAnonymousImplDoesNotClaimFunctions(t *testing.T) {
	// An impl whose target cannot be read must not silently attach every
	// function inside it to the empty receiver.
	syms := extract(t, "l.rs", "impl {\n    fn orphan() {}\n}\n")
	got := found(t, syms, "orphan", KindFunc, 2)
	if got.Receiver != "" {
		t.Errorf("receiver = %q, want empty", got.Receiver)
	}
}

func TestKeywordAtRespectsWordBoundaries(t *testing.T) {
	// Without the boundary check, `classes` declares a class and
	// `defaults` declares a function.
	for _, tc := range []struct {
		line, kw string
		want     bool
	}{
		{"class C", "class", true},
		{"classes = 1", "class", false},
		{"myclass C", "class", false},
		{"class", "class", true},
		{"cla", "class", false},
	} {
		_, got := keywordAt(tc.line, 0, tc.kw)
		if got != tc.want {
			t.Errorf("keywordAt(%q, %q) = %t, want %t", tc.line, tc.kw, got, tc.want)
		}
	}
}

// TestPythonComparisonIsNotABinding covers the one case where `=` at
// statement level does not declare anything.
func TestPythonComparisonIsNotABinding(t *testing.T) {
	syms := extract(t, "m.py", "VALUE == 1\nVALUE = 1\n")
	// Exactly one VALUE, from the assignment on line 2.
	n := 0
	for _, s := range syms {
		if s.Name == "VALUE" {
			n++
			if s.Line != 2 {
				t.Errorf("VALUE extracted at line %d; line 1 is a comparison", s.Line)
			}
		}
	}
	if n != 1 {
		t.Errorf("VALUE extracted %d times, want 1", n)
	}
}

func TestRustModifierShapes(t *testing.T) {
	syms := extract(t, "l.rs", strings.Join([]string{
		`pub extern "C" fn ffi() {}`,
		"pub static mut COUNTER: usize = 0;",
		"pub const fn compile_time() -> u8 { 0 }",
		"pub const LIMIT: usize = 9;",
		"pub unsafe extern fn raw() {}",
	}, "\n")+"\n")

	found(t, syms, "ffi", KindFunc, 1)
	found(t, syms, "COUNTER", KindConst, 2)
	// `const fn` is a function; a bare `const` is a constant. The word is
	// the same and only what follows it tells them apart.
	found(t, syms, "compile_time", KindFunc, 3)
	found(t, syms, "LIMIT", KindConst, 4)
	found(t, syms, "raw", KindFunc, 5)
}

func TestKeywordAtAtStringEnd(t *testing.T) {
	// A keyword at the very end of a line has no following byte to check,
	// which is its own branch.
	if _, ok := keywordAt("pub", 0, "pub"); !ok {
		t.Error("a keyword ending the line is still a keyword")
	}
	if _, ok := keywordAt("ab", 0, "abc"); ok {
		t.Error("a keyword longer than the line cannot match")
	}
}

func TestTypeScriptBindingWithoutAName(t *testing.T) {
	// Destructuring binds names this scanner deliberately does not read,
	// and it must decline rather than invent one.
	syms := extract(t, "m.ts", "const { a, b } = obj;\nconst [x] = arr;\n")
	absent(t, syms, "a")
	absent(t, syms, "b")
	absent(t, syms, "x")
	if len(syms) != 0 {
		t.Errorf("destructuring should yield nothing, got %s", render(syms))
	}
}

func TestTypeScriptDeclarationWithNoAssignment(t *testing.T) {
	// `let x;` and a class field with a type but no value: a binding with
	// no right-hand side at all.
	syms := extract(t, "m.ts", "let pending;\nclass C {\n  field: string;\n}\n")
	found(t, syms, "pending", KindVar, 1)
	found(t, syms, "C", KindType, 2)
}

// TestTsExtractorLanguageMethod covers the Extractor-interface method,
// which the registry requires even though per-file reporting uses
// languageFor.
func TestTsExtractorLanguageMethod(t *testing.T) {
	if got := (tsExtractor{}).Language(); got != "typescript" {
		t.Errorf("Language = %q, want typescript", got)
	}
}

// TestKeywordAtRejectsASuffixMatch covers the left-hand word boundary:
// without it, `myclass` contains `class` at offset 2 and would declare one.
func TestKeywordAtRejectsASuffixMatch(t *testing.T) {
	if _, ok := keywordAt("myclass", 2, "class"); ok {
		t.Error("a keyword preceded by identifier characters is not a keyword")
	}
	if _, ok := keywordAt("my class", 3, "class"); !ok {
		t.Error("a keyword preceded by a space is one")
	}
}

// TestRustExternAbiStringIsAlreadyBlanked pins why the extern handling is
// as short as it is: by the time a scanner sees the line, the ABI string
// has been replaced with spaces.
func TestRustExternAbiStringIsAlreadyBlanked(t *testing.T) {
	line := codeLines([]byte(`pub extern "C" fn ffi() {}`), rustStyle)[0]
	if strings.Contains(line, `"`) {
		t.Errorf("the ABI string should have been blanked: %q", line)
	}
	found(t, extract(t, "l.rs", `pub extern "C" fn ffi() {}`+"\n"), "ffi", KindFunc, 1)
}

// TestRustAssociatedTypesBelongToTheirImpl: a bare `type Output = ...`
// appears in every impl of an operator trait in a crate, so reporting them
// unqualified collides them all into one name nobody searches for.
func TestRustAssociatedTypesBelongToTheirImpl(t *testing.T) {
	src := strings.Join([]string{
		"pub type TopLevel = u8;",
		"impl Add for DateTime {",
		"    type Output = Result<Self, Error>;",
		"    fn add(self, rhs: u8) -> Self::Output { self }",
		"}",
		"pub trait Parse {",
		"    type Err;",
		"}",
	}, "\n") + "\n"
	syms := extract(t, "l.rs", src)

	// A genuine top-level alias keeps its bare name.
	top := found(t, syms, "TopLevel", KindType, 1)
	if top.Receiver != "" {
		t.Errorf("a top-level alias has no receiver, got %q", top.Receiver)
	}
	found(t, syms, "DateTime.Output", KindType, 3)
	found(t, syms, "DateTime.add", KindMethod, 4)
	found(t, syms, "Parse.Err", KindType, 7)
}

func TestMultilineDecoratorsAndAttributes(t *testing.T) {
	// 1. Python multiline decorator
	pySrc := strings.Join([]string{
		`@route(`,
		`    "/api/users",`,
		``,
		`    methods=["GET", "POST"],`,
		`)`,
		`def list_users():`,
		`    pass`,
	}, "\n") + "\n"
	pySyms := extract(t, "api.py", pySrc)
	found(t, pySyms, "list_users", KindFunc, 6)
	absent(t, pySyms, "methods")
	absent(t, pySyms, "route")

	// 2. TypeScript multiline decorator
	tsSrc := strings.Join([]string{
		`@Component({`,
		`  selector: "app",`,
		``,
		`  template: "<div></div>",`,
		`})`,
		`export class AppComponent {`,
		`  title = "app";`,
		`}`,
	}, "\n") + "\n"
	tsSyms := extract(t, "app.component.ts", tsSrc)
	found(t, tsSyms, "AppComponent", KindType, 6)
	absent(t, tsSyms, "Component")
	absent(t, tsSyms, "selector")
	absent(t, tsSyms, "template")

	// 3. Rust multiline attribute
	rsSrc := strings.Join([]string{
		`#[derive(`,
		`    Debug,`,
		``,
		`    Clone,`,
		`)]`,
		`pub struct ServerConfig {`,
		`    pub port: u16,`,
		`}`,
		`#[cfg(`,
		`    test`,
		`)]`,
		`mod tests {`,
		`    fn helper() {}`,
		`}`,
	}, "\n") + "\n"
	rsSyms := extract(t, "config.rs", rsSrc)
	found(t, rsSyms, "ServerConfig", KindType, 6)
	h := found(t, rsSyms, "helper", KindFunc, 13)
	if !h.Test {
		t.Errorf("expected helper to be marked as test")
	}
	absent(t, rsSyms, "derive")
	absent(t, rsSyms, "Debug")
}

func TestDeltaAndDepthHelpers(t *testing.T) {
	if got := parenDelta("((a) + b)"); got != 0 {
		t.Fatalf("parenDelta = %d, want 0", got)
	}
	if got := bracketDelta("[[a], b]"); got != 0 {
		t.Fatalf("bracketDelta = %d, want 0", got)
	}
	if got := updateDepth(2, -5); got != 0 {
		t.Fatalf("updateDepth(2, -5) = %d, want 0", got)
	}
	if got := updateDepth(2, 3); got != 5 {
		t.Fatalf("updateDepth(2, 3) = %d, want 5", got)
	}
}

// ---------------------------------------------------------------------------
// C & C++
// ---------------------------------------------------------------------------

const cSource = `/* Comment with struct FakeStruct */
// Another comment with class FakeClass
#include <stdio.h>

#define BUFFER_SIZE 1024
#define _PRIVATE_MACRO 1
#define CLAMP(x, low, high) (((x) > (high)) ? (high) : (((x) < (low)) ? (low) : (x)))

struct ForwardDecl;
union ForwardUnion;

struct Point {
    int x;
    int y;
};

union DataValue {
    int ival;
    double dval;
};

enum Status {
    STATUS_OK,
    STATUS_ERR
};

static void private_helper(int a) {
    if (a > 0) {
        printf("positive\n");
    }
}

int calculate_area(int w, int h) {
    return w * h;
}
`

const cppSource = `// C++ example
#include <vector>
#include <string>

using StringList = std::vector<std::string>;

namespace graphics {

class BaseWidget; // Forward decl

class Widget {
public:
    Widget();
    virtual ~Widget();
    void show();
    bool operator==(const Widget &other);

private:
    void initInternal();
};

enum class Theme {
    Light,
    Dark
};

} // namespace graphics

void Widget::render() {
    // inside function call
    show();
}

int main(int argc, char **argv) {
    return 0;
}
`

func TestCPPExtractor(t *testing.T) {
	// 1. C source extraction
	cSyms := extract(t, "math.c", cSource)

	bs := found(t, cSyms, "BUFFER_SIZE", KindConst, 5)
	if !bs.Exported || bs.Language != "c" {
		t.Errorf("unexpected BUFFER_SIZE: %+v", bs)
	}

	priv := found(t, cSyms, "_PRIVATE_MACRO", KindConst, 6)
	if priv.Exported {
		t.Errorf("expected _PRIVATE_MACRO to not be exported: %+v", priv)
	}

	clamp := found(t, cSyms, "CLAMP", KindFunc, 7)
	if !clamp.Exported {
		t.Errorf("expected CLAMP to be exported: %+v", clamp)
	}

	pt := found(t, cSyms, "Point", KindType, 12)
	if !pt.Exported {
		t.Errorf("expected Point to be exported")
	}

	dv := found(t, cSyms, "DataValue", KindType, 17)
	if !dv.Exported {
		t.Errorf("expected DataValue to be exported")
	}

	st := found(t, cSyms, "Status", KindType, 22)
	if !st.Exported {
		t.Errorf("expected Status to be exported")
	}

	ph := found(t, cSyms, "private_helper", KindFunc, 27)
	if ph.Exported {
		t.Errorf("expected static function private_helper to not be exported")
	}

	ca := found(t, cSyms, "calculate_area", KindFunc, 33)
	if !ca.Exported {
		t.Errorf("expected calculate_area to be exported")
	}

	absent(t, cSyms, "FakeStruct")
	absent(t, cSyms, "FakeClass")
	absent(t, cSyms, "ForwardDecl")
	absent(t, cSyms, "ForwardUnion")
	absent(t, cSyms, "if")
	absent(t, cSyms, "printf")

	// 2. C++ source extraction
	cppSyms := extract(t, "widget.cpp", cppSource)

	sl := found(t, cppSyms, "StringList", KindType, 5)
	if !sl.Exported || sl.Language != "cpp" {
		t.Errorf("unexpected StringList: %+v", sl)
	}

	w := found(t, cppSyms, "Widget", KindType, 11)
	if !w.Exported {
		t.Errorf("expected Widget to be exported")
	}

	sh := found(t, cppSyms, "Widget.show", KindMethod, 15)
	if !sh.Exported || sh.Receiver != "Widget" {
		t.Errorf("unexpected Widget.show: %+v", sh)
	}

	eq := found(t, cppSyms, "Widget.operator==", KindMethod, 16)
	if !eq.Exported || eq.Receiver != "Widget" {
		t.Errorf("unexpected Widget.operator==: %+v", eq)
	}

	initInt := found(t, cppSyms, "Widget.initInternal", KindMethod, 19)
	if initInt.Exported {
		t.Errorf("expected private method initInternal to not be exported")
	}

	th := found(t, cppSyms, "Theme", KindType, 22)
	if !th.Exported {
		t.Errorf("expected Theme to be exported")
	}

	rnd := found(t, cppSyms, "Widget.render", KindMethod, 29)
	if !rnd.Exported || rnd.Receiver != "Widget" {
		t.Errorf("unexpected out-of-line method Widget.render: %+v", rnd)
	}

	mainFn := found(t, cppSyms, "main", KindFunc, 34)
	if !mainFn.Exported {
		t.Errorf("expected main to be exported")
	}

	absent(t, cppSyms, "BaseWidget")
	absent(t, cppSyms, "graphics")

	// 3. Extensions claiming
	for _, ext := range []string{".c", ".h"} {
		e, ok := ExtractorFor("file" + ext)
		if !ok || e.Language() != "c" {
			t.Errorf("expected c extractor for %s", ext)
		}
	}
	for _, ext := range []string{".cpp", ".hpp", ".cc", ".cxx", ".hh"} {
		e, ok := ExtractorFor("file" + ext)
		if !ok || e.Language() != "cpp" {
			t.Errorf("expected cpp extractor for %s", ext)
		}
	}

	// 4. Test naming detection
	testPaths := []string{
		"test_calc.c", "calc_test.cpp", "tests/foo.cc", "test/bar.cxx", "pkg/testing/baz.hh", "foo_unittest.hpp",
	}
	for _, p := range testPaths {
		if !cppIsTest(p) {
			t.Errorf("expected %s to be recognised as test", p)
		}
	}
	if cppIsTest("src/calc.cpp") {
		t.Error("src/calc.cpp should not be recognised as test")
	}

	// 5. C++ struct pending brace on next line
	pendingBraceSrc := `
struct
Config
{
    int port;
};
`
	pbSyms := extract(t, "config.hpp", pendingBraceSrc)
	found(t, pbSyms, "Config", KindType, 3)

	// 6. Access specifiers (protected)
	protSrc := `
class Account {
protected:
    void protectMe();
};
`
	protSyms := extract(t, "account.hpp", protSrc)
	pme := found(t, protSyms, "Account.protectMe", KindMethod, 4)
	if pme.Exported {
		t.Errorf("expected protected method to not be exported")
	}

	// 7. Additional C++ patterns
	extraCPPSrc := `
class
PendingClass
{
    void doWork() {
        // empty line inside function

        int temp = 1;
    }
};

bool operator +(const Point &a, const Point &b);

static_assert(sizeof(int) == 4, "int must be 4 bytes");

int top_val = calculate_area(2, 3);
(void)();
 (void)();
*();
`
	extraSyms := extract(t, "extra.cpp", extraCPPSrc)
	found(t, extraSyms, "PendingClass", KindType, 3)
	found(t, extraSyms, "PendingClass.doWork", KindMethod, 5)
	found(t, extraSyms, "operator+", KindFunc, 12)
	absent(t, extraSyms, "static_assert")
	absent(t, extraSyms, "calculate_area")

	if !cppReservedWord("for") || !cppReservedWord("while") || !cppReservedWord("case") {
		t.Error("expected reserved words to return true")
	}
	if cppReservedWord("myFunc") {
		t.Error("myFunc should not be reserved")
	}
}

// ---------------------------------------------------------------------------
// Protocol Buffers
// ---------------------------------------------------------------------------

const protoSource = `// Protocol Buffer example
/* Multi-line comment
   message FakeMessage {}
*/
syntax = "proto3";

package example.v1;

enum UserStatus {
    STATUS_UNKNOWN = 0;
    STATUS_ACTIVE = 1;
}

message User {
    string id = 1;
    string name = 2;
    UserStatus status = 3;

    message Profile {
        string bio = 1;
    }

    enum Role {
        ROLE_GUEST = 0;
        ROLE_ADMIN = 1;
    }
}

service UserService {
    rpc GetUser (GetUserRequest) returns (User);
    rpc UpdateUser (UpdateUserRequest) returns (User);
}
`

func TestProtoExtractor(t *testing.T) {
	syms := extract(t, "user.proto", protoSource)

	us := found(t, syms, "UserStatus", KindType, 9)
	if !us.Exported || us.Language != "protobuf" {
		t.Errorf("unexpected UserStatus: %+v", us)
	}

	u := found(t, syms, "User", KindType, 14)
	if !u.Exported {
		t.Errorf("expected User to be exported")
	}

	prof := found(t, syms, "User.Profile", KindType, 19)
	if !prof.Exported || prof.Receiver != "User" {
		t.Errorf("unexpected nested Profile message: %+v", prof)
	}

	role := found(t, syms, "User.Role", KindType, 23)
	if !role.Exported || role.Receiver != "User" {
		t.Errorf("unexpected nested Role enum: %+v", role)
	}

	svc := found(t, syms, "UserService", KindInterface, 29)
	if !svc.Exported {
		t.Errorf("expected UserService to be exported")
	}

	gu := found(t, syms, "UserService.GetUser", KindMethod, 30)
	if !gu.Exported || gu.Receiver != "UserService" {
		t.Errorf("unexpected GetUser rpc: %+v", gu)
	}

	uu := found(t, syms, "UserService.UpdateUser", KindMethod, 31)
	if !uu.Exported || uu.Receiver != "UserService" {
		t.Errorf("unexpected UpdateUser rpc: %+v", uu)
	}

	absent(t, syms, "FakeMessage")
	absent(t, syms, "proto3")
	absent(t, syms, "example.v1")

	// Extension
	e, ok := ExtractorFor("schema.proto")
	if !ok || e.Language() != "protobuf" {
		t.Errorf("expected protobuf extractor for schema.proto")
	}

	// Test naming
	if !protoIsTest("user_test.proto") || !protoIsTest("test_user.proto") || !protoIsTest("tests/user.proto") {
		t.Error("expected test convention matching for proto")
	}
	if protoIsTest("schema/user.proto") {
		t.Error("schema/user.proto should not be test")
	}
}

func TestJavaExtraction(t *testing.T) {
	src := `package com.example.service;

import java.util.List;

// class FakeClassInComment { }
@Deprecated
public abstract class UserService {
    public static final int MAX_COUNT = 100;
    private static final String SECRET_KEY = "xyz";

    public UserService() {
    }

    public synchronized void process(String item) {
        String msg = "class FakeInString";
        if (item != null) {
            return;
        }
    }

    protected int calculateTotal() {
        return 42;
    }

    private boolean isInternal() {
        return true;
    }
}

public interface DataHandler {
    void handle();
}

public enum Status {
    ACTIVE, INACTIVE
}

public record Profile(String id, String email) {
}

@interface Audited {
}
`
	syms := extract(t, "UserService.java", src)

	found(t, syms, "UserService", KindType, 7)
	found(t, syms, "UserService.MAX_COUNT", KindConst, 8)
	found(t, syms, "UserService.SECRET_KEY", KindConst, 9)
	found(t, syms, "UserService.UserService", KindMethod, 11)
	found(t, syms, "UserService.process", KindMethod, 14)
	found(t, syms, "UserService.calculateTotal", KindMethod, 21)
	found(t, syms, "UserService.isInternal", KindMethod, 25)
	found(t, syms, "DataHandler", KindInterface, 30)
	found(t, syms, "DataHandler.handle", KindMethod, 31)
	found(t, syms, "Status", KindType, 34)
	found(t, syms, "Profile", KindType, 38)
	found(t, syms, "Audited", KindInterface, 41)

	absent(t, syms, "FakeClassInComment")
	absent(t, syms, "FakeInString")

	// Test naming conventions
	if !jvmIsTest("src/test/java/UserServiceTest.java") ||
		!jvmIsTest("UserServiceTests.java") ||
		!jvmIsTest("UserServiceTestCase.java") ||
		!jvmIsTest("testService.java") ||
		!jvmIsTest("androidTest/Foo.java") ||
		!jvmIsTest("UserIT.java") {
		t.Error("expected Java test naming conventions to match")
	}
	if jvmIsTest("src/main/java/UserService.java") {
		t.Error("src/main/java/UserService.java should not be marked as test")
	}
}

func TestKotlinExtraction(t *testing.T) {
	src := `package com.example.app

// class FakeComment
@Target(AnnotationTarget.CLASS)
class AccountManager {

    val timeout = 3000
    var retryCount = 0

    fun authenticate() {
        val s = "fun fake()"
    }

    private fun validateCredentials() {
    }

    companion object {
        val DEFAULT_PORT = 8080
    }
}

interface Authenticator {
    fun verify()
}

object NetworkClient {
    fun send() {}
}

fun <T> genericHelper() {
}

fun globalHelper() {
}

internal fun internalFunc() {
}

val GLOBAL_CONFIG = "prod"
`
	syms := extract(t, "App.kt", src)

	found(t, syms, "AccountManager", KindType, 5)
	found(t, syms, "AccountManager.timeout", KindConst, 7)
	found(t, syms, "AccountManager.retryCount", KindVar, 8)
	found(t, syms, "AccountManager.authenticate", KindMethod, 10)
	priv := found(t, syms, "AccountManager.validateCredentials", KindMethod, 14)
	if priv.Exported {
		t.Error("private Kotlin method should not be exported")
	}
	found(t, syms, "AccountManager.Companion", KindType, 17)
	found(t, syms, "Authenticator", KindInterface, 22)
	found(t, syms, "Authenticator.verify", KindMethod, 23)
	found(t, syms, "NetworkClient", KindType, 26)
	found(t, syms, "NetworkClient.send", KindMethod, 27)
	found(t, syms, "genericHelper", KindFunc, 30)
	found(t, syms, "globalHelper", KindFunc, 33)
	intern := found(t, syms, "internalFunc", KindFunc, 36)
	if intern.Exported {
		t.Error("internal Kotlin function should not be exported")
	}
	found(t, syms, "GLOBAL_CONFIG", KindConst, 39)

	absent(t, syms, "FakeComment")
	absent(t, syms, "fake")

	// Test naming conventions
	if !jvmIsTest("AccountTest.kt") || !jvmIsTest("AccountTests.kt") || !jvmIsTest("account_test.kt") {
		t.Error("expected Kotlin test naming conventions to match")
	}
	if jvmIsTest("Account.kt") {
		t.Error("Account.kt should not be marked as test")
	}
}

func TestSwiftExtraction(t *testing.T) {
	src := `import Foundation

// struct FakeStruct { }
@objc
struct GeoCoordinate {

    let latitude: Double
    var longitude: Double

    func format() -> String {
        return "\(latitude), \(longitude)"
    }
}

open class NetworkSession {
    public class func classMethod() {}
    public func resume() {
    }

    fileprivate func fileLocal() {
    }

    private func cancelInternal() {
    }
}

actor DataStore {
    func load() {}
}

enum ConnectionState {
    case connected
    case disconnected
}

protocol RequestDelegate {
    func didComplete()
}

extension GeoCoordinate {
    func distanceTo() {}
}

func topLevelFunction() {
}

let APP_VERSION = "1.0"
var activeRequests = 0
`
	syms := extract(t, "Geo.swift", src)

	found(t, syms, "GeoCoordinate", KindType, 5)
	found(t, syms, "GeoCoordinate.latitude", KindConst, 7)
	found(t, syms, "GeoCoordinate.longitude", KindVar, 8)
	found(t, syms, "GeoCoordinate.format", KindMethod, 10)

	found(t, syms, "NetworkSession", KindType, 15)
	found(t, syms, "NetworkSession.classMethod", KindMethod, 16)
	pubFn := found(t, syms, "NetworkSession.resume", KindMethod, 17)
	if !pubFn.Exported {
		t.Error("public Swift method should be exported")
	}
	filePrivFn := found(t, syms, "NetworkSession.fileLocal", KindMethod, 20)
	if filePrivFn.Exported {
		t.Error("fileprivate Swift method should not be exported")
	}
	privFn := found(t, syms, "NetworkSession.cancelInternal", KindMethod, 23)
	if privFn.Exported {
		t.Error("private Swift method should not be exported")
	}

	found(t, syms, "DataStore", KindType, 27)
	found(t, syms, "DataStore.load", KindMethod, 28)
	found(t, syms, "ConnectionState", KindType, 31)
	found(t, syms, "RequestDelegate", KindInterface, 36)
	found(t, syms, "RequestDelegate.didComplete", KindMethod, 37)
	found(t, syms, "GeoCoordinate.distanceTo", KindMethod, 41)
	found(t, syms, "topLevelFunction", KindFunc, 44)
	found(t, syms, "APP_VERSION", KindConst, 47)
	found(t, syms, "activeRequests", KindVar, 48)

	absent(t, syms, "FakeStruct")

	// Test naming conventions
	if !swiftIsTest("GeoTests.swift") || !swiftIsTest("GeoTest.swift") || !swiftIsTest("geo_test.swift") || !swiftIsTest("Tests/Geo.swift") {
		t.Error("expected Swift test naming conventions to match")
	}
	if swiftIsTest("Sources/Geo.swift") {
		t.Error("Sources/Geo.swift should not be marked as test")
	}
}

