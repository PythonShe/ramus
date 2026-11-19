package ui

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"
	"golang.org/x/image/tiff"

	"ramus/internal/git/diff"
	"ramus/internal/git/gittest"
	"ramus/internal/session"
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
	h.until("open", h.shows("No local changes"))
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

// openPictureChange opens a repository whose file name changes from old
// to new in the work tree, and shows the change.
func (h *harness) openPictureChange(name, old, new string) {
	h.t.Helper()
	r := h.repoWith()
	r.Write(name, old)
	r.Commit("picture")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No local changes"))
	r.Write(name, new)
	h.until("the change", h.shows(name))
	h.click(name)
}

// at is the color the last frame shows at x, y of the box of the
// element labelled name with role.
func (h *harness) at(role ui.Role, name string, x, y float32) color.RGBA {
	h.t.Helper()
	a, ok := h.tt.AccessByRole(role, name)
	if !ok {
		h.t.Fatalf("no %q", name)
	}
	img := h.tt.Image()
	k := float32(img.Bounds().Dx()) / 1200 // newHarness.s width
	return color.RGBAModel.Convert(img.At(int((a.Bounds.X+x*a.Bounds.W)*k), int((a.Bounds.Y+y*a.Bounds.H)*k))).(color.RGBA)
}

// A picture's sides compare side by side, swiped, one faded over the
// other or by their difference, as last chosen.
func TestPictureCompares(t *testing.T) {
	h := newHarness(t)
	red, blue := color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255}
	h.openPictureChange("logo.png", pngOf(t, 300, 300, red), pngOf(t, 300, 300, blue))
	h.until("the pictures", h.shows("Side by Side"))
	h.click("Swipe")
	h.until("the swipe", h.labelled("Compare Pictures"))
	h.tt.Frame()
	if l, r := h.at(ui.RoleImage, "Compare Pictures", 0.25, 0.5), h.at(ui.RoleImage, "Compare Pictures", 0.75, 0.5); l != red || r != blue {
		t.Fatalf("swiped halfway, %v and %v", l, r)
	}
	// A press on the picture moves the line to it: 100 of the picture's
	// 300 DIPs from the left, as it shows 300 pixels a side at a scale of 1.
	a, _ := h.tt.AccessByRole(ui.RoleImage, "Compare Pictures")
	x, y := a.Bounds.X+a.Bounds.W/2-50, a.Bounds.Y+a.Bounds.H/2
	h.tt.Press(x, y)
	h.tt.Frame()
	h.tt.Release(x, y)
	h.tt.Frame()
	if s := h.app.diffOpts().swipe; s < 0.3 || s > 0.36 {
		t.Fatalf("pressed a third of the way, swiped to %v", s)
	}
	if c := h.at(ui.RoleImage, "Compare Pictures", 0.5, 0.5); c != blue {
		t.Fatalf("the middle, now after the line, is %v", c)
	}
	h.click("Onion Skin")
	h.tt.Frame()
	if c := h.at(ui.RoleImage, "Compare Pictures", 0.5, 0.5); c.R < 100 || c.B < 100 || c.G > 20 {
		t.Fatalf("faded halfway, %v", c)
	}
	h.click("Difference")
	h.until("the difference", h.shows("90,000 pixels differ"))
	if c := h.at(ui.RoleImage, "Compare Pictures", 0.5, 0.5); c != (color.RGBA{255, 0, 255, 255}) {
		t.Fatalf("difference %v", c)
	}
	if h.app.Settings.PictureCompare != pictureDifference {
		t.Fatalf("the choice is not kept: %d", h.app.Settings.PictureCompare)
	}
}

// Pictures the same but in their files, as their metadata, have no
// pixels that differ.
func TestPictureDifferenceOfTheSame(t *testing.T) {
	h := newHarness(t)
	h.app.Settings.PictureCompare = pictureDifference
	a := pngOf(t, 4, 4, color.RGBA{0, 200, 0, 255})
	// The same pixels compressed less: another file.
	img, _ := png.Decode(strings.NewReader(a))
	var b bytes.Buffer
	(&png.Encoder{CompressionLevel: png.NoCompression}).Encode(&b, img)
	h.openPictureChange("same.png", a, b.String())
	h.until("the difference", h.shows("No pixels differ"))
}

