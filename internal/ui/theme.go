package ui

import "github.com/egoist/mygo/ui"

// ivoryTheme is the app's light theme: warm paper rather than mygo's
// white and zinc, with ink of the same warmth. Code and diffs take their
// background and text from it (editor.DefaultTheme), and diffs tint their
// lines by mixing into that background, so they warm with it.
func ivoryTheme() *ui.Theme {
	t := ui.LightTheme()
	t.Background = ui.Hex("#fffcf0")
	t.Surface = ui.Hex("#f5efdc")
	t.SurfaceHover = ui.Hex("#ede5cc")
	t.SurfacePressed = ui.Hex("#e3d9bc")
	t.Border = ui.Hex("#ddd2b2")
	t.Text = ui.Hex("#2a2416")
	t.TextMuted = ui.Hex("#6b6046")
	t.Accent = ui.Hex("#9a6a00")
	t.AccentHover = ui.Hex("#855b00")
	t.AccentPressed = ui.Hex("#704c00")
	t.AccentText = ui.Hex("#fffcf0")
	t.Danger = ui.Hex("#b42318")
	t.Warning = ui.Hex("#a15c07")
	t.Success = ui.Hex("#3f7d20")
	t.Selection = ui.RGBA(196, 146, 20, 0.28)
	t.Focus = ui.RGBA(154, 106, 0, 0.5)
	t.Scrollbar = ui.RGBA(60, 45, 10, 0.3)
	return t
}

// themes keeps the theme a window last built with, so that a frame does
// not make one: it changes only with the appearance or the desktop's
// preferences.
type themes struct {
	dark  bool
	prefs ui.Preferences
	theme *ui.Theme
}

// apply gives the window the app's theme for its appearance: dark when
// choice is "dark", light when it is "light", else the system's, which
// mygo has resolved into c.Theme by now.
func (ts *themes) apply(c *ui.Context, choice string) {
	dark := c.Theme().Dark
	switch choice {
	case "light":
		dark = false
	case "dark":
		dark = true
	}
	prefs := c.Preferences()
	if ts.theme == nil || ts.dark != dark || ts.prefs != prefs {
		t, follow := ivoryTheme(), prefs
		// Ivory keeps its own accent, which the system's would clash with;
		// it follows the rest of the desktop's preferences.
		follow.Accent = ui.Color{}
		if dark {
			t, follow = ui.DarkTheme(), prefs
		}
		ts.dark, ts.prefs, ts.theme = dark, prefs, t.WithPreferences(follow)
	}
	c.SetTheme(ts.theme)
}
