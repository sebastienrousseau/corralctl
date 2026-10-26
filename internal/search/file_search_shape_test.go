// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package search

import (
	"reflect"
	"strings"
	"testing"
)

// TestSearchFileCharacterisation pins what searchFile returns for every
// shape of input it branches on, so restructuring it for the complexity
// ceilings cannot change a result. Each case is one file and one query;
// the expectations were recorded from the implementation before it was
// split into helpers.
func TestSearchFileCharacterisation(t *testing.T) {
	long := strings.Repeat("a", maxLineBytes+1)
	wide := strings.Repeat("x", maxHitTextBytes+10)
	cases := []struct {
		name  string
		body  string
		q     Query
		hits  []Hit
		more  bool
		noPre bool
	}{
		{name: "empty file", body: "", q: Query{Pattern: "needle"}},
		{name: "NUL in the head is binary", body: "needle\x00\n", q: Query{Pattern: "needle"}},
		{
			name: "NUL past the head is text",
			body: strings.Repeat("a", 8000) + "\x00\nneedle\n",
			q:    Query{Pattern: "needle"},
			hits: []Hit{{File: "f.txt", Line: 2, Column: 1, Text: "needle"}},
		},
		{name: "prefilter miss, folded", body: "nothing here\n", q: Query{Pattern: "needle"}},
		{name: "prefilter miss, exact", body: "nothing here\n", q: Query{Pattern: "needle", CaseSensitive: true}},
		{name: "regex miss", body: "nothing here\n", q: Query{Pattern: "ne+dle", Regex: true}},
		{name: "no prefilter, miss", body: "nothing here\n", q: Query{Pattern: "needle"}, noPre: true},
		{
			name:  "no prefilter, hit",
			body:  "a\nneedle\n",
			q:     Query{Pattern: "needle"},
			hits:  []Hit{{File: "f.txt", Line: 2, Column: 1, Text: "needle"}},
			noPre: true,
		},
		{
			name: "CRLF is stripped",
			body: "a\r\nfoo needle\r\n",
			q:    Query{Pattern: "needle", CaseSensitive: true},
			hits: []Hit{{File: "f.txt", Line: 2, Column: 5, Text: "foo needle"}},
		},
		{
			name: "CRLF does not hide a line-end anchor",
			body: "needle\r\n",
			q:    Query{Pattern: "needle$", Regex: true},
			hits: []Hit{{File: "f.txt", Line: 1, Column: 1, Text: "needle"}},
		},
		{
			name: "last line without a newline",
			body: "x\n  needle  ",
			q:    Query{Pattern: "needle"},
			hits: []Hit{{File: "f.txt", Line: 2, Column: 3, Text: "needle"}},
		},
		{
			name: "ASCII fold",
			body: "NEEDLE\n",
			q:    Query{Pattern: "needle"},
			hits: []Hit{{File: "f.txt", Line: 1, Column: 1, Text: "NEEDLE"}},
		},
		{
			name: "Unicode fold takes the regex path",
			body: "K needle\n",
			q:    Query{Pattern: "needle"},
			hits: []Hit{{File: "f.txt", Line: 1, Column: 5, Text: "K needle"}},
		},
		{
			name: "regex hit",
			body: "x\nneeeedle y\n",
			q:    Query{Pattern: "ne+dle", Regex: true},
			hits: []Hit{{File: "f.txt", Line: 2, Column: 1, Text: "neeeedle y"}},
		},
		{
			name: "first match per line only",
			body: "needle needle\n",
			q:    Query{Pattern: "needle"},
			hits: []Hit{{File: "f.txt", Line: 1, Column: 1, Text: "needle needle"}},
		},
		{
			name: "an over-long line stops the file",
			body: "needle\n" + long + "\nneedle\n",
			q:    Query{Pattern: "needle"},
			hits: []Hit{{File: "f.txt", Line: 1, Column: 1, Text: "needle"}},
			more: true,
		},
		{
			name: "the hit cap stops the file",
			body: "needle\nneedle\nneedle\n",
			q:    Query{Pattern: "needle", MaxHits: 2},
			hits: []Hit{
				{File: "f.txt", Line: 1, Column: 1, Text: "needle"},
				{File: "f.txt", Line: 2, Column: 1, Text: "needle"},
			},
			more: true,
		},
		{
			name: "reaching the cap on the last line still reports more",
			body: "needle\nneedle\n",
			q:    Query{Pattern: "needle", MaxHits: 2},
			hits: []Hit{
				{File: "f.txt", Line: 1, Column: 1, Text: "needle"},
				{File: "f.txt", Line: 2, Column: 1, Text: "needle"},
			},
			more: true,
		},
		{
			name: "hit text is bounded",
			body: "needle" + wide + "\n",
			q:    Query{Pattern: "needle"},
			hits: []Hit{{File: "f.txt", Line: 1, Column: 1, Text: trimHitText("needle" + wide)}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.noPre {
				old := prefilterEnabled
				prefilterEnabled = false
				t.Cleanup(func() { prefilterEnabled = old })
			}
			root := t.TempDir()
			write(t, root, "f.txt", c.body)
			m, err := Compile(c.q)
			if err != nil {
				t.Fatal(err)
			}
			hits, more := searchFile(root, "f.txt", m, newFileBuffer())
			if !reflect.DeepEqual(hits, c.hits) || more != c.more {
				t.Errorf("got %#v, %v\nwant %#v, %v", hits, more, c.hits, c.more)
			}
		})
	}
}

// TestSearchFileKeepsAGrownBuffer: a file larger than the worker's scratch
// grows it, and the grown buffer is kept for the next file rather than
// reallocated every time.
func TestSearchFileKeepsAGrownBuffer(t *testing.T) {
	root := t.TempDir()
	write(t, root, "f.txt", strings.Repeat("line\n", 100))
	m, err := Compile(Query{Pattern: "needle"})
	if err != nil {
		t.Fatal(err)
	}
	buf := &fileBuffers{data: make([]byte, 0, 4)}
	searchFile(root, "f.txt", m, buf)
	if len(buf.data) != 0 || cap(buf.data) < 500 {
		t.Errorf("buffer len %d cap %d, want an empty buffer of at least 500", len(buf.data), cap(buf.data))
	}
}