// Sides that say they are too large to compare are found so before any
// is drawn or decoded at that size: an SVG names the size it is drawn
// at, which nothing else bounds.
func TestDifferenceTooLargeAllocatesNothing(t *testing.T) {
	svg := func(fill string) []byte {
		return []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="6000" height="6000"><rect width="10" height="10" fill="` + fill + `"/></svg>`)
	}
	big := pngOf(t, 4097, 4097, color.RGBA{0, 200, 0, 255})
	for _, sides := range [][2][]byte{{svg("red"), svg("blue")}, {[]byte(big), []byte(pngOf(t, 4, 4, color.RGBA{200, 0, 0, 255}))}} {
		a, _, _ := decodePicture(sides[0])
		b, _, _ := decodePicture(sides[1])
		if a == nil || b == nil {
			t.Fatal("a side did not decode")
		}
		h := newHarness(t)
		pv := &patchView{}
		pv.pics.images = [2]ui.ImageSource{a, b}
		pv.pics.sides = sides
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		tt := ui.NewTester(func(c *ui.Context) { h.app.makeDifference(c, pv) }, 200, 200)
		tt.Frame()
		(<-h.updates)()
		runtime.ReadMemStats(&after)
		if !pv.pics.tooLarge || pv.pics.diff != nil {
			t.Fatalf("tooLarge %v", pv.pics.tooLarge)
		}
		if made := after.TotalAlloc - before.TotalAlloc; made > 16<<20 {
			t.Fatalf("%d MB made to find the sides too large", made>>20)
		}
	}
}

// Pictures decode one file at a time, and a file no longer shown by the
// time its turn comes is not decoded: arrowing through large pictures
// would otherwise decode every one passed, at once.
func TestPictureDecodeStopsWhenStale(t *testing.T) {
	big := []byte(pngOf(t, 4000, 4000, color.RGBA{0, 200, 0, 255}))
	h := newHarness(t)
	pv := &patchView{}
	h.app.setPatch(pv, session.PatchResult{})
	pv.pics.sides = [2][]byte{big, big}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	goroutines := runtime.NumGoroutine()
	takePicture(t) // another picture decoding
	h.app.decodePictures(pv)
	h.app.setPatch(pv, session.PatchResult{}) // another file chosen
	endPicture()
	for deadline := time.Now().Add(10 * time.Second); runtime.NumGoroutine() > goroutines; {
		if time.Now().After(deadline) {
			t.Fatal("the decode did not end")
		}
		time.Sleep(time.Millisecond)
	}
	runtime.ReadMemStats(&after)
	if n := len(h.updates); n != 0 {
		t.Fatalf("%d updates from a file no longer shown", n)
	}
	if made := after.TotalAlloc - before.TotalAlloc; made > 16<<20 {
		t.Fatalf("%d MB decoded of a file no longer shown", made>>20)
	}
}

// takePicture takes the picture turn, as a long decode does, until
// endPicture.
func takePicture(t *testing.T) {
	t.Helper()
	if took, _ := waitPicture(nil, nil); !took {
		t.Fatal("the picture turn is taken")
	}
}

// picturesWaiting is how many decodes wait for the picture turn, and how
// many were sent away so far.
func picturesWaiting() (waiting, sentAway int) {
	p := &pictureTurn
	p.Lock()
	defer p.Unlock()
	return len(p.queue), p.sentAway
}

// untilWaiting waits until n decodes wait for the picture turn: each
// joins the queue, and leaves it, itself.
func untilWaiting(t *testing.T, what string, n int) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
		if waiting, _ := picturesWaiting(); waiting == n {
			return
		}
		if time.Now().After(deadline) {
			waiting, _ := picturesWaiting()
			t.Fatalf("%s: %d decodes wait, not %d", what, waiting, n)
		}
	}
}

// A decode waiting for its turn leaves the queue, and its bytes, as soon
// as its file is no longer shown or its repository closes: not once the
// decode ahead of it ends.
func TestPictureWaitEnds(t *testing.T) {
	h := newHarness(t)
	takePicture(t) // another picture decoding, for as long as it takes
	defer endPicture()

	pv := &patchView{}
	h.app.setPatch(pv, session.PatchResult{})
	pv.pics.sides = [2][]byte{[]byte("old"), []byte("new")}
	h.app.decodePictures(pv)
	untilWaiting(t, "a file shown", 1)
	h.app.setPatch(pv, session.PatchResult{}) // another file chosen
	untilWaiting(t, "another file chosen", 0)

	life := make(chan struct{})
	pv = &patchView{life: life}
	h.app.setPatch(pv, session.PatchResult{})
	pv.pics.sides = [2][]byte{[]byte("old"), []byte("new")}
	h.app.decodePictures(pv)
	untilWaiting(t, "a file shown", 1)
	close(life) // the repository closed
	untilWaiting(t, "the repository closed", 0)
	if n := len(h.updates); n != 0 {
		t.Fatalf("%d updates from decodes no longer wanted", n)
	}
}

// However many files ask for their pictures while one decodes, only a
// few wait, holding their bytes: one more sends the oldest away, whose
// view asks again, if it still shows it, once a decode has ended. Asked
// again at once, it would send another away, which would ask again, a
// frame's work over and over for as long as the long decode runs. Once
// it ends, the picture asked for last, the one shown, decodes first,
// and every one still shown gets its turn.
func TestPictureWaitersAreBounded(t *testing.T) {
	h := newHarness(t)
	takePicture(t)
	held := true
	t.Cleanup(func() {
		if held {
			endPicture()
		}
	})
	_, before := picturesWaiting()
	pvs := make([]*patchView, maxPictureWaiters+3)
	for i := range pvs {
		pv := &patchView{}
		pvs[i] = pv
		h.app.setPatch(pv, session.PatchResult{})
		pv.pics.sides = [2][]byte{[]byte("old"), []byte("new")}
		h.app.decodePictures(pv)
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(time.Millisecond) {
			waiting, away := picturesWaiting()
			if waiting == min(i+1, maxPictureWaiters) && away-before == max(0, i+1-maxPictureWaiters) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("file %d shown: %d wait, %d sent away", i, waiting, away-before)
			}
		}
	}
	// Frames go by while the long decode runs, each building every view,
	// which asks for its pictures unless it waits for them.
	frame := func() {
		for _, pv := range pvs {
			h.app.decodePictures(pv)
		}
		for len(h.updates) > 0 {
			(<-h.updates)()
		}
	}
	for range 200 {
		frame()
		runtime.Gosched()
	}
	if waiting, away := picturesWaiting(); waiting != maxPictureWaiters || away-before != len(pvs)-maxPictureWaiters {
		t.Fatalf("over 200 frames of one decode, %d sent away; %d wait", away-before, waiting)
	}
	for i, pv := range pvs {
		if !pv.pics.asked {
			t.Fatalf("file %d asks again while the decode runs", i)
		}
	}

	endPicture()
	held = false
	var order []int
	for len(order) < len(pvs) {
		select {
		case fn := <-h.updates:
			fn()
		case <-time.After(10 * time.Second):
			t.Fatalf("decoded %v of %d", order, len(pvs))
		}
		for i, pv := range pvs {
			if pv.pics.pictured && !slices.Contains(order, i) {
				order = append(order, i)
			}
			h.app.decodePictures(pv)
		}
	}
	if order[0] != len(pvs)-1 {
		t.Fatalf("decoded in the order %v: the file shown last not first", order)
	}
	if _, away := picturesWaiting(); away-before > len(pvs)*len(pvs) {
		t.Fatalf("%d sent away for %d files", away-before, len(pvs))
	}
}

// A patch replaced lets go of the lines picked in the viewer before,
// which can be a whole patch's, even when no viewer follows it.
func TestSetPatchForgetsPickedLines(t *testing.T) {
	h := newHarness(t)
	pv := &patchView{p: &diff.Parsed{}, d: &editor.DiffState{}}
	pv.picked = pickedLines{d: pv.d, chosen: func(int) bool { return true }, n: 3}
	h.app.setPatch(pv, session.PatchResult{}) // as a binary, empty or submodule patch
	if pv.picked.d != nil || pv.picked.chosen != nil {
		t.Fatal("the lines picked in the viewer replaced are kept")
	}
}

func TestDifference(t *testing.T) {
	a := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	copy(a.Pix, []byte{10, 20, 30, 255, 0, 0, 0, 0})
	b := image.NewNRGBA(image.Rect(5, 5, 6, 6)) // from its own origin
	copy(b.Pix, []byte{10, 25, 20, 255})
	d, n := difference(a, b, 2, 2)
	want := []byte{0, 5, 10, 255, 0, 0, 0, 255, 0, 0, 0, 255, 0, 0, 0, 255}
	if !bytes.Equal(d.Pix, want) || n != 1 {
		t.Fatalf("%v, %d", d.Pix, n)
	}
	// Half transparent, against nothing: as bright as the alpha.
	c := image.NewNRGBA(image.Rect(0, 0, 1, 1))
	copy(c.Pix, []byte{0, 0, 0, 128})
	if d, n := difference(c, image.NewNRGBA(c.Rect), 1, 1); d.Pix[0] != 128 || n != 1 {
		t.Fatalf("%v, %d", d.Pix, n)
	}
}

const (
	svgBefore = `<svg xmlns="http://www.w3.org/2000/svg" width="20" height="10"><rect width="20" height="10" fill="red"/></svg>` + "\n"
	svgAfter  = `<svg xmlns="http://www.w3.org/2000/svg" width="20" height="10"><rect width="20" height="10" fill="blue"/></svg>` + "\n"
)

// An SVG, which git diffs as text, shows as its text or, when asked, as
// a picture, as every SVG then does.
func TestSVGAsPicture(t *testing.T) {
	h := newHarness(t)
	h.openPictureChange("icon.svg", svgBefore, svgAfter)
	h.until("the text", h.shows("Picture"))
	if h.tt.HasText("Before") {
		t.Fatalf("shown as a picture: %q", h.tt.Texts())
	}
	h.click("Picture")
	h.until("the pictures", h.shows("Before"))
	if !h.tt.HasText("20 × 10, 111 bytes") || !h.app.Settings.SVGAsPicture {
		t.Fatalf("texts %q, setting %v", h.tt.Texts(), h.app.Settings.SVGAsPicture)
	}
	h.click("Text")
	h.until("the text again", func() bool { return !h.tt.HasText("Before") })
}

// An SVG with what is not drawn says what.
func TestSVGNotDrawn(t *testing.T) {
	h := newHarness(t)
	h.app.Settings.SVGAsPicture = true
	filtered := strings.Replace(svgAfter, `<rect`, `<filter id="f"><feGaussianBlur stdDeviation="2"/></filter><rect filter="url(#f)"`, 1)
	h.openPictureChange("icon.svg", svgBefore, filtered)
	h.until("the note", func() bool {
		for _, s := range h.tt.Texts() {
			if strings.HasPrefix(s, "Not drawn: ") {
				return true
			}
		}
		return false
	})
}

// TIFF and Windows icons show as pictures too.
func TestPictureFormats(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 12, 6))
	var tf, pn bytes.Buffer
	if err := tiff.Encode(&tf, img, nil); err != nil {
		t.Fatal(err)
	}
	png.Encode(&pn, image.NewNRGBA(image.Rect(0, 0, 16, 16)))
	// An icon of one picture, kept as PNG.
	var ico bytes.Buffer
	binary.Write(&ico, binary.LittleEndian, [3]uint16{0, 1, 1})
	binary.Write(&ico, binary.LittleEndian, [4]byte{16, 16, 0, 0})
	binary.Write(&ico, binary.LittleEndian, [2]uint16{1, 32})
	binary.Write(&ico, binary.LittleEndian, [2]uint32{uint32(pn.Len()), 22})
	ico.Write(pn.Bytes())
	for _, tc := range []struct{ name, data, want string }{
		{"scan.tiff", tf.String(), "12 × 6"},
		{"favicon.ico", ico.String(), "16 × 16"},
	} {
		h := newHarness(t)
		h.openPictureChange(tc.name, "", tc.data)
		h.until(tc.name, func() bool {
			for _, s := range h.tt.Texts() {
				if strings.HasPrefix(s, tc.want+",") {
					return true
				}
			}
			return false
		})
	}
}

// A picture stored in Git LFS, which git diffs as its pointers, shows
// before and after from the local LFS store; a side never downloaded
// says so.
func TestPictureDiffFromLFS(t *testing.T) {
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Skip("no git-lfs:", err)
	}
	h := newHarness(t)
	r := h.repoWith()
	// What git lfs install writes, in the repository's own config.
	for _, kv := range [][2]string{{"clean", "git-lfs clean -- %f"}, {"smudge", "git-lfs smudge -- %f"},
		{"process", "git-lfs filter-process"}, {"required", "true"}} {
		r.Git("config", "filter.lfs."+kv[0], kv[1])
	}
	r.Write(".gitattributes", "*.png filter=lfs diff=lfs merge=lfs -text\n")
	r.Write("logo.png", pngOf(t, 40, 20, color.RGBA{200, 0, 0, 255}))
	r.Write("gone.png", pngOf(t, 8, 8, color.RGBA{0, 200, 0, 255}))
	r.Git("add", ".gitattributes", "logo.png", "gone.png")
	r.Commit("logo")
	if p := r.Git("cat-file", "-p", "HEAD:logo.png"); !strings.HasPrefix(p, "version https://git-lfs") {
		t.Fatalf("not a pointer: %q", p)
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No local changes"))
	r.Write("logo.png", pngOf(t, 64, 32, color.RGBA{0, 0, 200, 255}))
	r.Write("gone.png", pngOf(t, 16, 16, color.RGBA{0, 0, 200, 255}))
	// gone.png's old content was never downloaded: its object goes once
	// no file in the work tree holds it, as git's clean filter, run on
	// such a file, would store it again.
	oid := strings.TrimPrefix(strings.Fields(strings.Split(r.Git("cat-file", "-p", "HEAD:gone.png"), "\n")[1])[1], "sha256:")
	if err := os.Remove(filepath.Join(r.GitDir(), "lfs", "objects", oid[:2], oid[2:4], oid)); err != nil {
		t.Fatal(err)
	}
	// Both writes, which a status run between them can see one of.
	h.until("the changes", func() bool { return h.tt.HasText("logo.png") && h.tt.HasText("gone.png") })
	h.click("logo.png")
	h.until("the pictures", h.shows("40 × 20, 95 bytes"))
	if !h.tt.HasText("64 × 32") || h.tt.HasText("version https://git-lfs") {
		t.Fatalf("texts %q", h.tt.Texts())
	}
	h.click("gone.png")
	h.until("the side not downloaded", h.shows("Not downloaded from Git LFS"))
	if !h.tt.HasText("16 × 16") {
		t.Fatalf("texts %q", h.tt.Texts())
	}
}

// A submodule's change lists the commits it moves over.
func TestSubmoduleDiff(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	sub := gittest.Init(t)
	sub.Write("s", "0")
	sub.Commit("first")
	r.Git("-c", "protocol.file.allow=always", "submodule", "add", "-q", sub.Dir, "lib")
	r.Commit("with lib")
	inner := filepath.Join(r.Dir, "lib")
	for _, msg := range []string{"second", "third"} {
		os.WriteFile(filepath.Join(inner, "s"), []byte(msg), 0o644)
		gittest.Open(t, inner).Git("commit", "-q", "-a", "-m", msg)
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the change", h.shows("modified lib"))
	// The row, not the submodule in the sidebar, which opens it.
	h.click("modified lib")
	h.until("the range", h.shows("2 commits added"))
	for _, want := range []string{"second", "third"} {
		if !h.tt.HasText(want) {
			t.Fatalf("no %q in %q", want, h.tt.Texts())
		}
	}
}
