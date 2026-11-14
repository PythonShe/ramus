package ui

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/history"
	"ramus/internal/git/redact"
	"ramus/internal/git/refs"
	"ramus/internal/session"
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
}

// refPrefs are a repository's pinned branches and the refs hidden from
// its history, as the library keeps them.
type refPrefs struct {
	loaded         bool
	loading        bool
	pinned, hidden []string
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
	go func() {
		pinned, hidden := lib.RefPrefs(path)
		update(func() {
			rp.loading, rp.loaded = false, true
			rp.pinned, rp.hidden = pinned, hidden
			rp.gen++
			if len(hidden) > 0 {
				a.applyHistoryFilter(t)
			}
		})
	}()
}

// saveRefPrefs keeps the pinned and hidden refs off the main thread.
func (a *App) saveRefPrefs(t *tab) {
	if a.Library == nil {
		return
	}
	lib, path, update := a.Library, t.s.Path, a.Update
	pinned, hidden := slices.Clone(t.prefs.pinned), slices.Clone(t.prefs.hidden)
	go func() {
		if err := lib.SetRefPrefs(path, pinned, hidden); err != nil {
			update(func() { a.notice = errText(err) })
		}
	}()
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
			return f, "Dates are written as 2024-01-31"
		}
		f.Since = d
	}
	if s := strings.TrimSpace(hf.until); s != "" {
		d, ok := day(s)
		if !ok {
			return f, "Dates are written as 2024-01-31"
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
	if err != "" {
		return
	}
	v := session.HistoryView{Filter: f, Hidden: slices.Clone(t.prefs.hidden)}
	if hf.only != "" {
		v.Only = []string{hf.only}
	}
	t.s.SetHistoryView(v)
}

// active counts the filters set, for the button that opens them.
func (hf *historyFilter) active() int {
	n := 0
	for _, s := range []string{hf.author, hf.message, hf.changed, hf.path, hf.since, hf.until, hf.only} {
		if strings.TrimSpace(s) != "" {
			n++
		}
	}
	for _, b := range []bool{hf.firstParent, hf.noMerges} {
		if b {
			n++
		}
	}
	return n
}

// filterButton opens and closes the filter bar.
func (a *App) filterButton(c *ui.Context, t *tab) {
	hf := &t.hist.filter
	label := "Filter"
	if n := hf.active(); n > 0 {
		label += " (" + strconv.Itoa(n) + ")"
	}
	if ui.Button(c, label).Clicked() {
		hf.open = !hf.open
	}
}

// filterBar is the history filter: Return in a field applies it, a
// checkbox or a branch applies at once.
func (a *App) filterBar(c *ui.Context, t *tab) {
	hf := &t.hist.filter
	if !hf.open {
		return
	}
	th := c.Theme()
	apply := false
	field := func(v *string, label string, grow float32) {
		if ui.TextInput(c, v).Placeholder(label).Label(label).Grow(grow).Submitted() {
			apply = true
		}
	}
	check := func(v *bool, label string) {
		if ui.Checkbox(c, v, label).Changed() {
			apply = true
		}
	}
	ui.Column(c).Padding(0, 8, 6, 8).Gap(6).FillWidth().Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).FillWidth().Children(func() {
			label := "All Branches"
			if hf.only != "" {
				label = redact.Control(shortRefName(hf.only))
			}
			ui.PopoverMenuButton(c, label, func(m *ui.Menu) {
				if m.Item("All Branches").Radio(hf.only == "").Chosen() {
					hf.only, apply = "", true
				}
				m.Separator()
				for _, b := range a.branchesPinnedFirst(t) {
					if m.Item(redact.Control(strings.TrimPrefix(b, "refs/heads/"))).Radio(hf.only == b).Chosen() {
						hf.only, apply = b, true
					}
				}
			})
			field(&hf.author, "Author", 1)
			field(&hf.message, "Message", 1)
			field(&hf.path, "Path", 1)
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).FillWidth().Children(func() {
			field(&hf.changed, "Changed text", 2)
			check(&hf.regexp, "Regular expression")
			field(&hf.since, "Since", 1)
			field(&hf.until, "Until", 1)
		})
		ui.Row(c).Gap(12).AlignItems(ui.Center).FillWidth().Children(func() {
			check(&hf.firstParent, "First parent only")
			check(&hf.noMerges, "Hide merges")
			if hf.err != "" {
				ui.Text(c, hf.err).FontSize(12).TextColor(th.Danger)
			}
			ui.Spacer(c)
			if ui.Button(c, "Clear").Disabled(hf.active() == 0).Clicked() {
				open := hf.open
				*hf = historyFilter{open: open}
				apply = true
			}
		})
	})
	if apply {
		a.applyHistoryFilter(t)
	}
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
}

func (sr *sideRefs) build(t *tab) *sideRefs {
	list := t.s.State.Refs
	if list == sr.refs && t.prefs.gen == sr.gen {
		return sr
	}
	*sr = sideRefs{refs: list, gen: t.prefs.gen}
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
