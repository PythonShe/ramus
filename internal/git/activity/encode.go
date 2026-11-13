package activity

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// magic starts an encoded record set; the digit is the format version.
const magic = "RAMUS-ACTIVITY-1\n"

// maxDecoded bounds what a decode allocates, against a damaged file.
const maxDecoded = 1 << 30

// Encode writes the records and key, which says what they were read
// from, so a later Decode can tell whether they are still current.
func (r *Records) Encode(w io.Writer, key string) error {
	bw := bufio.NewWriter(w)
	le := binary.LittleEndian
	bw.WriteString(magic)
	for _, v := range []any{uint32(len(key)), []byte(key), uint32(len(r.At)), r.At, r.Zone, r.Commit, r.Who,
		uint32(len(r.buf)), r.buf, uint32(len(r.idEnds)), r.idEnds} {
		if err := binary.Write(bw, le, v); err != nil {
			return err
		}
	}
	return bw.Flush()
}

// ErrFormat is a file that is not encoded records.
var ErrFormat = errors.New("activity: not a records file")

// Decode reads records Encode wrote and the key they were written with.
func Decode(rd io.Reader) (*Records, string, error) {
	br := bufio.NewReader(rd)
	head := make([]byte, len(magic))
	if _, err := io.ReadFull(br, head); err != nil || string(head) != magic {
		return nil, "", ErrFormat
	}
	le := binary.LittleEndian
	length := func(each int) (int, error) {
		var n uint32
		if err := binary.Read(br, le, &n); err != nil {
			return 0, err
		}
		if int64(n)*int64(each) > maxDecoded {
			return 0, fmt.Errorf("%w: a length of %d", ErrFormat, n)
		}
		return int(n), nil
	}
	n, err := length(1)
	if err != nil {
		return nil, "", err
	}
	key := make([]byte, n)
	if _, err := io.ReadFull(br, key); err != nil {
		return nil, "", err
	}
	if n, err = length(22); err != nil {
		return nil, "", err
	}
	r := &Records{At: make([]int64, n), Zone: make([]int16, n), Commit: make([]int64, n), Who: make([]uint32, n)}
	for _, v := range []any{r.At, r.Zone, r.Commit, r.Who} {
		if err := binary.Read(br, le, v); err != nil {
			return nil, "", err
		}
	}
	if n, err = length(1); err != nil {
		return nil, "", err
	}
	r.buf = make([]byte, n)
	if _, err := io.ReadFull(br, r.buf); err != nil {
		return nil, "", err
	}
	if n, err = length(4); err != nil {
		return nil, "", err
	}
	r.idEnds = make([]uint32, n)
	if err := binary.Read(br, le, r.idEnds); err != nil {
		return nil, "", err
	}
	// Check what indexes trust before anything uses it.
	if len(r.idEnds)%2 != 0 {
		return nil, "", ErrFormat
	}
	prev := uint32(0)
	for _, e := range r.idEnds {
		if e < prev || int(e) > len(r.buf) {
			return nil, "", ErrFormat
		}
		prev = e
	}
	ids := uint32(len(r.idEnds) / 2)
	for _, w := range r.Who {
		if w >= ids {
			return nil, "", ErrFormat
		}
	}
	r.ids = make(map[string]uint32, ids)
	for i := range ids {
		name, email := r.Identity(i)
		r.ids[name+"\x00"+email] = i
	}
	return r, string(key), nil
}
