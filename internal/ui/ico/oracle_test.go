package ico

import (
	"bytes"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Ramus's own icons decode as macOS's sips reads them, to the largest
// picture, pixel for pixel but for rounding.
func TestDecodesAsSips(t *testing.T) {
	if _, err := exec.LookPath("sips"); err != nil {
		t.Skip("no sips:", err)
	}
	icons, _ := filepath.Glob("../../../assets/app-icon/*/windows/*.ico")
	if len(icons) == 0 {
		t.Fatal("no icons")
	}
	for _, path := range icons {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		img, err := Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		out := filepath.Join(t.TempDir(), "out.png")
		if b, err := exec.Command("sips", "-s", "format", "png", path, "--out", out).CombinedOutput(); err != nil {
			t.Fatalf("sips: %v: %s", err, b)
		}
		f, err := os.Open(out)
		if err != nil {
			t.Fatal(err)
		}
		want, err := png.Decode(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if img.Bounds().Size() != want.Bounds().Size() {
			t.Fatalf("%s: %v, sips %v", path, img.Bounds(), want.Bounds())
		}
		got, exp := pixels(t, img), pixels(t, want)
		for i := range got {
			g, e := got[i], exp[i]
			if g.A == 0 && e.A == 0 {
				continue
			}
			if far(g.R, e.R) || far(g.G, e.G) || far(g.B, e.B) || far(g.A, e.A) {
				t.Fatalf("%s: pixel %d of %v is %v, sips %v", path, i, img.Bounds(), g, e)
			}
		}
	}
}

// far says two channels differ by more than the rounding of a color
// profile's conversion.
func far(a, b uint8) bool { return max(a, b)-min(a, b) > 3 }
