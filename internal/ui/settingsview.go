package ui

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"ramus/internal/cli"
	"ramus/internal/git/write"
	"ramus/internal/settings"
	"ramus/internal/tools"
	"ramus/locales"
)

type settingsState struct {
	open bool
	// edit is the form; it is saved when the user presses Save.
	edit  settings.Settings
	theme int
	lang  string
	// editor and terminal are the programs chosen, nil for the app's own
	// choice.
	editor, terminal []string
	emails           string
	days, recent     int
	focusRefresh     bool
	// fetchMinutes is 0 for no fetching in the background.
	fetchMinutes int
	pulls        bool
	// difftool is whether git's global config makes the app its difftool
	// and mergetool: as read, in difftoolWas, once difftoolRead.
	difftool, difftoolWas, difftoolRead bool
	err                                 string
}

func (a *App) openSettings() {
	s := &a.settingsUI
	s.open, s.err = true, ""
	s.edit = a.Settings.WithDefaults()
	s.theme = max(0, indexOf(settings.Themes, s.edit.Theme))
	s.editor, s.terminal = slices.Clone(s.edit.Editor), slices.Clone(s.edit.Terminal)
	s.emails = strings.Join(s.edit.Emails, ", ")
	s.days, s.recent = s.edit.SnapshotDays, s.edit.RecentLimit
	s.focusRefresh = !s.edit.NoFocusRefresh
	s.fetchMinutes = s.edit.FetchEvery()
	s.pulls = s.edit.PullRequests
	s.difftool, s.difftoolWas, s.difftoolRead = false, false, false
	a.readDifftool()
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

// settingsInput is the width of the settings' text inputs, at the end
// of their rows.
const settingsInput = 260

func (a *App) settingsView(c *ui.Context) {
	s := &a.settingsUI
	if !s.open {
		return
	}
	th := c.Theme()
	ww, wh := c.Size()
	// A modal of the theme's look but for its padding, so that the line
	// above the buttons runs from edge to edge.
	ui.DialogBase(c, &s.open, func(back, panel ui.Element) {
		back.Background(ui.RGBA(0, 0, 0, 0.4))
		panel.Width(min(dialogMedium+40, ww-2*space2XL)).MaxHeight(max(wh-2*space2XL, 240)).Radius(10).Background(th.Background).
			Shadow(0, 10, 30, 0, ui.RGBA(0, 0, 0, 0.3))
		// The title and the buttons stay; what is between them scrolls
		// when the window is too short for it.
		ui.Column(c).FillWidth().Grow(1).Shrink(1).MinHeight(0).Children(func() {
			ui.Text(c, locales.SettingsTitle()).Bold().Padding(dialogPad, dialogPad, spaceM, dialogPad)
			ui.Scroll(c).Grow(1).Shrink(1).FillWidth().Children(func() {
				ui.Column(c).Gap(space2XL).Padding(spaceM, dialogPad, dialogPad, dialogPad).Children(func() {
					a.settingsSections(c)
				})
			})
			if s.err != "" {
				ui.Text(c, s.err).TextColor(th.Danger).Padding(spaceS, dialogPad)
			}
			ui.Divider(c)
			ui.Row(c).Gap(spaceM).AlignItems(ui.Center).Padding(spaceL, dialogPad).Children(func() {
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
				if ui.PrimaryButton(c, locales.CommonSave()).Disabled(!a.settingsChanged()).Clicked() {
					a.saveSettings()
				}
			})
		})
	})
}

// settingsSections are the settings, grouped as the system's are: each
// group a box of rows under its heading, a row's name at its start and
// its control at its end. Every value is chosen, none typed, but the
// email addresses.
func (a *App) settingsSections(c *ui.Context) {
	s := &a.settingsUI
	settingsGroup(c, locales.SettingsGeneral(), func(row settingsRow) {
		row(locales.SettingsAppearance(), func() ui.Element {
			return ui.Segmented(c, &s.theme, locales.SettingsThemeSystem(), locales.SettingsThemeLight(), locales.SettingsThemeDark())
		})
		if a.SetLocale != nil {
			row(locales.SettingsLanguage(), func() ui.Element {
				_, names := a.languageChoices()
				return ui.Select(c, &s.lang, names)
			})
		}
		row(locales.SettingsEmails(), func() ui.Element {
			return ui.TextInput(c, &s.emails).Placeholder(locales.SettingsEmailsExample()).Width(settingsInput)
		})
	})
	settingsGroup(c, locales.SettingsRepositories(), func(row settingsRow) {
		row(locales.SettingsFetch(), func() ui.Element {
			return choice(c, &s.fetchMinutes, fetchChoices, func(n int) string {
				switch {
				case n == 0:
					return locales.SettingsFetchNever()
				case n%60 == 0:
					return locales.SettingsEveryHours(n / 60)
				}
				return locales.SettingsEveryMinutes(n)
			})
		})
		row(locales.SettingsFocusRefresh(), func() ui.Element { return ui.Switch(c, &s.focusRefresh) })
		row(locales.SettingsPullRequests(), func() ui.Element { return ui.Switch(c, &s.pulls) })
		row(locales.SettingsRecentLimit(), func() ui.Element {
			return choice(c, &s.recent, []int{5, 10, 20, 50, 100}, strconv.Itoa)
		})
		row(locales.SettingsSnapshotDays(), func() ui.Element {
			return choice(c, &s.days, []int{7, 14, 30, 90, 365}, locales.SettingsDays)
		})
	})
	settingsGroup(c, locales.SettingsTools(), func(row settingsRow) {
		row(locales.SettingsEditor(), func() ui.Element { return a.toolChoice(c, tools.Editor, &s.editor) })
		row(locales.SettingsTerminal(), func() ui.Element { return a.toolChoice(c, tools.Terminal, &s.terminal) })
		if a.Program != "" && a.Runner != nil {
			row(locales.SettingsDiffTool(), func() ui.Element {
				return ui.Switch(c, &s.difftool).Disabled(!s.difftoolRead)
			})
		}
	})
}

// fetchChoices are the minutes between background fetches offered, 0
// for none.
var fetchChoices = []int{0, 5, 10, 15, 30, 60, 120}

// choice is a pop-up menu choosing *v among vs, each named by name; a
// value set in the settings file that is not among them is offered too.
func choice(c *ui.Context, v *int, vs []int, name func(int) string) ui.Element {
	if !slices.Contains(vs, *v) {
		vs = append(slices.Clone(vs), *v)
		slices.Sort(vs)
	}
	names := make([]string, len(vs))
	for i, x := range vs {
		names[i] = name(x)
	}
	sel := name(*v)
	e := ui.Select(c, &sel, names)
	if i := slices.Index(names, sel); i >= 0 {
		*v = vs[i]
	}
	return e
}

// toolChoice is a pop-up menu choosing the editor or the terminal: the
// app's own choice, which names the one it takes, each one found, the
// one chosen before if it is none of those, and one to choose from the
// disk.
func (a *App) toolChoice(c *ui.Context, kind tools.Kind, argv *[]string) ui.Element {
	title := locales.SettingsChooseEditor()
	if kind == tools.Terminal {
		title = locales.SettingsChooseTerminal()
	}
	auto := locales.SettingsToolAutomaticNone()
	if name := a.palette.auto[kind]; name != "" {
		auto = locales.SettingsToolAutomatic(name)
	}
	names, vals := []string{auto}, [][]string{nil}
	for _, tl := range a.palette.tools {
		if tl.Kind == kind && tl.Name != yourEditor && tl.Name != yourTerminal && !slices.Contains(names, tl.Name) {
			names, vals = append(names, tl.Name), append(vals, []string{tl.Program()})
		}
	}
	cur := slices.IndexFunc(vals, func(v []string) bool { return slices.Equal(v, *argv) })
	if cur < 0 {
		names, vals, cur = append(names, programName(*argv)), append(vals, *argv), len(names)
	}
	other := locales.SettingsToolOther()
	names = append(names, other)
	sel := names[cur]
	e := ui.Select(c, &sel, names)
	switch i := slices.Index(names, sel); {
	case sel == other:
		a.pickTool(kind, title, argv)
	case i >= 0 && i != cur:
		*argv = vals[i]
	}
	return e
}

// programName is what a program chosen is called: its app's name, or its
// file's.
func programName(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(argv[0]), ".app")
}

