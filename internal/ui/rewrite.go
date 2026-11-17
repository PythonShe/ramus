package ui

import (
	"bytes"
	"errors"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/redact"
	"ramus/internal/git/status"
	"ramus/internal/git/write"
	"ramus/internal/session"
	"ramus/locales"
)

// messageDialog asks for a commit message, to reword a commit or squash
// into it.
type messageDialog struct {
	open          bool
	t             *tab
	title, button string
	message       string
	run           func(msg string) (write.Op, error)
	err           string
}

// rewriteActions are what rewrites commit id of HEAD's history: reword,
// fix up or squash what is staged into it, drop it.
func (a *App) rewriteActions(m *ui.Menu, t *tab, id []byte) {
	ro := t.s.Queue == nil || t.s.Repo == nil
	staged := hasStaged(t.s.State.Status)
	if m.Item(locales.RewriteRewordMenu()).Disabled(ro).Chosen() {
		a.withCommit(t, id, func(d session.CommitDetail) {
			rw := t.s.Rewrite(d)
			a.message = messageDialog{open: true, t: t, title: locales.RewriteRewordTitle(shortID(id)), button: locales.RewriteReword(),
				message: string(bytes.TrimRight(d.Commit.Message, "\n")),
				run:     func(msg string) (write.Op, error) { return write.Reword(t.s.Where(), rw, msg, t.s.Seen()) }}
		})
	}
	if m.Item(locales.RewriteFixUpStaged()).Disabled(ro || !staged).Chosen() {
		a.withCommit(t, id, func(d session.CommitDetail) {
			op, err := write.FixupInto(t.s.Where(), t.s.Rewrite(d), "", t.s.Seen())
			a.submitRewrite(t, op, err)
		})
	}
	if !ro && t.s.Repo.Runner.Git().Version.Has(gitexe.HistoryFixup) {
		if m.Item(locales.RewriteSquashStaged()).Disabled(!staged).Chosen() {
			a.withCommit(t, id, func(d session.CommitDetail) {
				rw := t.s.Rewrite(d)
				a.message = messageDialog{open: true, t: t, title: locales.RewriteSquashIntoTitle(shortID(id)), button: locales.RewriteSquash(),
					message: string(bytes.TrimRight(d.Commit.Message, "\n")),
					run:     func(msg string) (write.Op, error) { return write.FixupInto(t.s.Where(), rw, msg, t.s.Seen()) }}
			})
		}
	}
	if m.Item(locales.RewriteEditHistoryHere()).Disabled(ro).Chosen() {
		a.editHistory(t, id, nil)
	}
	m.Separator()
	if m.Item(locales.RewriteDropCommit()).Disabled(ro).Danger().Chosen() {
		a.withCommit(t, id, func(d session.CommitDetail) {
			op, err := write.DropCommit(t.s.Where(), t.s.Rewrite(d), t.s.Seen())
			if err != nil {
				a.tell(t, errText(err))
				return
			}
			subject, _, _ := bytes.Cut(d.Commit.Message, []byte("\n"))
			a.confirm = confirmState{
				open:    true,
				title:   locales.RewriteDropTitle(shortID(id)),
				message: locales.RewriteDropMessage(string(subject)),
				button:  locales.RewriteDrop(),
				run:     func() { a.submitRewrite(t, op, nil) },
			}
		})
	}
}

// hasStaged reports whether st has a change staged; it stops at the
// first.
func hasStaged(st *status.Status) bool {
	if st == nil {
		return false
	}
	for i := range st.Len() {
		switch st.Kind(i) {
		case status.Changed, status.Renamed:
			if x, _ := st.XY(i); x != '.' {
				return true
			}
		}
	}
	return false
}

// withCommit reads commit id, kept from the view most often, and hands it
// to fn once whole.
func (a *App) withCommit(t *tab, id []byte, fn func(session.CommitDetail)) {
	t.s.LoadCommit(id, func(d session.CommitDetail) {
		switch {
		case d.Err != nil:
			a.tell(t, errText(d.Err))
		case d.Done:
			fn(d)
		}
	})
}

