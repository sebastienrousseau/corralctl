// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package symbols

import (
	"path/filepath"
	"strings"
)

func init() {
	register(javaExtractor{})
	register(kotlinExtractor{})
}

// javaExtractor reads declarations out of Java (.java) source files.
type javaExtractor struct{}

func (javaExtractor) Language() string     { return "java" }
func (javaExtractor) Extensions() []string { return []string{".java"} }
func (j javaExtractor) Extract(path string, src []byte) ([]Symbol, error) {
	return extractJava(path, src, j.Language())
}

// kotlinExtractor reads declarations out of Kotlin (.kt, .kts) source files.
type kotlinExtractor struct{}

func (kotlinExtractor) Language() string     { return "kotlin" }
func (kotlinExtractor) Extensions() []string { return []string{".kt", ".kts"} }
func (k kotlinExtractor) Extract(path string, src []byte) ([]Symbol, error) {
	return extractKotlin(path, src, k.Language())
}

var jvmStyle = commentStyle{
	lineComment:  "//",
	blockOpen:    "/*",
	blockClose:   "*/",
	quotes:       `"'`,
	tripleQuotes: true,
}

// jvmIsTest reports whether path belongs to a test suite by naming convention.
func jvmIsTest(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if strings.HasSuffix(base, "test.java") || strings.HasSuffix(base, "tests.java") ||
		strings.HasSuffix(base, "testcase.java") || strings.HasPrefix(base, "test") ||
		strings.HasSuffix(base, "test.kt") || strings.HasSuffix(base, "tests.kt") ||
		strings.HasSuffix(base, "_test.kt") || strings.HasSuffix(base, "it.java") {
		return true
	}
	for _, seg := range strings.Split(strings.ToLower(filepath.ToSlash(path)), "/") {
		if seg == "test" || seg == "tests" || seg == "testing" || seg == "androidtest" {
			return true
		}
	}
	return false
}

// javaReservedWord lists Java control flow and language keywords taking parentheses.
func javaReservedWord(name string) bool {
	switch name {
	case "if", "for", "while", "switch", "catch", "synchronized",
		"return", "throw", "new", "super", "this", "assert":
		return true
	}
	return false
}

