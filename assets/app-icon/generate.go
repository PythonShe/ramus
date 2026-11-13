//go:build ignore

// Generates the app icon assets: for each design an Icon Composer document
// (light and dark appearances), flat SVG masters, and the rasters that
// Icon Composer's ictool renders from the document with its Liquid Glass
// treatment (hi-res PNG, a Linux PNG set, Windows .ico).
//
//	go run assets/app-icon/generate.go
//
// Needs a Mac with Icon Composer (it ships inside Xcode 26 and later).
package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Glyphs are drawn on the macOS icon grid: a 1024 canvas whose plate is the
// centered 824 square. An Icon Composer layer is full-bleed, so there the
// same glyph is scaled up by 1024/824 about the center.
const (
	canvas = 1024
	plate  = 824
)

// Icon Composer fills every path and ignores strokes, so a layer is made of
// filled shapes only; strokes are outlined here.
type layer struct {
	name        string
	body        string // SVG elements, painted with currentColor
	light, dark string // hex colors per appearance
}

type design struct {
	dir       string
	transform string  // placement of the glyph on the grid
	layers    []layer // back to front
}

type gradient struct{ top, bottom string }

var (
	paper    = gradient{"#ffffff", "#e6e6eb"}
	graphite = gradient{"#3a3b40", "#18191b"}
)

const (
	ink    = "#1d1d1f"
	chalk  = "#f5f5f7"
	blue   = "#0a84ff"
	sky    = "#3b9dff"
	orange = "#ff9f0a"
)

var designs = []design{
	{
		// An R whose leg leaves the bowl as a branch.
		dir:       "monogram",
		transform: "translate(0 -14)",
		layers: []layer{
			{
				name: "leg",
				body: curve(pt{470, 512}, pt{570, 512}, pt{640, 560}, pt{640, 660}, 60) + `
<rect x="610" y="659" width="60" height="81"/>
<circle cx="640" cy="740" r="54"/>`,
				light: blue, dark: sky,
			},
			{
				name: "stem",
				body: `<rect x="332" y="262" width="60" height="508" rx="30"/>
<path d="M362 262H522A140 140 0 0 1 522 542H362V482H522A80 80 0 0 0 522 322H362Z"/>`,
				light: ink, dark: chalk,
			},
		},
	},
	{
		// The branch glyph: a trunk between two commits and one branch tip.
		dir:       "fork",
		transform: "translate(512 512) scale(1.14) translate(-512 -512)",
		layers: []layer{
			{
				name: "trunk",
				body: `<rect x="381" y="346" width="42" height="332"/>
<rect x="601" y="430" width="42" height="41"/>
` + curve(pt{622, 470}, pt{622, 560}, pt{402, 520}, pt{402, 620}, 42) + "\n" +
					ring(402, 300, 46, 42) + "\n" + ring(402, 724, 46, 42),
				light: ink, dark: chalk,
			},
			{
				name:  "tip",
				body:  `<circle cx="622" cy="384" r="64"/>`,
				light: orange, dark: orange,
			},
		},
	},
}

var appearances = []struct {
	name, rendition string
	fill            gradient
}{
	{"light", "Default", paper},
	{"dark", "Dark", graphite},
}

var (
	linuxSizes = []int{16, 24, 32, 48, 64, 128, 256, 512, 1024}
	icoSizes   = []int{16, 24, 32, 48, 64, 128, 256}
)

func main() {
	_, self, _, _ := runtime.Caller(0)
	root := filepath.Dir(self)
	ictool := findIctool()
	for _, d := range designs {
		base := filepath.Join(root, d.dir)
		for _, sub := range []string{"AppIcon.icon", "svg", "png", "linux", "windows"} {
			check(os.RemoveAll(filepath.Join(base, sub)))
		}
		doc := filepath.Join(base, "AppIcon.icon")
		for _, l := range d.layers {
			write(filepath.Join(doc, "Assets", l.name+".svg"), layerSVG(d, l))
		}
		write(filepath.Join(doc, "icon.json"), iconJSON(d))

		for _, a := range appearances {
			write(filepath.Join(base, "svg", "icon-"+a.name+".svg"), flatSVG(d, a.name, a.fill))

			render := func(out string, px, scale int) {
				check(os.MkdirAll(filepath.Dir(out), 0o755))
				run(ictool, doc, "--export-image", "--output-file", out,
					"--platform", "macOS", "--rendition", a.rendition,
					"--width", strconv.Itoa(px), "--height", strconv.Itoa(px),
					"--scale", strconv.Itoa(scale))
				toSRGB8(out)
			}
			render(filepath.Join(base, "png", "icon-"+a.name+"-1024.png"), 1024, 1)
			render(filepath.Join(base, "png", "icon-"+a.name+"-2048.png"), 1024, 2)
			for _, px := range linuxSizes {
				render(filepath.Join(base, "linux", a.name, fmt.Sprintf("%dx%d.png", px, px)), px, 1)
			}
			var images [][]byte
			for _, px := range icoSizes {
				b, err := os.ReadFile(filepath.Join(base, "linux", a.name, fmt.Sprintf("%dx%d.png", px, px)))
				check(err)
				images = append(images, b)
			}
			write(filepath.Join(base, "windows", "ramus-"+a.name+".ico"), ico(icoSizes, images))
		}
	}
}

