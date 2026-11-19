package ui

import (
	"encoding/hex"
	"strings"
	"time"

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
	track session.Track
	// trackOf is the branch track was read for: until the branch
	// switched to is read, the button is not shown.
	trackOf string
	readFor *refs.List
	head    string
	loading bool
	// running is the fetch, pull or push the user started last, and
	// runKind which of them: the button turns while it runs.
	running *session.LogEntry
	runKind syncKind
	// fetched is when a fetch or pull the user started last ended well.
	fetched time.Time
}

// syncKind is what a write the user started does with a remote.
type syncKind uint8

const (
	syncNone syncKind = iota
	syncFetch
	syncPull
	syncPush
)

// syncButton does what the branch most needs to meet its upstream: pull
// what it is behind, push what it is ahead, or fetch to find out; a
// branch with no upstream is published to the remote. It shows that as
// glyphs and counts, and says it in words to assistive technology and in
// its tooltip. The chevron beside it opens every way to fetch, pull and
// push.
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
			s.track, s.trackOf = tr, tr.Branch
		})
	}
	if s.trackOf != head.Target {
		return
	}
	tr := s.track
	w := t.s.Where()
	ro := t.s.Queue == nil
	g := glyphs()
	var label, tip, upstream, pushTo string
	var act func()
	// pairs are the glyphs and counts shown, in order; publish shows a
	// word in place of a count.
	type pair struct {
		g    *ui.SVG
		text string
	}
	var pairs []pair
	switch {
	case tr.Upstream == "" || tr.Gone:
		opt, ok := pushOptions(t)
		if !ok || opt.Remote == "" {
			return
		}
		upstream = redact.Control(opt.Remote)
		pushTo = upstream
		label = locales.SyncPublish()
		tip = locales.SyncPublishTo(upstream)
		pairs = []pair{{g.cloudUp, locales.SyncPublishShort()}}
		opt.Branch = head.Target
		act = func() { a.submit(t, write.Push(w, opt), nil) }
	default:
		upstream = redact.Control(strings.TrimPrefix(tr.Upstream, "refs/remotes/"))
		// What a push sends, where it goes: a fork's branch to the fork,
		// all it is ahead of upstream until it is there once.
		pushTo = upstream
		ahead := tr.Ahead
		if tr.Push != "" {
			pushTo = redact.Control(strings.TrimPrefix(tr.Push, "refs/remotes/"))
			if !tr.PushGone {
				ahead = tr.PushAhead
			}
		}
		if tr.Behind > 0 {
			pairs = append(pairs, pair{g.arrowDown, locales.SyncCount(tr.Behind)})
		}
		if ahead > 0 {
			pairs = append(pairs, pair{g.arrowUp, locales.SyncCount(ahead)})
		}
		switch {
		case tr.Behind > 0:
			label = locales.SyncPullLabel(tr.Behind, upstream)
			if ahead > 0 {
				label = locales.SyncDivergedLabel(tr.Behind, ahead, upstream)
			}
			act = func() { a.syncPull(t, "") }
		case ahead > 0:
			label = locales.SyncPushLabel(ahead, pushTo)
			act = func() { a.submit(t, write.Push(w, write.PushOptions{Branch: head.Target}), nil) }
		default:
			label = locales.SyncFetch()
			act = func() {
				op, err := write.Fetch(w, "", t.refs.prune)
				a.submit(t, op, err)
			}
		}
		tip = label
		if len(pairs) == 0 {
			tip = locales.SyncUpToDate(upstream)
		}
	}
	busy := t.s.Fetching()
	kind := syncFetch
	mine := s.running != nil && s.running.Running
	if mine {
		busy, kind = true, s.runKind
	}
	if busy {
		switch kind {
		case syncPull:
			tip = locales.SyncPulling(upstream)
		case syncPush:
			tip = locales.SyncPushing(pushTo)
		default:
			tip = locales.SyncFetching(upstream)
		}
	}
	if at := a.lastFetched(t); !at.IsZero() {
		tip += "\n" + locales.SyncLastFetched(ago(time.Now(), at))
	}
	still := c.Preferences().ReduceMotion
	ui.Toolbar(c, func() {
		// Not Disabled while busy: dimmed, a turning glyph reads as off.
		b := ui.Button(c, "").Label(label).Tooltip(tip).Description(tip).Disabled(ro)
		b.Children(func() {
			ui.Row(c).Gap(spaceM).AlignItems(ui.Center).Children(func() {
				for i, p := range pairs {
					c.Key(i)
					ui.Row(c).Gap(spaceXS).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, p.g).FontSize(iconLarge)
						ui.Text(c, p.text).FontFeatures("tnum")
					})
				}
				if busy || len(pairs) == 0 {
					c.Key("refresh")
					ic := ui.Icon(c, g.refresh).FontSize(iconLarge)
					if busy && !still {
						ic.Rotate(ic.Loop("sync", time.Second, ui.Linear) * 360)
					}
				}
			})
		})
		// A pull or push asked for during a fetch the app started runs at
		// once, the fetch giving up its turn; a second fetch, or a second
		// of what the user started, would do nothing more.
		if b.Clicked() && !mine && !(busy && len(pairs) == 0) {
			act()
		}
		a.syncMenu(c, t, tr)
	}).Label(label)
}

