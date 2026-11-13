// Package mmap maps files read-only into memory, so that large on-disk
// formats are read without copying them onto the Go heap.
package mmap

import (
	"errors"
	"fmt"
	"os"
	"sync"
)

// File is a read-only mapping of a whole file. Its bytes stay valid until
// Close; the owner (a repository) closes every mapping it made when it
// closes.
type File struct {
	mu   sync.Mutex
	data []byte
	// unmap releases the mapping; nil for an empty file, which maps nothing.
	unmap func([]byte) error
}

// ErrTooLarge is returned for a file that does not fit the address space
// this build can map.
var ErrTooLarge = errors.New("mmap: file too large")

// Open maps the file at path.
func Open(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("mmap: %s is not a regular file", path)
	}
	size := st.Size()
	if size == 0 {
		return &File{}, nil
	}
	if size != int64(int(size)) || size < 0 {
		return nil, ErrTooLarge
	}
	data, unmap, err := mapFile(f, int(size))
	if err != nil {
		return nil, fmt.Errorf("mmap: %s: %w", path, err)
	}
	return &File{data: data, unmap: unmap}, nil
}

// Bytes returns the mapped contents. They must not be written, and must not
// be used after Close.
func (m *File) Bytes() []byte { return m.data }

// Close releases the mapping. It is safe to call more than once.
func (m *File) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.unmap == nil {
		return nil
	}
	data, unmap := m.data, m.unmap
	m.data, m.unmap = nil, nil
	return unmap(data)
}
