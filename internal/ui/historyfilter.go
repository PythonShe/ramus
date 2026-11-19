package ui

import (
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/history"
	"ramus/internal/git/redact"
	"ramus/internal/git/refs"
	"ramus/internal/library"
	"ramus/internal/session"
	"ramus/locales"
)

// historyFilter is the history's filter bar as typed; apply hands it to
// the session.
type historyFilter struct {
	open                                         bool
	author, message, changed, path, since, until string
	regexp, firstParent, noMerges                bool
	// only is the branch history shows alone, "" for every branch.
	only string
	err  string
	// person names whom author, set to personAuthor from a person's
	// name in a commit, matches: the bar shows the name, while author,
	// their email, filters. Once author is anything else, it shows as
	// typed.
	person, personAuthor string
	// applied is the filter as last applied, by its fields, "" for none;
	// typed, when set, is when a field last changed, to apply once typing
	// pauses.
	applied string
	typed   time.Time
}

// filterPause is how long typing in the filter bar pauses before what
// was typed applies.
const filterPause = 400 * time.Millisecond

// key is the filter as typed, to tell whether it changed since it was
// applied.
func (hf *historyFilter) key() string {
	flag := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}
	return strings.Join([]string{hf.author, hf.message, hf.changed, hf.path, hf.since, hf.until, hf.only,
		flag(hf.regexp), flag(hf.firstParent), flag(hf.noMerges)}, "\x00")
}

// pending reports whether the filter as typed differs from the one
// applied: an empty filter typed into and cleared again is none, and
// reloads nothing.
func (hf *historyFilter) pending() bool {
	k := hf.key()
	if hf.applied == "" {
		return k != noFilterKey
	}
	return k != hf.applied
}

// noFilterKey is the key of the empty filter.
var noFilterKey = (&historyFilter{}).key()

// refPrefs are a repository's pinned branches and the refs hidden from
// its history, as the library keeps them.
type refPrefs struct {
	loaded         bool
	loading        bool
	pinned, hidden []string
	// fetchOff keeps the repository from fetching in the background.
	fetchOff bool
	// editor names the editor the repository's files open in, "" the
	// app's own choice.
	editor string
	// gen counts changes, for what is built from them.
	gen int
}

// loadRefPrefs reads the pinned and hidden refs once per tab, and shows
// history without the hidden ones.
func (a *App) loadRefPrefs(t *tab) {
	rp := &t.prefs
	if rp.loaded || rp.loading {
		return
	}
	if a.Library == nil {
		rp.loaded = true
		return
	}
	rp.loading = true
	lib, path, update := a.Library, t.s.Path, a.Update
	a.goWork(t, func() {
		pinned, hidden := lib.RefPrefs(path)
		fetchOff, editor := lib.FetchOff(path), lib.Editor(path)
		update(func() {
			rp.loading, rp.loaded = false, true
			rp.fetchOff, rp.editor = fetchOff, editor
			rp.pinned, rp.hidden = pinned, hidden
			rp.gen++
			if len(hidden) > 0 {
				a.applyHistoryFilter(t)
			}
		})
	})
}

// saveRefPrefs keeps the pinned and hidden refs off the main thread.
func (a *App) saveRefPrefs(t *tab) {
	if a.Library == nil {
		return
	}
	lib, path, update := a.Library, t.s.Path, a.Update
	pinned, hidden := slices.Clone(t.prefs.pinned), slices.Clone(t.prefs.hidden)
	a.goWrite(func() func() {
		if err := lib.SetRefPrefs(path, pinned, hidden); err != nil {
			return func() { update(func() { a.tell(t, errText(err)) }) }
		}
		return nil
	})
}

// toggleRef adds name to the list or takes it out, and saves.
func (a *App) toggleRef(t *tab, list *[]string, name string) {
	if i := slices.Index(*list, name); i >= 0 {
		*list = slices.Delete(*list, i, i+1)
	} else {
		*list = append(*list, name)
	}
	t.prefs.gen++
	a.saveRefPrefs(t)
	a.applyHistoryFilter(t)
}

