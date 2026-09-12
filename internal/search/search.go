// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package search finds text inside the source files of a repository.
//
// It is the other half of the question internal/symbols answers. A symbol
// index says where a name is *declared*; this says where it is *used*, and
// the second question is the one asked more often — "who calls this", "what
// reads this environment variable", "which repository has the retry
// constant in it".
//
// # Why an agent needs this from corral rather than from a shell
//
// An agent with shell access can run ripgrep. What it cannot do is run it
// across every clone on the machine without first knowing they exist, where
// they are, and which of their files are safe to read. That is the index
// corral already maintains, and the file policy it already enforces — so
// search here inherits both, and a hit can never come from a credential
// file that the file resource would have refused to serve.
//
// # What it is not
//
// Not an inverted index. Nothing is precomputed and nothing is stored: a
// query walks the files and reads them. That is a deliberate ceiling —
// searching a large workspace costs real I/O every time — and it is the
// right trade while the index itself is in-memory and rebuilt per process.
// A persistent index changes this calculation, not this interface.
package search

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Hit is one matching line.
type Hit struct {
	// File is the path relative to the repository root, forward-slashed.
	File string `json:"file"`
	// Line is the 1-indexed line the match is on.
	Line int `json:"line"`
	// Column is the 1-indexed byte offset of the match within the line.
	Column int `json:"column"`
	// Text is the matching line, trimmed of surrounding whitespace and
	// truncated. It is source written by whoever owns the repository, so
	// the caller must treat it as data — see the sanitising the MCP layer
	// applies before it reaches a model.
	Text string `json:"text"`
}

// Result is what one repository's search produced.
type Result struct {
	// Hits are the matches, in walk order: by file, then by line.
	Hits []Hit
	// Files is how many files were actually read.
	Files int
	// Truncated reports that a bound was reached — the file cap, the hit
	// cap, or a file too large — so the caller can say the answer is
	// partial rather than presenting it as complete.
	Truncated bool
}

// Query is one search.
type Query struct {
	// Pattern is the text to find, or a regular expression when Regex is
	// set.
	Pattern string
	// Regex treats Pattern as RE2 rather than as literal text.
	Regex bool
	// CaseSensitive matches exactly. The default is insensitive, because
	// somebody searching a workspace for "timeout" wants TIMEOUT too.
	CaseSensitive bool
	// PathGlob, when set, limits the search to files whose relative path
	// matches it.
	PathGlob string
	// IncludeTests keeps hits in test files. Off by default for the same
	// reason the symbol index excludes them: on a well-tested repository
	// they outnumber everything else and bury the answer.
	IncludeTests bool
	// MaxHits bounds the result for one repository. Zero means the
	// package default.
	MaxHits int
}

// Matcher is a compiled Query, ready to run against many lines.
//
// Compiling once and reusing is not an optimisation detail: a regex
// compiled per line on a hundred thousand lines is the difference between
// a search that answers and one that times out.
type Matcher struct {
	// re is set for a regex query and for a case-insensitive literal,
	// which is compiled to a quoted regex rather than searched by
	// lowercasing every line.
	re *regexp.Regexp
	// literal is set only for a case-sensitive literal, which
	// strings.Index handles faster than any regex.
	literal string
	// literalBytes is literal pre-converted, because MatchBytes runs once
	// per line read and converting there would allocate on every one —
	// reintroducing, per line, exactly the cost MatchBytes exists to avoid.
	literalBytes []byte
	pathGlob     string
	maxHits      int
	// includeTests is carried here so a walker has one object to consult.
	includeTests bool
	// rawLiteral is the user's pattern when it is a literal, in either case
	// sense. literalBytes is empty for the case-insensitive form, which is
	// searched as a regex, so the index needs its own copy.
	rawLiteral []byte
	// caseFolded records that matching is case-insensitive, which the
	// trigram index needs to know: folding is a Unicode operation and a byte
	// index cannot model it.
	caseFolded bool
	// literalOnly records that the pattern is a literal, in either case
	// sense, and therefore carries no anchors. That is what makes a
	// whole-file prefilter sound: for an unanchored pattern, "matches
	// somewhere in this file" and "matches on some line of this file" are
	// the same question. A user-supplied regex may anchor with ^ or $,
	// where the two differ, so it does not get the prefilter.
	literalOnly bool
}

// maxPatternLen bounds a pattern. A regex is user input compiled into a
// program, and while RE2 cannot backtrack catastrophically, an enormous
// pattern can still cost real time and memory to compile.
const maxPatternLen = 1024

// DefaultMaxHits bounds one repository's results when a Query does not.
const DefaultMaxHits = 200

// casefoldFlag is the RE2 group that makes a pattern case-insensitive.
const casefoldFlag = "(?i)"

