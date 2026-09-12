// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package search

import (
	"bytes"
	"context"
	"os"
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
//
// # The rule this must not break
//
// A filter that wrongly drops a file turns a search into a confident lie: the
// caller is told "no matches" and has no way to tell that from the truth. So
// every judgement below is made in the direction of reading MORE files than
// necessary, and the cases it cannot reason about are not filtered at all.

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
	//
	// Flat arrays rather than map[trigram][]uint32, which is how this was
	// first built and why it did not fit. Measured on a 234-repository
	// workspace, the map form held 202MB for 72 repositories — the data is
	// four bytes per posting, and everything else was Go map buckets and
	// slice headers, roughly five bytes of overhead for every byte of index.
	// Three slices carry the same information with none of that.
	trigrams []uint32
	offs     []uint32
	posts    []uint32
	// foldRisk lists files that a case-insensitive query must read whatever
	// the trigrams say.
	//
	// Case-insensitive matching folds Unicode, and folding reaches outside
	// ASCII: (?i)k matches U+212A KELVIN SIGN, which shares no trigram with
	// "k", so a byte index would drop the file holding it and the search
	// would report nothing with complete confidence.
	//
	// The first version of this took the safe-looking route and treated every
	// file containing any byte above 0x7F as unreadable-by-index. That is
	// almost every source file — one em dash or accented name in a comment is
	// enough — and it cost the index nearly all of its value: measured on the
	// reporting workspace, 193 of 200 repositories used the index and it still
	// read 24,940 files, about 129 per repository.
	//
	// Enumerating Unicode shows the risk is two runes, in total: U+017F ſ
	// folds to "s" and U+212A K folds to "k". Nothing else outside ASCII
	// folds to an ASCII letter. So the set is files containing one of two
	// byte sequences, which in practice is none of them.
	foldRisk []uint32
	// truncated records that the build hit a bound, so a caller knows the
	// index does not describe the whole repository.
	truncated bool
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
//
// A file larger than this is still searched — it is added to the always-read
// set rather than skipped — because an index that quietly excluded big files
// would make a search miss matches inside them.
var maxIndexedFileBytes = maxFileBytes

// BuildIndex indexes every file discover would search, ignoring the query.
//
// The index is query-independent on purpose: it is built once in the
// background and answers any later pattern. That means it cannot apply the
// per-query filters (path_glob, include_tests), so those are applied to the
// candidate list afterwards.
func BuildIndex(ctx context.Context, root string, allowed FileFilter) (*Index, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// Everything discover would ever return: tests included and no path
	// glob, because the index answers queries that have not been asked yet.
	// The per-query filters are applied to the candidate list afterwards.
	paths, truncated, err := discover(ctx, root, true, "", allowed)
	if err != nil {
		return nil, err
	}

	ix := &Index{files: paths, truncated: truncated}
	// The map is transient: it accumulates one repository's postings and is
	// dropped at the end of this function. What survives is the frozen form.
	postings := make(map[trigramKey][]uint32, 1<<12)

	// One buffer for the whole build, reused for every file.
	buf := newFileBuffer()

	// seen is reused across files so one file's trigrams are recorded once
	// without allocating a set per file.
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
			// Binary files are never searched, so excluding them from every
			// candidate list matches what a full scan would have done.
			continue
		case indexAlwaysRead:
			// Unreadable or over-sized: always a candidate, never dropped.
			ix.foldRisk = append(ix.foldRisk, uint32(i))
			continue
		}

		clear(seen)
		for j := 0; j+2 < len(data); j++ {
			seen[key(lowerASCII(data[j]), lowerASCII(data[j+1]), lowerASCII(data[j+2]))] = struct{}{}
		}
		// A file shorter than three bytes cannot contain a pattern of three
		// bytes or more, and Candidates declines anything shorter, so it needs
		// no special treatment: having no trigrams is the correct answer.
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
//
// Posting lists come out in file order already, because files are indexed in
// order; the sort is belt and braces for the intersection, which relies on it.
func (ix *Index) freeze(postings map[trigramKey][]uint32) {
	common := int(float64(len(ix.files)) * commonTrigramFraction)
	if common < 1 {
		common = 1
	}

	keys := make([]uint32, 0, len(postings))
	// kept, not total: sizing posts by everything collected left three
	// quarters of the array as unused capacity, which a slice keeps hold of.
	// The estimate said 181MB while the heap held 710MB — a budget is only as
	// honest as the number it is given, and len() is not what is retained.
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
		ix.offs[i] = uint32(len(ix.posts))
		list := postings[trigramKey(k)]
		// Too common to be worth storing: the key stays so the trigram is
		// still known to exist, the list does not.
		if len(list) > common {
			continue
		}
		sort.Slice(list, func(a, b int) bool { return list[a] < list[b] })
		ix.posts = append(ix.posts, list...)
	}
	ix.offs[len(keys)] = uint32(len(ix.posts))
	ix.bytes = ix.estimateBytes()
}

