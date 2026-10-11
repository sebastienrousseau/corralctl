// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build darwin

package engine

import (
	"errors"
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPlatformFinderTagsRoundTrip(t *testing.T) {
	path := t.TempDir()
	want := []string{"In-Flight\n2", "Ecosystem: Go\n0"}
	if err := platformWriteFinderTags(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := platformReadFinderTags(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tags = %v, want %v", got, want)
	}

	// Write again to test existing 32-byte FinderInfo preservation
	want2 := []string{"Pristine\n4", "Ecosystem: Go\n0"}
	if err := platformWriteFinderTags(path, want2); err != nil {
		t.Fatal(err)
	}
}

func TestPlatformReadFinderTagsErrors(t *testing.T) {
	oldGet, oldUnmarshal := getFinderXattr, unmarshalPlist
	t.Cleanup(func() { getFinderXattr, unmarshalPlist = oldGet, oldUnmarshal })

	getFinderXattr = func(string, string, []byte) (int, error) { return 0, unix.ENOATTR }
	if tags, err := platformReadFinderTags("repo"); err != nil || tags != nil {
		t.Fatalf("missing attribute = %v, %v", tags, err)
	}

	wantErr := errors.New("get")
	getFinderXattr = func(string, string, []byte) (int, error) { return 0, wantErr }
	if _, err := platformReadFinderTags("repo"); !errors.Is(err, wantErr) {
		t.Fatalf("first get error = %v", err)
	}

	calls := 0
	getFinderXattr = func(_ string, _ string, data []byte) (int, error) {
		calls++
		if data == nil {
			return 1, nil
		}
		return 0, wantErr
	}
	if _, err := platformReadFinderTags("repo"); !errors.Is(err, wantErr) {
		t.Fatalf("second get error = %v", err)
	}
	if calls != 2 {
		t.Fatalf("get calls = %d", calls)
	}

	getFinderXattr = func(_ string, _ string, data []byte) (int, error) {
		if data == nil {
			return 1, nil
		}
		data[0] = 0
		return 1, nil
	}
	unmarshalPlist = func([]byte, interface{}) (int, error) { return 0, wantErr }
	if _, err := platformReadFinderTags("repo"); !errors.Is(err, wantErr) {
		t.Fatalf("unmarshal error = %v", err)
	}
}

func TestPlatformWriteFinderTagsErrors(t *testing.T) {
	oldMarshal, oldSet, oldGet := marshalPlist, setFinderXattr, getFinderXattr
	t.Cleanup(func() { marshalPlist, setFinderXattr, getFinderXattr = oldMarshal, oldSet, oldGet })

	wantErr := errors.New("write")
	marshalPlist = func(interface{}, int) ([]byte, error) { return nil, wantErr }
	if err := platformWriteFinderTags("repo", nil); !errors.Is(err, wantErr) {
		t.Fatalf("marshal error = %v", err)
	}
	marshalPlist = oldMarshal

	// First setFinderXattr call errors (finderTagsAttribute)
	setFinderXattr = func(_ string, attr string, _ []byte, _ int) error {
		if attr == finderTagsAttribute {
			return wantErr
		}
		return nil
	}
	if err := platformWriteFinderTags("repo", nil); !errors.Is(err, wantErr) {
		t.Fatalf("setxattr tags error = %v", err)
	}

	// getFinderXattr for finderInfoAttribute errors with non-ENOATTR
	setFinderXattr = func(string, string, []byte, int) error { return nil }
	getFinderXattr = func(_ string, attr string, _ []byte) (int, error) {
		if attr == finderInfoAttribute {
			return 0, wantErr
		}
		return 0, nil
	}
	if err := platformWriteFinderTags("repo", []string{"Pristine\n4"}); !errors.Is(err, wantErr) {
		t.Fatalf("getxattr finderInfo error = %v", err)
	}

	// setFinderXattr for finderInfoAttribute errors
	getFinderXattr = func(_ string, attr string, _ []byte) (int, error) {
		return 0, unix.ENOATTR
	}
	setFinderXattr = func(_ string, attr string, _ []byte, _ int) error {
		if attr == finderInfoAttribute {
			return wantErr
		}
		return nil
	}
	if err := platformWriteFinderTags("repo", []string{"Pristine\n4"}); !errors.Is(err, wantErr) {
		t.Fatalf("setxattr finderInfo error = %v", err)
	}
}