func (a *App) messageView(c *ui.Context) {
	d := &a.message
	if !d.open || d.t == nil {
		return
	}
	th := c.Theme()
	a.modal(c, &d.open, func() {
		dialogBox(c).Width(dialogMedium).Children(func() {
			ui.Text(c, d.title).Bold()
			ui.TextArea(c, &d.message).Label(locales.CommonMessage()).Lines(4, 14).FillWidth().AutoFocus()
			if d.err != "" {
				ui.Text(c, d.err).FontSize(textSmall).TextColor(th.Danger).Selectable()
			}
			ui.Row(c).Gap(spaceM).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, locales.CommonCancel()).Clicked() {
					d.open = false
				}
				if ui.PrimaryButton(c, d.button).Disabled(len(bytes.TrimSpace([]byte(d.message))) == 0).Clicked() {
					op, err := d.run(d.message + "\n")
					if err != nil {
						if errors.Is(err, write.ErrNeedsNewerGit) {
							err = errors.New(locales.RewriteNeedsNewerGit())
						}
						d.err = errText(err)
						return
					}
					d.open = false
					a.submitRewrite(d.t, op, nil)
				}
			})
		})
	})
}

// forcePush offers to force a branch that a rewrite left apart from what
// was pushed.
type forcePush struct {
	branch, upstream string
}

// submitRewrite runs a write that rewrites history, and offers a force
// push when it rewrote commits already pushed.
func (a *App) submitRewrite(t *tab, op write.Op, err error) {
	if err != nil {
		a.notice = errText(err)
		return
	}
	seen := t.s.Seen()
	j, err := t.s.Submit(op)
	if err != nil {
		a.notice = errText(err)
		return
	}
	t.s.Then(j, func(err error) {
		if err == nil {
			a.checkPushed(t, seen)
		}
	})
}

// checkPushed offers a force push when the branch seen no longer holds
// what its upstream got from it.
func (a *App) checkPushed(t *tab, seen write.Seen) {
	t.s.PushedRewrite(seen.Branch, seen.Head, func(up string) {
		t.forcePush = forcePush{branch: seen.Branch, upstream: up}
	})
}

// forcePushOffer is the offer, while HEAD is on the branch rewritten.
func (a *App) forcePushOffer(c *ui.Context, t *tab) {
	fp := t.forcePush
	if fp.upstream == "" {
		return
	}
	if h := t.s.State.Head; h.Detached || h.Target != fp.branch {
		t.forcePush = forcePush{}
		return
	}
	th := c.Theme()
	ui.Row(c).Padding(bannerPadY, insetX).Gap(spaceL).AlignItems(ui.Center).Background(th.Surface).FillWidth().Children(func() {
		ui.Text(c, locales.RewritePushedOffer(redact.Control(strings.TrimPrefix(fp.branch, "refs/heads/")), redact.Control(fp.upstream))).Grow(1)
		if ui.Button(c, locales.RewriteNotNow()).Clicked() {
			t.forcePush = forcePush{}
		}
		// It overwrites the upstream, so it asks first, as Force Push
		// with Lease does from the sync button.
		if ui.Button(c, locales.RewriteForcePush()).Disabled(t.s.Queue == nil).Clicked() {
			a.confirm = confirmState{open: true, title: locales.RefsForcePushTitle(redact.Control(fp.upstream)),
				message: locales.RefsForcePushMessage(), button: locales.RefsForcePush(),
				run: func() {
					t.forcePush = forcePush{}
					// git pushes the branch HEAD is on as it runs: only
					// the branch the offer named, still checked out.
					if h := t.s.State.Head; h.Detached || h.Target != fp.branch {
						a.notice = locales.RewriteForcePushMoved(redact.Control(strings.TrimPrefix(fp.branch, "refs/heads/")))
						return
					}
					a.submit(t, write.Push(t.s.Where(), write.PushOptions{ForceWithLease: true, Branch: fp.branch}), nil)
				}}
		}
	})
}
