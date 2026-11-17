package ui

import (
	"hash/fnv"
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
	return nil
}