// syncMenu is the chevron beside the sync button: every way to fetch,
// pull and push the branch checked out, as Branches offers them.
func (a *App) syncMenu(c *ui.Context, t *tab, tr session.Track) {
	label := locales.SyncMenu()
	b := ui.Button(c, "").Label(label).Tooltip(label).Disabled(t.s.Queue == nil).Padding(0, spaceXS)
	b.Children(func() { ui.Icon(c, glyphs().chevronDown).FontSize(iconSmall).TextColor(c.Theme().TextMuted) })
	b.Menu(func(m *ui.Menu) {
		w := t.s.Where()
		// The branch the menu is for: a push refuses once HEAD is on
		// another, which git would push in its place.
		headBranch := t.s.Seen().Branch
		var remotes []string
		if r := t.s.Repo; r != nil {
			remotes = r.Config().Subsections("remote", "url")
		}
		if m.Item(locales.RefsFetchAll()).Disabled(len(remotes) == 0).Chosen() {
			op, err := write.Fetch(w, "", t.refs.prune)
			a.submit(t, op, err)
		}
		if len(remotes) > 1 {
			for _, r := range remotes {
				if m.Item(locales.RefsFetchRemote(redact.Control(r))).Chosen() {
					op, err := write.Fetch(w, r, t.refs.prune)
					a.submit(t, op, err)
				}
			}
		}
		m.Separator()
		noUp := tr.Upstream == "" || tr.Gone
		for _, mode := range []struct {
			label string
			mode  write.PullMode
		}{
			{locales.RefsPullMerging(), write.PullMerge},
			{locales.RefsPullRebasing(), write.PullRebase},
			{locales.RefsPullFFOnly(), write.PullFFOnly},
		} {
			if m.Item(mode.label).Disabled(noUp).Chosen() {
				a.syncPull(t, mode.mode)
			}
		}
		m.Separator()
		if noUp {
			opt, ok := pushOptions(t)
			opt.Branch = headBranch
			if m.Item(locales.SyncPublish()).Disabled(!ok || opt.Remote == "").Chosen() {
				a.submit(t, write.Push(w, opt), nil)
			}
			return
		}
		if m.Item(locales.RefsPush()).Chosen() {
			a.submit(t, write.Push(w, write.PushOptions{Branch: headBranch}), nil)
		}
		if m.Item(locales.RefsForcePushItem()).Danger().Chosen() {
			up := strings.TrimPrefix(tr.Upstream, "refs/remotes/")
			if r := t.s.Repo; r != nil {
				// A fork's branch goes to its push remote, of its name.
				branch := strings.TrimPrefix(t.s.State.Head.Target, "refs/heads/")
				if pr := pushRemote(r.Config(), branch); pr != "" && !strings.HasPrefix(up, pr+"/") {
					up = pr + "/" + branch
				}
			}
			a.confirm = confirmState{open: true, title: locales.RefsForcePushTitle(redact.Control(up)),
				message: locales.RefsForcePushMessage(), button: locales.RefsForcePush(),
				run: func() { a.submit(t, write.Push(w, write.PushOptions{ForceWithLease: true, Branch: headBranch}), nil) }}
		}
	})
}

