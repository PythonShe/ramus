package ui

import (
	"bytes"
	"errors"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/gitexe"
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
			a.submit(t, op, err)
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
				run:     func() { a.submit(t, op, nil) },
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
					if err == nil {
						_, err = d.t.s.Submit(op)
					}
					if err != nil {
						if errors.Is(err, write.ErrNeedsNewerGit) {
							err = errors.New("this git cannot do it; git 2.55 or later can")
						}
						d.err = errText(err)
						return
					}
					d.open = false
				}
			})
		})
	})
}
