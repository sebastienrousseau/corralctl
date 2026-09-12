// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package search

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"
)

// The index on disk.
//
// Held in memory, a complete trigram index over this workspace is several
// hundred megabytes — measured, 181MB for 233 repositories once common
// trigrams were dropped, and the dropping is what limits how well it narrows.
// That is the wrong shape for a background server: the data is read-only,
// rebuilt from the workspace whenever it is lost, and touched in small pieces
// by any one query. It is exactly what a file the kernel can page should hold.
//
// So the index is written once and mapped. Resident memory becomes whatever
// the queries actually touch, and the kernel reclaims the rest under pressure
// without the process noticing. That is the difference between an index whose
// size is a budget to police and one whose size is a file.
//
// # Layout
//
// Fixed-width, little-endian, and laid out so the mapped bytes ARE the index:
// the posting lists and trigram table are read in place, with no decode step
// and no second copy. A decode would put the whole index back in the heap and
// undo the point of mapping it.
//
//	magic     8   "corralix"
//	version   4   formatVersion
//	files     4   number of files
//	trigrams  4   number of distinct trigrams
//	postings  4   number of posting entries
//	foldRisk  4   number of fold-risk file ids
//	flags     4   bit 0: the index covers only part of the repository
//	fpFiles   8   fingerprint: file count at build time
//	fpBytes   8   fingerprint: total bytes
//	fpModNano 8   fingerprint: newest modification time
//	pathLen   4   bytes of the path block
//	_pad      4   so the arrays below start 8-byte aligned
//	trigrams  4 × trigrams
//	offs      4 × (trigrams+1)
//	posts     4 × postings
//	foldRisk  4 × foldRisk
//	paths     NUL-separated, pathLen bytes
//
// Alignment matters: the uint32 arrays are read as uint32 slices over the
// mapped bytes, which requires their offsets to be multiples of four. The
// header is sized to guarantee that.

const (
	indexMagic = "corralix"
	// formatVersion is bumped whenever the layout changes. A file written by
	// a different version is discarded and rebuilt rather than misread —
	// there is no migration, because the index is derived data and rebuilding
	// costs less than being wrong about what the bytes mean.
	formatVersion = 1
	headerSize    = 8 + 4*6 + 8*3 + 4 + 4
)

var errWrongFormat = errors.New("search: index file is not a usable corral index")

// DiskFingerprint identifies the state of a repository an index was built from.
//
// The same three facts the symbol cache uses, and for the same reason: a walk
// can compute them without reading a byte of content, so validating an index
// costs a walk rather than a rebuild.
type DiskFingerprint struct {
	Files       int64
	Bytes       int64
	ModUnixNano int64
}

