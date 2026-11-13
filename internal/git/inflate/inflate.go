// Package inflate decompresses zlib streams, the encoding of git's loose
// objects and pack entries, behind an interface so that another
// implementation can replace the first.
//
// Every stream is bounded: a caller says how many bytes it expects, and a
// stream that inflates past that, or past a sane ratio of its input, is
// an error rather than an allocation.
package inflate

import (
	"bytes"
	"errors"
	"fmt"
	"io"
)

// Inflater opens zlib streams.
type Inflater interface {
	// NewReader returns a reader of r's inflated bytes.
	NewReader(r io.Reader) (io.ReadCloser, error)
}

// Default is the implementation the engine uses.
var Default Inflater = klauspost{}

// MaxRatio bounds inflated size over compressed size. zlib's own ceiling
// is about 1032:1; anything above it is not a real stream.
const MaxRatio = 1100

// ErrTooLarge is returned for a stream that inflates past its limit.
var ErrTooLarge = errors.New("inflate: output exceeds its limit")

// All inflates data, which must hold exactly one zlib stream, expecting at
// most limit bytes, and appends them to dst.
func All(in Inflater, dst, data []byte, limit int) ([]byte, error) {
	if limit < 0 {
		return dst, fmt.Errorf("inflate: negative limit")
	}
	if most := int64(len(data))*MaxRatio + 64; int64(limit) > most {
		limit = int(most)
	}
	r, err := in.NewReader(bytes.NewReader(data))
	if err != nil {
		return dst, err
	}
	defer r.Close()
	start := len(dst)
	buf := bytes.NewBuffer(dst)
	n, err := io.Copy(buf, io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return buf.Bytes(), err
	}
	if n > int64(limit) {
		return buf.Bytes()[:start], ErrTooLarge
	}
	return buf.Bytes(), nil
}
