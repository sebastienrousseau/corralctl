// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package search

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// A trigram index over one repository.
//
// Without it, every content query reads every candidate file: measured on a
// 234-repository workspace, 58,170 files per query, which is why
// corral_search_code sat at a 7.9s p95 no matter how cheap the per-file work
// became. Reading fewer files is the only thing that changes that, and knowing
// which files cannot possibly match is the only way to read fewer.
//
// The index maps each three-byte sequence to the files containing it. A query
// for a literal needs every one of its trigrams present, so the files worth
// reading are the intersection of those posting lists — usually a handful, and
// often none.
//
// It never decides a match. It decides which files to open; the matcher then
// runs over those files exactly as before, so a hit is found and reported by
// the same code whether the index was used or not.

// trigramKey packs three bytes into one integer.
type trigramKey uint32

func key(a, b, c byte) trigramKey {
	return trigramKey(a)<<16 | trigramKey(b)<<8 | trigramKey(c)
}

// Index is one repository's trigram index.
type Index struct {
	// files is the indexed path set, in the order the walk produced, which
	// is the order results are reported in.
	files []string
	// trigrams are the distinct trigrams present, sorted, and offs indexes
	// posts: the ids for trigrams[i] are posts[offs[i]:offs[i+1]].
	trigrams []uint32
	offs     []uint32
	posts    []uint32
	// foldRisk lists files that a case-insensitive query must read whatever
	// the trigrams say.
	foldRisk []uint32
	// truncated records that a bound was hit, so a search says its answer is
	// partial. It does NOT decide whether the index may be used.
	truncated bool
	// incomplete records that the file list is missing entries a search would
	// otherwise read, which is the only condition that makes narrowing unsafe.
	incomplete bool
	// bytes is the approximate retained size, for the cache budget.
	bytes int
}

// Files is how many files the index covers.
func (ix *Index) Files() int { return len(ix.files) }

// Bytes is the approximate memory the index occupies.
func (ix *Index) Bytes() int { return ix.bytes }

// Truncated reports that the index covers only part of the repository.
func (ix *Index) Truncated() bool { return ix.truncated }

// maxIndexedFileBytes bounds what is indexed from one file.
var maxIndexedFileBytes = maxFileBytes

// BuildIndex indexes every file discover would search, ignoring the query.
func BuildIndex(ctx context.Context, root string, allowed FileFilter) (*Index, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	paths, hitFileCap, skippedLarge, err := discover(ctx, root, true, "", allowed)
	if err != nil {
		return nil, err
	}

	ix := &Index{
		files:      paths,
		truncated:  hitFileCap || skippedLarge,
		incomplete: hitFileCap,
	}
	postings := make(map[trigramKey][]uint32, 1<<12)
	buf := newFileBuffer()
	seen := make(map[trigramKey]struct{}, 1<<12)

	for i, rel := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, status := readForIndex(root, rel, buf)
		if cap(data) > cap(buf.data) {
			buf.data = data[:0]
		}
		switch status {
		case indexSkipBinary:
			continue
		case indexAlwaysRead:
			ix.foldRisk = append(ix.foldRisk, uint32(i))
			continue
		}

		clear(seen)
		for j := 0; j+2 < len(data); j++ {
			seen[key(lowerASCII(data[j]), lowerASCII(data[j+1]), lowerASCII(data[j+2]))] = struct{}{}
		}
		if containsFoldRisk(data) {
			ix.foldRisk = append(ix.foldRisk, uint32(i))
		}
		for k := range seen {
			postings[k] = append(postings[k], uint32(i))
		}
	}

	ix.freeze(postings)
	return ix, nil
}

