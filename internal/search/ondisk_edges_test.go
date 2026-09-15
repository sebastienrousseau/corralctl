// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

package search

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The index writer's failure paths.
//
// A half-written index that still maps cleanly is the one outcome this format
// exists to prevent: it would describe the wrong files and send every search to
// the wrong places, confidently. So each way the write can fail is exercised,
// and each must leave no index rather than a plausible one.

func swap[T any](t *testing.T, target *T, replacement T) {
	t.Helper()
	original := *target
	*target = replacement
	t.Cleanup(func() { *target = original })
}

func smallIndex(t *testing.T) *Index {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "a.go", "package a\nfunc Alpha() {}\n")
	ix, err := BuildIndex(context.Background(), root, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func TestWriteIndexRefusesNothingToWrite(t *testing.T) {
	if err := WriteIndex(filepath.Join(t.TempDir(), "x.idx"), nil, testFP); err == nil {
		t.Error("writing a nil index should be an error, not an empty file")
	}
}

func TestWriteIndexRefusesDimensionsThatWouldWrap(t *testing.T) {
	// fitsUint32 is the guard; drive it directly for the boundary, since an
	// index with four billion entries cannot be allocated to test end to end.
	if err := fitsUint32(0, 1, math.MaxUint32); err != nil {
		t.Errorf("values inside the range were rejected: %v", err)
	}
	if err := fitsUint32(-1); err == nil {
		t.Error("a negative dimension should be refused")
	}
	if err := fitsUint32(math.MaxUint32 + 1); err == nil {
		t.Error("a dimension above uint32 should be refused, not silently wrapped")
	}
}

func TestWriteIndexRefusesNegativeFingerprint(t *testing.T) {
	ix := smallIndex(t)
	for _, fp := range []DiskFingerprint{
		{Files: -1}, {Bytes: -1}, {ModUnixNano: -1},
	} {
		if err := WriteIndex(filepath.Join(t.TempDir(), "x.idx"), ix, fp); err == nil {
			t.Errorf("negative fingerprint %+v was accepted", fp)
		}
	}
}

func TestWriteIndexSurfacesFilesystemFailures(t *testing.T) {
	ix := smallIndex(t)
	boom := errors.New("boom")

	t.Run("mkdir", func(t *testing.T) {
		swap(t, &osMkdirAll, func(string, os.FileMode) error { return boom })
		if err := WriteIndex(filepath.Join(t.TempDir(), "x.idx"), ix, testFP); !errors.Is(err, boom) {
			t.Errorf("err = %v, want the mkdir failure", err)
		}
	})

	t.Run("create temp", func(t *testing.T) {
		swap(t, &osCreateTemp, func(string, string) (*os.File, error) { return nil, boom })
		if err := WriteIndex(filepath.Join(t.TempDir(), "x.idx"), ix, testFP); !errors.Is(err, boom) {
			t.Errorf("err = %v, want the create failure", err)
		}
	})

	t.Run("write", func(t *testing.T) {
		dir := t.TempDir()
		swap(t, &osCreateTemp, func(d, pattern string) (*os.File, error) {
			f, err := os.CreateTemp(d, pattern)
			if err != nil {
				return nil, err
			}
			// Closed already, so the write fails and the partial file is
			// removed rather than renamed into place.
			_ = f.Close()
			return f, nil
		})
		path := filepath.Join(dir, "x.idx")
		if err := WriteIndex(path, ix, testFP); err == nil {
			t.Error("a failed write should be reported")
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Error("a failed write left an index behind; a later read would trust it")
		}
		leftovers, _ := filepath.Glob(filepath.Join(dir, ".idx-*"))
		if len(leftovers) != 0 {
			t.Errorf("temporary files left behind: %v", leftovers)
		}
	})

	t.Run("rename", func(t *testing.T) {
		swap(t, &osRename, func(string, string) error { return boom })
		dir := t.TempDir()
		if err := WriteIndex(filepath.Join(dir, "x.idx"), ix, testFP); !errors.Is(err, boom) {
			t.Errorf("err = %v, want the rename failure", err)
		}
		leftovers, _ := filepath.Glob(filepath.Join(dir, ".idx-*"))
		if len(leftovers) != 0 {
			t.Errorf("a failed rename left its temporary file: %v", leftovers)
		}
	})
}

func TestOpenIndexSurfacesFailures(t *testing.T) {
	ix := smallIndex(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "x.idx")
	if err := WriteIndex(path, ix, testFP); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("boom")

	t.Run("missing file", func(t *testing.T) {
		if _, err := OpenIndex(filepath.Join(dir, "absent.idx"), testFP); err == nil {
			t.Error("opening a missing index should fail")
		}
	})

	t.Run("open fails", func(t *testing.T) {
		swap(t, &osOpen, func(string) (*os.File, error) { return nil, boom })
		if _, err := OpenIndex(path, testFP); !errors.Is(err, boom) {
			t.Errorf("err = %v, want the open failure", err)
		}
	})

	t.Run("map fails", func(t *testing.T) {
		swap(t, &mapFileFn, func(*os.File) ([]byte, error) { return nil, boom })
		if _, err := OpenIndex(path, testFP); !errors.Is(err, boom) {
			t.Errorf("err = %v, want the mmap failure", err)
		}
	})

	t.Run("fingerprint out of signed range", func(t *testing.T) {
		raw, err := os.ReadFile(path) //nolint:gosec // a path this test just wrote
		if err != nil {
			t.Fatal(err)
		}
		// The writer refuses negatives, so this can only come from a corrupt
		// or foreign file — and it must be refused, not reinterpreted.
		binary.LittleEndian.PutUint64(raw[32:], math.MaxUint64)
		bad := filepath.Join(dir, "badfp.idx")
		if err := os.WriteFile(bad, raw, 0o600); err != nil { //nolint:gosec // a name this test chose, under t.TempDir()
			t.Fatal(err)
		}
		if _, err := OpenIndex(bad, testFP); err == nil {
			t.Error("a fingerprint above the signed range was accepted")
		}
	})

	t.Run("path count disagrees with the header", func(t *testing.T) {
		raw, err := os.ReadFile(path) //nolint:gosec // a path this test just wrote
		if err != nil {
			t.Fatal(err)
		}
		// Claim one more file than the path block contains. Every other
		// length still adds up, so only the path count catches it.
		binary.LittleEndian.PutUint32(raw[12:], binary.LittleEndian.Uint32(raw[12:])+1)
		bad := filepath.Join(dir, "badcount.idx")
		if err := os.WriteFile(bad, raw, 0o600); err != nil { //nolint:gosec // a name this test chose, under t.TempDir()
			t.Fatal(err)
		}
		if _, err := OpenIndex(bad, testFP); err == nil {
			t.Error("a header claiming more paths than exist was accepted")
		}
	})
}

func TestMappedIndexCloseIsIdempotent(t *testing.T) {
	ix := smallIndex(t)
	path := filepath.Join(t.TempDir(), "x.idx")
	if err := WriteIndex(path, ix, testFP); err != nil {
		t.Fatal(err)
	}
	mi, err := OpenIndex(path, testFP)
	if err != nil {
		t.Fatal(err)
	}
	if err := mi.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	// The cache can drop an entry more than once — an expired read and an
	// eviction can both reach it — so a second close must be harmless.
	if err := mi.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
	var nilMapped *MappedIndex
	if err := nilMapped.Close(); err != nil {
		t.Errorf("closing a nil mapping: %v", err)
	}
}

func TestUint32sAtHandlesEmptyRuns(t *testing.T) {
	data := make([]byte, 16)
	s, off := uint32sAt(data, 8, 0)
	if s != nil || off != 8 {
		t.Errorf("an empty run should consume nothing: got %v, off=%d", s, off)
	}
}

func TestMapFileRefusesAnEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path) //nolint:gosec // a path this test just wrote
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := mapFile(f); err == nil {
		t.Error("mapping an empty file should fail rather than return an empty index")
	}
}

func TestMapFileReportsStatFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gone")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path) //nolint:gosec // a path this test just wrote
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close() // Stat on a closed descriptor fails.
	if _, err := mapFile(f); err == nil {
		t.Error("mapping a closed file should fail")
	}
}

func TestUnmapEmptyIsHarmless(t *testing.T) {
	if err := unmapFile(nil); err != nil {
		t.Errorf("unmapping nothing: %v", err)
	}
}

func TestWriteIndexRefusesAnIndexTooLargeForTheFormat(t *testing.T) {
	ix := smallIndex(t)
	// Lowering the limit is the only way to reach the guard: an index with
	// four billion entries cannot be allocated to prove it works.
	swap(t, &maxIndexDimension, int64(0))
	if err := WriteIndex(filepath.Join(t.TempDir(), "x.idx"), ix, testFP); err == nil {
		t.Error("an index beyond the format's range was written anyway")
	}
}

func TestWriteIndexRefusesAPathBlockTooLargeForTheFormat(t *testing.T) {
	// The path block is bounded separately from the counts, and reaching that
	// second guard needs an index where the names are large and everything
	// else is small: files of two bytes have no trigrams at all, so only the
	// path block grows.
	root := t.TempDir()
	long := strings.Repeat("n", 200)
	for i := 0; i < 20; i++ {
		writeFile(t, root, fmt.Sprintf("%s%02d.go", long, i), "x\n")
	}
	ix, err := BuildIndex(context.Background(), root, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	if len(ix.trigrams) != 0 {
		t.Fatalf("setup produced %d trigrams; the counts must stay below the limit", len(ix.trigrams))
	}

	// Above every count, below the path block.
	swap(t, &maxIndexDimension, int64(len(ix.files)+1))
	if err := WriteIndex(filepath.Join(t.TempDir(), "y.idx"), ix, testFP); err == nil {
		t.Error("a path block beyond the format's range was written anyway")
	}
}

func TestWriteIndexReportsACloseFailure(t *testing.T) {
	ix := smallIndex(t)
	dir := t.TempDir()
	boom := errors.New("close failed")
	swap(t, &closeFile, func(f *os.File) error {
		_ = f.Close()
		return boom
	})
	path := filepath.Join(dir, "x.idx")
	if err := WriteIndex(path, ix, testFP); !errors.Is(err, boom) {
		t.Errorf("err = %v, want the close failure", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a file that failed to close was renamed into place anyway")
	}
}

func TestIndexFlagsSurviveTheRoundTrip(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "a.go", "package a\nfunc A() {}\n")
	ix, err := BuildIndex(context.Background(), root, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ truncated, incomplete bool }{
		{false, false}, {true, false}, {false, true}, {true, true},
	} {
		ix.truncated, ix.incomplete = tc.truncated, tc.incomplete
		path := filepath.Join(t.TempDir(), "x.idx")
		if err := WriteIndex(path, ix, testFP); err != nil {
			t.Fatal(err)
		}
		mi, err := OpenIndex(path, testFP)
		if err != nil {
			t.Fatal(err)
		}
		if mi.Truncated() != tc.truncated {
			t.Errorf("truncated: wrote %v, read %v", tc.truncated, mi.Truncated())
		}
		if mi.incomplete != tc.incomplete {
			t.Errorf("incomplete: wrote %v, read %v", tc.incomplete, mi.incomplete)
		}
		_ = mi.Close()
	}
}

func TestOpenIndexRefusesADifferentPruningThreshold(t *testing.T) {
	ix := smallIndex(t)
	path := filepath.Join(t.TempDir(), "x.idx")
	if err := WriteIndex(path, ix, testFP); err != nil {
		t.Fatal(err)
	}
	// An index built at one threshold keeps a different set of postings from
	// one built at another, and answers the same query by reading a different
	// number of files. Leaving it in place would silently measure the old
	// shape, which is exactly what happened once.
	swap(t, &commonTrigramFraction, commonTrigramFraction/2)
	if _, err := OpenIndex(path, testFP); err == nil {
		t.Error("an index built under a different pruning threshold was accepted")
	}
}

func TestOpenIndexRefusesABrokenOffsetTable(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		writeFile(t, root, filepath.Join("pkg", "f"+string(rune('a'+i))+".go"),
			"package p\nfunc Distinct"+string(rune('A'+i))+"() {}\n")
	}
	ix, err := BuildIndex(context.Background(), root, allowAll)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "x.idx")
	if err := WriteIndex(path, ix, testFP); err != nil {
		t.Fatal(err)
	}
	sound, err := os.ReadFile(path) //nolint:gosec // a path this test just wrote
	if err != nil {
		t.Fatal(err)
	}
	nTri := int(binary.LittleEndian.Uint32(sound[16:]))
	offsAt := headerSize + 4*nTri

	for _, tc := range []struct {
		name   string
		damage func([]byte)
	}{
		{"last offset does not close the table", func(b []byte) {
			binary.LittleEndian.PutUint32(b[offsAt+4*nTri:], 0)
		}},
		{"offsets run backwards", func(b []byte) {
			binary.LittleEndian.PutUint32(b[offsAt+4:], math.MaxUint32-1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := append([]byte(nil), sound...)
			tc.damage(c)
			bad := filepath.Join(dir, tc.name+".idx")
			if err := os.WriteFile(bad, c, 0o600); err != nil { //nolint:gosec // a name this test chose, under t.TempDir()
				t.Fatal(err)
			}
			// An offset table that does not add up would slice outside the
			// postings array, which is a read past the mapping rather than a
			// wrong answer.
			if mi, err := OpenIndex(bad, testFP); err == nil {
				_ = mi.Close()
				t.Error("a broken offset table was accepted")
			}
		})
	}
}

func TestMapFileReportsAnUnmappableTarget(t *testing.T) {
	// A directory has a size and opens cleanly, and mmap refuses it — which is
	// the shape of every real mmap failure this code can meet.
	d, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	if _, err := mapFile(d); err == nil {
		t.Error("mapping a directory should fail")
	}
}
