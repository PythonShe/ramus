package ui

import (
	"strings"
	"time"
)

// showView tells the session which view of t is shown, and lets go of
// what the view left was still reading: the session cancels those reads,
// and the view reads again when it is shown again.
func (a *App) showView(t *tab) {
	name := viewName(t)
	if t.shown == name {
		return
	}
	a.leaveView(t, t.shown)
	t.shown = name
	t.s.SetView(name)
}

// viewName names the view of t shown: its sidebar item's, or "review"
// for the changes shown to review.
func viewName(t *tab) string {
	if t.sel == "changes" && t.cv.mode == modeReview {
		return "review"
	}
	return t.sel
}

// hideTab is showView for a tab put in the background.
func (a *App) hideTab(t *tab) {
	a.leaveView(t, t.shown)
	t.shown = ""
	t.s.SetView("")
}

// leaveView drops the loads view name had in flight, so it asks again.
func (a *App) leaveView(t *tab, name string) {
	if name == "refs" || strings.HasPrefix(name, "refs/") {
		t.pulls.loading = false
	}
	if t.locks.loading {
		// The read of the locks runs as the view does, which the file
		// menus of any view start: it stops, and is read again.
		t.locks.loading, t.locks.listed = false, -1
	}
	switch name {
	case "":
	case "history":
		leaveDetail(&t.hist.detail)
	case "reflog":
		leaveDetail(&t.reflog.detail)
		if rs := &t.reflog; rs.lostLoading {
			rs.lostLoading, rs.lostSearched = false, false
		}
	case "stashes":
		leaveDetail(&t.stash.detail)
	case sidePulls:
		// Its reads stop: what was read stays, and is read again.
		ps := &t.pr
		if ps.listing {
			ps.listing, ps.listAt = false, time.Time{}
		}
		if rv := ps.rv; rv != nil {
			if rv.loading {
				rv.loading, rv.read = false, false
			}
			if rv.cancelCmp != nil || rv.fetching {
				// Read again when shown, which fetches and compares again:
				// a fetch on its way goes on to nothing, or it would
				// compare under the view shown next, whose leaving stops
				// it for good.
				if rv.cancelCmp != nil {
					rv.cancelCmp()
				}
				rv.fetchN++
				rv.cancelCmp, rv.fetching, rv.fetchedFor = nil, false, ""
				rv.reread()
			}
			if rv.patchLoading {
				rv.patchFor, rv.patchLoading = "", false
			}
		}
	case sideLocks:
		// Its read stops, and others may have locked or unlocked files
		// meanwhile: it is read again when shown, the list kept till then.
		t.locks.loading, t.locks.listed = false, -1
	case "compare":
		cc := &t.cmp
		// The merge base read stops with the view: asked again when shown.
		cc.basing = false
		if cc.loading {
			cc.cancel()
			cc.loading = false
			// Listed but not counted: read again when shown.
			if cc.d != nil && !cc.d.Done {
				cc.d = nil
			}
		}
		if cc.cv.patchLoading {
			cc.cv.patchFor, cc.cv.patchLoading = "", false
		}
	case "file":
		fs := &t.file
		leaveDetail(&fs.detail)
		if fs.logLoading {
			fs.logKey, fs.logLoading = "", false
		}
		fs.dropBlame()
	case "review":
		t.rev.leave()
		t.cv.leaveCount()
	case "stats":
		ss := &t.stats
		if ss.loading {
			ss.asked, ss.loading = nil, false
		}
		for _, ts := range []*treemapState{&ss.sizes, &ss.changes} {
			if ts.loading {
				*ts = treemapState{}
			}
		}
	default: // the changes view and what it shows beside the list
		t.cv.leaveCount()
		if d := &t.cv.diff; d.loading {
			d.key, d.loading = diffKey{}, false
		}
		if ms := &t.merge; ms.loading {
			ms.path, ms.loading = "", false
		}
	}
}

// leaveDetail drops a commit still being read, its patch, and its pull
// request still being asked for.
func leaveDetail(cv *commitView) {
	cv.stopPull()
	if cv.d == nil || !cv.d.Done {
		if cv.cancel != nil {
			cv.cancel()
		}
		cv.id = nil
	}
	if cv.patchLoading {
		cv.patchFor, cv.patchLoading = "", false
	}
}
