package ui

import (
	"encoding/hex"
	"net/url"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/redact"
	"ramus/locales"
)

// A tab's pages go through a router, which keeps them for Back and
// Forward: the toolbar's buttons, Cmd+[ and Cmd+] (Alt+Left and
// Alt+Right elsewhere) and a mouse's side buttons. The tab's own state
// stays what chooses the page; each frame the router hears of a page the
// tab went to, and the tab of one the router went back or forward to.

// tabNav is a tab's history of pages.
type tabNav struct {
	router *ui.Router
	// at is the location the router and the tab last agreed on.
	at string
	// jump is set when a commit was gone to, rather than moved to in the
	// list: it is a page of its own, which Back returns from.
	jump bool
}

// location is the page tab t shows: its sidebar item, the commit chosen
// in the history, the file shown.
func (t *tab) location() string {
	q := url.Values{}
	switch t.sel {
	case "history":
		if id := t.hist.chosenID; id != nil {
			q.Set("commit", hex.EncodeToString(id))
		}
	case "file":
		q.Set("path", t.file.path)
		if t.file.blame != 0 {
			q.Set("blame", "1")
		}
	}
	loc := "/" + url.PathEscape(t.sel)
	if len(q) > 0 {
		loc += "?" + q.Encode()
	}
	return loc
}

// syncNav brings tab t and its router to the same page: the one the router
// went back or forward to, else the one the tab went to. A section or a
// commit gone to is a new page; a commit moved to in the list takes the
// place of the one before.
func (a *App) syncNav(t *tab) {
	n := &t.nav
	jump := n.jump
	n.jump = false
	if n.router == nil {
		n.router = ui.NewRouter(t.location())
		// Pages come at once: a section built twice as one fades into
		// the other would load twice.
		n.router.Transition = ui.TransitionNone
		n.at = n.router.Location()
		return
	}
	r := n.router
	if loc := r.Location(); loc != n.at {
		a.goTo(t, loc)
	} else if loc := t.location(); loc != n.at {
		if jump || pagePath(loc) != pagePath(n.at) {
			r.Push(loc)
		} else {
			r.Replace(loc)
		}
	}
	n.at = r.Location()
}

func pagePath(loc string) string { p, _, _ := strings.Cut(loc, "?"); return p }

// goTo shows the page at loc in tab t.
func (a *App) goTo(t *tab, loc string) {
	u, err := url.Parse(loc)
	if err != nil {
		return
	}
	q := u.Query()
	sel := strings.TrimPrefix(u.Path, "/")
	switch sel {
	case "history":
		if id, err := hex.DecodeString(q.Get("commit")); err == nil && len(id) > 0 {
			a.showCommit(t, id)
		}
	case "file":
		a.showFile(t, q.Get("path"), q.Get("blame") != "")
	}
	t.sel = sel
}

// pageTitle names the page tab t shows, as Back and Forward list it.
func (a *App) pageTitle(t *tab) string {
	switch {
	case t.sel == sideChanges:
		return locales.RepoSectionChanges()
	case t.sel == sideCompare:
		return locales.RepoSectionComparison()
	case t.sel == "file":
		return t.file.path
	case t.sel == "history" && t.hist.chosenID != nil:
		if d := t.hist.detail.d; d != nil && string(d.OID) == string(t.hist.chosenID) {
			subject, _ := messageParts(d)
			// Back and Forward list it: a bidi override in it could
			// make it read as another commit's.
			return redact.Control(string(subject))
		}
		return hex.EncodeToString(t.hist.chosenID)[:10]
	case t.sel == "history":
		return locales.RepoSectionHistory()
	case strings.HasPrefix(t.sel, "refs/"):
		return shortRef(t.sel)
	}
	return partName(t.sel)
}

func shortRef(name string) string {
	for _, p := range []string{"refs/heads/", "refs/tags/", "refs/remotes/"} {
		if s, ok := strings.CutPrefix(name, p); ok {
			return s
		}
	}
	return name
}

// navBar holds what goes through the pages of tab t: the sidebar's
// toggle, Back and Forward.
func (a *App) navBar(c *ui.Context, t *tab) {
	// In a toolbar the buttons have no face until hovered.
	ui.Toolbar(c, func() {
		label := locales.HidingHideSidebar()
		if a.Settings.NoSidebar {
			label = locales.HidingShowSidebar()
		}
		if iconButton(c, glyphs().sidebar, label).Clicked() {
			a.setSidebar(a.Settings.NoSidebar)
		}
		if r := t.nav.router; r != nil {
			ui.BackButton(c, r)
			ui.ForwardButton(c, r)
		}
	}).Label(locales.NavToolbar())
}
