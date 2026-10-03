// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package symbols

import "strings"

func init() { register(rustExtractor{}) }

// rustExtractor reads declarations out of Rust source.
//
// Rust is the friendliest of the scanned languages: every declaration
// begins with an unambiguous keyword, and the one piece of structure that
// matters — which type a method belongs to — is written explicitly at the
// top of the `impl` block rather than inferred. Tracking brace depth is
// enough to know when that block ends.
type rustExtractor struct{}

// rustStyle is Rust's comment and string syntax. Block comments nest,
// which is unusual and worth honouring: `/* /* */ */` is one comment, and
// treating the inner close as the outer one would un-blank real code.
var rustStyle = commentStyle{
	lineComment:  "//",
	blockOpen:    "/*",
	blockClose:   "*/",
	nestedBlocks: true,
	quotes:       `"`,
}

// Language implements Extractor.
func (rustExtractor) Language() string { return "rust" }

// Extensions implements Extractor.
func (rustExtractor) Extensions() []string { return []string{".rs"} }

// Extract implements Extractor.
func (r rustExtractor) Extract(path string, src []byte) ([]Symbol, error) {
	lines := codeLines(src, rustStyle)
	isTest := rustIsTest(path)

	var out []Symbol
	// inTest is per line rather than per file: a #[cfg(test)] mod inside
	// an ordinary source file makes everything in it test code, and Rust
	// puts its unit tests exactly there.
	add := func(name string, kind Kind, receiver string, line int, exported, inTest bool) {
		if name == "" || name == "_" {
			return
		}
		out = append(out, Symbol{
			Name:     name,
			Kind:     kind,
			File:     path,
			Line:     line,
			Receiver: receiver,
			Exported: exported,
			Language: r.Language(),
			Test:     inTest,
		})
	}

	// impls maps the brace depth of an impl body to the type it is for, so
	// a fn inside it is reported as a method on that type.
	impls := map[int]string{}
	depth := 0
	// testModuleDepth is the brace depth of a `#[cfg(test)] mod tests`
	// body, which is where Rust puts its unit tests — inside the file they
	// test, so the filename cannot reveal them. -1 when not in one.
	//
	// pendingTest carries the attribute across the lines between it and the
	// `mod` it applies to, since `#[cfg(test)]` sits on its own line.
	testModuleDepth := -1
	pendingTest := false
	attributeBracketDepth := 0
	inCfg := false

	for n, raw := range lines {
		indent := indentOf(raw)
		if indent < 0 {
			depth += braceDelta(raw)
			continue
		}
		if attributeBracketDepth > 0 {
			if strings.Contains(raw, "cfg(test)") || (inCfg && strings.Contains(raw, "test")) {
				pendingTest = true
			}
			attributeBracketDepth = updateDepth(attributeBracketDepth, bracketDelta(raw))
			if attributeBracketDepth == 0 {
				inCfg = false
			}
			depth += braceDelta(raw)
			continue
		}
		lineNo := n + 1
		i := indent

		inTest := isTest || testModuleDepth >= 0

		// An attribute declares nothing; the item is on a following line.
		// #[cfg(test)] is the exception worth noticing, because what
		// follows it is test code.
		if raw[i] == '#' {
			if strings.Contains(raw, "cfg(test)") {
				pendingTest = true
			}
			if bd := bracketDelta(raw[i:]); bd > 0 {
				attributeBracketDepth = bd
				inCfg = strings.Contains(raw, "cfg(")
			}
			depth += braceDelta(raw)
			continue
		}

		exported := false
		if end, ok := keywordAt(raw, i, "pub"); ok {
			exported = true
			i = skipSpace(raw, end)
			// pub(crate), pub(super), pub(in path) are narrower than
			// public, but they are still the declared surface rather than
			// a private detail.
			if i < len(raw) && raw[i] == '(' {
				if j := strings.IndexByte(raw[i:], ')'); j >= 0 {
					i = skipSpace(raw, i+j+1)
				}
			}
		}
		for _, mod := range []string{"default", "const", "async", "unsafe", "extern"} {
			// `const fn` is a function; a bare `const NAME` is a constant,
			// and is handled below by looking at what follows.
			if mod == "const" && !rustConstIsFnModifier(raw, i) {
				continue
			}
			if end, ok := keywordAt(raw, i, mod); ok {
				// The ABI string in `extern "C" fn` needs no handling: the
				// blanking pass has already replaced it with spaces, and
				// skipSpace walks over what is left.
				i = skipSpace(raw, end)
			}
		}

		switch {
		case matchKeyword(raw, &i, "impl"):
			// `impl Trait for Type` and `impl Type` both belong to Type.
			// Generic parameters on the impl itself are skipped.
			impls[depth+1] = rustImplTarget(raw, i)

		case matchKeyword(raw, &i, "fn"):
			i = skipSpace(raw, i)
			name, _ := takeIdent(raw, i)
			kind, receiver := KindFunc, ""
			if target, ok := impls[depth]; ok && target != "" {
				kind, receiver = KindMethod, target
			}
			add(name, kind, receiver, lineNo, exported, inTest)

		case matchKeyword(raw, &i, "trait"):
			i = skipSpace(raw, i)
			name, _ := takeIdent(raw, i)
			add(name, KindInterface, "", lineNo, exported, inTest)
			// A trait's methods belong to the trait. `Fetch.fetch` is how
			// somebody refers to one, so it is how they should find it.
			impls[depth+1] = name

		case matchKeyword(raw, &i, "struct"), matchKeyword(raw, &i, "enum"), matchKeyword(raw, &i, "union"):
			i = skipSpace(raw, i)
			name, _ := takeIdent(raw, i)
			add(name, KindType, "", lineNo, exported, inTest)

		case matchKeyword(raw, &i, "type"):
			i = skipSpace(raw, i)
			name, _ := takeIdent(raw, i)
			// An associated type inside an impl or trait belongs to it.
			// Reported bare, every `type Output = ...` in a crate collides
			// into one meaningless name that nobody would ever search for;
			// qualified, `DateTime.Err` is a thing somebody can look up.
			add(name, KindType, impls[depth], lineNo, exported, inTest)

		case matchKeyword(raw, &i, "const"), matchKeyword(raw, &i, "static"):
			i = skipSpace(raw, i)
			// `static mut X` and `const _: T = ...`.
			if end, ok := keywordAt(raw, i, "mut"); ok {
				i = skipSpace(raw, end)
			}
			name, _ := takeIdent(raw, i)
			add(name, KindConst, "", lineNo, exported, inTest)

		case matchKeyword(raw, &i, "macro_rules"):
			i = skipSpace(raw, i)
			if i < len(raw) && raw[i] == '!' {
				i = skipSpace(raw, i+1)
			}
			name, _ := takeIdent(raw, i)
			// A macro is invoked like a function and looked for like one.
			add(name, KindFunc, "", lineNo, exported, inTest)
		}

		// A `mod` body is a scope like any other, but not one that owns
		// methods: clearing the entry stops the enclosing impl from
		// claiming functions declared inside it.
		if _, ok := keywordAt(raw, indent, "mod"); ok {
			delete(impls, depth+1)
		}

		opened := braceDelta(raw)
		if pendingTest && opened > 0 {
			testModuleDepth = depth + 1
			pendingTest = false
		}
		depth += opened
		// Everything scoped deeper than the current depth has closed.
		for d := range impls {
			if d > depth {
				delete(impls, d)
			}
		}
		if testModuleDepth >= 0 && depth < testModuleDepth {
			testModuleDepth = -1
		}
	}
	return out, nil
}

