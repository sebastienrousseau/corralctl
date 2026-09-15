// SPDX-FileCopyrightText: 2026 Sebastien Rousseau <sebastian.rousseau@gmail.com>
// SPDX-License-Identifier: GPL-3.0-only

//go:build windows

package search

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// mapFile maps a file read-only into memory.
//
// The Windows equivalent of the unix path: a file mapping object, then a view
// of it. Same reason — the index should cost address space, not resident
// memory, so the kernel can drop its pages and fetch them again on demand.
func mapFile(f *os.File) ([]byte, error) {
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	// No 32-bit guard here: every release target is 64-bit, so int always
	// holds the size, and a branch that cannot run is a branch that cannot be
	// tested. If a 32-bit target is ever added, the conversion below is where
	// to put one back.
	size := info.Size()
	if size == 0 {
		return nil, fmt.Errorf("index file is empty")
	}

	h, err := syscall.CreateFileMapping(syscall.Handle(f.Fd()), nil,
		syscall.PAGE_READONLY, uint32(size>>32), uint32(size), nil)
	if err != nil {
		return nil, fmt.Errorf("CreateFileMapping: %w", err)
	}
	// The mapping object can be closed as soon as the view exists; the view
	// keeps the mapping alive.
	defer func() { _ = syscall.CloseHandle(h) }()

	addr, err := syscall.MapViewOfFile(h, syscall.FILE_MAP_READ, 0, 0, uintptr(size))
	if err != nil {
		return nil, fmt.Errorf("MapViewOfFile: %w", err)
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(addr)), int(size)), nil
}

// unmapFile releases a mapping.
func unmapFile(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	return syscall.UnmapViewOfFile(uintptr(unsafe.Pointer(&data[0])))
}
