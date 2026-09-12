// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build unix

package search

import (
	"fmt"
	"os"
	"syscall"
)

// mapFile maps a file read-only into memory.
//
// The whole point of the on-disk index is that its pages are file-backed: the
// kernel can drop them under pressure and read them back on demand, so a
// workspace index costs address space rather than resident memory. Reading the
// file into a []byte would persist the index without that, which is the half
// that matters — the in-memory form already worked and cost 321MB of RSS.
func mapFile(f *os.File) ([]byte, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()
	if size == 0 {
		return nil, fmt.Errorf("index file is empty")
	}
	if size != int64(int(size)) {
		return nil, fmt.Errorf("index file too large to map: %d bytes", size)
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, int(size), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("mmap: %w", err)
	}
	return data, nil
}

// unmapFile releases a mapping.
func unmapFile(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	return syscall.Munmap(data)
}
