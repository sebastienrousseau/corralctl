// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package symbols

import "strings"

func init() { register(tsExtractor{}) }

// tsExtractor reads declarations out of TypeScript and JavaScript.
//
// One extractor for both because the declaration syntax this cares about is
// the same, and the difference — types and interfaces — is additive: a .js
// file simply never contains an `interface`. Reporting the language as
// "typescript" for a .js file would be wrong, so it is derived from the
// extension rather than fixed.
//
// Braces are tracked rather than indentation, because JavaScript's
// indentation means nothing. Depth 1 inside a `class` is where methods
// live, and that is the only nesting this needs to distinguish.
type tsExtractor struct{}

// tsStyle is JavaScript's comment and string syntax. Template literals are
// the reason multiLineQuote exists: a backticked string routinely spans
// twenty lines of HTML containing the word "function".
var tsStyle = commentStyle{
	lineComment:    "//",
	blockOpen:      "/*",
	blockClose:     "*/",
	quotes:         "\"'`",
	multiLineQuote: '`',
}

// Language implements Extractor.
//
// The registry keys on extension and calls this for the reported language,
// so a single name is required here; per-file languages come from
// languageFor below.
func (tsExtractor) Language() string { return "typescript" }

// Extensions implements Extractor.
func (tsExtractor) Extensions() []string {
	return []string{".ts", ".tsx", ".mts", ".cts", ".js", ".jsx", ".mjs", ".cjs"}
}

// Languages implements multiLanguage: this extractor reports two, so the
// advertised capability has to name both.
func (tsExtractor) Languages() []string { return []string{"typescript", "javascript"} }

// languageFor reports javascript for a file with no type syntax, so a
// result set says which of the two it actually found.
func (tsExtractor) languageFor(path string) string {
	lower := strings.ToLower(path)
	for _, ext := range []string{".js", ".jsx", ".mjs", ".cjs"} {
		if strings.HasSuffix(lower, ext) {
			return "javascript"
		}
	}
	return "typescript"
}

// Extract implements Extractor.
func (e tsExtractor) Extract(path string, src []byte) ([]Symbol, error) {
	lines := codeLines(src, tsStyle)
	lang := e.languageFor(path)
	isTest := tsIsTest(path)

	var out []Symbol
	add := func(name string, kind Kind, receiver string, line int, exported bool) {
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
			Language: lang,
			Test:     isTest,
		})
	}

	// classes maps a brace depth to the class opened at it, so a method's
	// receiver is whatever class encloses it. A map rather than a stack
	// because braces close in the scanner without the class being popped
	// explicitly; a depth that is left behind is simply never consulted.
	classes := map[int]string{}
	depth := 0
	decoratorParenDepth := 0

	for n, raw := range lines {
		indent := indentOf(raw)
		if indent < 0 {
			depth += braceDelta(raw)
			continue
		}
		if decoratorParenDepth > 0 {
			decoratorParenDepth = updateDepth(decoratorParenDepth, parenDelta(raw))
			depth += braceDelta(raw)
			continue
		}
		lineNo := n + 1
		i := indent

		// A decorator line declares nothing itself; the declaration is below it.
		if raw[i] == '@' {
			if pd := parenDelta(raw[i:]); pd > 0 {
				decoratorParenDepth = pd
			}
			depth += braceDelta(raw)
			continue
		}

		// `export` and `export default` mark a module's visible surface.
		// A class member has no export keyword: it is public unless it
		// says otherwise, so visibility is tracked separately and the two
		// are reconciled at the point of use.
		exported := false
		if end, ok := keywordAt(raw, i, "export"); ok {
			exported = true
			i = skipSpace(raw, end)
			if end, ok := keywordAt(raw, i, "default"); ok {
				i = skipSpace(raw, end)
			}
		}
		// Modifiers that change nothing about what is being declared. The
		// loop runs over the list rather than once, because they combine:
		// `public static async foo()` is four words before the name.
		memberPublic := true
		for _, mod := range []string{"declare", "abstract", "async", "static", "readonly", "public", "private", "protected", "override"} {
			if end, ok := keywordAt(raw, i, mod); ok {
				i = skipSpace(raw, end)
				if mod == "private" || mod == "protected" {
					memberPublic = false
				}
			}
		}

		switch {
		case matchKeyword(raw, &i, "class"):
			i = skipSpace(raw, i)
			name, _ := takeIdent(raw, i)
			add(name, KindType, "", lineNo, exported)
			// The body opens at the brace on this line, so the class owns
			// the depth *after* this line's opening brace.
			classes[depth+1] = name

		case matchKeyword(raw, &i, "interface"):
			i = skipSpace(raw, i)
			name, _ := takeIdent(raw, i)
			add(name, KindInterface, "", lineNo, exported)

		case matchKeyword(raw, &i, "enum"):
			i = skipSpace(raw, i)
			name, _ := takeIdent(raw, i)
			add(name, KindType, "", lineNo, exported)

		case matchKeyword(raw, &i, "type"):
			// `type X = ...` only. A variable named `type` is legal, and
			// `type` alone is not a declaration.
			j := skipSpace(raw, i)
			name, after := takeIdent(raw, j)
			if name != "" && tsIsAliasDecl(raw, after) {
				add(name, KindType, "", lineNo, exported)
			}

		case matchKeyword(raw, &i, "function"):
			i = skipSpace(raw, i)
			// `function*` is a generator.
			if i < len(raw) && raw[i] == '*' {
				i = skipSpace(raw, i+1)
			}
			name, _ := takeIdent(raw, i)
			add(name, KindFunc, "", lineNo, exported)

		default:
			if name, kind, ok := tsBinding(raw, i); ok {
				add(name, kind, "", lineNo, exported)
				break
			}
			// A method: an identifier followed by a parameter list, inside
			// a class body. Checked last because it is the loosest pattern
			// here and would otherwise shadow the keyword cases.
			if cls, inClass := classes[depth]; inClass {
				if name, ok := tsMethod(raw, i); ok {
					// A class member is public by default, which is what
					// "part of the visible surface" means for a method.
					add(name, KindMethod, cls, lineNo, memberPublic)
				}
			}
		}

		depth += braceDelta(raw)
	}
	return out, nil
}

