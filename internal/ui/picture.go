package ui

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	_ "golang.org/x/image/tiff" // registered with image, as MyGo decodes

	_ "ramus/internal/ui/ico" // registered with image, as MyGo decodes
	"ramus/locales"
)

// The ways a picture's two sides compare, as diffOptions.picture.
const (
	pictureSideBySide = iota
	pictureSwipe
	pictureOnion
	pictureDifference
)

// pictureState is a patch's picture: its sides decoded, and what is
// known of them.
type pictureState struct {
	// images are the sides as pictures, nil where a side is none or not
	// a picture, and what each says of itself; notDrawn is what an SVG
	// side has that MyGo does not draw.
	images   [2]ui.ImageSource
	about    [2]string
	notDrawn string
	// lfs is a picture stored in Git LFS, shown as pictures once
	// pictured, whatever git made of its pointers; notFetched are the
	// sides whose content was never downloaded.
	lfs, pictured bool
	notFetched    [2]bool
	// binary is a picture shown as pictures, never as text: one git
	// diffs as binary, or one in LFS.
	binary bool
	// text is a picture git diffs as text, as SVG is, shown as one when
	// the user asks; asked says its sides are decoding.
	text, asked bool
	// sides are the files read, for the decoding and the difference.
	sides [2][]byte
	// diff is the sides' difference, made when first shown: a picture,
	// how many pixels differ, or tooLarge for sides too large to compare.
	diff                ui.ImageSource
	differ              int
	diffAsked, tooLarge bool
}

// isPicture reports whether a path names a picture the app can show.
func isPicture(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg", ".tif", ".tiff", ".ico":
		return true
	}
	return false
}

// maxPixels bounds a picture decoded, against one that says it is huge.
const maxPixels = 64 << 20

// maxDiffPixels bounds the difference made of two sides: it holds two
// copies of their pixels while it is made.
const maxDiffPixels = 16 << 20

// pictureTurn lets one picture's sides decode, or one difference be
// made, at a time: each can take hundreds of megabytes, and arrowing
// through a folder of pictures starts one per file shown.
var pictureTurn struct {
	sync.Mutex
	busy bool
	// queue are the decodes waiting for the turn, oldest first, each
	// holding its sides' bytes: at most maxPictureWaiters wait, and one
	// more sends the oldest away. The turn goes to the newest, the
	// picture last shown.
	queue []*pictureWaiter
	// freed is closed, and replaced, as a turn ends, for the decodes
	// sent away while it ran; nil while none waits on it.
	freed chan struct{}
	// sentAway counts the decodes sent away.
	sentAway int
}

// pictureWaiter is a decode waiting for its turn: turn is closed as it
// gets it, away as it is sent away, with the freed of then in freed.
type pictureWaiter struct {
	turn, away chan struct{}
	freed      chan struct{}
}

const maxPictureWaiters = 4

// waitPicture waits for the picture turn and reports whether it took
// it, then to be given back with endPicture: not once the patch went
// stale or life ended, as the repository closing, nor once a newer
// decode sent it away, which away reports once a turn has ended since.
// Asked again at once, a decode sent away would send away another,
// which would ask again: the views would go round at the frame rate for
// as long as one long decode runs.
func waitPicture(stale *staleness, life <-chan struct{}) (took, away bool) {
	wanted := func() bool {
		select {
		case <-life:
			return false
		default:
			return !stale.Load()
		}
	}
	if !wanted() {
		return false, false
	}
	p := &pictureTurn
	p.Lock()
	if !p.busy {
		p.busy = true
		p.Unlock()
		return true, false
	}
	me := &pictureWaiter{turn: make(chan struct{}), away: make(chan struct{})}
	if len(p.queue) == maxPictureWaiters {
		// The queue is full only while a turn runs, which ends.
		old := p.queue[0]
		p.queue = slices.Delete(p.queue, 0, 1)
		if p.freed == nil {
			p.freed = make(chan struct{})
		}
		old.freed = p.freed
		p.sentAway++
		close(old.away)
	}
	p.queue = append(p.queue, me)
	p.Unlock()
	select {
	case <-me.turn:
		// The turn may come as the decode stops being wanted, before it
		// wakes: it goes on to the next.
		if !wanted() {
			endPicture()
			return false, false
		}
		return true, false
	case <-me.away:
		select {
		case <-me.freed:
			return false, true
		case <-stale.Done():
		case <-life:
		}
		return false, false
	case <-stale.Done():
	case <-life:
	}
	p.Lock()
	if i := slices.Index(p.queue, me); i >= 0 {
		p.queue = slices.Delete(p.queue, i, i+1)
		p.Unlock()
		return false, false
	}
	p.Unlock()
	// Out of the queue meanwhile: given the turn, which goes on, or sent
	// away.
	select {
	case <-me.turn:
		endPicture()
	default:
	}
	return false, false
}