type pt struct{ x, y float64 }

// curve outlines a cubic Bezier stroked at the given width with butt ends,
// as a polygon fine enough to stay smooth at the largest render.
func curve(p0, p1, p2, p3 pt, width float64) string {
	const steps = 64
	var left, right []pt
	for i := 0; i <= steps; i++ {
		t := float64(i) / steps
		u := 1 - t
		x := u*u*u*p0.x + 3*u*u*t*p1.x + 3*u*t*t*p2.x + t*t*t*p3.x
		y := u*u*u*p0.y + 3*u*u*t*p1.y + 3*u*t*t*p2.y + t*t*t*p3.y
		dx := 3*u*u*(p1.x-p0.x) + 6*u*t*(p2.x-p1.x) + 3*t*t*(p3.x-p2.x)
		dy := 3*u*u*(p1.y-p0.y) + 6*u*t*(p2.y-p1.y) + 3*t*t*(p3.y-p2.y)
		n := math.Hypot(dx, dy)
		ox, oy := -dy/n*width/2, dx/n*width/2
		left = append(left, pt{x + ox, y + oy})
		right = append(right, pt{x - ox, y - oy})
	}
	var b strings.Builder
	b.WriteString(`<path d="`)
	for i, p := range left {
		cmd := "L"
		if i == 0 {
			cmd = "M"
		}
		fmt.Fprintf(&b, "%s%.2f %.2f", cmd, p.x, p.y)
	}
	for i := len(right) - 1; i >= 0; i-- {
		fmt.Fprintf(&b, "L%.2f %.2f", right[i].x, right[i].y)
	}
	b.WriteString(`Z"/>`)
	return b.String()
}

// ring is a circle stroked at the given width: two contours wound in
// opposite directions, so the hole holds under either fill rule.
func ring(cx, cy, r, width float64) string {
	o, i := r+width/2, r-width/2
	return fmt.Sprintf(`<path d="M%g %ga%g %g 0 1 1 %g 0a%g %g 0 1 1 %g 0ZM%g %ga%g %g 0 1 0 %g 0a%g %g 0 1 0 %g 0Z"/>`,
		cx-o, cy, o, o, 2*o, o, o, -2*o, cx-i, cy, i, i, 2*i, i, i, -2*i)
}

func layerSVG(d design, l layer) string {
	k := float64(canvas) / plate
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" color="%s">
<g fill="currentColor" transform="translate(512 512) scale(%.5f) translate(-512 -512) %s">
%s
</g>
</svg>
`, canvas, canvas, canvas, canvas, l.light, k, d.transform, l.body)
}

func flatSVG(d design, appearance string, fill gradient) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d">
<defs>
<linearGradient id="plate" x1="0" y1="0" x2="0" y2="1">
<stop offset="0" stop-color="%s"/>
<stop offset="1" stop-color="%s"/>
</linearGradient>
</defs>
<path d="%s" fill="url(#plate)"/>
<g transform="%s">
`, canvas, canvas, canvas, canvas, fill.top, fill.bottom, squircle(), d.transform)
	for _, l := range d.layers {
		color := l.light
		if appearance == "dark" {
			color = l.dark
		}
		fmt.Fprintf(&b, "<g fill=\"%s\">\n%s\n</g>\n", color, l.body)
	}
	b.WriteString("</g>\n</svg>\n")
	return b.String()
}