// commonTrigramFraction is the point past which a trigram stops earning its
// storage.
//
// A trigram in 5% of a repository's files narrows a search by twenty to one at
// best, and in practice by nothing: a query is only as selective as its RAREST
// trigram, so the common ones cost memory to restate what the rare ones have
// already established. Measured on the reporting workspace, trigrams above this
// fraction held 74% of all postings — 33 of 44 million.
//
// Dropping them is why the whole workspace fits. They are dropped as
// CONSTRAINTS, not as knowledge: the trigram stays in the table with an empty
// posting list, so a query can tell "every file might have this" from "no file
// has this". Confusing the two would turn the second into a wrong empty answer.
// It is deliberately tunable. Raising it keeps more postings, narrows harder
// and costs more memory; on the reporting workspace the index held 321MB of
// RSS at 0.05 and roughly doubles at 0.20. The default favours fitting in
// memory over narrowing, because an index too large to hold is worth nothing.
var commonTrigramFraction = envFloat("CORRAL_INDEX_COMMON_FRACTION", 0.05)

// lookup returns the posting list for one trigram, and whether the trigram is
// known at all.
//
// Three outcomes, and the caller must treat them differently:
//
//	present, non-empty — only these files can match
//	present, empty     — too common to store; it constrains nothing
//	absent             — no file contains it; nothing can match
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

// lookup returns the posting list for one trigram.
func (ix *Index) lookup(k trigramKey) []uint32 {
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
		return nil
	}
	return ix.posts[ix.offs[lo]:ix.offs[lo+1]]
}

// foldRiskSeqs are the UTF-8 encodings of the only two runes outside ASCII
// that case-fold to an ASCII letter.
//
// Derived by walking every rune's fold orbit rather than asserted from memory:
// U+017F LATIN SMALL LETTER LONG S and U+212A KELVIN SIGN, and nothing else in
// Unicode. TestFoldRiskSetIsComplete recomputes it so a future Unicode table
// that adds a third cannot pass unnoticed.
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

// lowerASCII folds A-Z only. Indexing and querying both use it, so the two
// agree; anything outside ASCII is handled by the always-read set instead.
func lowerASCII(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

// estimateBytes is the retained size.
//
// With the flat form this is close to exact — three slices of fixed-width
// elements plus the path strings — rather than the guess the map form needed.
// That matters: the budget is only as good as the number it is given, and the
// map-form estimate was 40% under what the heap actually showed.
func (ix *Index) estimateBytes() int {
	// Capacity, not length: a slice retains its whole backing array, and
	// counting only what is in use is how the first version of this reported
	// a quarter of what it actually held.
	n := cap(ix.trigrams)*4 + cap(ix.offs)*4 + cap(ix.posts)*4 + cap(ix.foldRisk)*4
	for _, f := range ix.files {
		n += len(f) + 16 // string header plus bytes
	}
	return n
}

// Candidates returns the files that could contain a match for m, and whether
// the index was able to narrow at all.
//
// A false second return means "no opinion": the caller must search everything,
// which is what happens for a regex, for a pattern too short to yield a
// trigram, and for a truncated index.
func (ix *Index) Candidates(m *Matcher) ([]string, bool) {
	if ix == nil || ix.truncated {
		return nil, false
	}
	// Only a literal can be reduced to required trigrams. A regex may match
	// text sharing no trigram with its source (`a.c` matches "abc"), and
	// deriving the required set from a regex is a different and much larger
	// problem than this solves.
	if !m.CanPrefilter() {
		return nil, false
	}
	pattern := m.indexPattern()
	if len(pattern) < 3 {
		return nil, false
	}
	// A case-insensitive pattern containing a non-ASCII byte cannot be
	// reduced to trigrams at all.
	//
	// Go's (?i) folds Unicode, and folding crosses the ASCII boundary in both
	// directions: U+212A KELVIN SIGN matches "k" and vice versa. The index
	// stores the bytes a file actually contains, so intersecting the posting
	// lists for U+212A's three bytes finds only files written with that
	// character — and silently misses every file containing a plain "k",
	// which is what the query actually matches.
	//
	// The mirror case, an ASCII pattern matching non-ASCII content, is handled
	// by the always-read set: any file with a byte above 0x7F is a candidate
	// for every query. This direction has no such escape, because the file
	// that must be read looks entirely ordinary. So the index declines.
	if m.caseFolded {
		for _, b := range pattern {
			if b > 0x7F {
				return nil, false
			}
		}
	}

	// Every trigram of the pattern must be present in a matching file, so
	// the answer is the intersection of their posting lists. Folding both
	// sides with lowerASCII makes this a superset for a case-sensitive
	// query too — the exact matcher then discards what does not match.
	var acc []uint32
	first := true
	for j := 0; j+2 < len(pattern); j++ {
		k := key(lowerASCII(pattern[j]), lowerASCII(pattern[j+1]), lowerASCII(pattern[j+2]))
		list, known := ix.lookupList(k)
		if !known {
			// No file in this repository holds this trigram, so nothing can
			// match — except the files the index declines to reason about.
			acc, first = nil, false
			break
		}
		if len(list) == 0 {
			// Known but too common to store. It constrains nothing, which is
			// different from constraining everything: skipping it leaves a
			// superset, and the matcher discards what does not match.
			continue
		}
		if first {
			acc, first = list, false
			continue
		}
		acc = intersect(acc, list)
		if len(acc) == 0 {
			break
		}
	}
	if first {
		// Every trigram was too common to store, so nothing was narrowed.
		// Saying "no opinion" sends the caller to the full walk, which is
		// what it would have had to do anyway.
		return nil, false
	}

	// The fold-risk set is only relevant to a case-insensitive query. An
	// exact-byte search has no folding to worry about, so those files are
	// judged on their trigrams like any other.
	extra := ix.foldRisk
	if !m.caseFolded {
		extra = nil
	}
	out := make([]string, 0, len(acc)+len(extra))
	merged := mergeSorted(acc, extra)
	for _, id := range merged {
		out = append(out, ix.files[id])
	}
	return out, true
}

// intersect returns the ids present in both sorted lists.
func intersect(a, b []uint32) []uint32 {
	out := a[:0:0]
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, a[i])
			i++
			j++
		case a[i] < b[j]:
			i++
		default:
			j++
		}
	}
	return out
}

