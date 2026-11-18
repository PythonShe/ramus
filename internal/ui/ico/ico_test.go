package ico

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// icon writes an icon of the pictures given, each a PNG or a bitmap.
func icon(pics ...[]byte) []byte {
	var b bytes.Buffer
	le := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	le([3]uint16{0, 1, uint16(len(pics))})
	off := 6 + 16*len(pics)
	for _, p := range pics {
		// The directory's sizes and bits, which Decode reads from the
		// picture itself.
		le([4]byte{byte(len(p)), byte(len(p)), 0, 0})
		le([2]uint16{1, 32})
		le([2]uint32{uint32(len(p)), uint32(off)})
		off += len(p)
	}
	for _, p := range pics {
		b.Write(p)
	}
	return b.Bytes()
}

// dib writes a bitmap of w by h at bits per pixel: the palette, then the
// rows of pixel indexes or BGR(A) bytes given top down, then the mask's,
// top down; each row is padded here.
func dib(w, h, bits int, palette []color.NRGBA, rows [][]byte, mask [][]byte) []byte {
	var b bytes.Buffer
	le := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	le(uint32(40))
	le(int32(w))
	le(int32(2 * h))
	le([2]uint16{1, uint16(bits)})
	le([6]uint32{0, 0, 0, 0, uint32(len(palette)), 0})
	for _, c := range palette {
		b.Write([]byte{c.B, c.G, c.R, 0})
	}
	pad := func(rows [][]byte, stride int) {
		for i := len(rows) - 1; i >= 0; i-- {
			r := make([]byte, stride)
			copy(r, rows[i])
			b.Write(r)
		}
	}
	pad(rows, (w*bits+31)/32*4)
	pad(mask, (w+31)/32*4)
	return b.Bytes()
}

func pixels(t *testing.T, img image.Image) []color.NRGBA {
	t.Helper()
	var out []color.NRGBA
	r := img.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			out = append(out, color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA))
		}
	}
	return out
}

func decode(t *testing.T, data []byte) image.Image {
	t.Helper()
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil || format != "ico" {
		t.Fatalf("decode: %v, %q", err, format)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width != img.Bounds().Dx() || cfg.Height != img.Bounds().Dy() {
		t.Fatalf("config %+v, %v, for %v", cfg, err, img.Bounds())
	}
	return img
}

var (
	red   = color.NRGBA{255, 0, 0, 255}
	blue  = color.NRGBA{0, 0, 255, 255}
	clear = color.NRGBA{}
)

func same(t *testing.T, got, want []color.NRGBA) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d pixels, want %d", len(got), len(want))
	}
	for i := range got {
		// A transparent pixel's color is no one's.
		if got[i] != want[i] && !(got[i].A == 0 && want[i].A == 0) {
			t.Fatalf("pixel %d is %v, want %v", i, got[i], want[i])
		}
	}
}

// Of the pictures of an icon, the largest shows, from the PNG it keeps.
func TestDecodesTheLargestPNG(t *testing.T) {
	small := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	large := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	for i := range large.Pix {
		large.Pix[i] = byte(i)
	}
	var a, b bytes.Buffer
	png.Encode(&a, small)
	png.Encode(&b, large)
	img := decode(t, icon(a.Bytes(), b.Bytes()))
	same(t, pixels(t, img), pixels(t, large))
}

// A 32-bit bitmap has its alpha; its mask, which writers fill in too,
// changes nothing.
func TestDecodes32BitAlpha(t *testing.T) {
	img := decode(t, icon(dib(2, 2, 32, nil,
		[][]byte{{0, 0, 255, 255, 255, 0, 0, 128}, {0, 0, 0, 0, 0, 255, 0, 255}},
		[][]byte{{0xff}, {0xff}})))
	same(t, pixels(t, img), []color.NRGBA{red, {0, 0, 255, 128}, clear, {0, 255, 0, 255}})
}

// A 32-bit bitmap without alpha takes its transparency from its mask.
func TestDecodes32BitWithoutAlpha(t *testing.T) {
	img := decode(t, icon(dib(2, 1, 32, nil, [][]byte{{0, 0, 255, 0, 255, 0, 0, 0}}, [][]byte{{0b0100_0000}})))
	same(t, pixels(t, img), []color.NRGBA{red, clear})
}

// Bitmaps of 1, 4 and 8 bits index a palette, their transparency in
// the mask.
func TestDecodesPalettes(t *testing.T) {
	for _, tc := range []struct {
		bits int
		rows [][]byte
	}{
		{1, [][]byte{{0b0110_0000}, {0b1000_0000}}},
		{4, [][]byte{{0x01, 0x10}, {0x10, 0x00}}},
		{8, [][]byte{{0, 1, 1}, {1, 0, 0}}},
	} {
		img := decode(t, icon(dib(3, 2, tc.bits, []color.NRGBA{red, blue}, tc.rows, [][]byte{{0b0010_0000}, {0}})))
		same(t, pixels(t, img), []color.NRGBA{red, blue, clear, blue, red, red})
	}
}

// A 24-bit bitmap is opaque but where its mask says.
func TestDecodes24Bit(t *testing.T) {
	img := decode(t, icon(dib(2, 1, 24, nil, [][]byte{{255, 0, 0, 0, 0, 255}}, [][]byte{{0b1000_0000}})))
	same(t, pixels(t, img), []color.NRGBA{clear, red})
}

// What is not an icon, or one cut short, is an error, not a panic.
func TestRejectsBrokenIcons(t *testing.T) {
	good := icon(dib(2, 1, 24, nil, [][]byte{{255, 0, 0, 0, 0, 255}}, [][]byte{{0}}))
	for n := range len(good) {
		if _, err := Decode(bytes.NewReader(good[:n])); err == nil {
			t.Fatalf("%d bytes decode", n)
		}
	}
	huge := icon(dib(2, 1, 24, nil, nil, nil))
	binary.LittleEndian.PutUint32(huge[6+16+4:], 1<<20)
	if _, err := Decode(bytes.NewReader(huge)); err == nil {
		t.Fatal("a bitmap of a million pixels a side decodes")
	}
}

func FuzzDecode(f *testing.F) {
	f.Add(icon(dib(2, 1, 24, nil, [][]byte{{255, 0, 0, 0, 0, 255}}, [][]byte{{0}})))
	f.Add(icon(dib(3, 2, 4, []color.NRGBA{red, blue}, [][]byte{{0x01, 0x10}, {0x10, 0x00}}, nil)))
	f.Fuzz(func(t *testing.T, b []byte) {
		Decode(bytes.NewReader(b))
		DecodeConfig(bytes.NewReader(b))
	})
}