// filter is the history filter as typed; err says what is wrong with it.
func (hf *historyFilter) filter(now time.Time) (history.Filter, string) {
	f := history.Filter{
		Author: strings.TrimSpace(hf.author), Message: strings.TrimSpace(hf.message),
		Changed: hf.changed, Regexp: hf.regexp,
		FirstParent: hf.firstParent, NoMerges: hf.noMerges,
	}
	if p := strings.Trim(strings.TrimSpace(hf.path), "/"); p != "" {
		f.Paths = []string{p}
	}
	day := func(s string) (time.Time, bool) {
		d, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(s), now.Location())
		return d, err == nil
	}
	if s := strings.TrimSpace(hf.since); s != "" {
		d, ok := day(s)
		if !ok {
			return f, locales.HistoryDateFormat()
		}
		f.Since = d
	}
	if s := strings.TrimSpace(hf.until); s != "" {
		d, ok := day(s)
		if !ok {
			return f, locales.HistoryDateFormat()
		}
		// The day named is included.
		f.Until = d.AddDate(0, 0, 1).Add(-time.Second)
	}
	return f, ""
}

// applyHistoryFilter loads history again with the filter as typed and the
// branches shown.
func (a *App) applyHistoryFilter(t *tab) {
	hf := &t.hist.filter
	f, err := hf.filter(a.Now())
	hf.err = err
	var only []string
	if hf.only != "" {
		only = []string{hf.only}
	}
	if err != "" {
		// A field half typed keeps its error, and the filter applied
		// stays; the branches hidden or shown alone apply all the same.
		v := t.s.HistoryView()
		if !slices.Equal(v.Hidden, t.prefs.hidden) || !slices.Equal(v.Only, only) {
			v.Hidden, v.Only = slices.Clone(t.prefs.hidden), only
			t.s.SetHistoryView(v)
		}
		return
	}
	hf.applied, hf.typed = hf.key(), time.Time{}
	t.s.SetHistoryView(session.HistoryView{Filter: f, Hidden: slices.Clone(t.prefs.hidden), Only: only})
}

// active counts the filters applied, for the button that opens them:
// what is typed counts once it narrows history.
func (hf *historyFilter) active() int { return filtersIn(hf.applied) }

// filtersIn counts the filters set in key, as key writes them: the
// fields, then the regexp flag, which narrows nothing, then the others.
func filtersIn(key string) int {
	if key == "" {
		return 0
	}
	n := 0
	for i, f := range strings.Split(key, "\x00") {
		switch {
		case i < 7 && strings.TrimSpace(f) != "", i > 7 && f == "1":
			n++
		}
	}
	return n
}

// filterButton opens and closes the filter bar. It looks pressed while
// the bar is open, and in the accent while a filter narrows history,
// which the count says too.
func (a *App) filterButton(c *ui.Context, t *tab) {
	th := c.Theme()
	hf := &t.hist.filter
	label := locales.CommonFilter()
	n := hf.active()
	if n > 0 {
		label = locales.HistoryFilterCount(n)
	}
	b := ui.Button(c, label)
	if hf.open {
		b.Background(th.SurfacePressed)
	}
	if n > 0 {
		b.TextColor(th.Accent)
	}
	if b.Clicked() {
		hf.open = !hf.open
		if !hf.open && hf.pending() {
			// Closed before typing paused: what was typed applies, as
			// Escape applies it.
			a.applyHistoryFilter(t)
		}
	}
}

// findInMessages shows the commits whose message has text, in the
// filter bar, open.
func (a *App) findInMessages(t *tab, text string) {
	hf := &t.hist.filter
	hf.open, hf.message = true, text
	a.applyHistoryFilter(t)
}

// clearHistoryFilter shows every commit again, the bar left as it was.
func (a *App) clearHistoryFilter(t *tab) {
	hf := &t.hist.filter
	*hf = historyFilter{open: hf.open}
	a.applyHistoryFilter(t)
}

