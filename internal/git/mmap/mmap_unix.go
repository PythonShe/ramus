//go:build unix

package mmap

import (
	"os"
	"syscall"
)

func mapFile(f *os.File, size int) ([]byte, func([]byte) error, error) {
	data, err := syscall.Mmap(int(f.Fd()), 0, size, syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, nil, err
	}
	return data, syscall.Munmap, nil
}
