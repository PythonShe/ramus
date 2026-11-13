package inflate

import (
	"bytes"
	"compress/zlib"
	"errors"
	"io"
	"testing"
)

func deflate(b []byte) []byte {
	var out bytes.Buffer
	w := zlib.NewWriter(&out)
	w.Write(b)
	w.Close()
	return out.Bytes()
}

type stdlib struct{}

func (stdlib) NewReader(r io.Reader) (io.ReadCloser, error) { return zlib.NewReader(r) }

func TestAll(t *testing.T) {
	want := bytes.Repeat([]byte("tree 4b825dc642cb6eb9a060e54bf8d69288fbee4904\n"), 100)
	z := deflate(want)
	for _, in := range []Inflater{Default, stdlib{}} {
		got, err := All(in, []byte("pre"), z, len(want))
		if err != nil || !bytes.Equal(got, append([]byte("pre"), want...)) {
			t.Fatalf("%T: %v", in, err)
		}
		if _, err := All(in, nil, z, len(want)-1); !errors.Is(err, ErrTooLarge) {
			t.Fatalf("%T: short limit gave %v", in, err)
		}
		if _, err := All(in, nil, []byte("not zlib"), 100); err == nil {
			t.Fatalf("%T: inflated garbage", in)
		}
	}
}

// A bomb: a tiny stream that inflates to far more than its ratio allows
// stops at the ratio, whatever limit the caller passed.
func TestRatio(t *testing.T) {
	z := deflate(make([]byte, 10<<20))
	_, err := All(Default, nil, z, 1<<30)
	if int64(len(z))*MaxRatio < 10<<20 && !errors.Is(err, ErrTooLarge) {
		t.Fatalf("bomb of %d bytes inflated: %v", len(z), err)
	}
}

func BenchmarkInflate(b *testing.B) {
	data := bytes.Repeat([]byte("static int buffer_length(struct device *dev)\n"), 2000)
	z := deflate(data)
	for _, in := range []struct {
		name string
		in   Inflater
	}{{"klauspost", Default}, {"stdlib", stdlib{}}} {
		b.Run(in.name, func(b *testing.B) {
			var dst []byte
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				dst, _ = All(in.in, dst[:0], z, len(data))
			}
		})
	}
}