// filterBar is the history filter: what is typed in a field applies as
// typing pauses, or at once on Return; a checkbox or a branch applies at
// once. Escape closes it.
func (a *App) filterBar(c *ui.Context, t *tab) {
	hf := &t.hist.filter
	if !hf.open {
		return
	}
	th := c.Theme()
	apply := false
	field := func(v *string, label, placeholder, tip string) ui.Element {
		in := ui.TextInput(c, v).Placeholder(placeholder).Label(label).Tooltip(tip).MinWidth(leastFieldW)
		if in.Changed() {
			hf.typed = c.Now()
		}
		if in.Submitted() {
			apply = true
		}
		return in
	}
	check := func(v *bool, label string) {
		if ui.Checkbox(c, v, label).Changed() {
			apply = true
		}
	}
	bar := ui.Column(c).Padding(0, insetX, spaceS, insetX).Gap(spaceS).FillWidth()
	bar.OnShortcut(0, ui.KeyEscape, func() {
		hf.open = false
		t.hist.focusList = true
		if hf.pending() {
			// Closed before typing paused: what was typed applies, as
			// the bar no longer shows to apply it.
			a.applyHistoryFilter(t)
		}
	})
	bar.Children(func() {
		ui.Row(c).Gap(spaceM).AlignItems(ui.Center).FillWidth().Children(func() {
			label := locales.HistoryAllBranches()
			if hf.only != "" {
				label = redact.Control(shortRefName(hf.only))
			}
			ui.PopoverMenuButton(c, label, func(m *ui.Menu) {
				if m.Item(locales.HistoryAllBranches()).Radio(hf.only == "").Chosen() {
					hf.only, apply = "", true
				}
				m.Separator()
				for _, b := range a.branchesPinnedFirst(t) {
					if m.Item(redact.Control(strings.TrimPrefix(b, "refs/heads/"))).Radio(hf.only == b).Chosen() {
						hf.only, apply = b, true
					}
				}
			})
			for _, f := range []struct {
				v     *string
				label string
			}{{&hf.author, locales.HistoryFilterAuthor()}, {&hf.message, locales.CommonMessage()}, {&hf.path, locales.HistoryFilterPath()}} {
				c.Key(f.label)
				if f.v == &hf.author && hf.person != "" && hf.author == hf.personAuthor {
					if a.personChip(c, hf) {
						apply = true
					}
					continue
				}
				field(f.v, f.label, f.label, f.label).Grow(1)
			}
		})
		// The rest on one row, which wraps only where it must.
		ui.Row(c).GapX(spaceM).GapY(spaceS).Wrap().AlignItems(ui.Center).FillWidth().Children(func() {
			field(&hf.changed, locales.HistoryFilterChanged(), locales.HistoryFilterChanged(), locales.HistoryFilterChangedTip()).Grow(2)
			check(&hf.regexp, locales.HistoryFilterRegexp())
			field(&hf.since, locales.HistoryFilterSince(), locales.HistoryFilterSinceDay(), locales.HistoryFilterSince()).Grow(1)
			field(&hf.until, locales.HistoryFilterUntil(), locales.HistoryFilterUntilDay(), locales.HistoryFilterUntil()).Grow(1)
			check(&hf.firstParent, locales.HistoryFilterFirstParent())
			check(&hf.noMerges, locales.HistoryFilterNoMerges())
			if hf.err != "" {
				ui.Text(c, hf.err).FontSize(textSmall).TextColor(th.Danger)
			}
			ui.Spacer(c)
			if ui.Button(c, locales.HistoryFilterClear()).Disabled(hf.active() == 0 && filtersIn(hf.key()) == 0).Clicked() {
				open := hf.open
				*hf = historyFilter{open: open}
				apply = true
			}
		})
	})
	if !apply && !hf.typed.IsZero() {
		// Typing paused long enough: what was typed applies, and a date
		// left half typed says how dates are written.
		switch wait := filterPause - c.Now().Sub(hf.typed); {
		case !hf.pending():
			// Typed back to the filter applied: nothing to load again,
			// but an error said of what was typed before may be stale.
			hf.typed = time.Time{}
			_, hf.err = hf.filter(a.Now())
		case wait > 0:
			c.After(wait)
		default:
			hf.typed = time.Time{}
			apply = true
		}
	}
	if apply {
		a.applyHistoryFilter(t)
	}
}