// WriteIndex serialises an index to path, atomically.
//
// Written to a temporary file and renamed, so a reader either sees the whole
// index or the previous one. A half-written index that mapped cleanly and
// described the wrong files would be the worst outcome available: a search
// that silently looks in the wrong places.
func WriteIndex(path string, ix *Index, fp DiskFingerprint) (err error) {
	if ix == nil {
		return errors.New("search: no index to write")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}

	pathBlock := make([]byte, 0, len(ix.files)*24)
	for _, f := range ix.files {
		pathBlock = append(pathBlock, f...)
		pathBlock = append(pathBlock, 0)
	}

	buf := make([]byte, headerSize, headerSize+
		4*(len(ix.trigrams)+len(ix.offs)+len(ix.posts)+len(ix.foldRisk))+len(pathBlock))

	copy(buf[0:], indexMagic)
	le := binary.LittleEndian
	le.PutUint32(buf[8:], formatVersion)
	le.PutUint32(buf[12:], uint32(len(ix.files)))
	le.PutUint32(buf[16:], uint32(len(ix.trigrams)))
	le.PutUint32(buf[20:], uint32(len(ix.posts)))
	le.PutUint32(buf[24:], uint32(len(ix.foldRisk)))
	var flags uint32
	if ix.truncated {
		flags |= 1
	}
	le.PutUint32(buf[28:], flags)
	le.PutUint64(buf[32:], uint64(fp.Files))
	le.PutUint64(buf[40:], uint64(fp.Bytes))
	le.PutUint64(buf[48:], uint64(fp.ModUnixNano))
	le.PutUint32(buf[56:], uint32(len(pathBlock)))

	buf = appendUint32s(buf, ix.trigrams)
	buf = appendUint32s(buf, ix.offs)
	buf = appendUint32s(buf, ix.posts)
	buf = appendUint32s(buf, ix.foldRisk)
	buf = append(buf, pathBlock...)

	tmp, err := os.CreateTemp(filepath.Dir(path), ".idx-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err = tmp.Write(buf); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func appendUint32s(dst []byte, v []uint32) []byte {
	var scratch [4]byte
	for _, x := range v {
		binary.LittleEndian.PutUint32(scratch[:], x)
		dst = append(dst, scratch[:]...)
	}
	return dst
}

// MappedIndex is an index backed by a mapped file.
//
// Close releases the mapping. Using the Index after Close is a use-after-free:
// its slices point into the mapped region, which is the whole point and the
// whole hazard. The cache that owns these keeps them alive for as long as any
// query can reach them.
type MappedIndex struct {
	*Index
	data []byte
	file *os.File
}

// Close unmaps the file.
func (m *MappedIndex) Close() error {
	if m == nil {
		return nil
	}
	err := unmapFile(m.data)
	m.data = nil
	if m.file != nil {
		if cerr := m.file.Close(); err == nil {
			err = cerr
		}
		m.file = nil
	}
	return err
}

// OpenIndex maps an index file and checks it describes the given fingerprint.
//
// A mismatch is reported as an error rather than served: an index built from a
// different state of the repository would send a search to the wrong files,
// and the caller's fallback — rebuild, or search exhaustively — is always
// correct where this would not be.
func OpenIndex(path string, want DiskFingerprint) (mi *MappedIndex, err error) {
	f, err := os.Open(path) // #nosec G304 -- path is derived from the cache dir and a repository hash
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
		}
	}()

	data, err := mapFile(f)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = unmapFile(data)
		}
	}()

	if len(data) < headerSize || string(data[:8]) != indexMagic {
		return nil, errWrongFormat
	}
	le := binary.LittleEndian
	if le.Uint32(data[8:]) != formatVersion {
		return nil, fmt.Errorf("%w: version %d", errWrongFormat, le.Uint32(data[8:]))
	}
	nFiles := int(le.Uint32(data[12:]))
	nTri := int(le.Uint32(data[16:]))
	nPosts := int(le.Uint32(data[20:]))
	nFold := int(le.Uint32(data[24:]))
	flags := le.Uint32(data[28:])
	got := DiskFingerprint{
		Files:       int64(le.Uint64(data[32:])),
		Bytes:       int64(le.Uint64(data[40:])),
		ModUnixNano: int64(le.Uint64(data[48:])),
	}
	pathLen := int(le.Uint32(data[56:]))

	if got != want {
		return nil, fmt.Errorf("search: index is stale")
	}

	// Every offset is checked against the file's actual length before any
	// slice is built over it. A truncated or corrupt file must fail here, not
	// by reading past the mapping.
	need := headerSize + 4*(nTri+(nTri+1)+nPosts+nFold) + pathLen
	if need != len(data) {
		return nil, fmt.Errorf("%w: says %d bytes, file is %d", errWrongFormat, need, len(data))
	}

	off := headerSize
	trigrams, off := uint32sAt(data, off, nTri)
	offs, off := uint32sAt(data, off, nTri+1)
	posts, off := uint32sAt(data, off, nPosts)
	foldRisk, off := uint32sAt(data, off, nFold)

	files := make([]string, 0, nFiles)
	block := data[off : off+pathLen]
	start := 0
	for i, b := range block {
		if b == 0 {
			files = append(files, string(block[start:i]))
			start = i + 1
		}
	}
	if len(files) != nFiles {
		return nil, fmt.Errorf("%w: %d paths, header says %d", errWrongFormat, len(files), nFiles)
	}

	// offs must be non-decreasing and end at len(posts), or a lookup could
	// slice outside the postings array.
	if len(offs) != nTri+1 || (nTri > 0 && int(offs[nTri]) != nPosts) {
		return nil, fmt.Errorf("%w: offset table does not close", errWrongFormat)
	}
	for i := 1; i < len(offs); i++ {
		if offs[i] < offs[i-1] || int(offs[i]) > nPosts {
			return nil, fmt.Errorf("%w: offset table is not ordered", errWrongFormat)
		}
	}

	ix := &Index{
		files:     files,
		trigrams:  trigrams,
		offs:      offs,
		posts:     posts,
		foldRisk:  foldRisk,
		truncated: flags&1 != 0,
	}
	// The mapped arrays are not on the heap, so they do not count toward the
	// in-memory budget. What the process holds is the path list.
	ix.bytes = 0
	for _, p := range files {
		ix.bytes += len(p) + 16
	}
	return &MappedIndex{Index: ix, data: data, file: f}, nil
}

// uint32sAt builds a uint32 slice over the mapped bytes, without copying.
//
// Safe because the caller has already checked that the whole region lies
// inside the mapping, and because the header is sized so every array starts on
// a four-byte boundary.
func uint32sAt(data []byte, off, n int) ([]uint32, int) {
	if n == 0 {
		return nil, off
	}
	//nolint:gosec // bounds are validated by the caller against len(data)
	s := unsafe.Slice((*uint32)(unsafe.Pointer(&data[off])), n)
	return s, off + 4*n
}
