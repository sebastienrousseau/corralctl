// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package symbols

import (
	"path/filepath"
	"strings"
)

func init() {
	register(cExtractor{})
	register(cppExtractor{})
}

// cExtractor reads declarations out of C source and header files.
type cExtractor struct{}

func (cExtractor) Language() string     { return "c" }
func (cExtractor) Extensions() []string { return []string{".c", ".h"} }
func (c cExtractor) Extract(path string, src []byte) ([]Symbol, error) {
	return extractCPP(path, src, c.Language())
}

// cppExtractor reads declarations out of C++ source and header files.
type cppExtractor struct{}

func (cppExtractor) Language() string     { return "cpp" }
func (cppExtractor) Extensions() []string { return []string{".cpp", ".hpp", ".cc", ".cxx", ".hh"} }
func (c cppExtractor) Extract(path string, src []byte) ([]Symbol, error) {
	return extractCPP(path, src, c.Language())
}

var cppStyle = commentStyle{
	lineComment: "//",
	blockOpen:   "/*",
	blockClose:  "*/",
	quotes:      `"'`,
}

// cppIsTest reports whether path belongs to a test suite by naming convention.
func cppIsTest(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test") ||
		strings.Contains(base, "_test.") || strings.Contains(base, "_unittest.") ||
		strings.Contains(base, "test.") {
		return true
	}
	for _, seg := range strings.Split(strings.ToLower(filepath.ToSlash(path)), "/") {
		if seg == "test" || seg == "tests" || seg == "testing" {
			return true
		}
	}
	return false
}

// cppReservedWord lists C/C++ keywords that take parentheses but are not functions.
func cppReservedWord(name string) bool {
	switch name {
	case "if", "for", "while", "switch", "catch", "return", "sizeof",
		"decltype", "alignas", "alignof", "static_assert", "static_cast",
		"dynamic_cast", "reinterpret_cast", "const_cast", "typeid",
		"requires", "case":
		return true
	}
	return false
}

