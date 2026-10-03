// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package symbols

import "strings"

// Line-oriented scanning for languages corral cannot parse properly.
//
// Go gets go/ast, which is the compiler's own parser. Nothing else can:
// every mature parser for Python, TypeScript or Rust is either CGO
// (tree-sitter), a port that lags the language, or a dependency an order of
// magnitude larger than corral itself. ADR-0006 records why CGO is not
// available here.
//
// So the other languages get what ctags has given people for thirty years:
// a scanner that recognises declaration syntax line by line. It is worth
// being plain about what that trades away, because the failure modes are
// specific rather than diffuse.
//
// What it cannot do:
//
//   - Resolve types, so it cannot tell a `type` alias from a `type`
//     re-export.
//   - See through macros, decorators that rewrite, or code generated at
//     import time.
//   - Follow a declaration split across lines in an unusual way.
//
// What it does do is answer "where is X defined" for the shape declarations
// actually take in real source, and be wrong in a way that costs almost
// nothing: a missed symbol is a lookup that falls back to reading files,
// and a spurious symbol is one wrong line in an otherwise right file.
//
// The one failure that would be expensive is matching a keyword inside a
// string or a comment, because that produces confident nonsense — a symbol
// that does not exist, at a line where nothing is declared. That is what
// this file prevents: every scanner runs over source that has had comment
// and string *contents* blanked out, so `# def not_a_function` and
// `"class Fake"` are invisible to it while every line number and column
// stays exactly where it was.

// commentStyle describes how one language delimits comments and strings.
//
// Only what the blanking pass needs: it is not a lexer, and it does not
// need to know an operator from an identifier.
type commentStyle struct {
	// lineComment starts a comment that runs to end of line.
	lineComment string
	// blockOpen and blockClose delimit a multi-line comment. Empty when
	// the language has none.
	blockOpen, blockClose string
	// nestedBlocks reports that block comments nest, as Rust's do.
	nestedBlocks bool
	// quotes are the characters that open a single-line string.
	quotes string
	// tripleQuotes reports Python-style ''' and """ strings, which span
	// lines and are the docstring convention, so they are everywhere.
	tripleQuotes bool
	// multiLineQuote is a quote character whose string may span lines
	// without a continuation, as a JavaScript template literal does.
	multiLineQuote byte
}

// blankNonCode returns src with the contents of comments and string
// literals replaced by spaces.
//
// Every byte keeps its offset and every newline stays where it was, so a
// scanner reads line N of the result and reports line N of the original.
// Delimiters are blanked along with their contents: a scanner looking for
// `class` has no use for the quote that was hiding it, and leaving quotes
// in place would only invite a second parser to try to interpret them.
//
// It is a state machine over bytes rather than a regex because the states
// are genuinely stateful — a block comment opened on line 4 and closed on
// line 40 cannot be recognised one line at a time.
func blankNonCode(src []byte, style commentStyle) []byte {
	out := make([]byte, len(src))
	// Start from a copy so anything the machine does not touch — which is
	// all the actual code — survives byte for byte.
	copy(out, src)

	blank := func(from, to int) {
		for i := from; i < to && i < len(out); i++ {
			// Newlines are preserved so line numbering is unaffected; a
			// multi-line string becomes blank lines, not one long line.
			if out[i] != '\n' {
				out[i] = ' '
			}
		}
	}

	i := 0
	for i < len(src) {
		switch {
		case style.blockOpen != "" && hasAt(src, i, style.blockOpen):
			start := i
			i += len(style.blockOpen)
			depth := 1
			for i < len(src) && depth > 0 {
				switch {
				case style.nestedBlocks && hasAt(src, i, style.blockOpen):
					depth++
					i += len(style.blockOpen)
				case hasAt(src, i, style.blockClose):
					depth--
					i += len(style.blockClose)
				default:
					i++
				}
			}
			blank(start, i)

		case style.lineComment != "" && hasAt(src, i, style.lineComment):
			start := i
			for i < len(src) && src[i] != '\n' {
				i++
			}
			blank(start, i)

		case style.tripleQuotes && isTripleQuote(src, i):
			start := i
			q := src[i]
			i += 3
			for i < len(src) && (src[i] != q || !isTripleQuote(src, i)) {
				i++
			}
			if i < len(src) {
				i += 3
			}
			blank(start, i)

		case strings.IndexByte(style.quotes, src[i]) >= 0:
			q := src[i]
			multiLine := q == style.multiLineQuote
			start := i
			i++
			for i < len(src) {
				if src[i] == '\\' {
					// An escape consumes the next byte, so \" does not
					// close the string and \\ does not escape the quote
					// after it.
					i += 2
					continue
				}
				if src[i] == q {
					i++
					break
				}
				if src[i] == '\n' && !multiLine {
					// An unterminated string is a syntax error, and source
					// in a workspace is often mid-edit. Ending it at the
					// newline keeps one bad line from blanking the rest of
					// the file.
					break
				}
				i++
			}
			blank(start, i)

		default:
			i++
		}
	}
	return out
}

