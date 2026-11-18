package ui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/i18n"
	"github.com/egoist/mygo/ui"

	"ramus/internal/cli"
	"ramus/internal/git/run"
	"ramus/internal/git/write"
	"ramus/internal/settings"
	"ramus/internal/tools"
	"ramus/locales"
)

type settingsState struct {
	open bool
	// edit is the form; what it says is in force at once (applySettings).
	edit  settings.Settings
	theme int
	lang  string
	// editor and terminal are the programs chosen, nil for the app's own
	// choice.
	editor, terminal []string
	emails           string
	days, recent     int
	focusRefresh     bool
	hideDeps         bool
	// fetchMinutes is 0 for no fetching in the background.
	fetchMinutes int
	pulls        bool
	// difftool is whether git's global config makes the app its difftool
	// and mergetool: as read, in difftoolWas, once difftoolRead.
	difftool, difftoolWas, difftoolRead bool
	// ident is the identity git uses, in the repository shown if any,
	// as the form has it, identWas as read or last written, once
	// identRead; identFrom says where each field is read from and goes.
	// It is written as the settings close, or as a field of it takes
	// Enter: not as typed.
	ident, identWas write.Identity
	identFrom       write.IdentityFrom
	identRead       bool
	identSaver      *identSaver
	// identGen counts the openings of the settings, for a read or a
	// failure of one before to leave the form alone.
	identGen int
	// nameField is the field of the name, which focusName focuses once
	// it can take the focus.
	nameField ui.Handle
	focusName bool
	err       string
}

func (a *App) openSettings() {
	s := &a.settingsUI
	s.open, s.err = true, ""
	a.loadSettingsForm()
	s.difftool, s.difftoolWas, s.difftoolRead = false, false, false
	a.readDifftool()
	// Closed, and opened again before a frame wrote what it had.
	a.saveIdentity()
	s.ident, s.identWas, s.identFrom, s.identRead = write.Identity{}, write.Identity{}, write.IdentityFrom{}, false
	s.identGen++
	a.readIdentity()
}

// openIdentity opens the settings with the focus in the name git
// commits with, for a commit git refused for want of it.
func (a *App) openIdentity() {
	a.openSettings()
	a.settingsUI.focusName = true
}

