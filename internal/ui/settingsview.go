package ui

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/settings"
	"ramus/internal/tools"
)

type settingsState struct {
	open bool
	// edit is the form; it is saved when the user presses Save.
	edit             settings.Settings
	theme            int
	editor, terminal string
	days, recent     float64
	err              string
}

func (a *App) openSettings() {
	s := &a.settingsUI
	s.open, s.err = true, ""
	s.edit = a.Settings.WithDefaults()
	s.theme = max(0, indexOf(settings.Themes, s.edit.Theme))
	s.editor, s.terminal = joinArgs(s.edit.Editor), joinArgs(s.edit.Terminal)
	s.days, s.recent = float64(s.edit.SnapshotDays), float64(s.edit.RecentLimit)
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}

func (a *App) settingsView(c *ui.Context) {
	s := &a.settingsUI
	if !s.open {
		return
	}
	th := c.Theme()
	ui.Modal(c, &s.open, func() {
		ui.Column(c).Width(540).Gap(14).Padding(20).Children(func() {
			ui.Text(c, "Settings").Bold()
			ui.Field(c, "Appearance", func() {
				ui.Segmented(c, &s.theme, "System", "Light", "Dark")
			})
			ui.Field(c, "Editor", func() {
				ui.TextInput(c, &s.editor).Placeholder("/usr/local/bin/code --wait")
			})
			ui.Field(c, "Terminal", func() {
				ui.TextInput(c, &s.terminal).Placeholder("/Applications/Ghostty.app")
			})
			ui.Field(c, "Keep snapshots of dropped work (days)", func() {
				ui.NumberInput(c, &s.days, 1, 365, 1)
			})
			ui.Field(c, "Recent repositories listed", func() {
				ui.NumberInput(c, &s.recent, 1, 200, 1)
			})
			if s.err != "" {
				ui.Text(c, s.err).TextColor(th.Danger)
			}
			ui.Row(c).Gap(8).Children(func() {
				if a.SettingsPath != "" && ui.Button(c, "Open Settings File").Clicked() {
					p := a.SettingsPath
					go tools.OpenDefault(p)
				}
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					s.open = false
				}
				if ui.PrimaryButton(c, "Save").Clicked() {
					a.saveSettings()
				}
			})
		})
	})
}

// saveSettings checks the form, applies it, and writes the file off the
// main thread.
func (a *App) saveSettings() {
	s := &a.settingsUI
	next := a.Settings
	next.Theme = settings.Themes[min(max(s.theme, 0), len(settings.Themes)-1)]
	next.SnapshotDays, next.RecentLimit = int(s.days), int(s.recent)
	for _, f := range []struct {
		text  string
		into  *[]string
		kind  tools.Kind
		label string
	}{{s.editor, &next.Editor, tools.Editor, "Editor"}, {s.terminal, &next.Terminal, tools.Terminal, "Terminal"}} {
		argv := splitArgs(f.text)
		if len(argv) > 0 {
			if _, err := tools.Custom(f.label, f.kind, argv); err != nil {
				s.err = f.label + ": " + err.Error()
				return
			}
		}
		*f.into = argv
	}
	a.Settings, s.open = next.WithDefaults(), false
	if a.ApplyTheme != nil {
		a.ApplyTheme(a.Settings.Theme)
	}
	if a.SettingsPath != "" {
		path, saved, update := a.SettingsPath, next, a.Update
		go func() {
			if err := settings.Save(path, saved); err != nil {
				update(func() { a.notice = "Settings not saved: " + err.Error() })
			}
		}()
	}
}

// splitArgs splits a command typed on one line at spaces, keeping text in
// double or single quotes together.
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	quote, in := rune(0), false
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '"' || r == '\''):
			quote, in = r, true
		case quote == 0 && (r == ' ' || r == '\t'):
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if in {
		out = append(out, cur.String())
	}
	return out
}

func joinArgs(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		if strings.ContainsAny(a, " \t'") {
			a = `"` + a + `"`
		} else if strings.Contains(a, `"`) {
			a = "'" + a + "'"
		}
		q[i] = a
	}
	return strings.Join(q, " ")
}
