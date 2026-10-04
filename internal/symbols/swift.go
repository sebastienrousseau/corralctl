// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package symbols

import (
	"path/filepath"
	"strings"
)

func init() {
	register(swiftExtractor{})
}

// swiftExtractor reads declarations out of Swift (.swift) source files.
type swiftExtractor struct{}

func (swiftExtractor) Language() string     { return "swift" }
func (swiftExtractor) Extensions() []string { return []string{".swift"} }
func (s swiftExtractor) Extract(path string, src []byte) ([]Symbol, error) {
	return extractSwift(path, src, s.Language())
}

var swiftStyle = commentStyle{
	lineComment:  "//",
	blockOpen:    "/*",
	blockClose:   "*/",
	nestedBlocks: true,
	quotes:       `"`,
	tripleQuotes: true,
}

// swiftIsTest reports whether path belongs to a test suite by naming convention.
func swiftIsTest(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if strings.HasSuffix(base, "test.swift") || strings.HasSuffix(base, "tests.swift") ||
		strings.HasSuffix(base, "_test.swift") || strings.HasPrefix(base, "test") {
		return true
	}
	for _, seg := range strings.Split(strings.ToLower(filepath.ToSlash(path)), "/") {
		if seg == "test" || seg == "tests" || seg == "testing" {
			return true
		}
	}
	return false
}

// extractSwift scans Swift source code for declarations.
func extractSwift(path string, src []byte, lang string) ([]Symbol, error) {
	lines := codeLines(src, swiftStyle)
	isTest := swiftIsTest(path)

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

	containers := map[int]string{}
	depth := 0

	for n, raw := range lines {
		indent := indentOf(raw)
		if indent < 0 {
			depth += braceDelta(raw)
			continue
		}
		lineNo := n + 1
		i := indent

		// Skip compiler directives or attributes like @objc, @main, @available
		if raw[i] == '@' || raw[i] == '#' {
			depth += braceDelta(raw)
			continue
		}

		exported := true

		// Parse Swift modifiers
		for {
			prev := i
			if end, ok := keywordAt(raw, i, "public"); ok {
				exported = true
				i = skipSpace(raw, end)
			} else if end, ok := keywordAt(raw, i, "open"); ok {
				exported = true
				i = skipSpace(raw, end)
			} else if end, ok := keywordAt(raw, i, "private"); ok {
				exported = false
				i = skipSpace(raw, end)
			} else if end, ok := keywordAt(raw, i, "fileprivate"); ok {
				exported = false
				i = skipSpace(raw, end)
			} else {
				for _, mod := range []string{
					"internal", "final", "static", "class",
					"mutating", "nonmutating", "override", "indirect",
					"lazy", "weak", "unowned", "async", "throws", "rethrows",
					"required", "convenience", "dynamic", "optional",
				} {
					if end, ok := keywordAt(raw, i, mod); ok {
						if mod == "class" {
							rest := skipSpace(raw, end)
							_, isFunc := keywordAt(raw, rest, "func")
							_, isVar := keywordAt(raw, rest, "var")
							_, isLet := keywordAt(raw, rest, "let")
							if !isFunc && !isVar && !isLet {
								continue
							}
						}
						i = skipSpace(raw, end)
						break
					}
				}
			}
			if i == prev {
				break
			}
		}

		switch {
		case matchKeyword(raw, &i, "struct"), matchKeyword(raw, &i, "class"),
			matchKeyword(raw, &i, "actor"), matchKeyword(raw, &i, "enum"):
			i = skipSpace(raw, i)
			name, _ := takeIdent(raw, i)
			if name != "" {
				add(name, KindType, containers[depth], lineNo, exported)
				containers[depth+1] = name
			}

		case matchKeyword(raw, &i, "protocol"):
			i = skipSpace(raw, i)
			name, _ := takeIdent(raw, i)
			if name != "" {
				add(name, KindInterface, containers[depth], lineNo, exported)
				containers[depth+1] = name
			}

		case matchKeyword(raw, &i, "extension"):
			i = skipSpace(raw, i)
			name, _ := takeIdent(raw, i)
			if name != "" {
				// Extension targets an existing type
				containers[depth+1] = name
			}

		case matchKeyword(raw, &i, "func"):
			i = skipSpace(raw, i)
			name, _ := takeIdent(raw, i)
			if name != "" {
				kind := KindFunc
				parent := containers[depth]
				if parent != "" {
					kind = KindMethod
				}
				add(name, kind, parent, lineNo, exported)
			}

		case matchKeyword(raw, &i, "let"):
			i = skipSpace(raw, i)
			name, _ := takeIdent(raw, i)
			if name != "" {
				add(name, KindConst, containers[depth], lineNo, exported)
			}

		case matchKeyword(raw, &i, "var"):
			i = skipSpace(raw, i)
			name, _ := takeIdent(raw, i)
			if name != "" {
				add(name, KindVar, containers[depth], lineNo, exported)
			}
		}

		depth += braceDelta(raw)
	}

	return out, nil
}
