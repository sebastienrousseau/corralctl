// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build darwin

package engine

import (
	"errors"

	"golang.org/x/sys/unix"
	"howett.net/plist"
)

const (
	finderTagsAttribute = "com.apple.metadata:_kMDItemUserTags"
	finderInfoAttribute = "com.apple.FinderInfo"
)

var (
	getFinderXattr = unix.Getxattr
	setFinderXattr = unix.Setxattr
	marshalPlist   = plist.Marshal
	unmarshalPlist = plist.Unmarshal
)

func platformReadFinderTags(path string) ([]string, error) {
	size, err := getFinderXattr(path, finderTagsAttribute, nil)
	if errors.Is(err, unix.ENOATTR) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	data := make([]byte, size)
	if _, err := getFinderXattr(path, finderTagsAttribute, data); err != nil {
		return nil, err
	}
	var tags []string
	if _, err := unmarshalPlist(data, &tags); err != nil {
		return nil, err
	}
	return tags, nil
}

func platformWriteFinderTags(path string, tags []string) error {
	data, err := marshalPlist(tags, plist.BinaryFormat)
	if err != nil {
		return err
	}
	if err := setFinderXattr(path, finderTagsAttribute, data, 0); err != nil {
		return err
	}

	// Synchronize com.apple.FinderInfo byte 9 to reflect folder color label
	color := extractFinderColor(tags)
	var finfo [32]byte
	if n, err := getFinderXattr(path, finderInfoAttribute, finfo[:]); err == nil && n == 32 {
		// Existing FinderInfo present, preserve all non-color flags
	} else if err != nil && !errors.Is(err, unix.ENOATTR) {
		return err
	}
	// Bits 1-3 of byte 9 store the label color index (0-7)
	finfo[9] = (finfo[9] & 0xF1) | byte((color&0x07)<<1)
	return setFinderXattr(path, finderInfoAttribute, finfo[:], 0)
}
