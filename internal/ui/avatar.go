package ui

import (
	"bytes"
	"hash/fnv"
	"image"
	"math"
	"strings"
	"unicode"

	"github.com/egoist/mygo/ui"
)

// avatar draws a person as a circle size across: their picture once the
// forge gave one, otherwise the initials of name on a color taken from
// email, so that one person keeps one color whatever name they commit
// under. ui.Avatar draws the same at one size only.
func (a *App) avatar(c *ui.Context, t *tab, name, email string, size float32) ui.Element {
	box := ui.Box(c).Size(size, size).Radius(size / 2).Clip().Shrink(0).Center().Role(ui.RoleImage).Label(name)
	if bmp := a.picture(t, email); bmp != nil {
		return box.Children(func() { ui.Image(c, bmp).Fit(ui.Cover).Size(size, size) })
	}
	h := fnv.New32a()
	h.Write([]byte(strings.ToLower(strings.TrimSpace(email))))
	box.Background(hsl(float64(h.Sum32()%360), 0.45, 0.55)).TextColor(ui.RGB(255, 255, 255))
	return box.Children(func() {
		// The circle names the person; its letters would say less.
		ui.Text(c, initials(name)).FontWeight(600).FontSize(size * 0.42).SingleLine().Role(ui.RoleNone)
	})
}

// initials are the first letters of the first and last words of name.
func initials(name string) string {
	var first, last rune
	inWord := false
	for _, r := range name {
		letter := unicode.IsLetter(r) || unicode.IsDigit(r)
		if letter && !inWord {
			if first == 0 {
				first = unicode.ToUpper(r)
			} else {
				last = unicode.ToUpper(r)
			}
		}
		inWord = letter
	}
	switch {
	case first == 0:
		return ""
	case last == 0:
		return string(first)
	}
	return string([]rune{first, last})
}

// hsl returns the color of hue h in degrees, saturation s and lightness l.
func hsl(h, s, l float64) ui.Color {
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2
	var r, g, b float64
	switch {
	case h < 60:
		r, g = c, x
	case h < 120:
		r, g = x, c
	case h < 180:
		g, b = c, x
	case h < 240:
		g, b = x, c
	case h < 300:
		r, b = x, c
	default:
		r, b = c, x
	}
	u := func(v float64) uint8 { return uint8(math.Round((v + m) * 255)) }
	return ui.RGB(u(r), u(g), u(b))
}

// picture is the picture of the person with email, decoded once, nil
// while the forge has given none.
func (a *App) picture(t *tab, email string) *ui.Bitmap {
	s := t.s
	if gen := s.AvatarsChanged(); gen != t.picsGen || len(t.pics) > picsKept {
		// Pictures dropped or come: decoded again as they are shown.
		t.pics, t.picsGen = nil, gen
	}
	key := strings.ToLower(email)
	if bmp, ok := t.pics[key]; ok {
		return bmp
	}
	data, ok := s.Avatar(email)
	if !ok {
		return nil
	}
	// A small picture, decoded here once; one that does not decode, or
	// says it is larger than a picture of a person is, is kept as nil,
	// for initials.
	var bmp *ui.Bitmap
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil && cfg.Width*cfg.Height <= maxAvatarPixels {
		if b, err := ui.DecodeBitmap(data); err == nil {
			bmp = b
		}
	}
	if t.pics == nil {
		t.pics = map[string]*ui.Bitmap{}
	}
	t.pics[key] = bmp
	return bmp
}

// maxAvatarPixels bounds a picture of a person decoded: the forge sends
// them 64 pixels square.
const maxAvatarPixels = 512 * 512

// picsKept bounds the pictures a tab keeps decoded: past it they are
// decoded again as shown.
const picsKept = 512

// syncAvatars lets the session ask the forge for people's pictures while
// the settings turn pull requests on, through the same tool, and asks for
// those of commits ids.
func (a *App) syncAvatars(t *tab, ids [][]byte) {
	if !a.Settings.PullRequests || a.Runner == nil && a.forgeFind == nil {
		t.s.SetForgeFind(nil)
		return
	}
	t.s.SetForgeFind(a.findForge())
	if len(ids) > 0 {
		t.s.RequestAvatars(ids)
	}
}