// mergeSorted unions two sorted id lists, keeping order and dropping
// duplicates, so candidates stay in walk order and a file is read once.
func mergeSorted(a, b []uint32) []uint32 {
	out := make([]uint32, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			out = append(out, a[i])
			i++
			j++
		case a[i] < b[j]:
			out = append(out, a[i])
			i++
		default:
			out = append(out, b[j])
			j++
		}
	}
	out = append(out, a[i:]...)
	return append(out, b[j:]...)
}

// indexStatus says how a file should be treated by the index.
type indexStatus int

const (
	// indexOK: the returned bytes are the file's content, index them.
	indexOK indexStatus = iota
	// indexSkipBinary: never searched, so it belongs in no candidate list.
	indexSkipBinary
	// indexAlwaysRead: could not be reasoned about, so it must always be a
	// candidate. This is the direction a filter is allowed to be wrong in.
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

// FilterCandidates applies the per-query filters the index cannot hold.
//
// The index is built once and answers patterns nobody has typed yet, so it
// cannot bake in path_glob or include_tests — those belong to a single query.
// discover applies them during the walk; an indexed search applies them here
// instead, to the same effect and in the same order, which is what keeps the
// two paths returning identical results.
func FilterCandidates(paths []string, m *Matcher) []string {
	if m.IncludeTests() && m.pathGlob == "" {
		return paths
	}
	out := paths[:0:0]
	for _, rel := range paths {
		if !m.IncludeTests() && IsTestFile(rel) {
			continue
		}
		if m.pathGlob != "" && !matchGlob(m.pathGlob, rel) {
			continue
		}
		out = append(out, rel)
	}
	return out
}

// Stats reports the index's internal sizes, for tuning and diagnostics.
func (ix *Index) Stats() (trigrams, postings, files int) {
	return len(ix.trigrams), len(ix.posts), len(ix.files)
}

// PostingHistogram reports how postings are distributed across trigrams,
// bucketed by the fraction of the repository's files a trigram appears in.
func (ix *Index) PostingHistogram() map[string]int {
	out := map[string]int{}
	n := len(ix.files)
	if n == 0 {
		return out
	}
	for i := range ix.trigrams {
		l := int(ix.offs[i+1] - ix.offs[i])
		switch frac := float64(l) / float64(n); {
		case frac > 0.5:
			out[">50%"] += l
		case frac > 0.2:
			out["20-50%"] += l
		case frac > 0.05:
			out["5-20%"] += l
		default:
			out["<5%"] += l
		}
	}
	return out
}

// envFloat reads a fraction in (0,1] from the environment, falling back to def.
//
// A value outside that range, or one that will not parse, falls back rather
// than erroring: a typo should not stop a server from starting, and the cost
// is an index built to the default shape.
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