// endPicture ends the turn waitPicture gave, handing it to the newest
// decode waiting.
func endPicture() {
	p := &pictureTurn
	p.Lock()
	defer p.Unlock()
	if p.freed != nil {
		close(p.freed)
		p.freed = nil
	}
	if n := len(p.queue); n > 0 {
		next := p.queue[n-1]
		p.queue = p.queue[:n-1]
		close(next.turn)
		return
	}
	p.busy = false
}

// decodePicture decodes one side of a picture's change, and says its
// size and, for an SVG, what of it is not drawn; nil for a side that is
// none or does not decode.
func decodePicture(data []byte) (ui.ImageSource, string, []string) {
	if len(data) == 0 {
		return nil, "", nil
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil && cfg.Width*cfg.Height > maxPixels {
		return nil, byteSize(int64(len(data))), nil
	}
	img, err := ui.DecodeImage(data)
	if err != nil {
		return nil, byteSize(int64(len(data))), nil
	}
	w, h := ui.ImageSize(img)
	var notDrawn []string
	switch s := img.(type) {
	case *ui.Bitmap:
		if pictureScale(w, h) > 1 {
			// Shown larger, a small picture's pixels stay sharp squares,
			// as an icon's are meant to be seen.
			s.Pixelated()
		}
	case *ui.SVG:
		notDrawn = s.Unsupported()
	}
	return img, fmt.Sprintf("%d × %d, %s", int(w), int(h), byteSize(int64(len(data)))), notDrawn
}

// decodePictures decodes pv's sides off the main thread, once.
func (a *App) decodePictures(pv *patchView) {
	ps := &pv.pics
	if ps.asked {
		return
	}
	ps.asked = true
	gen, update, sides, notFetched, stale, life := pv.gen, a.Update, ps.sides, ps.notFetched, pv.stale, pv.life
	a.goWork(nil, func() {
		if took, away := waitPicture(stale, life); !took {
			if away {
				update(func() {
					if pv.gen == gen && pv.stale == stale {
						pv.pics.asked = false
					}
				})
			}
			return
		}
		defer endPicture()
		var imgs [2]ui.ImageSource
		var about [2]string
		var notDrawn []string
		for k, data := range sides {
			if stale.Load() {
				// Another file shown since, whose pictures come next.
				return
			}
			if !notFetched[k] {
				var nd []string
				imgs[k], about[k], nd = decodePicture(data)
				notDrawn = append(notDrawn, nd...)
			}
		}
		slices.Sort(notDrawn)
		update(func() {
			if pv.gen == gen {
				ps := &pv.pics
				ps.images, ps.about, ps.notDrawn, ps.pictured = imgs, about, strings.Join(slices.Compact(notDrawn), ", "), true
			}
		})
	})
}

// pictureScale is how many times its size a picture of w by h shows: a
// small one, as an icon, larger, by whole times, up to pictureShown.
func pictureScale(w, h float32) float32 {
	const pictureShown, most = 256, 8
	side := max(w, h)
	if side <= 0 || side*2 > pictureShown {
		return 1
	}
	return min(float32(int(pictureShown/side)), most)
}

// fitIn is the box of w by h scaled down to fit r, centered in it.
func fitIn(r ui.Rect, w, h float32) ui.Rect {
	if w <= 0 || h <= 0 {
		return ui.Rect{}
	}
	s := min(r.W/w, r.H/h, 1)
	w, h = w*s, h*s
	return ui.Rect{X: r.X + (r.W-w)/2, Y: r.Y + (r.H-h)/2, W: w, H: h}
}

// pictures shows a picture's change: its sides compared as the user
// chose, with lead, when set, first in the bar of the choices.
func (a *App) pictures(c *ui.Context, pv *patchView, lead func()) {
	o, ps := a.diffOpts(), &pv.pics
	both := ps.images[0] != nil && ps.images[1] != nil
	ui.Column(c).Fill().Children(func() {
		if lead != nil || both {
			ui.Row(c).Wrap().Gap(spaceM).AlignItems(ui.Center).Padding(spaceM, insetX, 0, insetX).Children(func() {
				if lead != nil {
					lead()
				}
				if !both {
					return
				}
				modes := ui.Segmented(c, &o.picture, locales.PictureSideBySide(), locales.PictureSwipe(),
					locales.PictureOnionSkin(), locales.PictureDifference()).Label(locales.PictureCompare())
				if modes.Changed() {
					a.Settings.PictureCompare = o.picture
					a.keepSettingsOr(locales.DiffSettingsNotSaved)
				}
			})
		}
		if ps.notDrawn != "" {
			ui.Text(c, locales.PictureNotDrawn(ps.notDrawn)).FontSize(textSmall).TextColor(c.Theme().Warning).Padding(spaceM, insetX, 0, insetX)
		}
		switch {
		case !both || o.picture == pictureSideBySide:
			sideBySide(c, ps)
		default:
			a.overlaid(c, pv)
		}
	})
}

// sideBySide shows a picture before and after its change, side by side.
func sideBySide(c *ui.Context, ps *pictureState) {
	th := c.Theme()
	ui.Row(c).Fill().Gap(spaceXL).Padding(spaceXL).Children(func() {
		for k, title := range []string{locales.PatchBefore(), locales.PatchAfter()} {
			if ps.images[k] == nil && ps.about[k] == "" && !ps.notFetched[k] {
				continue
			}
			ui.Column(c.Key(k)).Grow(1).Basis(0).FillHeight().Gap(spaceM).Children(func() {
				ui.Row(c).Gap(spaceM).Children(func() {
					ui.Text(c, title).FontSize(textSmall).Bold()
					ui.Text(c, ps.about[k]).FontSize(textSmall).TextColor(th.TextMuted)
				})
				box := ui.Column(c).Grow(1).FillWidth().Center().Radius(th.Radius)
				if img := ps.images[k]; img != nil {
					w, h := ui.ImageSize(img)
					s := pictureScale(w, h)
					box.Role(ui.RoleImage).Label(title).Draw(func(p *ui.Painter, r ui.Rect) {
						fr := fitIn(r, w*s, h*s)
						checkers(p, fr)
						p.Image(img, fr, ui.FillBox)
					})
					return
				}
				box.Background(th.Surface).Padding(spaceM).Children(func() {
					if ps.notFetched[k] {
						ui.Text(c, locales.PatchNotFetched()).TextColor(th.TextMuted)
					} else {
						ui.Text(c, locales.PatchNotAPicture()).TextColor(th.TextMuted)
					}
				})
			})
		}
	})
}

// overlaid shows a picture's sides one over the other, in one frame as
// large as both, from its top-left corner, as their pixels line up: the
// after side swiped in over the before one, faded in over it, or the
// difference of the two.
func (a *App) overlaid(c *ui.Context, pv *patchView) {
	th, o, ps := c.Theme(), a.diffOpts(), &pv.pics
	before, after := ps.images[0], ps.images[1]
	w0, h0 := ui.ImageSize(before)
	w1, h1 := ui.ImageSize(after)
	w, h := max(w0, w1), max(h0, h1)
	s := pictureScale(w, h)
	mode := o.picture
	if mode == pictureDifference {
		a.makeDifference(c, pv)
	}
	// frame is where the sides show in r, the box of the frame's element.
	frame := func(r ui.Rect) ui.Rect { return fitIn(r, w*s, h*s) }
	side := func(fr ui.Rect, iw, ih float32) ui.Rect {
		return ui.Rect{X: fr.X, Y: fr.Y, W: fr.W * iw / w, H: fr.H * ih / h}
	}
	ui.Column(c).Fill().Gap(spaceM).Padding(spaceXL).Children(func() {
		ui.Row(c).Gap(spaceM).Children(func() {
			ui.Text(c, locales.PatchBefore()).FontSize(textSmall).Bold()
			ui.Text(c, ps.about[0]).FontSize(textSmall).TextColor(th.TextMuted)
			ui.Spacer(c)
			ui.Text(c, locales.PatchAfter()).FontSize(textSmall).Bold()
			ui.Text(c, ps.about[1]).FontSize(textSmall).TextColor(th.TextMuted)
		})
		box := ui.Column(c).Grow(1).FillWidth().Role(ui.RoleImage).Label(locales.PictureCompare())
		switch mode {
		case pictureSwipe:
			if box.Pressed() {
				x, _, _ := box.PointerPosition()
				b := box.LayoutBounds()
				if fr := frame(ui.Rect{W: b.W, H: b.H}); fr.W > 0 {
					o.swipe = min(max(float64((x-fr.X)/fr.W), 0), 1)
				}
			}
			box.Cursor(ui.CursorResizeEW).Draw(func(p *ui.Painter, r ui.Rect) {
				fr := frame(r)
				x := fr.X + fr.W*float32(o.swipe)
				p.Clip(ui.Rect{X: fr.X, Y: fr.Y, W: x - fr.X, H: fr.H}, 0, func() {
					checkers(p, fr)
					p.Image(before, side(fr, w0, h0), ui.FillBox)
				})
				p.Clip(ui.Rect{X: x, Y: fr.Y, W: fr.X + fr.W - x, H: fr.H}, 0, func() {
					checkers(p, fr)
					p.Image(after, side(fr, w1, h1), ui.FillBox)
				})
				p.Line(x, fr.Y, x, fr.Y+fr.H, 2, th.Accent)
			})
		case pictureOnion:
			box.Draw(func(p *ui.Painter, r ui.Rect) {
				fr := frame(r)
				checkers(p, fr)
				p.Image(before, side(fr, w0, h0), ui.FillBox)
			}).Children(func() {
				ui.Column(c).Absolute().Top(0).Left(0).Right(0).Bottom(0).Opacity(float32(o.onion)).Draw(func(p *ui.Painter, r ui.Rect) {
					p.Image(after, side(frame(r), w1, h1), ui.FillBox)
				})
			})
		case pictureDifference:
			switch {
			case ps.tooLarge:
				box.Center().Children(func() {
					ui.Text(c, locales.PictureTooLargeToCompare()).TextColor(th.TextMuted)
				})
			case ps.diff == nil:
				box.Children(func() { loading(c, "") })
			default:
				d := ps.diff
				box.Draw(func(p *ui.Painter, r ui.Rect) {
					fr := frame(r)
					p.Fill(fr, ui.RGB(0, 0, 0), 0)
					p.Image(d, fr, ui.FillBox)
				})
			}
		}
		switch mode {
		case pictureSwipe, pictureOnion:
			v := &o.swipe
			if mode == pictureOnion {
				v = &o.onion
			}
			ui.Row(c.Key(mode)).Gap(spaceM).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.PatchBefore()).FontSize(textSmall).TextColor(th.TextMuted)
				ui.Slider(c, v, 0, 1).Grow(1).Label(locales.PictureCompare())
				ui.Text(c, locales.PatchAfter()).FontSize(textSmall).TextColor(th.TextMuted)
			})
		case pictureDifference:
			if ps.diff != nil {
				msg := locales.PictureNoPixelsDiffer()
				if ps.differ > 0 {
					msg = locales.PicturePixelsDiffer(ps.differ)
				}
				ui.Text(c, msg).FontSize(textSmall).TextColor(th.TextMuted)
			}
		}
	})
}

