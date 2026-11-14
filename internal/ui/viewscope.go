package ui

// showView tells the session which view of t is shown, and lets go of
// what the view left was still reading: the session cancels those reads,
// and the view reads again when it is shown again.
func (a *App) showView(t *tab) {
	if t.shown == t.sel {
		return
	}
	a.leaveView(t, t.shown)
	t.shown = t.sel
	t.s.SetView(t.sel)
}

// hideTab is showView for a tab put in the background.
func (a *App) hideTab(t *tab) {
	a.leaveView(t, t.shown)
	t.shown = ""
	t.s.SetView("")
}

// leaveView drops the loads view name had in flight, so it asks again.
func (a *App) leaveView(t *tab, name string) {
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
	case "file":
		fs := &t.file
		leaveDetail(&fs.detail)
		if fs.logLoading {
			fs.logKey, fs.logLoading = "", false
		}
		if !fs.blameDone {
			if fs.cancel != nil {
				fs.cancel()
			}
			fs.blameKey = ""
		}
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
		if d := &t.cv.diff; d.loading {
			d.key, d.loading = diffKey{}, false
		}
		if ms := &t.merge; ms.loading {
			ms.path, ms.loading = "", false
		}
	}
}

// leaveDetail drops a commit still being read, and its patch.
func leaveDetail(cv *commitView) {
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