// pickTool asks for the program of an editor or a terminal, and makes it
// *argv once it is one.
func (a *App) pickTool(kind tools.Kind, title string, argv *[]string) {
	if a.PickFile == nil {
		return
	}
	pick, update, s := a.PickFile, a.Update, &a.settingsUI
	go func() {
		p, err := pick(title)
		if err == nil && p != "" {
			_, err = tools.Custom(title, kind, []string{p})
		}
		update(func() {
			switch {
			case err != nil:
				s.err = errText(err)
			case p != "" && s.open:
				*argv, s.err = []string{p}, ""
			}
		})
	}()
}

// readDifftool reads whether git's global config makes the app its
// difftool and mergetool, for the settings' switch.
func (a *App) readDifftool() {
	if a.Program == "" || a.Runner == nil {
		return
	}
	r, tool, update, s := a.Runner, a.globalTool(), a.Update, &a.settingsUI
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		on, err := write.ToolIsSet(ctx, r, tool)
		cancel()
		update(func() {
			if err == nil && s.open {
				s.difftool, s.difftoolWas, s.difftoolRead = on, on, true
			}
		})
	}()
}

// globalTool is the app as git's difftool and mergetool.
func (a *App) globalTool() write.GlobalTool {
	d, m := cli.ToolCommands(a.Program)
	return write.GlobalTool{Name: cli.ToolName, DiffCmd: d, MergeCmd: m}
}