// Compile turns a Query into a Matcher, reporting a pattern that cannot be
// used.
//
// An invalid regex is an error rather than a silent no-match. The two are
// indistinguishable to a caller looking at an empty result, and the second
// reading — "there is nothing here" — is the wrong conclusion to hand an
// agent that is about to act on it.
func Compile(q Query) (*Matcher, error) {
	if strings.TrimSpace(q.Pattern) == "" {
		return nil, fmt.Errorf("pattern must not be empty")
	}
	if len(q.Pattern) > maxPatternLen {
		return nil, fmt.Errorf("pattern is %d bytes; the limit is %d", len(q.Pattern), maxPatternLen)
	}
	if !utf8.ValidString(q.Pattern) {
		return nil, fmt.Errorf("pattern is not valid UTF-8")
	}

	m := &Matcher{
		pathGlob:     q.PathGlob,
		maxHits:      q.MaxHits,
		includeTests: q.IncludeTests,
	}
	if m.maxHits <= 0 {
		m.maxHits = DefaultMaxHits
	}

	if q.Regex {
		expr := q.Pattern
		if !q.CaseSensitive {
			expr = casefoldFlag + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			// The message has to describe the caller's pattern, not the
			// one this function built: an error quoting "(?i)func(" sends
			// somebody looking for a bug in input they never wrote. The
			// flag is removed from the message text rather than by
			// compiling twice, because a second compile would add a
			// branch that only fires when prefixing a valid pattern makes
			// it invalid — a case that cannot be constructed, and so
			// cannot be tested.
			detail := err.Error()
			if expr != q.Pattern {
				detail = strings.Replace(detail, casefoldFlag, "", 1)
			}
			return nil, fmt.Errorf("invalid regular expression %q: %s", q.Pattern, detail)
		}
		m.re = re
		return m, nil
	}

	if q.CaseSensitive {
		m.literal = q.Pattern
		m.literalBytes = []byte(q.Pattern)
		m.literalOnly = true
		m.rawLiteral = []byte(q.Pattern)
		return m, nil
	}

	// Case-insensitive is the default, and it used to lowercase every line
	// before searching it — an allocation and a full copy per line, over
	// every line of every file. Benchmarked at roughly forty times the
	// cost of the case-sensitive path and five times the cost of a regex,
	// which makes the default the slowest option.
	//
	// A quoted pattern behind the fold flag is the same search the regex
	// path already runs, and it fixes a correctness wart too: an offset
	// into a lowercased copy is not always an offset into the original,
	// because lowercasing can change a string's length.
	// QuoteMeta escapes everything the engine could object to, so this
	// cannot fail. The error is returned rather than discarded — every
	// caller checks it before using the matcher, and swallowing it would
	// be the one way a pattern silently matched nothing — but it is
	// returned without a branch, because a branch here would be a line no
	// test could ever reach.
	re, err := regexp.Compile(casefoldFlag + regexp.QuoteMeta(q.Pattern))
	m.re = re
	// QuoteMeta escaped the pattern, so whatever the user typed it is a
	// literal now and cannot anchor.
	m.literalOnly = true
	m.rawLiteral = []byte(q.Pattern)
	m.caseFolded = true
	return m, err
}

// MatchLine reports the 0-indexed byte offset of the first match in line,
// or -1.
//
// Only the first match per line is reported. A line with the same token
// twice is one place to look, and returning it twice would spend a
// caller's result budget on a single line.
func (m *Matcher) MatchLine(line string) int {
	if m.re != nil {
		loc := m.re.FindStringIndex(line)
		if loc == nil {
			return -1
		}
		return loc[0]
	}
	return strings.Index(line, m.literal)
}

// MatchBytes is MatchLine without turning the line into a string first.
//
// A content search reads far more lines than it reports — on a
// 234-repository workspace, tens of millions of lines to return a handful —
// so the allocation that MatchLine's string argument implies is paid on every
// line and recovered on almost none. regexp and bytes both offer the same
// search over []byte, so the conversion buys nothing.
//
// The string form is kept: it is what a caller with a line already in hand
// should use, and the two must agree, which TestMatchBytesAgreesWithMatchLine
// asserts over the same inputs.
func (m *Matcher) MatchBytes(line []byte) int {
	if m.re != nil {
		loc := m.re.FindIndex(line)
		if loc == nil {
			return -1
		}
		return loc[0]
	}
	return bytes.Index(line, m.literalBytes)
}

// CanPrefilter reports whether a whole-file test is equivalent to testing
// each line, which holds exactly when the pattern cannot anchor.
func (m *Matcher) CanPrefilter() bool { return m.literalOnly }

// MatchAny reports whether the pattern occurs anywhere in buf.
//
// This is the cheap question asked first. Almost every file in a workspace
// contains no match, and answering "no" for the whole file at once skips
// splitting it into lines and testing each one — which was the bulk of the
// work in a content search, spent entirely on files that had nothing to say.
func (m *Matcher) MatchAny(buf []byte) bool {
	if m.re != nil {
		return m.re.Match(buf)
	}
	return bytes.Contains(buf, m.literalBytes)
}

// indexPattern is the literal this matcher searches for, as bytes.
//
// Only meaningful when CanPrefilter reports true; the trigram index uses it to
// derive the trigrams a matching file must contain. For the case-insensitive
// form the literal was QuoteMeta'd into a regex, so the original is kept
// alongside it purely for this.
func (m *Matcher) indexPattern() []byte { return m.rawLiteral }

// MaxHits is the cap this matcher was compiled with.
func (m *Matcher) MaxHits() int { return m.maxHits }

// IncludeTests reports whether test files are in scope.
func (m *Matcher) IncludeTests() bool { return m.includeTests }
