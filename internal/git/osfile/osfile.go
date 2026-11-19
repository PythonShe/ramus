// Package osfile opens the files git replaces by renaming a lock file over
// them (the index, refs, packed-refs, reftables, the commit-graph), so that
// git's rename succeeds while one is being read here. Unix allows that
// always; Windows only when the reader opened the file sharing deletion,
// which os.Open does not.
package osfile

import (
	"io"
	"os"
)

// Open opens path for reading, as os.Open does.
func Open(path string) (*os.File, error) { return open(path) }

// ReadFile reads the whole file at path, as os.ReadFile does.
func ReadFile(path string) ([]byte, error) {
	f, err := open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	size := 512
	if st, err := f.Stat(); err == nil && st.Size() > 0 && int64(int(st.Size())) == st.Size() {
		size = int(st.Size()) + 1
	}
	b := make([]byte, 0, size)
	for {
		n, err := f.Read(b[len(b):cap(b)])
		b = b[:len(b)+n]
		if err == io.EOF {
			return b, nil
		}
		if err != nil {
			return b, err
		}
		if len(b) == cap(b) {
			b = append(b, 0)[:len(b)]
		}
	}
}
