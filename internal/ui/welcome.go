package ui

import (
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
			ui.Text(c, "Ramus").FontSize(textDisplay).Bold()
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
			if a.notice != "" {
				ui.Row(c).Gap(spaceM).AlignItems(ui.Center).Children(func() {
					ui.Text(c, a.notice).TextColor(th.Danger).Shrink(1)
					if closeButton(c, locales.RepoDismiss()).Clicked() {
						a.dropNotice()
					}
				})
			}
			if a.gitWarning != "" {
				ui.Text(c, a.gitWarning).TextColor(th.Warning)
			}
			repos := a.known()
			if a.Library == nil || (len(repos) == 0 && a.welcome.query == "") {
				return
			}
			ui.SearchField(c, &a.welcome.query).Label(locales.WelcomeFind()).Placeholder(locales.WelcomeFind())
			if len(repos) == 0 {
				ui.Text(c, locales.WelcomeNoMatch()).TextColor(th.TextMuted)
				return
			}
			a.welcome.list.Label = func(i int) string { return repos[i].Name }
			a.welcome.list.Selected = &a.welcome.chosen
			open := ""
			l := ui.List(c, &a.welcome.list, len(repos), func(i int) {
				r := repos[i]
				if ui.Column(c).Padding(rowTallPadY, insetX).FillWidth().Children(func() {
					ui.Text(c, r.Name).SingleLine()
					ui.Text(c, r.Path).SingleLine().FontSize(textSmall).TextColor(th.TextMuted)
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