// hasAt reports whether src has prefix at offset i.
func hasAt(src []byte, i int, prefix string) bool {
	if i+len(prefix) > len(src) {
		return false
	}
	return string(src[i:i+len(prefix)]) == prefix
}

// isTripleQuote reports whether offset i begins a Python triple-quoted
// string, in either the apostrophe or the double-quote spelling.
func isTripleQuote(src []byte, i int) bool {
	if i+3 > len(src) {
		return false
	}
	c := src[i]
	return (c == '\'' || c == '"') && src[i+1] == c && src[i+2] == c
}

// codeLines splits src into lines with comments and string contents
// blanked. The returned slice is 0-indexed; a scanner reporting line
// numbers must add one.
func codeLines(src []byte, style commentStyle) []string {
	return strings.Split(string(blankNonCode(src, style)), "\n")
}

// indentOf counts the leading whitespace of a line, with a tab counted as
// one level's worth rather than expanded.
//
// Only comparisons between indents matter — deeper, shallower, same — so
// the unit is irrelevant as long as it is consistent. Mixing tabs and
// spaces in one file defeats this, as it defeats Python itself.
func indentOf(line string) int {
	n := 0
	for n < len(line) && (line[n] == ' ' || line[n] == '\t') {
		n++
	}
	// A blank line has no meaningful indent; reporting its length would
	// close every enclosing block.
	if n == len(line) {
		return -1
	}
	return n
}

// isIdentByte reports whether c can appear inside an identifier in the
// languages scanned here. Close enough for all three: none of them allow
// an identifier to start with a digit, and the callers check the boundary
// rather than validating the name.
func isIdentByte(c byte) bool {
	return c == '_' || c == '$' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// takeIdent reads the identifier starting at i, returning it and the offset
// just past it. An empty name means there was no identifier there.
func takeIdent(s string, i int) (string, int) {
	start := i
	for i < len(s) && isIdentByte(s[i]) {
		i++
	}
	return s[start:i], i
}

// skipSpace advances past spaces and tabs.
func skipSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

// keywordAt reports whether s has kw at offset i as a whole word, and
// returns the offset just past it.
//
// The word boundary is the whole point: without it `classes = 1` declares a
// class and `functional` declares a function.
func keywordAt(s string, i int, kw string) (int, bool) {
	if i+len(kw) > len(s) || s[i:i+len(kw)] != kw {
		return i, false
	}
	if i > 0 && isIdentByte(s[i-1]) {
		return i, false
	}
	end := i + len(kw)
	if end < len(s) && isIdentByte(s[end]) {
		return i, false
	}
	return end, true
}

// parenDelta is the net change in parenthesis depth '(' minus ')' across a line.
func parenDelta(line string) int {
	d := 0
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '(':
			d++
		case ')':
			d--
		}
	}
	return d
}

// bracketDelta is the net change in square bracket depth '[' minus ']' across a line.
func bracketDelta(line string) int {
	d := 0
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '[':
			d++
		case ']':
			d--
		}
	}
	return d
}

// updateDepth adjusts a delimiter depth by delta, clamping at 0.
func updateDepth(current, delta int) int {
	next := current + delta
	if next < 0 {
		return 0
	}
	return next
}