// tsIsAliasDecl reports whether what follows a `type X` is an alias
// declaration rather than something else that happened to start with the
// word type. Generic parameters are allowed in between: `type Pair<A, B> =`.
func tsIsAliasDecl(line string, i int) bool {
	if i < len(line) && line[i] == '<' {
		if j := strings.IndexByte(line[i:], '>'); j >= 0 {
			i += j + 1
		}
	}
	i = skipSpace(line, i)
	return i < len(line) && line[i] == '='
}

// tsBinding recognises `const`, `let` and `var` declarations.
//
// A const bound to an arrow function or a function expression is reported
// as a function, because that is what it is to anybody looking for it —
// `export const handler = () => {}` is the single most common way to
// declare a function in modern JavaScript, and calling it a variable would
// make it unfindable by the question people actually ask.
func tsBinding(line string, i int) (string, Kind, bool) {
	var kind Kind
	switch {
	case matchKeyword(line, &i, "const"):
		kind = KindConst
	case matchKeyword(line, &i, "let"), matchKeyword(line, &i, "var"):
		kind = KindVar
	default:
		return "", "", false
	}
	i = skipSpace(line, i)
	name, after := takeIdent(line, i)
	if name == "" {
		return "", "", false
	}
	if tsBindsFunction(line, after) {
		return name, KindFunc, true
	}
	return name, kind, true
}

// tsBindsFunction reports whether the right-hand side of a binding is a
// function: an arrow, or the `function` keyword.
func tsBindsFunction(line string, i int) bool {
	eq := strings.IndexByte(line[i:], '=')
	if eq < 0 {
		return false
	}
	rhs := line[i+eq+1:]
	if strings.Contains(rhs, "=>") {
		return true
	}
	rhs = strings.TrimLeft(rhs, " \t")
	_, ok := keywordAt(rhs, 0, "function")
	if ok {
		return true
	}
	// `const f = async () => {}` — the arrow is on this line and already
	// matched above; `async function` needs the keyword skipped first.
	if end, isAsync := keywordAt(rhs, 0, "async"); isAsync {
		rhs = strings.TrimLeft(rhs[end:], " \t")
		_, ok = keywordAt(rhs, 0, "function")
		return ok
	}
	return false
}

// tsMethod recognises `name(` or `name<T>(` at the start of a class body
// line, which is how a method is written.
//
// Control flow is excluded by name: `if (x)` and `for (i)` have exactly
// this shape, and there is no other way to tell them apart without
// parsing.
func tsMethod(line string, i int) (string, bool) {
	// `get x()` and `set x(v)` are accessors, and are methods.
	for _, accessor := range []string{"get", "set"} {
		if end, ok := keywordAt(line, i, accessor); ok {
			j := skipSpace(line, end)
			if name, after := takeIdent(line, j); name != "" && tsCallSignature(line, after) {
				return name, true
			}
		}
	}
	name, after := takeIdent(line, i)
	if name == "" || tsReservedWord(name) {
		return "", false
	}
	if !tsCallSignature(line, after) {
		return "", false
	}
	return name, true
}

// tsCallSignature reports whether a parameter list opens at i, optionally
// after type parameters.
func tsCallSignature(line string, i int) bool {
	if i < len(line) && line[i] == '<' {
		if j := strings.IndexByte(line[i:], '>'); j >= 0 {
			i += j + 1
		}
	}
	i = skipSpace(line, i)
	return i < len(line) && line[i] == '('
}

// tsReservedWord lists the words that would otherwise be read as methods
// because a keyword followed by a parenthesis is indistinguishable from a
// call signature.
func tsReservedWord(name string) bool {
	switch name {
	case "if", "for", "while", "switch", "catch", "return", "do", "with",
		"function", "class", "super", "constructor", "typeof", "await", "yield", "new":
		// `constructor` is excluded on purpose. It is a method, but nobody
		// searches a workspace for one: every class has exactly one and it
		// is found by looking at the class.
		return true
	}
	return false
}

// braceDelta is the net change in brace depth across a line of blanked
// source. Strings and comments are already gone, so every brace counted is
// a real one.
func braceDelta(line string) int {
	d := 0
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '{':
			d++
		case '}':
			d--
		}
	}
	return d
}

// tsIsTest reports whether path is a test file, by the conventions Jest,
// Vitest and Mocha share.
func tsIsTest(path string) bool {
	lower := strings.ToLower(path)
	for _, marker := range []string{".test.", ".spec."} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	for _, seg := range strings.Split(lower, "/") {
		if seg == "__tests__" || seg == "test" || seg == "tests" {
			return true
		}
	}
	return false
}
