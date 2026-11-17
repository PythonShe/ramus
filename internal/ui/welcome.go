package ui

import (
	"slices"

	"github.com/egoist/mygo/ui"

	"ramus/internal/library"
	"ramus/locales"
)

type welcomeState struct {
	query  string
	list   ui.ListState
	chosen int
}

// welcomeView is the window with no repository open: open, clone, and
// the known repositories, searchable.
func (a *App) welcomeView(c *ui.Context) {
	ui.Column(c).Fill().Children(func() {
		// The app's button where the tab bar has it once a repository is
		// open.
		ui.Row(c).FillWidth().Padding(spaceXS, insetX).AlignItems(ui.Center).Children(func() {
			ui.Spacer(c)
			a.appButton(c)
		})
		a.welcomeBody(c)
	})
}

func (a *App) welcomeBody(c *ui.Context) {
	th := c.Theme()
	ui.Column(c).Grow(1).FillWidth().Center().Children(func() {
		ui.Column(c).Width(560).Gap(spaceXL).Children(func() {
			ui.Text(c, locales.AboutName()).FontSize(textDisplay).Bold()
			ui.Row(c).Gap(spaceM).Children(func() {
				if ui.PrimaryButton(c, locales.WelcomeOpen()).Clicked() {
					a.pickAndOpen()
				}
				if ui.Button(c, locales.WelcomeClone()).Clicked() {
					a.clone.open = true
				}
				if ui.Button(c, locales.WelcomeNew()).Clicked() {
					a.initAndOpen()
				}
			})
			// The messages of the error bar a repository has, in its look.
			note := func(n noteLine) {
				a.noteRow(c, n).Radius(th.Radius)
			}
			for i, n := range a.windowNotes {
				c.Key(windowKey(i))
				note(noteLine{text: oneLine(n), full: n, color: th.Danger, dismiss: func() { a.dropWindowNote(n) }})
			}
			if a.notice != "" && a.noticeBy.tab == nil && !slices.Contains(a.windowNotes, a.notice) {
				c.Key("notice")
				note(noteLine{text: oneLine(a.notice), full: a.notice, color: th.Danger, dismiss: a.dropNotice})
			}
			if a.gitWarning != "" {
				c.Key("git")
				note(noteLine{text: a.gitWarning, full: a.gitWarning, color: th.Warning, glyph: glyphs().warning, dismiss: func() { a.gitWarning = "" }})
			}
			repos := a.known()
			if a.Library == nil || (len(repos) == 0 && a.welcome.query == "") {
				return
			}
			w := &a.welcome
			// Typing searches at once, unless a dialog has the keys.
			f := ui.SearchField(c, &w.query).Label(locales.WelcomeFind()).Placeholder(locales.WelcomeFind()).Bind(&a.find.welcome)
			if !a.DialogOpen() {
				f.AutoFocus()
			}
			if f.Changed() {
				w.chosen = 0
			}
			if len(repos) == 0 {
				ui.Text(c, locales.WelcomeNoMatch()).TextColor(th.TextMuted)
				return
			}
			w.chosen = min(max(w.chosen, 0), len(repos)-1)
			switch {
			case f.Shortcut(0, ui.KeyDown):
				w.chosen = min(w.chosen+1, len(repos)-1)
				w.list.ScrollIntoView(w.chosen)
			case f.Shortcut(0, ui.KeyUp):
				w.chosen = max(w.chosen-1, 0)
				w.list.ScrollIntoView(w.chosen)
			}
			open := ""
			if f.Submitted() {
				open = repos[w.chosen].Path
			}
			w.list.Label = func(i int) string { return locales.WelcomeRepoLabel(repos[i].Name, repos[i].Path) }
			w.list.Selected = &w.chosen
			l := ui.List(c, &w.list, len(repos)).Rows(func(row ui.ListRow) {
				r := repos[row.Index]
				if ui.Column(c).Padding(rowTallPadY, insetX).FillWidth().Children(func() {
					ui.Text(c, r.Name).SingleLine()
					ui.Text(c, r.Path).SingleLine().FontSize(textSmall).TextColor(rowInk(row).Muted)
				}).RowClicked() {
					open = r.Path
				}
			}).Label(locales.WelcomeRecent()).Height(360)
			if l.Submitted() && a.welcome.chosen >= 0 && a.welcome.chosen < len(repos) {
				open = repos[a.welcome.chosen].Path
			}
			if open != "" {
				a.Open(open)
			}
		})
	})
}

// known lists the library's repositories matching the search, favourites
// and the most recent first.
func (a *App) known() []library.Repo {
	if a.Library == nil {
		return nil
	}
	if a.welcome.query != "" {
		return a.Library.Search(a.welcome.query)
	}
	fav := a.Library.Favourites()
	out := fav
	for _, r := range a.Library.Recent(a.Settings.WithDefaults().RecentLimit) {
		if !r.Favourite {
			out = append(out, r)
		}
	}
	return out
}
