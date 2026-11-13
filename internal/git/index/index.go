// Package index reads git's index file, versions 2 to 4: its entries and
// the framing of its extensions, not their contents. A zero or missing
// trailing checksum is accepted, as git does with index.skipHash.
//
// Entries are fixed-size and pointer-free, their paths in one shared
// buffer, so a 390,000-entry index is three allocations.
package index

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
)

// Limits on what a header may ask the reader to allocate or walk. The
// entry count is also checked against the file's length.
const (
	maxEntries = 1 << 27
	maxPath    = 1 << 16
	// maxPaths bounds the bytes of all paths together, and pathRatio their
	// size against the file's: version 4 shares prefixes between entries,
	// so a small file could otherwise name huge paths. Real indexes stay
	// far under both.
	maxPaths  = 1 << 30
	pathRatio = 16
	// maxExtensions bounds the extensions read; git writes a handful.
	maxExtensions = 64
	// maxFile bounds the index file read whole.
	maxFile = 2 << 30
)

// Entry flags.
const (
	FlagAssumeValid  = 0x8000
	flagExtended     = 0x4000
	stageMask        = 0x3000
	stageShift       = 12
	ExtIntentToAdd   = 0x2000 // in Entry.ExtFlags
	ExtSkipWorktree  = 0x4000
	modeSparseDirMsk = 0o040000
)

// Entry is one index entry. Path is read through Index.Path.
type Entry struct {
	CtimeSec, CtimeNsec uint32
	MtimeSec, MtimeNsec uint32
	Dev, Ino            uint32
	Mode                uint32
	UID, GID            uint32
	Size                uint32
	Flags               uint16
	ExtFlags            uint16
	pathEnd             uint32
	oid                 uint32 // offset of the object id in Index.oids
}

// Stage returns the merge stage: 0 normally, 1 to 3 for a conflict.
func (e *Entry) Stage() int { return int(e.Flags&stageMask) >> stageShift }

// SparseDir reports a sparse-directory entry of a sparse index.
func (e *Entry) SparseDir() bool { return e.Mode == modeSparseDirMsk }

// Extension is one extension's signature and where its data is.
type Extension struct {
	Sig      [4]byte
	Off, Len int
}

// Index is a parsed index file.
type Index struct {
	Version    int
	Entries    []Entry
	Extensions []Extension
	// Checksum is false when the trailing hash is zero (index.skipHash).
	Checksum bool
	hashSize int
	paths    []byte
	oids     []byte
	data     []byte
}

// ErrFormat reports an index file that cannot be read.
var ErrFormat = errors.New("index: malformed")

func bad(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrFormat, fmt.Sprintf(format, args...))
}

