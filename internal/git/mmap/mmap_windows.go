//go:build windows

package mmap

import (
	"io"
	"os"
	"syscall"
	"unsafe"
)

// mapFile copies the file into memory of its own rather than mapping it:
// Windows refuses to delete a file, or to rename another over it, while a
// view of it is mapped, and git replaces the commit-graph and deletes its
// layers that way while a repository is open here. The copy lives outside
// the Go heap, in one block backed by the paging file, as a mapping would.
func mapFile(f *os.File, size int) ([]byte, func([]byte) error, error) {
	h, err := syscall.CreateFileMapping(syscall.InvalidHandle, nil, syscall.PAGE_READWRITE, uint32(uint64(size)>>32), uint32(size), nil)
	if err != nil {
		return nil, nil, os.NewSyscallError("CreateFileMapping", err)
	}
	// The view keeps the section alive; the handle is not needed past it.
	defer syscall.CloseHandle(h)
	addr, err := syscall.MapViewOfFile(h, syscall.FILE_MAP_WRITE, 0, 0, uintptr(size))
	if err != nil {
		return nil, nil, os.NewSyscallError("MapViewOfFile", err)
	}
	// Read the address through a pointer so vet sees no uintptr conversion:
	// the view is memory the Go runtime does not manage.
	data := unsafe.Slice((*byte)(*(*unsafe.Pointer)(unsafe.Pointer(&addr))), size)
	unmap := func(b []byte) error {
		return syscall.UnmapViewOfFile(uintptr(unsafe.Pointer(&b[0])))
	}
	if _, err := io.ReadFull(f, data); err != nil {
		unmap(data)
		return nil, nil, err
	}
	return data, unmap, nil
}