// settingsRow adds a row to a group of settings: its name, which names
// its control for assistive technology and gives it a click, as a
// label does, and the control, which control builds.
type settingsRow func(label string, control func() ui.Element)

// settingsGroup is a group of settings under heading: fn adds its rows,
// with a line between them.
func settingsGroup(c *ui.Context, heading string, fn func(row settingsRow)) {
	th := c.Theme()
	ui.Column(c).Gap(spaceS).FillWidth().Children(func() {
		ui.Text(c, heading).FontSize(textSmall).Bold().TextColor(th.TextMuted).Padding(0, spaceXS)
		ui.Column(c).FillWidth().Radius(th.Radius+4).Border(1, th.Border).Children(func() {
			first := true
			fn(func(label string, control func() ui.Element) {
				if !first {
					ui.Box(c).Height(1).Background(th.Border).Margin(0, spaceL)
				}
				first = false
				ui.Row(c).Gap(spaceL).AlignItems(ui.Center).MinHeight(40).Padding(spaceS, spaceL).Children(func() {
					name := ui.Text(c, label).Grow(1).Shrink(1)
					control().LabelledBy(name)
				})
			})
		})
	})
}

// saveSettings checks the form, applies it, and writes the file off the
// main thread.
func (a *App) saveSettings() {
	s := &a.settingsUI
	next := a.formSettings()
	if s.difftoolRead && s.difftool != s.difftoolWas {
		r, tool, on, update := a.Runner, a.globalTool(), s.difftool, a.Update
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := write.SetTool(ctx, r, tool, on)
			cancel()
			if err != nil {
				update(func() { a.notice = locales.SettingsDiffToolFailed(errText(err)) })
			}
		}()
	}
	a.Settings, s.open = next.WithDefaults(), false
	a.findTools()
	if a.ApplyTheme != nil {
		a.ApplyTheme(a.Settings.Theme)
	}
	if tag, changed := a.formLanguage(); changed {
		a.SetLocale(tag)
	}
	a.writeSettings(next, locales.SettingsNotSaved)
}

// settingsChanged reports whether the form says anything other than
// what is in force, for Save to have something to do.
func (a *App) settingsChanged() bool {
	s := &a.settingsUI
	if _, changed := a.formLanguage(); changed || s.difftoolRead && s.difftool != s.difftoolWas {
		return true
	}
	// Compared as written, where a list left empty and none are the same,
	// and with the minutes between fetches as they take effect.
	norm := func(x settings.Settings) string {
		x = x.WithDefaults()
		on := x
		on.NoFetch = false
		x.FetchMinutes = on.FetchEvery()
		b, _ := json.Marshal(x)
		return string(b)
	}
	return norm(a.formSettings()) != norm(a.Settings)
}

// formLanguage is the language the form chooses, and whether it is
// another than the app's.
func (a *App) formLanguage() (string, bool) {
	if a.SetLocale == nil || a.ChosenLocale == nil {
		return "", false
	}
	tags, names := a.languageChoices()
	tag := tags[max(0, indexOf(names, a.settingsUI.lang))]
	return tag, tag != a.ChosenLocale()
}

// formSettings are the settings as the form has them.
func (a *App) formSettings() settings.Settings {
	s := &a.settingsUI
	next := a.Settings
	next.Theme = settings.Themes[min(max(s.theme, 0), len(settings.Themes)-1)]
	next.SnapshotDays, next.RecentLimit = s.days, s.recent
	next.NoFocusRefresh = !s.focusRefresh
	next.PullRequests = s.pulls
	// The minutes stay as they were while fetching is off, and at the
	// default they are left out, to follow it.
	next.NoFetch = s.fetchMinutes == 0
	if s.fetchMinutes > 0 {
		next.FetchMinutes = s.fetchMinutes
		if next.FetchMinutes == settings.DefaultFetchMinutes {
			next.FetchMinutes = 0
		}
	}
	next.Emails = nil
	for _, e := range strings.FieldsFunc(s.emails, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		next.Emails = append(next.Emails, e)
	}
	next.Editor, next.Terminal = s.editor, s.terminal
	return next
}
