// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package symbols

import (
	"path/filepath"
	"strings"
)

func init() {
	register(protoExtractor{})
}

// protoExtractor reads declarations out of Protocol Buffer (.proto) source files.
type protoExtractor struct{}

func (protoExtractor) Language() string     { return "protobuf" }
func (protoExtractor) Extensions() []string { return []string{".proto"} }
func (p protoExtractor) Extract(path string, src []byte) ([]Symbol, error) {
	return extractProto(path, src, p.Language())
}

var protoStyle = commentStyle{
	lineComment: "//",
	blockOpen:   "/*",
	blockClose:  "*/",
	quotes:      `"'`,
}

// protoIsTest reports whether path belongs to a test suite by naming convention.
func protoIsTest(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	if strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.proto") ||
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

// extractProto scans Protocol Buffer (.proto) source code for declarations.
func extractProto(path string, src []byte, lang string) ([]Symbol, error) {
	lines := codeLines(src, protoStyle)
	isTest := protoIsTest(path)

	var out []Symbol

	add := func(name string, kind Kind, receiver string, line int) {
		out = append(out, Symbol{
			Name:     name,
			Kind:     kind,
			File:     path,
			Line:     line,
			Receiver: receiver,
			Exported: true,
			Language: lang,
			Test:     isTest,
		})
	}

	services := map[int]string{}
	messages := map[int]string{}
	depth := 0

	for n, raw := range lines {
		indent := indentOf(raw)
		if indent < 0 {
			depth += braceDelta(raw)
			continue
		}
		lineNo := n + 1
		i := indent

		switch {
		case matchKeyword(raw, &i, "message"):
			i = skipSpace(raw, i)
			name, _ := takeIdent(raw, i)
			if name != "" {
				receiver := messages[depth]
				add(name, KindType, receiver, lineNo)
				messages[depth+1] = name
			}

		case matchKeyword(raw, &i, "enum"):
			i = skipSpace(raw, i)
			name, _ := takeIdent(raw, i)
			if name != "" {
				receiver := messages[depth]
				add(name, KindType, receiver, lineNo)
			}

		case matchKeyword(raw, &i, "service"):
			i = skipSpace(raw, i)
			name, _ := takeIdent(raw, i)
			if name != "" {
				add(name, KindInterface, "", lineNo)
				services[depth+1] = name
			}

		case matchKeyword(raw, &i, "rpc"):
			i = skipSpace(raw, i)
			name, _ := takeIdent(raw, i)
			if name != "" {
				receiver := services[depth]
				add(name, KindMethod, receiver, lineNo)
			}
		}

		depth += braceDelta(raw)
	}

	return out, nil
}