// ReadFile reads the index at path, whose object ids are hashSize bytes
// (20 for SHA-1, 32 for SHA-256).
func ReadFile(path string, hashSize int) (*Index, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Size() > maxFile || !st.Mode().IsRegular() {
		return nil, bad("%s is not a regular file under %d bytes", path, maxFile)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(b, hashSize)
}

// Parse reads an index from its bytes, which it keeps for Extension data.
func Parse(b []byte, hashSize int) (*Index, error) {
	if hashSize != 20 && hashSize != 32 {
		return nil, bad("hash size %d", hashSize)
	}
	if len(b) < 12+hashSize {
		return nil, bad("file too short")
	}
	if string(b[:4]) != "DIRC" {
		return nil, bad("bad signature")
	}
	x := &Index{Version: int(binary.BigEndian.Uint32(b[4:])), hashSize: hashSize, data: b}
	if x.Version < 2 || x.Version > 4 {
		return nil, bad("unsupported version %d", x.Version)
	}
	n := binary.BigEndian.Uint32(b[8:])
	body := b[:len(b)-hashSize]
	fixed := 40 + hashSize + 2
	// Every entry has its fixed part and at least one byte of path data
	// (a v4 prefix varint or a NUL), so the count is checked against the
	// file before anything is allocated for it.
	if n > maxEntries || uint64(n)*uint64(fixed+1) > uint64(len(body)-12) {
		return nil, bad("%d entries in %d bytes", n, len(b))
	}
	x.Checksum = !bytes.Equal(b[len(b)-hashSize:], make([]byte, hashSize))
	x.Entries = make([]Entry, 0, n)
	x.oids = make([]byte, 0, int(n)*hashSize)
	x.paths = make([]byte, 0, int(n)*32)
	off := 12
	var prev []byte
	pathBudget := min(maxPaths, pathRatio*len(b)+1<<20)
	for i := uint32(0); i < n; i++ {
		start := off
		if off+fixed > len(body) {
			return nil, bad("entry %d past the end", i)
		}
		be := binary.BigEndian
		e := Entry{
			CtimeSec: be.Uint32(b[off:]), CtimeNsec: be.Uint32(b[off+4:]),
			MtimeSec: be.Uint32(b[off+8:]), MtimeNsec: be.Uint32(b[off+12:]),
			Dev: be.Uint32(b[off+16:]), Ino: be.Uint32(b[off+20:]),
			Mode: be.Uint32(b[off+24:]), UID: be.Uint32(b[off+28:]), GID: be.Uint32(b[off+32:]),
			Size: be.Uint32(b[off+36:]),
		}
		e.oid = uint32(len(x.oids))
		x.oids = append(x.oids, b[off+40:off+40+hashSize]...)
		e.Flags = be.Uint16(b[off+40+hashSize:])
		off += fixed
		if e.Flags&flagExtended != 0 {
			if x.Version < 3 {
				return nil, bad("entry %d: extended flags in version 2", i)
			}
			if off+2 > len(body) {
				return nil, bad("entry %d past the end", i)
			}
			e.ExtFlags = be.Uint16(b[off:])
			off += 2
		}
		pathStart := len(x.paths)
		if x.Version == 4 {
			strip, w := varint(b[off:len(body)])
			if w == 0 || strip > uint64(len(prev)) {
				return nil, bad("entry %d: bad path prefix", i)
			}
			off += w
			end := bytes.IndexByte(b[off:len(body)], 0)
			if end < 0 {
				return nil, bad("entry %d: path not terminated", i)
			}
			x.paths = append(x.paths, prev[:len(prev)-int(strip)]...)
			x.paths = append(x.paths, b[off:off+end]...)
			off += end + 1
		} else {
			end := bytes.IndexByte(b[off:len(body)], 0)
			if end < 0 {
				return nil, bad("entry %d: path not terminated", i)
			}
			x.paths = append(x.paths, b[off:off+end]...)
			// Entries are padded with NULs to a multiple of 8 bytes.
			off = start + (off+end-start+8)&^7
			if off > len(body) {
				return nil, bad("entry %d: padding past the end", i)
			}
		}
		if len(x.paths)-pathStart > maxPath || len(x.paths) > pathBudget {
			return nil, bad("entry %d: path too long", i)
		}
		e.pathEnd = uint32(len(x.paths))
		prev = x.paths[pathStart:]
		x.Entries = append(x.Entries, e)
	}
	for off < len(body) {
		if off+8 > len(body) {
			return nil, bad("extension header past the end")
		}
		if len(x.Extensions) == maxExtensions {
			return nil, bad("more than %d extensions", maxExtensions)
		}
		var ext Extension
		copy(ext.Sig[:], b[off:off+4])
		size := binary.BigEndian.Uint32(b[off+4:])
		if uint64(size) > uint64(len(body)-off-8) {
			return nil, bad("extension %q past the end", ext.Sig[:])
		}
		ext.Off, ext.Len = off+8, int(size)
		x.Extensions = append(x.Extensions, ext)
		off += 8 + int(size)
	}
	return x, nil
}

// varint reads git's offset encoding, used for v4 path prefixes.
func varint(b []byte) (uint64, int) {
	if len(b) == 0 {
		return 0, 0
	}
	c := b[0]
	v := uint64(c & 127)
	i := 1
	for c&128 != 0 {
		if i >= len(b) || i > 9 {
			return 0, 0
		}
		c = b[i]
		i++
		v = ((v + 1) << 7) | uint64(c&127)
	}
	return v, i
}

// Path returns entry i's path.
func (x *Index) Path(i int) []byte {
	start := uint32(0)
	if i > 0 {
		start = x.Entries[i-1].pathEnd
	}
	return x.paths[start:x.Entries[i].pathEnd:x.Entries[i].pathEnd]
}

// OID returns entry i's object id.
func (x *Index) OID(i int) []byte {
	o := x.Entries[i].oid
	return x.oids[o : o+uint32(x.hashSize) : o+uint32(x.hashSize)]
}

// Extension returns the data of the first extension with signature sig.
func (x *Index) Extension(sig string) ([]byte, bool) {
	for _, e := range x.Extensions {
		if string(e.Sig[:]) == sig {
			return x.data[e.Off : e.Off+e.Len], true
		}
	}
	return nil, false
}

// Split reports a split index: its entries are only those that changed
// since the shared index named by the "link" extension.
func (x *Index) Split() bool { _, ok := x.Extension("link"); return ok }

// Sparse reports a sparse index: directories outside the sparse cone are
// single entries.
func (x *Index) Sparse() bool { _, ok := x.Extension("sdir"); return ok }