// makeDifference makes the difference of pv's sides off the main
// thread, once.
func (a *App) makeDifference(c *ui.Context, pv *patchView) {
	ps := &pv.pics
	if ps.diffAsked {
		return
	}
	ps.diffAsked = true
	t := c.Theme().Text
	current := color.NRGBA{t.R, t.G, t.B, t.A}
	gen, update, sides, images, stale, life := pv.gen, a.Update, ps.sides, ps.images, pv.stale, pv.life
	a.goWork(nil, func() {
		if took, away := waitPicture(stale, life); !took {
			if away {
				update(func() {
					if pv.gen == gen && pv.stale == stale {
						pv.pics.diffAsked = false
					}
				})
			}
			return
		}
		defer endPicture()
		if stale.Load() {
			return
		}
		// The size the sides say first: an SVG is drawn at the size it
		// names, however large, and a bitmap decoded again whole.
		var fw, fh float64
		for _, img := range images {
			iw, ih := ui.ImageSize(img)
			fw, fh = max(fw, math.Ceil(float64(iw))), max(fh, math.Ceil(float64(ih)))
		}
		w, h := 0, 0
		var pics [2]image.Image
		if fw*fh <= maxDiffPixels {
			w, h = int(fw), int(fh)
			for k, img := range images {
				iw, ih := ui.ImageSize(img)
				if svg, ok := img.(*ui.SVG); ok {
					pics[k] = svg.Image(int(math.Ceil(float64(iw))), int(math.Ceil(float64(ih))), current)
				} else if p, _, err := image.Decode(bytes.NewReader(sides[k])); err == nil {
					pics[k] = p
				}
			}
		}
		var d ui.ImageSource
		n, tooLarge := 0, pics[0] == nil || pics[1] == nil
		if !tooLarge {
			var img *image.RGBA
			img, n = difference(pics[0], pics[1], w, h)
			b := ui.NewBitmap(img)
			if pictureScale(float32(w), float32(h)) > 1 {
				b.Pixelated()
			}
			d = b
		}
		update(func() {
			if pv.gen == gen {
				pv.pics.diff, pv.pics.differ, pv.pics.tooLarge = d, n, tooLarge
			}
		})
	})
}