// loadSettingsForm puts the settings in force in the form: as it opens,
// and as another window changes them, or the form, applied as it is in
// each frame, would put back what it said.
func (a *App) loadSettingsForm() {
	s := &a.settingsUI
	s.edit = a.Settings.WithDefaults()
	s.theme = max(0, indexOf(settings.Themes, s.edit.Theme))
	s.editor, s.terminal = slices.Clone(s.edit.Editor), slices.Clone(s.edit.Terminal)
	s.emails = strings.Join(s.edit.Emails, ", ")
	s.days, s.recent = s.edit.SnapshotDays, s.edit.RecentLimit
	s.focusRefresh = !s.edit.NoFocusRefresh
	s.hideDeps = !s.edit.ShowDependencyFolders
	s.fetchMinutes = s.edit.FetchEvery()
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

// settingsControlW is the width of the settings' menus and text
// inputs, at the end of their rows, so that they line up.
const settingsControlW = 220

func (a *App) settingsView(c *ui.Context) {
	s := &a.settingsUI
	if !s.open {
		a.saveIdentity()
		return
	}
	th := c.Theme()
	ww, wh := c.Size()
	// A modal of the theme's look but for its padding, so that the line
	// above the buttons runs from edge to edge.
	a.dialogBase(c, &s.open, func(back, panel ui.Element) {
		back.Background(ui.RGBA(0, 0, 0, 0.4))
		panel.Width(min(dialogMedium+40, ww-2*space2XL)).MaxHeight(max(wh-2*space2XL, 240)).Radius(radiusLarge).Background(th.Background).
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
				ui.Spacer(c)
				if ui.PrimaryButton(c, locales.CommonDone()).Clicked() {
					s.open = false
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
				return ui.Select(c, &s.lang, names).Width(settingsControlW)
			})
		}
	})
	settingsGroup(c, locales.SettingsGit(), func(row settingsRow) {
		if a.Runner != nil {
			field := func(v *string, from write.Origin) ui.Element {
				in := ui.TextInput(c, v).Width(settingsControlW).Disabled(!s.identRead)
				if in.Submitted() {
					a.saveIdentity()
				}
				return identTooltip(in, from)
			}
			row(locales.SettingsGitName(), func() ui.Element {
				in := field(&s.ident.Name, s.identFrom[0]).Bind(&s.nameField)
				if s.focusName && s.identRead {
					s.nameField.Focus(c)
					s.focusName = false
				}
				return in
			})
			row(locales.SettingsGitEmail(), func() ui.Element { return field(&s.ident.Email, s.identFrom[1]) })
		}
		row(locales.SettingsEmails(), func() ui.Element {
			return ui.TextInput(c, &s.emails).Placeholder(locales.SettingsEmailsExample()).Width(settingsControlW)
		})
		if a.Runner != nil {
			row(locales.SettingsDefaultBranch(), func() ui.Element {
				in := ui.TextInput(c, &s.ident.DefaultBranch).Placeholder(locales.SettingsDefaultBranchGit()).Width(settingsControlW).Disabled(!s.identRead)
				if in.Submitted() {
					a.saveIdentity()
				}
				return identTooltip(in, s.identFrom[2])
			})
		}
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
			}).Width(settingsControlW)
		})
		row(locales.SettingsFocusRefresh(), func() ui.Element { return ui.Switch(c, &s.focusRefresh) })
		row(locales.SettingsHideDependencies(), func() ui.Element { return ui.Switch(c, &s.hideDeps) })
		row(locales.SettingsPullRequests(), func() ui.Element { return ui.Switch(c, &s.pulls) })
		row(locales.SettingsRecentLimit(), func() ui.Element {
			return choice(c, &s.recent, []int{5, 10, 20, 50, 100}, strconv.Itoa).Width(settingsControlW)
		})
		row(locales.SettingsSnapshotDays(), func() ui.Element {
			return choice(c, &s.days, []int{7, 14, 30, 90, 365}, locales.SettingsDays).Width(settingsControlW)
		})
	})
	settingsGroup(c, locales.SettingsTools(), func(row settingsRow) {
		row(locales.SettingsEditor(), func() ui.Element { return a.toolChoice(c, tools.Editor, &s.editor).Width(settingsControlW) })
		row(locales.SettingsTerminal(), func() ui.Element {
			return a.toolChoice(c, tools.Terminal, &s.terminal).Width(settingsControlW)
		})
		if a.Program != "" && a.Runner != nil {
			row(locales.SettingsDiffTool(), func() ui.Element {
				return ui.Switch(c, &s.difftool).Disabled(!s.difftoolRead)
			})
		}
	})
	settingsGroup(c, locales.SettingsAdvanced(), func(row settingsRow) {
		row(locales.SettingsKeyboardShortcuts(), func() ui.Element {
			b := ui.Button(c, locales.SettingsCustomize())
			if b.Clicked() {
				a.openKeys()
			}
			return b
		})
		if a.SettingsPath != "" {
			row(locales.SettingsFile(), func() ui.Element {
				b := ui.Button(c, locales.SettingsOpenFile())
				if b.Clicked() {
					p := a.SettingsPath
					a.goWork(nil, func() { tools.OpenDefault(p) })
				}
				return b
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
	a.goWork(nil, func() {
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
	})
}

// readDifftool reads whether git's global config makes the app its
// difftool and mergetool, for the settings' switch.
func (a *App) readDifftool() {
	if a.Program == "" || a.Runner == nil {
		return
	}
	r, tool, update, s := a.Runner, a.globalTool(), a.Update, &a.settingsUI
	a.goWork(nil, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		on, err := write.ToolIsSet(ctx, r, tool)
		cancel()
		update(func() {
			if err == nil && s.open {
				s.difftool, s.difftoolWas, s.difftoolRead = on, on, true
			}
		})
	})
}

// identTooltip says where a field of the identity is read from and
// goes when that is not the global config itself; else nothing.
func identTooltip(in ui.Element, from write.Origin) ui.Element {
	switch {
	case from.Kept != "":
		in.Tooltip(locales.SettingsGitRepositoryOwn(homeTilde(from.Kept)))
	case from.Included:
		in.Tooltip(locales.SettingsGitFromFile(homeTilde(from.File)))
	}
	return in
}

// homeTilde is path with the user's home folder at its start written ~.
// On Windows, whose file names ignore case, the folder is matched in
// any case, as a config can name it.
func homeTilde(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	home = filepath.Clean(home)
	if len(path) < len(home) {
		return path
	}
	head, rest := path[:len(home)], path[len(home):]
	same := head == home || runtime.GOOS == "windows" && strings.EqualFold(head, home)
	if same && (rest == "" || rest[0] == filepath.Separator) {
		return "~" + rest
	}
	return path
}

// readIdentity reads the identity git uses, in the repository shown if
// there is one, as its own config and includeIf make it there, for the
// settings' fields.
func (a *App) readIdentity() {
	if a.Runner == nil {
		return
	}
	var w write.Where
	if t := a.current(); t != nil && t.s.Repo != nil {
		w = t.s.Where()
	}
	r, update, s := a.Runner, a.Update, &a.settingsUI
	gen, saver := s.identGen, s.identSaver
	a.goWork(nil, func() {
		if saver != nil {
			// What a save asked before has to be in what is read, or
			// the form would show, and save over, what it replaced.
			saver.wait()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		id, from, err := write.ReadIdentity(ctx, r, w)
		cancel()
		update(func() {
			switch {
			case !s.open || s.identRead || s.identGen != gen:
				// Read for an opening of the settings since gone.
			case err != nil:
				// The fields stay off: what they would write over is
				// not known.
				s.err = locales.SettingsGitReadFailed(errText(err))
			default:
				s.ident, s.identWas, s.identFrom, s.identRead = id, id, from, true
			}
		})
	})
}

// saveIdentity writes the identity the form has to git's global config,
// or the files it includes where a field comes from, off the main
// thread, when it changed. A failure says so, in the form while the
// same opening of it shows; the identity is read again the next time
// the settings open.
func (a *App) saveIdentity() {
	s := &a.settingsUI
	if !s.identRead || s.ident == s.identWas || a.Runner == nil {
		return
	}
	r, update, was, now, from, gen := a.Runner, a.Update, s.identWas, s.ident, s.identFrom, s.identGen
	s.identWas = now
	if s.identSaver == nil {
		s.identSaver = newIdentSaver()
	}
	s.identSaver.save(a, r, &identWrite{was: was, now: now, from: from, failed: func(was write.Identity, err error) {
		msg := errText(err)
		if errors.Is(err, write.ErrBranchName) {
			msg = locales.SettingsDefaultBranchInvalid(strings.TrimSpace(now.DefaultBranch))
		}
		update(func() {
			if s.open && s.identGen == gen {
				s.err = locales.SettingsGitFailed(msg)
				// What the config has, for the next save to write
				// what did not go in.
				s.identWas = was
				return
			}
			if !s.open {
				s.identRead = false
			}
			a.notice = locales.SettingsGitFailed(msg)
		})
	}})
}

// SaveIdentity writes the identity the settings have, open or not: as
// the window closes or the app quits, which no frame follows to write it.
func (a *App) SaveIdentity() { a.saveIdentity() }

// WaitIdentity waits for the identity being written, for the app to quit
// once it is.
func (a *App) WaitIdentity() {
	if is := a.settingsUI.identSaver; is != nil {
		is.wait()
	}
}

// identSaver writes the identity one change at a time: two writers of
// git's global config at once, the second fails on its lock. A change
// asked for while one is written follows it, the latest only.
type identSaver struct {
	mu      sync.Mutex
	running bool
	next    *identWrite
	// idle is broadcast as the last write ends, after its failure is told.
	idle *sync.Cond
}

func newIdentSaver() *identSaver {
	is := &identSaver{}
	is.idle = sync.NewCond(&is.mu)
	return is
}

// wait waits until nothing is being written.
func (is *identSaver) wait() {
	is.mu.Lock()
	for is.running {
		is.idle.Wait()
	}
	is.mu.Unlock()
}

type identWrite struct {
	was, now write.Identity
	from     write.IdentityFrom
	failed   func(was write.Identity, err error)
}

// save writes w, or after the write running.
func (is *identSaver) save(a *App, r *run.Runner, w *identWrite) {
	is.mu.Lock()
	defer is.mu.Unlock()
	if is.running {
		if is.next != nil {
			w.was = is.next.was // the one dropped was never written
		}
		is.next = w
		return
	}
	is.running = true
	a.goWork(nil, func() { is.drain(r, w) })
}

// drain writes w, then each write asked meanwhile, until none is left,
// and leaves the saver idle.
func (is *identSaver) drain(r *run.Runner, w *identWrite) {
	for w != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		has, err := write.SetIdentity(ctx, r, w.was, w.now, w.from)
		cancel()
		is.mu.Lock()
		if is.next == nil && err != nil {
			// Told before the saver is idle, so that a read waiting
			// for it comes after.
			is.mu.Unlock()
			w.failed(has, err)
			is.mu.Lock()
		}
		next := is.next
		is.next = nil
		if next != nil && err != nil {
			// The next changes the config as it is, with what of
			// this one went in before the failure, not as asked.
			next.was = has
		}
		if next == nil {
			is.running = false
			is.idle.Broadcast()
		}
		is.mu.Unlock()
		w = next
	}
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
		// In line with the rows' names.
		ui.Text(c, heading).FontSize(textSmall).Bold().TextColor(th.TextMuted).Padding(0, spaceL)
		ui.Column(c).FillWidth().Radius(radiusLarge).Border(1, th.Border).Children(func() {
			first := true
			fn(func(label string, control func() ui.Element) {
				if !first {
					ui.Box(c).Height(1).Background(th.Border).Margin(0, spaceL)
				}
				first = false
				ui.Row(c).Gap(spaceL).AlignItems(ui.Center).MinHeight(barHeight).Padding(spaceS, spaceL).Children(func() {
					name := ui.Text(c, label).Grow(1).Shrink(1)
					control().LabelledBy(name)
				})
			})
		})
	})
}

// applySettings puts what the form says in force as soon as it says it,
// as the system's settings do: there is no Save. The file is written off
// the main thread, the last of several changes in a row only.
func (a *App) applySettings() {
	s := &a.settingsUI
	if !s.open || !a.settingsChanged() {
		return
	}
	next := a.formSettings()
	if s.difftoolRead && s.difftool != s.difftoolWas {
		r, tool, on, update := a.Runner, a.globalTool(), s.difftool, a.Update
		s.difftoolWas = on
		a.goWork(nil, func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			err := write.SetTool(ctx, r, tool, on)
			cancel()
			if err != nil {
				update(func() { a.notice = locales.SettingsDiffToolFailed(errText(err)) })
			}
		})
	}
	if next.Theme != a.Settings.Theme && a.ApplyTheme != nil {
		a.ApplyTheme(next.Theme)
	}
	a.Settings = next.WithDefaults()
	a.settingsToSessions()
	if a.SettingsChanged != nil {
		// The other windows too: one of them keeping what it had would
		// write it back over this the next time it keeps its settings.
		a.SettingsChanged(a.Settings)
	}
	a.findTools()
	if tag, changed := a.formLanguage(); changed {
		a.SetLocale(tag)
	}
	a.writeSettings(next, locales.SettingsNotSaved)
}

// settingsChanged reports whether the form says anything other than
// what is in force, for applySettings to have something to do.
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
	next.ShowDependencyFolders = !s.hideDeps
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