// freeze converts the build-time map into the flat form the queries use.
func (ix *Index) freeze(postings map[trigramKey][]uint32) {
	common := int(float64(len(ix.files)) * commonTrigramFraction)
	if common < minPrunedPostings {
		common = minPrunedPostings
	}

	keys := make([]uint32, 0, len(postings))
	kept := 0
	for k, v := range postings {
		keys = append(keys, uint32(k))
		if len(v) <= common {
			kept += len(v)
		}
	}
	sort.Slice(keys, func(a, b int) bool { return keys[a] < keys[b] })

	ix.trigrams = keys
	ix.offs = make([]uint32, len(keys)+1)
	ix.posts = make([]uint32, 0, kept)
	for i, k := range keys {
		ix.offs[i] = uint32(len(ix.posts)) //nolint:gosec // bounded by one repository's postings
		list := postings[trigramKey(k)]
		if len(list) > common {
			continue
		}
		sort.Slice(list, func(a, b int) bool { return list[a] < list[b] })
		ix.posts = append(ix.posts, list...)
	}
	ix.offs[len(keys)] = uint32(len(ix.posts)) //nolint:gosec // same bound as the loop above
	ix.bytes = ix.estimateBytes()
}

// minPrunedPostings is the shortest posting list worth dropping.
const minPrunedPostings = 64

// commonTrigramFraction is the point past which a trigram stops earning its
// storage.
var commonTrigramFraction = envFloat("CORRAL_INDEX_COMMON_FRACTION", 0.5)

// lookupList returns the posting list for one trigram, and whether the trigram is
// known at all.
func (ix *Index) lookupList(k trigramKey) ([]uint32, bool) {
	want := uint32(k)
	lo, hi := 0, len(ix.trigrams)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if ix.trigrams[mid] < want {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo == len(ix.trigrams) || ix.trigrams[lo] != want {
		return nil, false
	}
	return ix.posts[ix.offs[lo]:ix.offs[lo+1]], true
}

// foldRiskSeqs are the UTF-8 encodings of the only two runes outside ASCII
// that case-fold to an ASCII letter.
var foldRiskSeqs = [][]byte{
	[]byte(string(rune(0x017F))),
	[]byte(string(rune(0x212A))),
}

// containsFoldRisk reports whether a case-insensitive match could reach this
// file through a fold the trigram index cannot see.
func containsFoldRisk(data []byte) bool {
	for _, seq := range foldRiskSeqs {
		if bytes.Contains(data, seq) {
			return true
		}
	}
	return false
}

// lowerASCII folds A-Z only.
func lowerASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

// estimateBytes is the retained size.
func (ix *Index) estimateBytes() int {
	n := cap(ix.trigrams)*4 + cap(ix.offs)*4 + cap(ix.posts)*4 + cap(ix.foldRisk)*4
	for _, f := range ix.files {
		n += len(f) + 16
	}
	return n
}

// indexStatus says how a file should be treated by the index.
type indexStatus int

const (
	indexOK indexStatus = iota
	indexSkipBinary
	indexAlwaysRead
)

// readForIndex reads a file for indexing and classifies it.
func readForIndex(root, rel string, buf *fileBuffers) ([]byte, indexStatus) {
	data, err := readFileBounded(root, rel, buf.data[:0])
	if err != nil {
		return nil, indexAlwaysRead
	}
	if int64(len(data)) >= maxIndexedFileBytes {
		return data, indexAlwaysRead
	}
	head := data
	if len(head) > 8000 {
		head = head[:8000]
	}
	for _, b := range head {
		if b == 0 {
			return data, indexSkipBinary
		}
	}
	return data, indexOK
}

// envFloat reads a fraction in (0,1] from the environment, falling back to def.
func envFloat(name string, def float64) float64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v <= 0 || v > 1 {
		return def
	}
	return v
}

// Fingerprint summarises a repository's searchable files without reading one.
func Fingerprint(ctx context.Context, root string, allowed FileFilter) (DiskFingerprint, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	paths, _, _, err := discover(ctx, root, true, "", allowed)
	if err != nil {
		return DiskFingerprint{}, err
	}
	fp := DiskFingerprint{Files: int64(len(paths))}
	for _, rel := range paths {
		info, err := osLstat(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		fp.Bytes += info.Size()
		if mod := info.ModTime().UnixNano(); mod > fp.ModUnixNano {
			fp.ModUnixNano = mod
		}
	}
	return fp, nil
}
