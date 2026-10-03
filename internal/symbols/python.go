// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package symbols

import "strings"

func init() { register(pythonExtractor{}) }

// pythonExtractor reads declarations out of Python source.
//
// Python is the one language here where indentation carries the structure,
// which makes a line scanner *more* reliable rather than less: a `def` is a
// method exactly when it is indented inside a `class`, and that is directly
// visible without resolving anything. The enclosing class is tracked on a
// stack so a nested class's methods get the right receiver.
type pythonExtractor struct{}

// pythonStyle is Python's comment and string syntax. Triple quotes matter
// more here than anywhere else: every docstring is one, and a docstring
// containing the word "class" at the start of a line is completely normal.
var pythonStyle = commentStyle{
	lineComment:  "#",
	quotes:       `"'`,
	tripleQuotes: true,
}

// Language implements Extractor.
func (pythonExtractor) Language() string { return "python" }

// Extensions implements Extractor.
//
// .pyi stubs are included deliberately. A typed library often has its only
// readable declaration surface in its stubs, and a lookup that skipped them
// would report "not found" for a symbol whose signature is right there.
func (pythonExtractor) Extensions() []string { return []string{".py", ".pyi"} }

// pyScope is one enclosing block on the indentation stack.
type pyScope struct {
	indent int
	class  string
}

// Extract implements Extractor.
func (p pythonExtractor) Extract(path string, src []byte) ([]Symbol, error) {
	lines := codeLines(src, pythonStyle)
	isTest := pythonIsTest(path)

	var (
		out                 []Symbol
		scopes              []pyScope
		decoratorParenDepth int
	)

	add := func(name string, kind Kind, receiver string, line int) {
		if name == "" || name == "_" {
			return
		}
		out = append(out, Symbol{
			Name:     name,
			Kind:     kind,
			File:     path,
			Line:     line,
			Receiver: receiver,
			// Python has no access control, only the leading-underscore
			// convention — which is the convention every Python programmer
			// actually reads, so it is the right thing to report.
			Exported: !strings.HasPrefix(name, "_"),
			Language: p.Language(),
			Test:     isTest,
		})
	}

	for n, raw := range lines {
		indent := indentOf(raw)
		if indent < 0 {
			continue // blank, or blanked-out comment
		}

		if decoratorParenDepth > 0 {
			decoratorParenDepth = updateDepth(decoratorParenDepth, parenDelta(raw))
			continue
		}

		// Close every scope this line has dedented out of.
		for len(scopes) > 0 && indent <= scopes[len(scopes)-1].indent {
			scopes = scopes[:len(scopes)-1]
		}

		i := indent
		lineNo := n + 1

		// A decorator line declares nothing itself; the declaration is
		// below it, and will be seen on its own line.
		if raw[i] == '@' {
			if pd := parenDelta(raw[i:]); pd > 0 {
				decoratorParenDepth = pd
			}
			continue
		}

		// `async def` is a def.
		if end, ok := keywordAt(raw, i, "async"); ok {
			i = skipSpace(raw, end)
		}

		switch {
		case matchKeyword(raw, &i, "def"):
			i = skipSpace(raw, i)
			name, _ := takeIdent(raw, i)
			kind, receiver := KindFunc, ""
			if len(scopes) > 0 && scopes[len(scopes)-1].class != "" {
				kind, receiver = KindMethod, scopes[len(scopes)-1].class
			}
			add(name, kind, receiver, lineNo)
			// A def opens a scope, but not a class one: a function defined
			// inside a function is a closure, not a method.
			scopes = append(scopes, pyScope{indent: indent})

		case matchKeyword(raw, &i, "class"):
			i = skipSpace(raw, i)
			name, _ := takeIdent(raw, i)
			// A Protocol is Python's interface, and people look for it as
			// one. Detected from the bases on the same line, which is
			// where they are written.
			kind := KindType
			if pythonIsProtocol(raw) {
				kind = KindInterface
			}
			add(name, kind, "", lineNo)
			scopes = append(scopes, pyScope{indent: indent, class: name})

		default:
			// Module- and class-level assignments. Only at the top level or
			// directly in a class body: a local inside a function is not
			// something anyone looks up across a workspace, and including
			// them would bury the ones that matter.
			if len(scopes) > 0 && scopes[len(scopes)-1].class == "" {
				continue
			}
			if name, ok := pythonAssignment(raw, indent); ok {
				receiver := ""
				if len(scopes) > 0 {
					receiver = scopes[len(scopes)-1].class
				}
				add(name, pythonConstOrVar(name), receiver, lineNo)
			}
		}
	}
	return out, nil
}

// matchKeyword advances i past kw when the line has it there, reporting
// whether it did. A small helper because the switch above reads far better
// as a list of alternatives than as nested ifs.
func matchKeyword(line string, i *int, kw string) bool {
	end, ok := keywordAt(line, *i, kw)
	if ok {
		*i = end
	}
	return ok
}

// pythonAssignment recognises a name bound at statement level: `X = ...`,
// or an annotated `X: int = ...` or bare `X: int`.
//
// It deliberately does not handle tuple unpacking (`a, b = f()`). Those are
// rarely the module-level constants anyone searches for, and picking a name
// out of them reliably needs more parsing than this file is willing to do.
func pythonAssignment(line string, indent int) (string, bool) {
	name, i := takeIdent(line, indent)
	if name == "" {
		return "", false
	}
	i = skipSpace(line, i)
	if i >= len(line) {
		return "", false
	}
	switch line[i] {
	case ':':
		// An annotation. Anything else starting with a colon here — a
		// `for`, `if`, `while` — was already excluded by taking an
		// identifier, since those are keywords, but a variable genuinely
		// named `if` is impossible anyway.
		return name, true
	case '=':
		// Exclude ==, which is a comparison in an expression statement.
		if i+1 < len(line) && line[i+1] == '=' {
			return "", false
		}
		return name, true
	}
	return "", false
}

// pythonConstOrVar applies the SCREAMING_CASE convention.
//
// Python has no const, so the name is the only signal — and it is a signal
// Python programmers use consistently enough to be worth honouring, because
// "find the constant" is a different question from "find the variable".
func pythonConstOrVar(name string) Kind {
	hasLetter := false
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' {
			return KindVar
		}
		if c >= 'A' && c <= 'Z' {
			hasLetter = true
		}
	}
	if hasLetter {
		return KindConst
	}
	return KindVar
}

// pythonIsProtocol reports whether a class line names Protocol or ABC among
// its bases.
func pythonIsProtocol(line string) bool {
	open := strings.IndexByte(line, '(')
	if open < 0 {
		return false
	}
	bases := line[open:]
	for _, marker := range []string{"Protocol", "ABC", "ABCMeta"} {
		if strings.Contains(bases, marker) {
			return true
		}
	}
	return false
}

// pythonIsTest reports whether path is a test module, by the two
// conventions pytest and unittest actually use.
func pythonIsTest(path string) bool {
	base := path
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	if strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") {
		return true
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == "tests" || seg == "test" {
			return true
		}
	}
	return false
}