// personChip stands in the Author field for the person history is
// filtered to: their name, their email in its tooltip, and an x that
// stops filtering by them. It reports whether the x was clicked.
func (a *App) personChip(c *ui.Context, hf *historyFilter) bool {
	th := c.Theme()
	removed := false
	ui.Row(c).Grow(1).MinWidth(leastFieldW).AlignItems(ui.Center).Children(func() {
		ui.Row(c).Gap(space2XS).AlignItems(ui.Center).Padding(space2XS, space2XS, space2XS, spaceS).Shrink(1).MinWidth(0).
			Radius(th.Radius).Background(th.Background).Border(1, th.Border).Tooltip(hf.author).
			Label(locales.HistoryFilterPerson(hf.person)).Children(func() {
			ui.Text(c, hf.person).FontSize(textSmall).SingleLine().Shrink(1).MinWidth(0)
			if closeButton(c, locales.HistoryFilterRemovePerson(hf.person)).Clicked() {
				removed = true
			}
		})
	})
	if removed {
		hf.author, hf.person, hf.personAuthor = "", "", ""
	}
	return removed
}

// shortRefName is a branch or tag name as the user knows it.
func shortRefName(name string) string {
	for _, p := range []string{"refs/heads/", "refs/remotes/", "refs/tags/"} {
		if s, ok := strings.CutPrefix(name, p); ok {
			return s
		}
	}
	return name
}

// branchesPinnedFirst are the local branches, pinned ones first in the
// order they were pinned.
func (a *App) branchesPinnedFirst(t *tab) []string {
	return t.sideRefs.build(t).heads
}

// sideRefs are the refs the sidebar lists, split by kind and pinned
// branches first, built again only when the refs or the pins change.
type sideRefs struct {
	refs                 *refs.List
	gen                  int
	heads, remotes, tags []string
	// hidden are the refs hidden from history, as a set for the rows
	// the sidebar shows each frame.
	hidden map[string]bool
}

func (sr *sideRefs) build(t *tab) *sideRefs {
	list := t.s.State.Refs
	if list == sr.refs && t.prefs.gen == sr.gen {
		return sr
	}
	*sr = sideRefs{refs: list, gen: t.prefs.gen, hidden: make(map[string]bool, len(t.prefs.hidden))}
	for _, h := range t.prefs.hidden {
		sr.hidden[h] = true
	}
	if list == nil {
		return sr
	}
	var pinned []string
	for i := range list.Len() {
		name := list.Name(i)
		switch {
		case strings.HasPrefix(name, "refs/heads/"):
			if slices.Contains(t.prefs.pinned, name) {
				pinned = append(pinned, name)
			} else {
				sr.heads = append(sr.heads, name)
			}
		case strings.HasPrefix(name, "refs/remotes/"):
			if !strings.HasSuffix(name, "/HEAD") {
				sr.remotes = append(sr.remotes, name)
			}
		case strings.HasPrefix(name, "refs/tags/"):
			sr.tags = append(sr.tags, name)
		}
	}
	slices.SortStableFunc(pinned, func(x, y string) int {
		return slices.Index(t.prefs.pinned, x) - slices.Index(t.prefs.pinned, y)
	})
	sr.heads = append(pinned, sr.heads...)
	return sr
}

// keepFetching has t's repository fetch in the background as the
// settings and the repository's own choice say.
func (a *App) keepFetching(t *tab) {
	a.loadRefPrefs(t)
	every := time.Duration(a.Settings.FetchEvery()) * time.Minute
	if !t.prefs.loaded || t.prefs.fetchOff {
		every = 0
	}
	// Why it stopped stays in the warning bar, with a way to resume.
	t.s.SetBackgroundFetch(every, nil)
}

// setEditor has the files of the repository at path open in the editor
// named, "" in the app's own choice, in its open tab and in the library.
func (a *App) setEditor(path, name string) {
	for _, t := range a.tabs {
		// path is the library's, the path resolved; the tab's may be
		// spelled another way.
		if t.is(cleanPath(path)) {
			t.prefs.editor = name
		}
	}
	a.libraryWrite(func(l *library.Library) error { return l.SetEditor(path, name) })
}

// setFetchOff keeps the repository at path from fetching in the
// background, or lets it again, in its open tab and in the library.
func (a *App) setFetchOff(path string, off bool) {
	for _, t := range a.tabs {
		// path is the library's, the path resolved; the tab's may be
		// spelled another way.
		if t.is(cleanPath(path)) {
			t.prefs.fetchOff = off
		}
	}
	a.libraryWrite(func(l *library.Library) error { return l.SetFetchOff(path, off) })
}
