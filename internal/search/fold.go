// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package search

import "bytes"

// ASCII-folded literal matching.
//
// A case-insensitive search — the default — used to compile its pattern to
// `(?i)` + QuoteMeta and run the regex engine over every line. Profiling a
// workspace query showed what that costs: regexp.(*machine).match accounted for
// 45% of CPU and unicode.SimpleFold alone for 16%, against 37% for the file
// reads the search actually exists to do. Halving the number of files read did
// not move the wall clock, because the files were never the expensive part.
//
// The engine is doing real work: (?i) folds Unicode, so every rune is run
// through SimpleFold in case it folds onto the pattern. For a pattern that is
// plain ASCII, that generality is almost entirely wasted — only two runes in
// Unicode fold to an ASCII letter (see foldRiskSeqs), and a file containing
// neither can be matched with a byte comparison.
//
// So the regex is kept for exactly the files that need it. Every other file —
// effectively all of them — takes the byte path.

// asciiFoldable reports whether the pattern is a literal with no byte above
// 0x7F, which is what makes byte folding equivalent to Unicode folding.
func asciiFoldable(pattern []byte) bool {
	for _, b := range pattern {
		if b > 0x7F {
			return false
		}
	}
	return len(pattern) > 0
}

// CanFoldASCII reports whether this matcher has a byte fast path.
func (m *Matcher) CanFoldASCII() bool { return m.foldNeedle != nil }

// NeedsUnicodeFold reports whether data contains a rune that folds onto an
// ASCII letter, and so must be matched by the regex engine to stay correct.
//
// Checked once per file rather than per line: it is two substring searches,
// and it decides which path every line of that file takes.
func NeedsUnicodeFold(data []byte) bool { return containsFoldRisk(data) }

// indexFold returns the offset of the first ASCII-case-insensitive occurrence
// of the matcher's needle in line, or -1.
//
// The first byte is found with bytes.IndexByte, which is vectorised, so the
// scan only stops where a match could begin. Both cases of that byte are
// searched and the earlier taken, because a match may start with either.
func (m *Matcher) indexFold(line []byte) int {
	n := m.foldNeedle
	if len(n) == 0 || len(line) < len(n) {
		return -1
	}
	lo, up := n[0], m.foldFirstUpper
	from := 0
	for from+len(n) <= len(line) {
		rest := line[from:]
		i := bytes.IndexByte(rest, lo)
		if lo != up {
			if j := bytes.IndexByte(rest, up); j >= 0 && (i < 0 || j < i) {
				i = j
			}
		}
		if i < 0 {
			return -1
		}
		at := from + i
		if at+len(n) > len(line) {
			return -1
		}
		if equalFoldASCII(line[at:at+len(n)], n) {
			return at
		}
		from = at + 1
	}
	return -1
}

// equalFoldASCII compares a candidate against an already-lowercased needle.
func equalFoldASCII(a, needleLower []byte) bool {
	for i := range needleLower {
		if lowerASCII(a[i]) != needleLower[i] {
			return false
		}
	}
	return true
}

// containsFold reports whether the needle occurs anywhere in data, folded.
// Used by the whole-file prefilter.
func (m *Matcher) containsFold(data []byte) bool { return m.indexFold(data) >= 0 }