// rustConstIsFnModifier reports whether a `const` at i modifies a function
// rather than declaring a constant.
func rustConstIsFnModifier(line string, i int) bool {
	end, ok := keywordAt(line, i, "const")
	if !ok {
		return false
	}
	rest := strings.TrimLeft(line[end:], " \t")
	_, isFn := keywordAt(rest, 0, "fn")
	return isFn
}

// rustImplTarget extracts the type an impl block is for.
//
// `impl<T> Trait<T> for Type<T>` yields "Type": the target is what follows
// `for` when there is one, and the first path segment otherwise. Generic
// arguments and module paths are stripped, because a lookup is by the name
// people type.
func rustImplTarget(line string, i int) string {
	// Skip generic parameters on the impl itself: impl<T, U>.
	i = skipSpace(line, i)
	if i < len(line) && line[i] == '<' {
		depth := 0
		for ; i < len(line); i++ {
			if line[i] == '<' {
				depth++
			} else if line[i] == '>' {
				depth--
				if depth == 0 {
					i++
					break
				}
			}
		}
	}
	rest := line[min(i, len(line)):]
	// Everything up to the body, or the where clause.
	if j := strings.IndexByte(rest, '{'); j >= 0 {
		rest = rest[:j]
	}
	if j := strings.Index(rest, " where"); j >= 0 {
		rest = rest[:j]
	}
	if j := strings.Index(rest, " for "); j >= 0 {
		rest = rest[j+len(" for "):]
	}
	return rustBareTypeName(rest)
}

// rustBareTypeName reduces a type expression to its last path segment
// without generic arguments or references.
func rustBareTypeName(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, "&*")
	s = strings.TrimSpace(s)
	if end, ok := keywordAt(s, 0, "mut"); ok {
		s = strings.TrimSpace(s[end:])
	}
	if j := strings.IndexByte(s, '<'); j >= 0 {
		s = s[:j]
	}
	if j := strings.LastIndex(s, "::"); j >= 0 {
		s = s[j+2:]
	}
	name, _ := takeIdent(strings.TrimSpace(s), 0)
	return name
}

// rustIsTest reports whether path is a test file. Rust's integration tests
// live in a top-level tests/ directory; unit tests live inline and are
// found by the cfg(test) tracking in Extract.
func rustIsTest(path string) bool {
	for _, seg := range strings.Split(path, "/") {
		if seg == "tests" || seg == "benches" {
			return true
		}
	}
	return false
}
