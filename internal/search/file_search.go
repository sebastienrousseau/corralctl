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
	data, ok := readSearchable(root, rel, buf)
	if !ok {
		return nil, false
	}
	fold := m.CanFoldASCII() && !NeedsUnicodeFold(data)
	if !mayMatch(m, data, fold) {
		return nil, false
	}
	return matchLines(rel, data, m, fold)
}

// binarySniffBytes is how much of a file is checked for a NUL byte before
// it is treated as binary and skipped.
const binarySniffBytes = 8000

// readSearchable reads root/rel into buf's scratch, keeping the scratch
// if the read had to grow it. It reports false for a file that cannot be
// opened or read, or that looks binary.
func readSearchable(root, rel string, buf *fileBuffers) ([]byte, bool) {
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
	head := data[:min(len(data), binarySniffBytes)]
	return data, bytes.IndexByte(head, 0) < 0
}

// mayMatch is the whole-file prefilter: false only when m cannot match
// anywhere in data, so the per-line scan can be skipped.
func mayMatch(m *Matcher, data []byte, fold bool) bool {
	if !prefilterEnabled || !m.CanPrefilter() {
		return true
	}
	if fold {
		return m.containsFold(data)
	}
	return m.MatchAny(data)
}

// matchLines returns the first match on each line of data, stopping with
// more set at an over-long line or at m's hit cap.
func matchLines(rel string, data []byte, m *Matcher, fold bool) (hits []Hit, more bool) {
	line := 0
	for rest := data; len(rest) > 0; {
		var raw []byte
		raw, rest = nextLine(rest)
		line++
		if len(raw) > maxLineBytes {
			return hits, true
		}
		col := matchLine(m, raw, fold)
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

// nextLine splits the first line off data, without its newline or a
// trailing carriage return.
func nextLine(data []byte) (line, rest []byte) {
	line = data
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		line, rest = data[:i], data[i+1:]
	}
	if n := len(line); n > 0 && line[n-1] == '\r' {
		line = line[:n-1]
	}
	return line, rest
}

// matchLine returns the byte offset of m's first match in raw, or -1.
func matchLine(m *Matcher, raw []byte, fold bool) int {
	if fold {
		return m.indexFold(raw)
	}
	return m.MatchBytes(raw)
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
