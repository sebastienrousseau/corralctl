// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package search

import (
	"fmt"
	"sync"
)

// candidateScratch holds reusable buffers for posting list intersection and merging.
type candidateScratch struct {
	bufA []uint32
	bufB []uint32
}

var candidateScratchPool = sync.Pool{
	New: func() any {
		return &candidateScratch{
			bufA: make([]uint32, 0, 128),
			bufB: make([]uint32, 0, 128),
		}
	},
}

// Candidates returns the files that could contain a match for m, and whether
// the index was able to narrow at all.
//
// A false second return means "no opinion": the caller must search everything,
// which is what happens for a regex, for a pattern too short to yield a
// trigram, and for a truncated index.
func (ix *Index) Candidates(m *Matcher) ([]string, bool) {
	if ix == nil || ix.incomplete {
		return nil, false
	}
	if !m.CanPrefilter() {
		return nil, false
	}
	pattern := m.indexPattern()
	if len(pattern) < 3 {
		return nil, false
	}
	if m.caseFolded {
		for _, b := range pattern {
			if b > 0x7F {
				return nil, false
			}
		}
	}

	scratch := candidateScratchPool.Get().(*candidateScratch)
	scratch.bufA = scratch.bufA[:0]
	scratch.bufB = scratch.bufB[:0]
	defer candidateScratchPool.Put(scratch)

	var acc []uint32
	first := true
	useBufA := true
	for j := 0; j+2 < len(pattern); j++ {
		k := key(lowerASCII(pattern[j]), lowerASCII(pattern[j+1]), lowerASCII(pattern[j+2]))
		list, known := ix.lookupList(k)
		if !known {
			acc, first = nil, false
			break
		}
		if len(list) == 0 {
			continue
		}
		if first {
			acc, first = list, false
			continue
		}
		if useBufA {
			scratch.bufA = intersect(scratch.bufA, acc, list)
			acc = scratch.bufA
		} else {
			scratch.bufB = intersect(scratch.bufB, acc, list)
			acc = scratch.bufB
		}
		useBufA = !useBufA
		if len(acc) == 0 {
			break
		}
	}
	if first {
		return nil, false
	}

	extra := ix.foldRisk
	if !m.caseFolded {
		extra = nil
	}

	var merged []uint32
	if len(extra) == 0 {
		merged = acc
	} else {
		targetBuf := scratch.bufA
		if !useBufA {
			targetBuf = scratch.bufB
		}
		merged = mergeSorted(targetBuf, acc, extra)
	}

	out := make([]string, 0, len(merged))
	for _, id := range merged {
		out = append(out, ix.files[id])
	}
	return out, true
}

// intersect appends ids present in both sorted lists into dst, resetting dst's length.
func intersect(dst, a, b []uint32) []uint32 {
	out := dst[:0]
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

// mergeSorted unions two sorted id lists into dst, resetting dst's length,
// keeping order and dropping duplicates so candidates stay in walk order.
func mergeSorted(dst, a, b []uint32) []uint32 {
	out := dst[:0]
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

// FilterCandidates applies the per-query filters the index cannot hold.
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

// DeclineReason explains why Candidates returned no opinion, for diagnostics.
func (ix *Index) DeclineReason(m *Matcher) string {
	if ix == nil {
		return "no index"
	}
	if ix.incomplete {
		return "file list incomplete (hit the per-repository file cap)"
	}
	if !m.CanPrefilter() {
		return "not a literal"
	}
	p := m.indexPattern()
	if len(p) < 3 {
		return "pattern shorter than a trigram"
	}
	known, common, absent := 0, 0, 0
	for j := 0; j+2 < len(p); j++ {
		k := key(lowerASCII(p[j]), lowerASCII(p[j+1]), lowerASCII(p[j+2]))
		list, ok := ix.lookupList(k)
		switch {
		case !ok:
			absent++
		case len(list) == 0:
			common++
		default:
			known++
		}
	}
	return fmt.Sprintf("trigrams: %d usable, %d too-common, %d absent (files=%d)",
		known, common, absent, len(ix.files))
}
