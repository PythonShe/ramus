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
	m.Separator()
	if m.Item("Reword…").Disabled(ro).Chosen() {
		a.withCommit(t, id, func(d session.CommitDetail) {
			rw := t.s.Rewrite(d)
			a.message = messageDialog{open: true, t: t, title: "Reword " + shortID(id), button: "Reword",
				message: string(bytes.TrimRight(d.Commit.Message, "\n")),
				run:     func(msg string) (write.Op, error) { return write.Reword(t.s.Where(), rw, msg, t.s.Seen()) }}
		})
	}
	if m.Item("Fix Up with Staged Changes").Disabled(ro || !staged).Chosen() {
		a.withCommit(t, id, func(d session.CommitDetail) {
			op, err := write.FixupInto(t.s.Where(), t.s.Rewrite(d), "", t.s.Seen())
			a.submitRewrite(t, op, err)
		})
	}
	if !ro && t.s.Repo.Runner.Git().Version.Has(gitexe.HistoryFixup) {
		if m.Item("Squash Staged Changes into This…").Disabled(!staged).Chosen() {
			a.withCommit(t, id, func(d session.CommitDetail) {
				rw := t.s.Rewrite(d)
				a.message = messageDialog{open: true, t: t, title: "Squash into " + shortID(id), button: "Squash",
					message: string(bytes.TrimRight(d.Commit.Message, "\n")),
					run:     func(msg string) (write.Op, error) { return write.FixupInto(t.s.Where(), rw, msg, t.s.Seen()) }}
			})
		}
	}
	if m.Item("Drop Commit…").Disabled(ro).Danger().Chosen() {
		a.withCommit(t, id, func(d session.CommitDetail) {
			op, err := write.DropCommit(t.s.Where(), t.s.Rewrite(d), t.s.Seen())
			if err != nil {
				a.notice = errText(err)
				return
			}
			subject, _, _ := bytes.Cut(d.Commit.Message, []byte("\n"))
			a.confirm = confirmState{
				open:    true,
				title:   "Drop " + shortID(id) + "?",
				message: "“" + string(subject) + "” and its changes leave the history; the commits after it are rewritten. Undo brings it back.",
				button:  "Drop",
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
			a.notice = errText(d.Err)
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
	ui.Modal(c, &d.open, func() {
		ui.Column(c).Width(520).Gap(12).Padding(20).Children(func() {
			ui.Text(c, d.title).Bold()
			ui.TextArea(c, &d.message).Label("Message").Lines(4, 14).FillWidth().AutoFocus()
			if d.err != "" {
				ui.Text(c, d.err).FontSize(12).TextColor(th.Danger).Selectable()
			}
			ui.Row(c).Gap(8).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, "Cancel").Clicked() {
					d.open = false
				}
				if ui.PrimaryButton(c, d.button).Disabled(len(bytes.TrimSpace([]byte(d.message))) == 0).Clicked() {
					op, err := d.run(d.message + "\n")
					if err != nil {
						if errors.Is(err, write.ErrNeedsNewerGit) {
							err = errors.New("this git cannot do it; git 2.55 or later can")
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
	ui.Row(c).Padding(8, 12).Gap(12).AlignItems(ui.Center).Background(th.Surface).FillWidth().Children(func() {
		ui.Text(c, redact.Control(strings.TrimPrefix(fp.branch, "refs/heads/"))+" was rewritten after it was pushed to "+redact.Control(fp.upstream)).Grow(1)
		if ui.Button(c, "Not Now").Clicked() {
			t.forcePush = forcePush{}
		}
		if ui.PrimaryButton(c, "Force Push with Lease").Disabled(t.s.Queue == nil).Clicked() {
			t.forcePush = forcePush{}
			a.submit(t, write.Push(t.s.Where(), write.PushOptions{ForceWithLease: true}), nil)
		}
	})
}
