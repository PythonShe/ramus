package lfs

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// Pointer is what git keeps of a file stored in LFS: the id and size of
// its content, which git-lfs keeps apart.
type Pointer struct {
	// OID is the content's SHA-256, lower-case hex.
	OID  string
	Size int64
}

// maxPointer is the most a pointer file holds; git-lfs reads no more.
const maxPointer = 1024

// ParsePointer reads b as an LFS pointer file, as the specification
// writes one: a version line, then sorted keys, oid and size among them.
func ParsePointer(b []byte) (Pointer, bool) {
	if len(b) >= maxPointer {
		return Pointer{}, false
	}
	rest, ok := bytes.CutPrefix(b, []byte("version https://git-lfs.github.com/spec/v1\n"))
	if !ok {
		return Pointer{}, false
	}
	var p Pointer
	size := false
	for line := range bytes.SplitSeq(bytes.TrimSuffix(rest, []byte("\n")), []byte("\n")) {
		k, v, ok := bytes.Cut(line, []byte(" "))
		if !ok {
			return Pointer{}, false
		}
		switch string(k) {
		case "oid":
			hex, ok := bytes.CutPrefix(v, []byte("sha256:"))
			if !ok || !isHex64(hex) {
				return Pointer{}, false
			}
			p.OID = string(hex)
		case "size":
			n, err := strconv.ParseInt(string(v), 10, 64)
			if err != nil || n < 0 {
				return Pointer{}, false
			}
			p.Size, size = n, true
		}
	}
	return p, p.OID != "" && size
}

func isHex64(b []byte) bool {
	if len(b) != 64 {
		return false
	}
	for _, c := range b {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// ErrNotFetched is content git-lfs has not downloaded into the
// repository.
var ErrNotFetched = errors.New("lfs: the file's content is not downloaded")

// ErrTooLarge is content past the most asked for.
var ErrTooLarge = errors.New("lfs: the file's content is too large")

// ReadObject reads the content p points to from the local LFS store in
// commonDir, up to max bytes. A file whose size is not the pointer's is
// taken as not there, as one git-lfs is still writing.
func ReadObject(commonDir string, p Pointer, max int64) ([]byte, error) {
	if p.Size > max {
		return nil, ErrTooLarge
	}
	f, err := os.Open(filepath.Join(commonDir, "lfs", "objects", p.OID[:2], p.OID[2:4], p.OID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFetched
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, p.Size+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) != p.Size {
		return nil, ErrNotFetched
	}
	return b, nil
}
