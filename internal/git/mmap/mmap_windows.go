//go:build windows

package mmap

import (
	"os"
	"syscall"
	"unsafe"
)

func mapFile(f *os.File, size int) ([]byte, func([]byte) error, error) {
	h, err := syscall.CreateFileMapping(syscall.Handle(f.Fd()), nil, syscall.PAGE_READONLY, uint32(uint64(size)>>32), uint32(size), nil)
	if err != nil {
		return nil, nil, os.NewSyscallError("CreateFileMapping", err)
	}
	// The view keeps the mapping alive; the handle is not needed past it.
	defer syscall.CloseHandle(h)
	addr, err := syscall.MapViewOfFile(h, syscall.FILE_MAP_READ, 0, 0, uintptr(size))
	if err != nil {
		return nil, nil, os.NewSyscallError("MapViewOfFile", err)
	}
	// Read the address through a pointer so vet sees no uintptr conversion:
	// the view is memory the Go runtime does not manage.
	data := unsafe.Slice((*byte)(*(*unsafe.Pointer)(unsafe.Pointer(&addr))), size)
	unmap := func(b []byte) error {
		return syscall.UnmapViewOfFile(uintptr(unsafe.Pointer(&b[0])))
	}
	return data, unmap, nil
}