// syncPull pulls the branch checked out, taking in the upstream's commits
// as mode says, or as git's config says when mode is "".
func (a *App) syncPull(t *tab, mode write.PullMode) {
	if mode == "" {
		var cfg *config.Config
		if r := t.s.Repo; r != nil {
			cfg = r.Config()
		}
		mode = pullMode(cfg, t.s.State.Head.Target)
	}
	op, err := write.Pull(t.s.Where(), mode, t.s.LocalBranches(), t.s.Seen())
	a.submit(t, op, err)
}

// lastFetched is when tab t last fetched, quietly or as the user asked,
// as far as this session knows; zero for not since it opened.
func (a *App) lastFetched(t *tab) time.Time {
	at := t.s.Fetched()
	if t.sync.fetched.After(at) {
		at = t.sync.fetched
	}
	return at
}

// watchSync follows a write the user started, j for op, the last entry
// of the log of tab t: a fetch, pull or push of the branch checked out
// turns the sync button while it runs and says how it went once it ends
// well. Failures are the error bar's.
func (a *App) watchSync(t *tab, op write.Op, j *write.Job) {
	kind, remote := syncOf(op)
	if kind == syncNone || j == nil {
		return
	}
	s := &t.sync
	s.running, s.runKind = lastEntry(t), kind
	head := t.s.State.Head
	branch := strings.TrimPrefix(head.Target, "refs/heads/")
	old := hex.EncodeToString(head.OID)
	upstream := ""
	if st := t.s.State.Status; st != nil {
		upstream = st.Branch.Upstream
	}
	if kind == syncPush && remote == "" {
		remote = upstream
		if r := t.s.Repo; r != nil && branch != "" {
			if v := pushRemote(r.Config(), branch); v != "" {
				remote = v
			}
		}
	}
	target := head.Target
	t.s.Then(j, func(err error) {
		if err != nil {
			return
		}
		say := func(title string) { a.tabToast(t, ui.Toast{ID: "sync", Title: title}) }
		switch kind {
		case syncFetch:
			s.fetched = time.Now()
			if remote == "" {
				say(locales.SyncFetchedAll())
			} else {
				say(locales.SyncFetched(redact.Control(remote)))
			}
		case syncPull:
			s.fetched = time.Now()
			t.s.CountPulled(target, old, func(n int) {
				if n == 0 {
					say(locales.SyncAlreadyUpToDate(redact.Control(upstream)))
				} else {
					say(locales.SyncPulled(n, redact.Control(branch)))
				}
			})
		case syncPush:
			say(locales.SyncPushed(redact.Control(branch), redact.Control(remote)))
		}
	})
}

// pushRemote is the remote git pushes branch (a short name) to when no
// remote is named, as git push picks it: branch.<name>.pushRemote, then
// remote.pushDefault, then branch.<name>.remote; "" for none.
func pushRemote(cfg *config.Config, branch string) string {
	for _, key := range []string{"branch." + branch + ".pushRemote", "remote.pushDefault", "branch." + branch + ".remote"} {
		if v, ok := cfg.Get(key); ok && v != "" {
			return v
		}
	}
	return ""
}

// syncOf tells a write that fetches remotes, pulls or pushes the branch
// checked out, as write.Fetch, write.Pull and write.Push make them, and
// the remote it names, "" for every remote or the upstream's; syncNone
// for any other, as the fetch of a pull request or the push of a tag.
func syncOf(op write.Op) (syncKind, string) {
	args := op.Cmd.Args
	if len(args) == 0 {
		return syncNone, ""
	}
	after := func(mark string) string {
		for i, a := range args {
			if a == mark && i+1 < len(args) {
				return args[i+1]
			}
		}
		return ""
	}
	for _, a := range args[1:] {
		if strings.HasPrefix(a, "--refmap") || strings.Contains(a, ":") || strings.HasPrefix(a, "refs/") {
			return syncNone, ""
		}
	}
	switch args[0] {
	case "fetch":
		return syncFetch, after("--end-of-options")
	case "pull":
		return syncPull, ""
	case "push":
		if len(op.More) > 0 {
			return syncNone, ""
		}
		return syncPush, after("--")
	}
	return syncNone, ""
}