// squircle is the plate outline: a superellipse, close to the continuous
// corners the system draws.
func squircle() string {
	const n, steps, r = 5.0, 128, plate / 2.0
	var b strings.Builder
	for i := 0; i < steps; i++ {
		t := float64(i) / steps * 2 * math.Pi
		c, s := math.Cos(t), math.Sin(t)
		x := canvas/2 + r*math.Copysign(math.Pow(math.Abs(c), 2/n), c)
		y := canvas/2 + r*math.Copysign(math.Pow(math.Abs(s), 2/n), s)
		if i == 0 {
			fmt.Fprintf(&b, "M%.1f %.1f", x, y)
		} else {
			fmt.Fprintf(&b, "L%.1f %.1f", x, y)
		}
	}
	b.WriteString("Z")
	return b.String()
}

type m = map[string]any

func iconJSON(d design) string {
	fills := func(light, dark m) []m {
		return []m{{"value": light}, {"appearance": "dark", "value": dark}}
	}
	// Icon Composer lists layers front to back.
	var layers []m
	for i := len(d.layers) - 1; i >= 0; i-- {
		l := d.layers[i]
		layers = append(layers, m{
			"name":                 l.name,
			"image-name":           l.name + ".svg",
			"glass":                true,
			"fill-specializations": fills(m{"solid": srgb(l.light)}, m{"solid": srgb(l.dark)}),
		})
	}
	doc := m{
		"fill-specializations": fills(
			m{"linear-gradient": []string{srgb(paper.top), srgb(paper.bottom)}},
			m{"linear-gradient": []string{srgb(graphite.top), srgb(graphite.bottom)}},
		),
		// Liquid Glass kept low: a specular edge and a soft shadow, with
		// no translucency, so the glyph stays solid at small sizes.
		"groups": []m{{
			"name":         "Glyph",
			"layers":       layers,
			"shadow":       m{"kind": "neutral", "opacity": 0.3},
			"translucency": m{"enabled": false, "value": 0.5},
			"specular":     true,
		}},
		"supported-platforms": m{"squares": "shared"},
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	check(err)
	return string(out) + "\n"
}

func srgb(hex string) string {
	v, err := strconv.ParseUint(strings.TrimPrefix(hex, "#"), 16, 32)
	check(err)
	ch := func(shift uint) float64 { return float64(v>>shift&0xff) / 255 }
	return fmt.Sprintf("srgb:%.5f,%.5f,%.5f,1.00000", ch(16), ch(8), ch(0))
}

// ico packs PNG images into a Windows icon; every Windows version that can
// run the app reads PNG entries.
func ico(sizes []int, images [][]byte) string {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, []uint16{0, 1, uint16(len(images))})
	offset := 6 + 16*len(images)
	for i, img := range images {
		px := byte(sizes[i]) // 256 wraps to 0, which is how the format spells it
		b.Write([]byte{px, px, 0, 0})
		binary.Write(&b, binary.LittleEndian, []uint16{1, 32})
		binary.Write(&b, binary.LittleEndian, []uint32{uint32(len(img)), uint32(offset)})
		offset += len(img)
	}
	for _, img := range images {
		b.Write(img)
	}
	return b.String()
}

// toSRGB8 rewrites a render, which ictool writes as 16-bit Display P3, as
// 8-bit sRGB: the form Windows icons need, and a tenth of the size.
func toSRGB8(path string) {
	run("sips", "-m", "/System/Library/ColorSync/Profiles/sRGB Profile.icc", path)
	f, err := os.Open(path)
	check(err)
	img, err := png.Decode(f)
	f.Close()
	check(err)
	bounds := img.Bounds()
	dst := image.NewNRGBA(bounds)
	draw.Draw(dst, bounds, img, bounds.Min, draw.Src)
	var b bytes.Buffer
	check(png.Encode(&b, dst))
	check(os.WriteFile(path, b.Bytes(), 0o644))
}

func findIctool() string {
	const rel = "Icon Composer.app/Contents/Executables/ictool"
	candidates := []string{filepath.Join("/Applications", rel)}
	if dev, err := exec.Command("xcode-select", "-p").Output(); err == nil {
		xcode := filepath.Dir(strings.TrimSpace(string(dev)))
		candidates = append(candidates, filepath.Join(xcode, "Applications", rel))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	fmt.Fprintln(os.Stderr, "generate: Icon Composer's ictool not found; install Xcode 26 or later")
	os.Exit(1)
	return ""
}

func write(path, content string) {
	check(os.MkdirAll(filepath.Dir(path), 0o755))
	check(os.WriteFile(path, []byte(content), 0o644))
}

func run(name string, args ...string) {
	if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "generate: %s: %v\n%s\n", filepath.Base(name), err, out)
		os.Exit(1)
	}
}

func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate:", err)
		os.Exit(1)
	}
}
