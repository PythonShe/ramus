package ui

import (
	"github.com/egoist/mygo/ui"

	"ramus/internal/library"
)

type welcomeState struct {
	query  string
	list   ui.ListState
	chosen int
}

// welcomeView is the window with no repository open: open, clone, and
// the known repositories, searchable.
func (a *App) welcomeView(c *ui.Context) {
	th := c.Theme()
	ui.Column(c).Fill().Center().Children(func() {
		ui.Column(c).Width(560).Gap(16).Children(func() {
			ui.Text(c, "Ramus").FontSize(26).Bold()
			ui.Row(c).Gap(8).Children(func() {
				if ui.PrimaryButton(c, "Open Repository…").Clicked() {
					a.pickAndOpen()
				}
				if ui.Button(c, "Clone…").Clicked() {
					a.clone.open = true
				}
				if ui.Button(c, "New…").Clicked() {
					a.initAndOpen()
				}
			})
			if a.notice != "" {
				ui.Text(c, a.notice).TextColor(th.Danger)
			}
			if a.gitWarning != "" {
				ui.Text(c, a.gitWarning).TextColor(th.Warning)
			}
			repos := a.known()
			if a.Library == nil || (len(repos) == 0 && a.welcome.query == "") {
				return
			}
			ui.SearchField(c, &a.welcome.query).Placeholder("Find a repository")
			if len(repos) == 0 {
				ui.Text(c, "No repository matches").TextColor(th.TextMuted)
				return
			}
			a.welcome.list.Label = func(i int) string { return repos[i].Name }
			a.welcome.list.Selected = &a.welcome.chosen
			open := ""
			l := ui.List(c, &a.welcome.list, len(repos), func(i int) {
				r := repos[i]
				if ui.Column(c).Padding(6, 8).FillWidth().Children(func() {
					ui.Text(c, r.Name).SingleLine()
					ui.Text(c, r.Path).SingleLine().FontSize(12).TextColor(th.TextMuted)
				}).RowClicked() {
					open = r.Path
				}
			}).Height(360)
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
