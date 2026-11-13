package commitgraph

import (
	"encoding/binary"
	"math/bits"
	"strings"
)

// BloomSettings describes a graph's changed-path Bloom filters. Version is
// git's hash version: 1 hashes path bytes as signed chars (git's original
// behaviour, wrong for bytes above 0x7f), 2 as unsigned.
type BloomSettings struct {
	Version         uint32
	NumHashes       uint32
	BitsPerEntry    uint32
	MaxChangedPaths uint32
}

// BloomKey holds the hashes of one path, computed once and tested against
// many commits.
type BloomKey struct {
	hashes []uint32
}

// BloomKeys returns the keys a path-limited walk tests: the path and each
// of its leading directories, as git does, longest first.
func (s BloomSettings) BloomKeys(path string) []BloomKey {
	path = strings.Trim(path, "/")
	var keys []BloomKey
	for p := path; p != ""; {
		keys = append(keys, s.Key(p))
		i := strings.LastIndexByte(p, '/')
		if i < 0 {
			break
		}
		p = p[:i]
	}
	return keys
}

// Key returns the key of one path, without a trailing slash.
func (s BloomSettings) Key(path string) BloomKey {
	const seed0, seed1 = 0x293ae76f, 0x7e646e2c
	signed := s.Version == 1
	h0 := murmur3(seed0, path, signed)
	h1 := murmur3(seed1, path, signed)
	k := BloomKey{hashes: make([]uint32, s.NumHashes)}
	for i := range k.hashes {
		k.hashes[i] = h0 + uint32(i)*h1
	}
	return k
}

// BloomResult is the answer of a changed-path filter.
type BloomResult int8

const (
	// BloomUnknown: the commit has no filter, or one too large to say.
	BloomUnknown BloomResult = iota
	// BloomNo: the commit did not change the path.
	BloomNo
	// BloomMaybe: the commit may have changed the path.
	BloomMaybe
)

// MaybeChanged tests whether the commit at pos may have changed the paths
// whose keys are given (a path and its leading directories, from
// BloomKeys): BloomNo if any of them is surely absent.
func (g *Graph) MaybeChanged(pos uint32, keys []BloomKey) BloomResult {
	g.check(pos)
	if g.bloom.Version == 0 {
		return BloomUnknown
	}
	l, i := g.layerOf(pos)
	f := l.filter(i)
	if len(f) == 0 {
		return BloomUnknown
	}
	mod := uint64(len(f)) * 8
	for _, k := range keys {
		for _, h := range k.hashes {
			bit := uint64(h) % mod
			if f[bit/8]&(1<<(bit%8)) == 0 {
				return BloomNo
			}
		}
	}
	return BloomMaybe
}

// Filter returns the raw filter of the commit at pos, for tests.
func (g *Graph) filter(pos uint32) []byte {
	l, i := g.layerOf(pos)
	return l.filter(i)
}

func (l *layer) filter(i uint32) []byte {
	if l.bidx == nil {
		return nil
	}
	start := uint32(0)
	if i > 0 {
		start = binary.BigEndian.Uint32(l.bidx[(i-1)*4:])
	}
	end := binary.BigEndian.Uint32(l.bidx[i*4:])
	return l.bdat[start:end:end]
}

// murmur3 is git's murmur3_seeded_v1 (signed) and _v2 (unsigned).
func murmur3(seed uint32, data string, signed bool) uint32 {
	const (
		c1 = 0xcc9e2d51
		c2 = 0x1b873593
		m  = 5
		n  = 0xe6546b64
	)
	b := func(i int) uint32 {
		if signed {
			return uint32(int32(int8(data[i])))
		}
		return uint32(data[i])
	}
	h := seed
	len4 := len(data) / 4
	for i := 0; i < len4; i++ {
		k := b(4*i) | b(4*i+1)<<8 | b(4*i+2)<<16 | b(4*i+3)<<24
		k *= c1
		k = bits.RotateLeft32(k, 15)
		k *= c2
		h ^= k
		h = bits.RotateLeft32(h, 13)*m + n
	}
	t := len4 * 4
	var k1 uint32
	switch len(data) & 3 {
	case 3:
		k1 ^= b(t+2) << 16
		fallthrough
	case 2:
		k1 ^= b(t+1) << 8
		fallthrough
	case 1:
		k1 ^= b(t)
		k1 *= c1
		k1 = bits.RotateLeft32(k1, 15)
		k1 *= c2
		h ^= k1
	}
	h ^= uint32(len(data))
	h ^= h >> 16
	h *= 0x85ebca6b
	h ^= h >> 13
	h *= 0xc2b2ae35
	h ^= h >> 16
	return h
}
