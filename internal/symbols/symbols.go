// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

// Package symbols extracts declaration locations from source files so an
// agent can ask where something is defined rather than which repositories
// exist.
//
// Corral's index answers "which repository". Every competing code-context
// server answers "where is this symbol", but each assumes a single open
// repository. The dimension only corral can serve is the intersection:
// a definition lookup across every clone on the machine.
//
// # Scope
//
// Definitions only — no call graph, no references, no type resolution. A
// symbol is a name, a kind, and a location. The agent reads the file; this
// package tells it which file, and which line.
//
// That is deliberate. Locations are cheap to extract, cheap to store, and
// survive being wrong: an agent handed the wrong line still sees the right
// file. A call graph is none of those things.
//
// # Why not tree-sitter
//
// See docs/adr/0006-symbol-extraction-without-cgo.md. In short: its Go
// binding is CGO, corral builds CGO_ENABLED=0 everywhere, and three of four
// release targets fail to cross-compile with it. `go/ast` is the standard
// library, needs no CGO, and is the same parser the Go compiler uses.
//
// The cost is that Go is the only language indexed. The Extractor interface
// exists so that is a new file rather than a rewrite.
package symbols

import (
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Kind classifies a declaration. The set is deliberately small and
// language-neutral: an agent filtering for "the function called Parse"
// should not have to know how a language spells "function".
type Kind string

const (
	// KindFunc is a free function.
	KindFunc Kind = "func"
	// KindMethod is a function bound to a type.
	KindMethod Kind = "method"
	// KindType is a named type: struct, class, enum, alias.
	KindType Kind = "type"
	// KindInterface is an interface or protocol.
	KindInterface Kind = "interface"
	// KindConst is a named constant.
	KindConst Kind = "const"
	// KindVar is a package-level or module-level variable.
	KindVar Kind = "var"
)

// AllKinds is every Kind, in a stable order, for help text and validation.
var AllKinds = []Kind{KindFunc, KindMethod, KindType, KindInterface, KindConst, KindVar}

// ParseKind maps a caller-supplied string to a Kind.
//
// An unrecognised kind is an error rather than a silent no-match: a filter
// that cannot be honoured must say so, or an empty result is
// indistinguishable from a correct one.
func ParseKind(s string) (Kind, error) {
	want := Kind(strings.ToLower(strings.TrimSpace(s)))
	for _, k := range AllKinds {
		if k == want {
			return k, nil
		}
	}
	names := make([]string, 0, len(AllKinds))
	for _, k := range AllKinds {
		names = append(names, string(k))
	}
	return "", fmt.Errorf("unknown symbol kind %q; supported kinds are %s", s, strings.Join(names, ", "))
}

// Symbol is one declaration and where to find it.
type Symbol struct {
	// Name is the declared identifier, without any receiver or package
	// qualifier.
	Name string `json:"name"`
	// Kind is what was declared.
	Kind Kind `json:"kind"`
	// File is the path relative to the repository root, forward-slashed so
	// it reads the same on every platform.
	File string `json:"file"`
	// Line is the 1-indexed line the declaration starts on.
	Line int `json:"line"`
	// Receiver is the type a method is bound to, without a pointer star.
	// Empty for everything else.
	Receiver string `json:"receiver,omitempty"`
	// Exported reports whether the symbol is visible outside its package.
	// An agent orienting in an unfamiliar repository almost always wants
	// the exported surface first.
	Exported bool `json:"exported"`
	// Language is the extractor that produced this symbol.
	Language string `json:"language"`
	// Test reports that the symbol was declared in a test file.
	//
	// Kept as data rather than filtered at extraction, because "where is
	// this tested" is a real question — but excluded by default from
	// lookups, because it is not the common one. On corral itself, test
	// declarations are the majority of all functions; returning them
	// unranked buries every answer someone actually wanted.
	Test bool `json:"test,omitempty"`
}

// Qualified renders the symbol the way a person writing about it would:
// Receiver.Name for a method, Name otherwise.
func (s Symbol) Qualified() string {
	if s.Receiver != "" {
		return s.Receiver + "." + s.Name
	}
	return s.Name
}

// Extractor turns one source file into the declarations it contains.
//
// Implementations must be safe for concurrent use: the walker calls
// Extract from a worker pool. They must not read the filesystem — the
// caller supplies the bytes, so an extractor is a pure function of its
// input and trivially testable.
type Extractor interface {
	// Language is the name reported on every symbol it produces.
	Language() string
	// Extensions are the lowercase file extensions this extractor claims,
	// each with a leading dot.
	Extensions() []string
	// Extract parses src. path is supplied for error messages and for
	// languages where the filename is meaningful; it is not opened.
	//
	// A file that does not parse is not an error the caller should abort
	// on — source in a workspace is frequently mid-edit — so a partial or
	// empty result with a nil error is a valid return.
	Extract(path string, src []byte) ([]Symbol, error)
}

// registry maps an extension to the extractor that claims it.
var registry = map[string]Extractor{}

// register adds an extractor for each extension it claims. Called from
// package init functions; panics on a duplicate claim, which can only be a
// programming error and should never reach a user.
func register(e Extractor) {
	for _, ext := range e.Extensions() {
		if existing, taken := registry[ext]; taken {
			panic(fmt.Sprintf("symbols: %s already claimed by %s, cannot also be claimed by %s",
				ext, existing.Language(), e.Language()))
		}
		registry[ext] = e
	}
}

var (
	fallbackMu        sync.RWMutex
	fallbackExtractor Extractor
)

// SetFallbackExtractor registers a fallback extractor for file extensions
// not claimed by any built-in extractor. Pass nil to disable fallback.
func SetFallbackExtractor(e Extractor) {
	fallbackMu.Lock()
	defer fallbackMu.Unlock()
	fallbackExtractor = e
}

// GetFallbackExtractor returns the current fallback extractor, if any.
func GetFallbackExtractor() Extractor {
	fallbackMu.RLock()
	defer fallbackMu.RUnlock()
	return fallbackExtractor
}

// ExtractorFor returns the extractor claiming path's extension, if any.
func ExtractorFor(path string) (Extractor, bool) {
	i := strings.LastIndexByte(path, '.')
	if i < 0 {
		return nil, false
	}
	e, ok := registry[strings.ToLower(path[i:])]
	if ok {
		return e, true
	}
	fallbackMu.RLock()
	fb := fallbackExtractor
	fallbackMu.RUnlock()
	if fb != nil {
		return fb, true
	}
	return nil, false
}

// multiLanguage is implemented by an extractor that reports more than one
// language name across the files it claims.
//
// The TypeScript extractor is one: it handles .js as well, and reporting a
// JavaScript symbol as "typescript" would be a lie an agent could act on.
// Optional rather than part of Extractor, because it is the exception.
type multiLanguage interface {
	// Languages is every language name this extractor can report.
	Languages() []string
}

// Languages lists the indexed languages, sorted, for help text and for the
// capability a tool description advertises.
//
// This is what a tool description tells an agent it covers, so it has to
// match what symbols actually come back. An extractor that quietly reports
// a name missing from here makes the tool look like it does not support a
// language it does.
func Languages() []string {
	seen := map[string]struct{}{}
	for _, e := range registry {
		if m, ok := e.(multiLanguage); ok {
			for _, l := range m.Languages() {
				seen[l] = struct{}{}
			}
			continue
		}
		seen[e.Language()] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for l := range seen {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// Sort orders symbols deterministically: by file, then line, then name.
//
// The order has to be stable across runs or an agent paging through
// results would see them shuffle, and two identical queries would disagree.
func Sort(syms []Symbol) {
	sort.Slice(syms, func(i, j int) bool {
		a, b := syms[i], syms[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Qualified() < b.Qualified()
	})
}
