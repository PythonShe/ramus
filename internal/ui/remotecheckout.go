package ui

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/redact"
	"ramus/internal/git/write"
	"ramus/internal/session"
	"ramus/locales"
)

// remoteCheckout is the dialog for checking out a remote branch whose
// name a local branch already has: switch to that one, bring it up to
// the remote one, or make another.
type remoteCheckout struct {
	open bool
	t    *tab
	// remote is the remote branch's full name; local the branch of the
	// same name.
	remote string
	local  session.BranchInfo
	// unread is set while the branches' details, which say how local
	// stands to remote, are read; early on a dialog opened before then,
	// which keeps the shape it opened with as they land.
	unread, early bool
	// other is the name of a new branch to make instead.
	other string
	err   string
}

// elsewhere is the dialog for a branch checked out in another worktree,
// which git will not check out here too.
type elsewhere struct {
	open           bool
	branch, folder string
}

// localName is the branch a remote branch is checked out as:
// origin/topic/x makes topic/x.
func localName(remote string) string {
	_, name, _ := strings.Cut(strings.TrimPrefix(remote, "refs/remotes/"), "/")
	return name
}

// checkOutRemote checks out remote branch remote as a local branch of
// its name, asking first when one already exists.
func (a *App) checkOutRemote(t *tab, remote string) {
	name := localName(remote)
	ref := "refs/heads/" + name
	var local *session.BranchInfo
	unread := false
	if info := t.refs.info; info != nil {
		for i, b := range info.Branches {
			if b.Name == ref {
				local = &info.Branches[i]
			}
		}
	} else if slices.Contains(t.s.LocalBranches(), ref) {
		// Its upstream, its counts and its worktree come with the details:
		// the dialog waits for them before it offers a reset.
		local, unread = &session.BranchInfo{Name: ref}, true
		a.loadRefs(t)
	}
	switch {
	case local == nil:
		op, err := write.CheckoutRemote(t.s.Where(), remote, name, "", false, write.CarryChanges, t.s.Seen())
		a.submit(t, op, err)
	case local.Worktree != "":
		a.elsewhereUI = elsewhere{open: true, branch: ref, folder: local.Worktree}
	default:
		a.remoteUI = remoteCheckout{open: true, t: t, remote: remote, local: *local, unread: unread, early: unread,
			other: strings.ReplaceAll(strings.TrimPrefix(remote, "refs/remotes/"), "/", "-")}
	}
}

// relation says where the local branch stands to the remote one.
func (d *remoteCheckout) relation() (text string, ff, behind bool) {
	short := strings.TrimPrefix(d.remote, "refs/remotes/")
	name := strings.TrimPrefix(d.local.Name, "refs/heads/")
	b := d.local
	switch {
	case d.unread:
		return locales.RemoteCheckoutReading(name, short), false, false
	case b.Worktree != "":
		// Only a dialog opened before the details were read gets here: one
		// opened after explains the other worktree instead.
		return locales.RemoteCheckoutElsewhereMessage(b.Worktree), false, false
	case b.Upstream != d.remote:
		return locales.RemoteCheckoutNotTracking(name, short), false, true
	case b.Ahead == 0 && b.Behind == 0:
		return locales.RemoteCheckoutSame(name, short), false, false
	case b.Ahead == 0:
		return locales.RemoteCheckoutBehind(b.Behind, name, short), true, true
	case b.Behind == 0:
		return locales.RemoteCheckoutAhead(b.Ahead, name, short), false, false
	}
	return locales.RemoteCheckoutDiverged(name, short, b.Ahead, b.Behind), false, true
}

