package ui

import (
	"strings"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"ramus/internal/cli"
	"ramus/internal/settings"
	"ramus/internal/tools"
	"ramus/locales"
)

type settingsState struct {
	open bool
	// edit is the form; it is saved when the user presses Save.
	edit             settings.Settings
	theme            int
	lang             string
	editor, terminal string
	emails           string
	days, recent     float64
	focusRefresh     bool
	fetch            bool
	fetchMinutes     float64
	pulls            bool
	err              string
}

func (a *App) openSettings() {
	s := &a.settingsUI
	s.open, s.err = true, ""
	s.edit = a.Settings.WithDefaults()
	s.theme = max(0, indexOf(settings.Themes, s.edit.Theme))
	s.editor, s.terminal = joinArgs(s.edit.Editor), joinArgs(s.edit.Terminal)
	s.emails = strings.Join(s.edit.Emails, ", ")
	s.days, s.recent = float64(s.edit.SnapshotDays), float64(s.edit.RecentLimit)
	s.focusRefresh = !s.edit.NoFocusRefresh
	// Turned off, fetching shows the minutes it would take, turned on.
	on := s.edit
	on.NoFetch = false
	s.fetch, s.fetchMinutes = !s.edit.NoFetch, float64(on.FetchEvery())
	s.pulls = s.edit.PullRequests
	if a.ChosenLocale != nil {
		tags, names := a.languageChoices()
		s.lang = names[max(0, indexOf(tags, a.ChosenLocale()))]
	}
}

// languageChoices are what Settings offers as the app's language: the
// system's first, then each language the app is written in, named in
// itself.
func (a *App) languageChoices() (tags, names []string) {
	tags, names = []string{""}, []string{locales.SettingsLanguageSystem()}
	for _, tag := range a.Languages {
		tags, names = append(tags, tag), append(names, i18n.Autonym(tag))
	}
	return tags, names
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
		dialogBox(c).Width(dialogMedium).Children(func() {
			ui.Text(c, locales.SettingsTitle()).Bold()
			ui.Field(c, locales.SettingsAppearance(), func() {
				ui.Segmented(c, &s.theme, locales.SettingsThemeSystem(), locales.SettingsThemeLight(), locales.SettingsThemeDark())
			})
			if a.SetLocale != nil {
				ui.Field(c, locales.SettingsLanguage(), func() {
					_, names := a.languageChoices()
					ui.Select(c, &s.lang, names)
				})
			}
			ui.Field(c, locales.SettingsEditor(), func() {
				ui.TextInput(c, &s.editor).Placeholder("/usr/local/bin/code --wait")
			})
			ui.Field(c, locales.SettingsTerminal(), func() {
				ui.TextInput(c, &s.terminal).Placeholder("/Applications/Ghostty.app")
			})
			ui.Field(c, locales.SettingsEmails(), func() {
				ui.TextInput(c, &s.emails).Placeholder(locales.SettingsEmailsExample())
			})
			ui.Field(c, locales.SettingsSnapshotDays(), func() {
				ui.NumberInput(c, &s.days, 1, 365, 1)
			})
			ui.Field(c, locales.SettingsRecentLimit(), func() {
				ui.NumberInput(c, &s.recent, 1, 200, 1)
			})
			ui.Checkbox(c, &s.focusRefresh, locales.SettingsFocusRefresh())
			ui.Checkbox(c, &s.fetch, locales.SettingsFetch())
			if s.fetch {
				ui.Field(c, locales.SettingsFetchMinutes(), func() {
					ui.NumberInput(c, &s.fetchMinutes, settings.MinFetchMinutes, 24*60, 5)
				})
			}
			ui.Checkbox(c, &s.pulls, locales.SettingsPullRequests())
			if a.Program != "" {
				ui.Field(c, locales.SettingsDiffTool(), func() {
					snippet := cli.ToolConfig(a.Program)
					ui.Row(c).Gap(spaceM).AlignItems(ui.Start).Children(func() {
						ui.Text(c, strings.TrimSuffix(snippet, "\n")).Font("monospace").FontSize(textCaption).Selectable().Grow(1)
						if ui.Button(c, locales.CommonCopy()).Clicked() {
							a.clip = snippet
						}
					})
				})
			}
			if s.err != "" {
				ui.Text(c, s.err).TextColor(th.Danger)
			}
			ui.Row(c).Gap(spaceM).Children(func() {
				if ui.Button(c, locales.SettingsKeyboardShortcuts()).Clicked() {
					a.openKeys()
				}
				if a.SettingsPath != "" && ui.Button(c, locales.SettingsOpenFile()).Clicked() {
					p := a.SettingsPath
					go tools.OpenDefault(p)
				}
				ui.Spacer(c)
				if ui.Button(c, locales.CommonCancel()).Clicked() {
					s.open = false
				}
				if ui.PrimaryButton(c, locales.CommonSave()).Clicked() {
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
	next.NoFocusRefresh = !s.focusRefresh
	next.PullRequests = s.pulls
	// The minutes stay as they were while fetching is off, and at the
	// default they are left out, to follow it.
	next.NoFetch = !s.fetch
	if s.fetch {
		next.FetchMinutes = int(s.fetchMinutes)
		if next.FetchMinutes == settings.DefaultFetchMinutes {
			next.FetchMinutes = 0
		}
	}
	next.Emails = nil
	for _, e := range strings.FieldsFunc(s.emails, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		next.Emails = append(next.Emails, e)
	}
	for _, f := range []struct {
		text  string
		into  *[]string
		kind  tools.Kind
		label string
	}{{s.editor, &next.Editor, tools.Editor, locales.SettingsEditor()}, {s.terminal, &next.Terminal, tools.Terminal, locales.SettingsTerminal()}} {
		argv := splitArgs(f.text)
		if len(argv) > 0 {
			if _, err := tools.Custom(f.label, f.kind, argv); err != nil {
				s.err = locales.SettingsToolError(f.label, errText(err))
				return
			}
		}
		*f.into = argv
	}
	a.Settings, s.open = next.WithDefaults(), false
	a.findTools()
	if a.ApplyTheme != nil {
		a.ApplyTheme(a.Settings.Theme)
	}
	if a.SetLocale != nil && a.ChosenLocale != nil {
		tags, names := a.languageChoices()
		if tag := tags[max(0, indexOf(names, s.lang))]; tag != a.ChosenLocale() {
			a.SetLocale(tag)
		}
	}
	if a.SettingsPath != "" {
		path, saved, update := a.SettingsPath, next, a.Update
		go func() {
			if err := settings.Save(path, saved); err != nil {
				update(func() { a.notice = locales.SettingsNotSaved(errText(err)) })
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
