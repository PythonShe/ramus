package ui

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/config"
	"ramus/internal/git/redact"
	"ramus/internal/git/refs"
	"ramus/internal/git/write"
	"ramus/internal/session"
	"ramus/locales"
)

// syncState is how the branch checked out stands with its upstream, for
// the button beside the branch picker, read again as the refs change.
type syncState struct {
	track   session.Track
	readFor *refs.List
	head    string
	loading bool
}

// syncButton does what the branch most needs to meet its upstream: pull
// what it is behind, push what it is ahead, or fetch to find out; a
// branch with no upstream is published to the remote.
func (a *App) syncButton(c *ui.Context, t *tab) {
	s := &t.sync
	head := t.s.State.Head
	if head.Detached || head.Unborn || len(head.OID) == 0 {
		return
	}
	if !s.loading && (s.readFor != t.s.State.Refs || s.head != head.Target) {
		s.readFor, s.head, s.loading = t.s.State.Refs, head.Target, true
		t.s.LoadTrack(func(tr session.Track) {
			s.loading = false
			s.track = tr
		})
	}
	if s.head != head.Target {
		return
	}
	tr := s.track
	w := t.s.Where()
	ro := t.s.Queue == nil
	var label, tip string
	var act func()
	switch {
	case tr.Upstream == "" || tr.Gone:
		opt, ok := pushOptions(t)
		if !ok || opt.Remote == "" {
			return
		}
		label = locales.SyncPublish()
		tip = locales.SyncPublishTo(redact.Control(opt.Remote))
		act = func() { a.submit(t, write.Push(w, opt), nil) }
	default:
		upstream := redact.Control(strings.TrimPrefix(tr.Upstream, "refs/remotes/"))
		tip = locales.WorktreeAheadBehind(tr.Ahead, tr.Behind, upstream)
		switch {
		case tr.Behind > 0:
			label = locales.SyncPull(tr.Behind)
			act = func() {
				var cfg *config.Config
				if r := t.s.Repo; r != nil {
					cfg = r.Config()
				}
				op, err := write.Pull(w, pullMode(cfg, head.Target), t.s.LocalBranches(), t.s.Seen())
				a.submit(t, op, err)
			}
		case tr.Ahead > 0:
			label = locales.SyncPush(tr.Ahead)
			act = func() { a.submit(t, write.Push(w, write.PushOptions{}), nil) }
		default:
			label = locales.SyncFetch()
			act = func() {
				op, err := write.Fetch(w, "", t.refs.prune)
				a.submit(t, op, err)
			}
		}
	}
	ui.Toolbar(c, func() {
		if ui.Button(c, label).Description(tip).Tooltip(tip).Disabled(ro || t.s.Fetching()).Clicked() {
			act()
		}
	}).Label(label)
}
