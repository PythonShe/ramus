package ui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

func pngOf(t *testing.T, w, h int, c color.Color) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// A picture's change shows it before and after.
func TestPictureDiff(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("logo.png", pngOf(t, 40, 20, color.RGBA{200, 0, 0, 255}))
	r.Commit("logo")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	r.Write("logo.png", pngOf(t, 64, 32, color.RGBA{0, 0, 200, 255}))
	h.until("the change", h.shows("logo.png"))
	h.click("logo.png")
	h.until("the pictures", h.shows("Before"))
	for _, want := range []string{"After", "40 × 20, 95 bytes", "64 × 32"} {
		if !h.tt.HasText(want) {
			t.Fatalf("no %q in %q", want, h.tt.Texts())
		}
	}
}
