// Package ico decodes Windows icons (.ico): the largest picture of the
// file, kept as PNG or as a device-independent bitmap with its mask. It
// registers the format with package image, so that image.Decode, and
// MyGo's ui.DecodeImage through it, read icons.
package ico

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
)

func init() {
	image.RegisterFormat("ico", "\x00\x00\x01\x00", Decode, DecodeConfig)
}

// maxFile bounds an icon read: the format's own pictures are at most 256
// pixels on a side, and a larger file is not one.
const maxFile = 16 << 20

// maxSide bounds a bitmap's side, which its header, unlike the
// directory, can say is larger than 256.
const maxSide = 1024

var errFormat = errors.New("ico: not a valid icon")

// entry is one picture of the directory.
type entry struct {
	w, h, bits int
	data       []byte
}

// best reads the directory and returns its largest picture, of the most
// colors among those of one size.
func best(r io.Reader) (entry, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxFile+1))
	if err != nil {
		return entry{}, err
	}
	if len(b) > maxFile || len(b) < 6 || binary.LittleEndian.Uint16(b[0:]) != 0 || binary.LittleEndian.Uint16(b[2:]) != 1 {
		return entry{}, errFormat
	}
	n := int(binary.LittleEndian.Uint16(b[4:]))
	var out entry
	for i := range n {
		d := b[min(6+16*i, len(b)):]
		if len(d) < 16 {
			return entry{}, errFormat
		}
		// 0 is 256 in the directory's byte of each side.
		w, h := int(d[0]), int(d[1])
		if w == 0 {
			w = 256
		}
		if h == 0 {
			h = 256
		}
		bits := int(binary.LittleEndian.Uint16(d[6:]))
		size, off := binary.LittleEndian.Uint32(d[8:]), binary.LittleEndian.Uint32(d[12:])
		if uint64(off)+uint64(size) > uint64(len(b)) {
			continue
		}
		if e := (entry{w, h, bits, b[off : off+size]}); out.data == nil || w*h > out.w*out.h || w*h == out.w*out.h && bits > out.bits {
			out = e
		}
	}
	if out.data == nil {
		return entry{}, errFormat
	}
	return out, nil
}

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// DecodeConfig returns the size of the icon's largest picture.
func DecodeConfig(r io.Reader) (image.Config, error) {
	e, err := best(r)
	if err != nil {
		return image.Config{}, err
	}
	if bytes.HasPrefix(e.data, pngMagic) {
		return png.DecodeConfig(bytes.NewReader(e.data))
	}
	h, err := readHeader(e.data)
	if err != nil {
		return image.Config{}, err
	}
	return image.Config{ColorModel: color.NRGBAModel, Width: h.w, Height: h.h}, nil
}

// Decode decodes the icon's largest picture.
func Decode(r io.Reader) (image.Image, error) {
	e, err := best(r)
	if err != nil {
		return nil, err
	}
	if bytes.HasPrefix(e.data, pngMagic) {
		return png.Decode(bytes.NewReader(e.data))
	}
	return decodeDIB(e.data)
}

// header is what decodeDIB uses of a BITMAPINFOHEADER.
type header struct {
	size, w, h, bits, colors int
}

func readHeader(d []byte) (header, error) {
	if len(d) < 40 {
		return header{}, errFormat
	}
	size := int(binary.LittleEndian.Uint32(d[0:]))
	w := int(int32(binary.LittleEndian.Uint32(d[4:])))
	// The height is of the colors and the mask one above the other.
	h := int(int32(binary.LittleEndian.Uint32(d[8:]))) / 2
	bits := int(binary.LittleEndian.Uint16(d[14:]))
	compression := binary.LittleEndian.Uint32(d[16:])
	colors := int(binary.LittleEndian.Uint32(d[32:]))
	// BI_RGB, or BI_BITFIELDS that some writers give 32-bit icons in the
	// usual order.
	if size < 40 || size > len(d) || w <= 0 || h <= 0 || w > maxSide || h > maxSide ||
		compression != 0 && !(compression == 3 && bits == 32) {
		return header{}, errFormat
	}
	switch bits {
	case 1, 4, 8:
		if colors == 0 || colors > 1<<bits {
			colors = 1 << bits
		}
	case 24, 32:
		colors = 0
	default:
		return header{}, errFormat
	}
	return header{size: size, w: w, h: h, bits: bits, colors: colors}, nil
}

// decodeDIB decodes a bitmap of an icon: a header, a palette for 8 bits
// or fewer, the colors' rows and the mask's, each bottom up and padded to
// four bytes. The mask says the transparent pixels where there is no
// alpha.
func decodeDIB(d []byte) (image.Image, error) {
	h, err := readHeader(d)
	if err != nil {
		return nil, err
	}
	if h.bits == 32 && h.size == 40 && binary.LittleEndian.Uint32(d[16:]) == 3 {
		h.size += 12 // the three masks after the header
	}
	pal := d[h.size:]
	if len(pal) < 4*h.colors {
		return nil, errFormat
	}
	pix := pal[4*h.colors:]
	stride := (h.w*h.bits + 31) / 32 * 4
	maskStride := (h.w + 31) / 32 * 4
	if len(pix) < stride*h.h {
		return nil, errFormat
	}
	mask := pix[stride*h.h:]
	hasMask := len(mask) >= maskStride*h.h
	img := image.NewNRGBA(image.Rect(0, 0, h.w, h.h))
	alpha := false
	for y := range h.h {
		row := pix[(h.h-1-y)*stride:]
		out := img.Pix[y*img.Stride:]
		for x := range h.w {
			var c [4]byte
			switch h.bits {
			case 32:
				c = [4]byte{row[4*x+2], row[4*x+1], row[4*x], row[4*x+3]}
				alpha = alpha || c[3] != 0
			case 24:
				c = [4]byte{row[3*x+2], row[3*x+1], row[3*x], 255}
			default:
				i := int(row[x*h.bits/8]>>(8-h.bits-x*h.bits%8)) & (1<<h.bits - 1)
				if i < h.colors {
					c = [4]byte{pal[4*i+2], pal[4*i+1], pal[4*i], 255}
				}
			}
			copy(out[4*x:], c[:])
		}
	}
	// A 32-bit picture with no alpha at all is of the time before alpha,
	// whose transparency is the mask's, as the others'.
	if h.bits == 32 && !alpha {
		for i := 3; i < len(img.Pix); i += 4 {
			img.Pix[i] = 255
		}
	}
	if hasMask && (h.bits != 32 || !alpha) {
		for y := range h.h {
			row := mask[(h.h-1-y)*maskStride:]
			for x := range h.w {
				if row[x/8]>>(7-x%8)&1 == 1 {
					img.Pix[y*img.Stride+4*x+3] = 0
				}
			}
		}
	}
	return img, nil
}