// extractCPP scans C and C++ source code for declarations.
func extractCPP(path string, src []byte, lang string) ([]Symbol, error) {
	lines := codeLines(src, cppStyle)
	isTest := cppIsTest(path)

	var out []Symbol

	add := func(name string, kind Kind, receiver string, line int, exported bool) {
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

	classes := map[int]string{}
	accessPublic := map[int]bool{}
	depth := 0
	funcDepth := 0
	pendingKeyword := ""

	for n, raw := range lines {
		indent := indentOf(raw)
		if indent < 0 {
			continue
		}
		lineNo := n + 1
		i := indent

		// If we are currently inside a function implementation, don't parse declarations.
		if funcDepth > 0 {
			depth += braceDelta(raw)
			if depth < funcDepth {
				funcDepth = 0
			}
			continue
		}

		// Preprocessor directives
		if raw[i] == '#' {
			i = skipSpace(raw, i+1)
			if end, ok := keywordAt(raw, i, "define"); ok {
				i = skipSpace(raw, end)
				name, after := takeIdent(raw, i)
				if name != "" {
					kind := KindConst
					if after < len(raw) && raw[after] == '(' {
						kind = KindFunc
					}
					add(name, kind, "", lineNo, !strings.HasPrefix(name, "_"))
				}
			}
			depth += braceDelta(raw)
			continue
		}

		// Access specifiers in class body
		trimmed := strings.TrimSpace(raw[i:])
		if trimmed == "public:" {
			accessPublic[depth] = true
			depth += braceDelta(raw)
			continue
		}
		if trimmed == "private:" || trimmed == "protected:" {
			accessPublic[depth] = false
			depth += braceDelta(raw)
			continue
		}

		// Skip common modifiers
		exported := true
		if isPub, ok := accessPublic[depth]; ok {
			exported = isPub
		}

		for {
			advanced := false
			for _, mod := range []string{"extern", "static", "inline", "virtual", "explicit", "friend", "constexpr", "consteval", "volatile", "const"} {
				if end, ok := keywordAt(raw, i, mod); ok {
					i = skipSpace(raw, end)
					advanced = true
					if mod == "static" && depth == 0 {
						exported = false
					}
				}
			}
			if !advanced {
				break
			}
		}

		switch {
		case pendingKeyword != "":
			name, after := takeIdent(raw, i)
			if name != "" {
				rest := strings.TrimSpace(raw[after:])
				if !strings.HasPrefix(rest, ";") {
					add(name, KindType, "", lineNo, exported && !strings.HasPrefix(name, "_"))
					classes[depth+1] = name
					if pendingKeyword == "class" {
						accessPublic[depth+1] = false
					} else {
						accessPublic[depth+1] = true
					}
				}
			}
			pendingKeyword = ""

		case matchKeyword(raw, &i, "class"), matchKeyword(raw, &i, "struct"),
			matchKeyword(raw, &i, "union"), matchKeyword(raw, &i, "enum"):
			i = skipSpace(raw, i)
			if matchKeyword(raw, &i, "class") || matchKeyword(raw, &i, "struct") {
				i = skipSpace(raw, i)
			}
			name, after := takeIdent(raw, i)
			if name != "" {
				rest := strings.TrimSpace(raw[after:])
				// Forward declarations end with ';' before any '{'
				if !strings.HasPrefix(rest, ";") {
					add(name, KindType, "", lineNo, exported && !strings.HasPrefix(name, "_"))
					classes[depth+1] = name
					if strings.Contains(raw[indent:], "class") {
						accessPublic[depth+1] = false
					} else {
						accessPublic[depth+1] = true
					}
				}
			} else {
				if strings.Contains(raw[indent:], "class") {
					pendingKeyword = "class"
				} else {
					pendingKeyword = "struct"
				}
			}

		case matchKeyword(raw, &i, "using"):
			i = skipSpace(raw, i)
			name, after := takeIdent(raw, i)
			if name != "" {
				rest := strings.TrimSpace(raw[after:])
				if strings.HasPrefix(rest, "=") {
					add(name, KindType, "", lineNo, exported && !strings.HasPrefix(name, "_"))
				}
			}

		case matchKeyword(raw, &i, "namespace"):
			i = skipSpace(raw, i)
			_, _ = takeIdent(raw, i)

		default:
			// Function or method
			if name, receiver, ok := cppFunctionOrMethod(raw[i:], classes[depth]); ok {
				kind := KindFunc
				if receiver != "" {
					kind = KindMethod
				}
				add(name, kind, receiver, lineNo, exported && !strings.HasPrefix(name, "_"))
				if strings.Contains(raw, "{") {
					funcDepth = depth + 1
				}
			}
		}

		depth += braceDelta(raw)
	}

	return out, nil
}

// cppFunctionOrMethod inspects a line to extract a function or method signature.
func cppFunctionOrMethod(line, currentClass string) (name, receiver string, ok bool) {
	// Look for a parameter list opening '('
	parenIdx := strings.IndexByte(line, '(')
	if parenIdx <= 0 {
		return "", "", false
	}

	beforeParen := strings.TrimSpace(line[:parenIdx])

	// Must not have '=' before '(' unless it is an operator overload (e.g. operator= or operator==)
	if !strings.Contains(beforeParen, "operator") {
		eqIdx := strings.IndexByte(line, '=')
		if eqIdx >= 0 && eqIdx < parenIdx {
			return "", "", false
		}
	}

	tokens := strings.Fields(beforeParen)
	lastToken := tokens[len(tokens)-1]
	lastToken = strings.TrimLeft(lastToken, "*&")
	if lastToken == "" {
		return "", "", false
	}

	// Check if this was an operator overload, e.g. "operator ==" or "operator=="
	if len(tokens) >= 2 && tokens[len(tokens)-2] == "operator" {
		lastToken = "operator" + lastToken
	}

	if cppReservedWord(lastToken) {
		return "", "", false
	}

	// Check for qualified name: Receiver::Name
	if parts := strings.Split(lastToken, "::"); len(parts) == 2 {
		return parts[1], parts[0], true
	}

	// In-class method
	if currentClass != "" {
		return lastToken, currentClass, true
	}

	// Global or namespace function
	return lastToken, "", true
}
