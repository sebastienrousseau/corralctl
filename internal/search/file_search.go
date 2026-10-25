// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package search

import (
	"bytes"
	"io"
	"os"
	"strings"

	"github.com/sebastienrousseau/corralctl/internal/safefs"
)

// fileBuffers is the per-file scratch a search needs.
type fileBuffers struct {
	data []byte
}

// newFileBuffer is the per-worker read scratch.
func newFileBuffer() *fileBuffers {
	return &fileBuffers{data: make([]byte, 0, 64*1024)}
}

// openSearchFile opens a file to search without following a symlink. A
// seam, so a read failure after a clean open is testable.
var openSearchFile = func(root, rel string) (*os.File, error) {
	f, _, err := safefs.OpenRegular(root, rel)
	return f, err
}

// searchFile reads one file and returns the lines matching m. It reads
// through safefs, so a symlink is never followed.
func searchFile(root, rel string, m *Matcher, buf *fileBuffers) (hits []Hit, more bool) {
	f, err := openSearchFile(root, rel)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()

	data, readErr := readBounded(f, buf.data[:0])
	if cap(data) > cap(buf.data) {
		buf.data = data[:0]
	}
	if readErr != nil {
		return nil, false
	}

	head := data
	if len(head) > 8000 {
		head = head[:8000]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return nil, false
	}

	fold := m.CanFoldASCII() && !NeedsUnicodeFold(data)

	if prefilterEnabled && m.CanPrefilter() {
		hit := false
		if fold {
			hit = m.containsFold(data)
		} else {
			hit = m.MatchAny(data)
		}
		if !hit {
			return nil, false
		}
	}

	line := 0
	rest := data
	for len(rest) > 0 {
		var raw []byte
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			raw, rest = rest[:i], rest[i+1:]
		} else {
			raw, rest = rest, nil
		}
		if n := len(raw); n > 0 && raw[n-1] == '\r' {
			raw = raw[:n-1]
		}
		line++
		if len(raw) > maxLineBytes {
			return hits, true
		}
		var col int
		if fold {
			col = m.indexFold(raw)
		} else {
			col = m.MatchBytes(raw)
		}
		if col < 0 {
			continue
		}
		hits = append(hits, Hit{
			File:   rel,
			Line:   line,
			Column: col + 1,
			Text:   trimHitText(string(raw)),
		})
		if len(hits) >= m.MaxHits() {
			return hits, true
		}
	}
	return hits, false
}

// prefilterEnabled is a seam. A benchmark flips it to measure the same files,
// the same reads and the same matcher with and without the whole-file test.
var prefilterEnabled = true

// readFileBounded opens root/rel without following symlinks and reads it,
// bounded.
func readFileBounded(root, rel string, dst []byte) ([]byte, error) {
	f, err := openSearchFile(root, rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return readBounded(f, dst)
}

// readBounded reads at most maxFileBytes from f, appending into dst.
func readBounded(f *os.File, dst []byte) ([]byte, error) {
	for {
		if len(dst) == cap(dst) {
			dst = append(dst, 0)[:len(dst)]
		}
		n, err := f.Read(dst[len(dst):cap(dst)])
		dst = dst[:len(dst)+n]
		if err != nil {
			if err == io.EOF {
				return dst, nil
			}
			return dst, err
		}
		if int64(len(dst)) >= maxFileBytes {
			return dst, nil
		}
	}
}

// trimHitText prepares a matching line for a caller: leading and trailing
// whitespace removed, and bounded.
func trimHitText(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxHitTextBytes {
		return s
	}
	cut := maxHitTextBytes
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// utf8Start reports whether b begins a UTF-8 sequence rather than continuing one.
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
