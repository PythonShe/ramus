package ui

import (
	"image/color"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
)

// The window is ivory in the light, dark in the dark: the system's
// appearance unless the settings choose one, whatever the system says.
func TestTheme(t *testing.T) {
	h := newHarness(t)
	ivory, dark := ivoryTheme().Background, ui.DarkTheme().Background
	for _, c := range []struct {
		choice     string
		systemDark bool
		want       ui.Color
	}{
		{"system", false, ivory},
		{"system", true, dark},
		{"light", true, ivory},
		{"dark", false, dark},
	} {
		h.app.Settings.Theme = c.choice
		h.tt.SetDark(c.systemDark)
		h.tt.Frame()
		if got := h.tt.Image().RGBAAt(600, 400); got.R != c.want.R || got.G != c.want.G || got.B != c.want.B {
			t.Errorf("%s with the system dark %v: %v, want %v", c.choice, c.systemDark, got, c.want)
		}
	}
}

// isAccent reports whether px is the light theme's accent, as a row the
// pointer is on is drawn.
func isAccent(px color.RGBA) bool {
	return rgbDist([3]int{int(px.R), int(px.G), int(px.B)}, rgb(ivoryTheme().Accent)) < 40
}

// A theme chosen in the settings is in force as it is chosen, for the
// system's appearance too.
func TestThemeChosen(t *testing.T) {
	h := newHarness(t)
	var applied []string
	h.app.ApplyTheme = func(s string) { applied = append(applied, s) }
	h.tt.SetDark(true)
	h.app.openSettings()
	h.tt.Frame()
	h.click("Light")
	h.click("Done")
	if px, want := h.tt.Image().RGBAAt(600, 790), ivoryTheme().Background; px.R != want.R || px.G != want.G || px.B != want.B {
		t.Errorf("Light chosen: %v, want %v", px, want)
	}
	if !slices.Equal(applied, []string{"light"}) {
		t.Errorf("system appearance set to %q", applied)
	}
}