// extractJava scans Java source code for type, method, and constant declarations.
func extractJava(path string, src []byte, lang string) ([]Symbol, error) {
	lines := codeLines(src, jvmStyle)
	isTest := jvmIsTest(path)

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
	depth := 0

	for n, raw := range lines {
		indent := indentOf(raw)
		if indent < 0 {
			depth += braceDelta(raw)
			continue
		}
		lineNo := n + 1
		i := indent

		// Skip annotations
		if raw[i] == '@' {
			if strings.HasPrefix(raw[i:], "@interface") {
				i += len("@interface")
				i = skipSpace(raw, i)
				name, _ := takeIdent(raw, i)
				if name != "" {
					add(name, KindInterface, classes[depth], lineNo, true)
					classes[depth+1] = name
				}
			}
			depth += braceDelta(raw)
			continue
		}

		exported := false
		isStatic := false
		isFinal := false

		// Parse modifiers
		for {
			prev := i
			if end, ok := keywordAt(raw, i, "public"); ok {
				exported = true
				i = skipSpace(raw, end)
			} else if end, ok := keywordAt(raw, i, "protected"); ok {
				exported = true
				i = skipSpace(raw, end)
			} else if end, ok := keywordAt(raw, i, "private"); ok {
				exported = false
				i = skipSpace(raw, end)
			} else if end, ok := keywordAt(raw, i, "static"); ok {
				isStatic = true
				i = skipSpace(raw, end)
			} else if end, ok := keywordAt(raw, i, "final"); ok {
				isFinal = true
				i = skipSpace(raw, end)
			} else {
				for _, mod := range []string{"abstract", "default", "synchronized", "native", "transient", "volatile", "sealed", "non-sealed"} {
					if end, ok := keywordAt(raw, i, mod); ok {
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
		case matchKeyword(raw, &i, "class"), matchKeyword(raw, &i, "enum"), matchKeyword(raw, &i, "record"):
			i = skipSpace(raw, i)
			name, _ := takeIdent(raw, i)
			if name != "" {
				add(name, KindType, classes[depth], lineNo, exported)
				classes[depth+1] = name
			}

		case matchKeyword(raw, &i, "interface"):
			i = skipSpace(raw, i)
			name, _ := takeIdent(raw, i)
			if name != "" {
				add(name, KindInterface, classes[depth], lineNo, exported)
				classes[depth+1] = name
			}

		default:
			// Check for constant declarations: static final ... NAME = ...
			if isStatic && isFinal && strings.Contains(raw, "=") && !strings.Contains(raw, "(") {
				beforeEq := strings.TrimSpace(raw[:strings.IndexByte(raw, '=')])
				fields := strings.Fields(beforeEq)
				if len(fields) >= 2 {
					constName := fields[len(fields)-1]
					if constName != "" && !javaReservedWord(constName) {
						add(constName, KindConst, classes[depth], lineNo, exported)
					}
				}
			} else if strings.Contains(raw, "(") {
				// Method or constructor
				parenIdx := strings.IndexByte(raw, '(')
				beforeParen := strings.TrimSpace(raw[:parenIdx])
				if eqIdx := strings.IndexByte(beforeParen, '='); eqIdx < 0 {
					fields := strings.Fields(beforeParen)
					if len(fields) >= 1 {
						methodName := fields[len(fields)-1]
						if methodName != "" && !javaReservedWord(methodName) {
							// If inside a type, receiver is current class; else free
							currentClass := classes[depth]
							kind := KindFunc
							if currentClass != "" {
								kind = KindMethod
							}
							add(methodName, kind, currentClass, lineNo, exported)
						}
					}
				}
			}
		}

		depth += braceDelta(raw)
	}

	return out, nil
}

// extractKotlin scans Kotlin source code for type, function, and property declarations.
func extractKotlin(path string, src []byte, lang string) ([]Symbol, error) {
	lines := codeLines(src, jvmStyle)
	isTest := jvmIsTest(path)

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

		// Skip annotations
		if raw[i] == '@' {
			depth += braceDelta(raw)
			continue
		}

		exported := true

		// Parse Kotlin modifiers
		for {
			prev := i
			if end, ok := keywordAt(raw, i, "private"); ok {
				exported = false
				i = skipSpace(raw, end)
			} else if end, ok := keywordAt(raw, i, "internal"); ok {
				exported = false
				i = skipSpace(raw, end)
			} else {
				for _, mod := range []string{
					"public", "protected", "open", "final", "abstract",
					"override", "data", "sealed", "inline", "suspend",
					"infix", "operator", "tailrec", "const", "lateinit",
					"companion", "inner", "enum", "value",
				} {
					if end, ok := keywordAt(raw, i, mod); ok {
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
		case matchKeyword(raw, &i, "class"):
			i = skipSpace(raw, i)
			name, _ := takeIdent(raw, i)
			if name != "" {
				add(name, KindType, containers[depth], lineNo, exported)
				containers[depth+1] = name
			}

		case matchKeyword(raw, &i, "interface"):
			i = skipSpace(raw, i)
			name, _ := takeIdent(raw, i)
			if name != "" {
				add(name, KindInterface, containers[depth], lineNo, exported)
				containers[depth+1] = name
			}

		case matchKeyword(raw, &i, "object"):
			i = skipSpace(raw, i)
			name, _ := takeIdent(raw, i)
			if name == "" {
				name = "Companion"
			}
			add(name, KindType, containers[depth], lineNo, exported)
			containers[depth+1] = name

		case matchKeyword(raw, &i, "fun"):
			i = skipSpace(raw, i)
			// Handle generic type params if any e.g. fun <T> foo()
			if i < len(raw) && raw[i] == '<' {
				if endAngle := strings.IndexByte(raw[i:], '>'); endAngle >= 0 {
					i = skipSpace(raw, i+endAngle+1)
				}
			}
			name, _ := takeIdent(raw, i)
			if name != "" {
				kind := KindFunc
				parent := containers[depth]
				if parent != "" {
					kind = KindMethod
				}
				add(name, kind, parent, lineNo, exported)
			}

		case matchKeyword(raw, &i, "val"):
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
