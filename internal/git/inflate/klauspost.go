package inflate

import (
	"io"

	"github.com/klauspost/compress/zlib"
)

// klauspost is github.com/klauspost/compress's zlib, faster than the
// standard library's on git's object sizes.
type klauspost struct{}

func (klauspost) NewReader(r io.Reader) (io.ReadCloser, error) { return zlib.NewReader(r) }
