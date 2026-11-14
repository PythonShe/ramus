package ui

import (
	"slices"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/repo"
	"ramus/internal/issuelink"
)

// tabLinks are the issue link rules in use in a tab: the repository's own,
// then those its remote's host implies.
type tabLinks struct {
	set *issuelink.Set
	// own and remote are the rules the set was built from.
	own, remote []issuelink.Rule
	// For is the open repository they were read for; trusting it opens it
	// again.
	For *repo.Repository
}

// loadLinks reads a tab's rules off the main thread, as the library
// resolves paths on disk, once per open repository.
func (a *App) loadLinks(t *tab) {
	r := t.s.Repo
	if r == nil || t.links.For == r {
		return
	}
	t.links.For = r
	lib, path, update := a.Library, t.s.Path, a.Update
	go func() {
		var own []issuelink.Rule
		if lib != nil {
			own = lib.IssueLinks(path)
		}
		remote := issuelink.FromRemote(remoteURL(r))
		update(func() {
			if t.links.For == r {
				t.links.own, t.links.remote = own, remote
				t.links.set = issuelink.Compile(append(slices.Clone(own), remote...))
			}
		})
	}()
}

// remoteURL is the URL of origin, or of the first remote when there is no
// origin.
func remoteURL(r *repo.Repository) string {
	if u, ok := r.Config().Get("remote.origin.url"); ok {
		return u
	}
	for _, name := range r.Config().Subsections("remote", "url") {
		if u, ok := r.Config().Get("remote." + name + ".url"); ok {
			return u
		}
	}
	return ""
}

// linkedText shows text with its issue references as links, selectable.
func linkedText(c *ui.Context, text string, set *issuelink.Set) ui.Element {
	ms := set.Find(text)
	if len(ms) == 0 {
		return ui.Text(c, text).Selectable()
	}
	return ui.RichText(c).Selectable().Children(func() {
		at := 0
		for _, m := range ms {
			if m.Start > at {
				ui.Text(c, text[at:m.Start])
			}
			ui.Link(c, text[m.Start:m.End], m.URL).Tooltip(m.URL)
			at = m.End
		}
		if at < len(text) {
			ui.Text(c, text[at:])
		}
	})
}

// linksDialog edits a repository's own issue link rules.
type linksDialog struct {
	open  bool
	t     *tab
	rules []issuelink.Rule
	err   string
}

func (a *App) openLinks(t *tab) {
	d := &a.links
	d.open, d.t, d.err = true, t, ""
	d.rules = slices.Clone(t.links.own)
	if len(d.rules) == 0 {
		d.rules = []issuelink.Rule{{}}
	}
}

func (a *App) linksView(c *ui.Context) {
	d := &a.links
	if !d.open || d.t == nil {
		return
	}
	th := c.Theme()
	t := d.t
	ui.Modal(c, &d.open, func() {
		ui.Column(c).Width(640).Gap(12).Padding(20).Children(func() {
			ui.Text(c, "Issue Links").Bold()
			for i := range d.rules {
				r := &d.rules[i]
				ui.Row(c).Key(i).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.TextInput(c, &r.Pattern).Placeholder(`\bPROJ-(\d+)\b`).Label("Pattern").Font("monospace").Grow(1)
					ui.TextInput(c, &r.URL).Placeholder("https://tracker.example.com/browse/PROJ-$1").Label("Link").Grow(2)
					if ui.Button(c, "Remove").Clicked() {
						d.rules = slices.Delete(d.rules, i, i+1)
					}
				})
				if i >= len(d.rules) {
					break
				}
				if r.Pattern != "" || r.URL != "" {
					if err := issuelink.Check(*r); err != nil {
						ui.Text(c, errText(err)).FontSize(12).TextColor(th.Danger)
					}
				}
			}
			if ui.Button(c, "Add Rule").Clicked() {
				d.rules = append(d.rules, issuelink.Rule{})
			}
			for _, r := range t.links.remote {
				ui.Row(c).Gap(8).Children(func() {
					ui.Text(c, r.Pattern).Font("monospace").FontSize(12).TextColor(th.TextMuted).Grow(1)
					ui.Text(c, r.URL).FontSize(12).TextColor(th.TextMuted).Grow(2)
				})
			}
			if d.err != "" {
				ui.Text(c, d.err).TextColor(th.Danger)
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					d.open = false
				}
				if ui.PrimaryButton(c, "Save").Clicked() {
					a.saveLinks()
				}
			})
		})
	})
}

func (a *App) saveLinks() {
	d := &a.links
	var rules []issuelink.Rule
	for _, r := range d.rules {
		if r.Pattern == "" && r.URL == "" {
			continue
		}
		if err := issuelink.Check(r); err != nil {
			d.err = errText(err)
			return
		}
		rules = append(rules, r)
	}
	t, lib, update := d.t, a.Library, a.Update
	d.open = false
	go func() {
		err := lib.SetIssueLinks(t.s.Path, rules)
		update(func() {
			if err != nil {
				a.notice = errText(err)
				return
			}
			// Read again with the remote's rules.
			t.links.For = nil
		})
	}()
}