func (a *App) remoteCheckoutView(c *ui.Context) {
	d := &a.remoteUI
	if !d.open || d.t == nil {
		return
	}
	t := d.t
	if d.unread {
		a.readLocal(d)
		if !d.open {
			return
		}
	}
	th := c.Theme()
	name := strings.TrimPrefix(d.local.Name, "refs/heads/")
	short := strings.TrimPrefix(d.remote, "refs/remotes/")
	text, ff, behind := d.relation()
	current := !t.s.State.Head.Detached && t.s.State.Head.Target == d.local.Name
	canSwitch, canReset := true, !d.unread
	if d.early {
		// Opened before the details were read, the dialog was laid out as
		// for a branch that does not track the remote one, all it could
		// say then; it keeps that shape as they land, so that nothing moves
		// under the pointer. A fast-forward is a reset too, and takes the
		// Reset button; what git would refuse, for a branch checked out in
		// another worktree, is disabled.
		elsewhere := d.local.Worktree != ""
		canSwitch, canReset = !elsewhere, !d.unread && behind
		ff, behind = false, true
	}
	submit := func(op write.Op, err error) {
		if err == nil {
			_, err = t.s.Submit(op)
		}
		if err != nil {
			d.err = errText(err)
			return
		}
		d.open = false
	}
	reset := func() {
		submit(write.CheckoutRemote(t.s.Where(), d.remote, name, d.local.Commit, true, write.CarryChanges, t.s.Seen()))
	}
	a.modal(c, &d.open, func() {
		dialogBox(c).Width(dialogNarrow).Children(func() {
			ui.Text(c, locales.RemoteCheckoutExists(redact.Control(name))).Bold()
			if d.early {
				texts := []string{redact.Control(text)}
				if d.local.Worktree != "" {
					texts = append(texts, locales.RemoteCheckoutElsewhereTitle(redact.Control(name)))
				}
				inRoomOf(c, redact.Control(locales.RemoteCheckoutNotTracking(name, short)), texts...)
			} else {
				ui.Text(c, redact.Control(text)).Selectable()
			}
			ui.Row(c).Gap(spaceM).Children(func() {
				if !current && ui.Button(c, locales.RemoteCheckoutSwitch(redact.Control(name))).Disabled(!canSwitch).Clicked() {
					submit(write.Switch(t.s.Where(), d.local.Name, t.s.Seen()), nil)
				}
				switch {
				case ff:
					if ui.PrimaryButton(c, locales.RemoteCheckoutFastForward()).Clicked() {
						reset()
					}
				case behind:
					if ui.Button(c, locales.RemoteCheckoutReset(redact.Control(short))).Disabled(!canReset).Clicked() {
						reset()
					}
				}
			})
			if behind && !ff {
				ui.Text(c, locales.RemoteCheckoutResetNote(redact.Control(name))).FontSize(textSmall).TextColor(th.TextMuted)
			}
			ui.Row(c).Gap(spaceM).AlignItems(ui.Center).FillWidth().Children(func() {
				in := ui.TextInput(c, &d.other).Label(locales.CommonNewBranch()).Placeholder(locales.CommonNewBranch()).Grow(1)
				ok := strings.TrimSpace(d.other) != ""
				if ui.Button(c, locales.RemoteCheckoutAsNew()).Disabled(!ok).Clicked() || ok && in.Submitted() {
					submit(write.CheckoutRemote(t.s.Where(), d.remote, strings.TrimSpace(d.other), "", false, write.CarryChanges, t.s.Seen()))
				}
			})
			if d.err != "" {
				ui.Text(c, d.err).FontSize(textSmall).TextColor(th.Danger).Selectable()
			}
			ui.Row(c).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, locales.CommonCancel()).Clicked() {
					d.open = false
				}
			})
		})
	})
}

// inRoomOf shows the first of texts whose lines fit in the lines room
// takes in the dialog, or else the last cut to them, in the room room
// takes: what was laid out stays where it was whatever is said.
func inRoomOf(c *ui.Context, room string, texts ...string) {
	lines := func(s string) int {
		_, h := c.MeasureText(dialogNarrow, ui.Span{Text: s})
		_, line := c.MeasureText(0, ui.Span{Text: s})
		if line <= 0 {
			return 1
		}
		return max(1, int(h/line+0.5))
	}
	n := lines(room)
	shown := texts[len(texts)-1]
	for _, s := range texts {
		if lines(s) <= n {
			shown = s
			break
		}
	}
	ui.Column(c).Children(func() {
		ui.Text(c, room).Invisible()
		ui.Text(c, shown).Selectable().MaxLines(n).Absolute().Top(0).Left(0).Right(0)
	})
}

// readLocal fills the local branch of d once the branches' details are
// read.
func (a *App) readLocal(d *remoteCheckout) {
	t := d.t
	info := t.refs.info
	if info == nil {
		a.loadRefs(t)
		return
	}
	d.unread = false
	for _, b := range info.Branches {
		if b.Name == d.local.Name {
			d.local = b
		}
	}
}

func (a *App) elsewhereView(c *ui.Context) {
	d := &a.elsewhereUI
	if !d.open {
		return
	}
	name := strings.TrimPrefix(d.branch, "refs/heads/")
	a.modal(c, &d.open, func() {
		dialogBox(c).Width(dialogNarrow).Children(func() {
			ui.Text(c, locales.RemoteCheckoutElsewhereTitle(redact.Control(name))).Bold()
			ui.Text(c, locales.RemoteCheckoutElsewhereMessage(d.folder)).Selectable()
			ui.Row(c).Gap(spaceM).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, locales.CommonCancel()).Clicked() {
					d.open = false
				}
				if ui.PrimaryButton(c, locales.RemoteCheckoutOpen(filepath.Base(d.folder))).Clicked() {
					d.open = false
					a.Open(d.folder)
				}
			})
		})
	})
}