// difference is the difference of two pictures in a frame of w by h,
// both from its top-left corner, and how many pixels differ: each
// pixel is opaque, black where they are the same, and as bright in each
// channel as the larger of that channel's change and the change of
// alpha, premultiplied as they show.
func difference(a, b image.Image, w, h int) (*image.RGBA, int) {
	frame := image.Rect(0, 0, w, h)
	pa, pb := image.NewRGBA(frame), image.NewRGBA(frame)
	draw.Draw(pa, frame, a, a.Bounds().Min, draw.Src)
	draw.Draw(pb, frame, b, b.Bounds().Min, draw.Src)
	n := 0
	for i := 0; i < len(pa.Pix); i += 4 {
		x, y := pa.Pix[i:i+4:i+4], pb.Pix[i:i+4:i+4]
		da := absDiff(x[3], y[3])
		changed := da != 0
		for c := range 3 {
			d := absDiff(x[c], y[c])
			changed = changed || d != 0
			x[c] = max(d, da)
		}
		x[3] = 255
		if changed {
			n++
		}
	}
	return pa, n
}

func absDiff(a, b uint8) uint8 { return max(a, b) - min(a, b) }

// checkerCell is the side of a square of the checkers, in DIPs.
const checkerCell = 8

// checkerBitmap is a checkerboard of a square a pixel, shown stretched
// and pixelated, so that one draw lays a whole frame's squares; tinted,
// it takes the theme's colors.
var checkerBitmap = sync.OnceValue(func() *ui.Bitmap {
	const n = 256
	img := image.NewGray(image.Rect(0, 0, n, n))
	for y := range n {
		for x := range n {
			img.Pix[y*n+x] = 255 - 40*uint8((x+y)%2)
		}
	}
	return ui.NewBitmap(img).Pixelated()
})

// checkers paints r with the squares that show where a picture is
// transparent.
func checkers(p *ui.Painter, r ui.Rect) {
	if r.W <= 0 || r.H <= 0 {
		return
	}
	th := p.Theme()
	tint := ui.RGB(255, 255, 255)
	if th.Dark {
		tint = th.Surface.Mix(ui.RGB(255, 255, 255), 0.12)
	}
	const most = 256 * checkerCell
	for y := r.Y; y < r.Y+r.H; y += most {
		for x := r.X; x < r.X+r.W; x += most {
			dst := ui.Rect{X: x, Y: y, W: min(most, r.X+r.W-x), H: min(most, r.Y+r.H-y)}
			p.ImageRegion(checkerBitmap(), ui.Rect{W: dst.W / checkerCell, H: dst.H / checkerCell}, dst, ui.ImageOptions{Tint: tint})
		}
	}
}
