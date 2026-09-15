// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package search

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
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
//	flags     4   bit 0: a bound was hit (answers are partial)
//	              bit 1: the file list is incomplete (narrowing unsafe)
//	fpFiles   8   fingerprint: file count at build time
//	fpBytes   8   fingerprint: total bytes
//	fpModNano 8   fingerprint: newest modification time
//	pathLen   4   bytes of the path block
//	common    4   commonTrigramFraction x 10000 at build time
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
	// formatVersion is bumped whenever the layout or its meaning changes. A file written by
	// a different version is discarded and rebuilt rather than misread —
	// there is no migration, because the index is derived data and rebuilding
	// costs less than being wrong about what the bytes mean.
	formatVersion = 3
	headerSize    = 8 + 4*6 + 8*3 + 4 + 4
)

var errWrongFormat = errors.New("search: index file is not a usable corral index")

// Filesystem seams.
//
// Writing an index is a sequence of operations that can each fail for reasons
// a test cannot arrange on a real disk — a full device, a revoked permission
// mid-write, a rename across a boundary. The failure paths matter more than
// most: a half-written index that still maps is the one outcome this format
// is built to prevent, so they are exercised rather than assumed.
var (
	osMkdirAll   = os.MkdirAll
	osCreateTemp = os.CreateTemp
	osRename     = os.Rename
	osOpen       = os.Open
	osLstat      = os.Lstat
	mapFileFn    = mapFile
	// closeFile is seamed because a Close that fails after a successful write
	// is the case that decides whether a truncated file gets renamed into
	// place, and no portable filesystem arrangement produces it on demand.
	closeFile = (*os.File).Close
)

// maxIndexDimension is the largest count the format can hold. A variable so a
// test can lower it: an index with four billion entries cannot be allocated to
// prove the guard works.
var maxIndexDimension int64 = math.MaxUint32

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
	if err := osMkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}

	// Every count below is written as a uint32 and every fingerprint field as
	// a uint64, so refuse anything that would not survive the round trip.
	// These limits cannot be reached by a real repository — four billion files
	// or postings — but an index that silently wrapped would describe the
	// wrong files, which is the failure this format exists to make impossible.
	if err := fitsUint32(len(ix.files), len(ix.trigrams), len(ix.posts), len(ix.foldRisk)); err != nil {
		return err
	}
	if fp.Files < 0 || fp.Bytes < 0 || fp.ModUnixNano < 0 {
		return fmt.Errorf("search: negative fingerprint field: %+v", fp)
	}

	pathBlock := make([]byte, 0, len(ix.files)*24)
	for _, f := range ix.files {
		pathBlock = append(pathBlock, f...)
		pathBlock = append(pathBlock, 0)
	}

	buf := make([]byte, headerSize, headerSize+
		4*(len(ix.trigrams)+len(ix.offs)+len(ix.posts)+len(ix.foldRisk))+len(pathBlock))

	if err := fitsUint32(len(pathBlock)); err != nil {
		return err
	}

	copy(buf[0:], indexMagic)
	le := binary.LittleEndian
	// Each conversion is guarded by the fitsUint32 call above; the
	// suppressions are per-line because that is the only form the linter
	// honours, not because each was judged separately.
	le.PutUint32(buf[8:], formatVersion)
	le.PutUint32(buf[12:], uint32(len(ix.files)))    //nolint:gosec // bounds-checked above
	le.PutUint32(buf[16:], uint32(len(ix.trigrams))) //nolint:gosec // bounds-checked above
	le.PutUint32(buf[20:], uint32(len(ix.posts)))    //nolint:gosec // bounds-checked above
	le.PutUint32(buf[24:], uint32(len(ix.foldRisk))) //nolint:gosec // bounds-checked above
	var flags uint32
	if ix.truncated {
		flags |= 1
	}
	if ix.incomplete {
		flags |= 2
	}
	le.PutUint32(buf[28:], flags)
	le.PutUint64(buf[32:], uint64(fp.Files))       //nolint:gosec // non-negative, checked above
	le.PutUint64(buf[40:], uint64(fp.Bytes))       //nolint:gosec // non-negative, checked above
	le.PutUint64(buf[48:], uint64(fp.ModUnixNano)) //nolint:gosec // non-negative, checked above
	le.PutUint32(buf[56:], uint32(len(pathBlock))) //nolint:gosec // bounds-checked above
	// The pruning threshold is part of what the file MEANS, not just how it
	// was made: an index built at 0.05 keeps a quarter of the postings one
	// built at 0.5 does, and answers the same query by reading four times as
	// many files. Without this, changing the setting leaves every existing
	// index in place and silently ignored — which is exactly what happened,
	// and cost a profiling run that measured the old shape.
	le.PutUint32(buf[60:], uint32(commonTrigramFraction*10000)) //nolint:gosec // a fraction in (0,1]

	buf = appendUint32s(buf, ix.trigrams)
	buf = appendUint32s(buf, ix.offs)
	buf = appendUint32s(buf, ix.posts)
	buf = appendUint32s(buf, ix.foldRisk)
	buf = append(buf, pathBlock...)

	tmp, err := osCreateTemp(filepath.Dir(path), ".idx-*")
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
	if err = closeFile(tmp); err != nil {
		return err
	}
	return osRename(tmpName, path)
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
	f, err := osOpen(path) // #nosec G304 -- path is derived from the cache dir and a repository hash
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = f.Close()
		}
	}()

	data, err := mapFileFn(f)
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
	// The writer refuses negative fingerprint fields, so anything above the
	// signed range came from a corrupt or foreign file rather than from us.
	fpFiles, fpBytes, fpMod := le.Uint64(data[32:]), le.Uint64(data[40:]), le.Uint64(data[48:])
	if fpFiles > math.MaxInt64 || fpBytes > math.MaxInt64 || fpMod > math.MaxInt64 {
		return nil, fmt.Errorf("%w: fingerprint out of range", errWrongFormat)
	}
	got := DiskFingerprint{
		Files:       int64(fpFiles),
		Bytes:       int64(fpBytes),
		ModUnixNano: int64(fpMod),
	}
	pathLen := int(le.Uint32(data[56:]))
	builtCommon := le.Uint32(data[60:])

	if builtCommon != uint32(commonTrigramFraction*10000) {
		return nil, fmt.Errorf("search: index was built with a different pruning threshold")
	}
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
		files:      files,
		trigrams:   trigrams,
		offs:       offs,
		posts:      posts,
		foldRisk:   foldRisk,
		truncated:  flags&1 != 0,
		incomplete: flags&2 != 0,
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

// fitsUint32 reports an error if any count cannot be written as a uint32.
func fitsUint32(ns ...int) error {
	for _, n := range ns {
		if n < 0 || int64(n) > maxIndexDimension {
			return fmt.Errorf("search: index dimension %d does not fit the on-disk format", n)
		}
	}
	return nil
}
