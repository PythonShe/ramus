package ui

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/message"
	"ramus/internal/git/write"
	"ramus/internal/session"
)

// commitState is a tab's commit editor.
type commitState struct {
	subject, body string
	coAuthors     string
	amend         bool
	signOff       bool
	noVerify      bool
	// setup is loaded once per tab; loading is set meanwhile.
	setup   *session.CommitSetup
	loading bool
	// encoding is the setup's commit encoding, kept while the setup is
	// read again after a commit; encodingRead once it was read at all.
	encoding     string
	encodingRead bool
	busy         bool
	err          string
	// wasAmend is amend as of the last frame, to load HEAD's message
	// when amend is turned on over an empty editor.
	wasAmend bool
	confirm  confirmState
}

var personsRE = regexp.MustCompile(`[^<>,;\n]+<[^<>\s]+>`)

// coAuthorList reads co-authors written one per line or separated by
// commas.
func coAuthorList(s string) []string {
	var out []string
	for _, m := range personsRE.FindAllString(s, -1) {
		out = append(out, strings.TrimSpace(m))
	}
	return out
}

// commitBox is the message editor under the changes list.
func (a *App) commitBox(c *ui.Context, t *tab) {
	th := c.Theme()
	cs := &t.commit
	if t.s.Queue == nil {
		return
	}
	if cs.setup == nil && !cs.loading {
		cs.loading = true
		t.s.LoadCommitSetup(func(setup session.CommitSetup) {
			cs.loading, cs.setup = false, &setup
			cs.encoding, cs.encodingRead = setup.Encoding, true
			if cs.subject == "" && cs.body == "" && setup.Template != "" {
				cs.subject, cs.body = message.Split(setup.Template)
			}
		})
	}
	if cs.amend && !cs.wasAmend && cs.subject == "" && cs.body == "" && cs.setup != nil {
		cs.subject, cs.body = message.Split(cs.setup.Last)
	}
	cs.wasAmend = cs.amend
	head := t.s.State.Head
	ui.Column(c).Padding(8).Gap(6).Background(th.Surface).FillWidth().Children(func() {
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.TextInput(c, &cs.subject).Label("Summary").Placeholder("Summary").Grow(1).Disabled(cs.busy)
			if cs.setup != nil && len(cs.setup.Recent) > 0 {
				recent := cs.setup.Recent
				ui.PopoverMenuButton(c, "Recent", func(m *ui.Menu) {
					for _, msg := range recent {
						subject, _ := message.Split(msg)
						if m.Item(subject).Chosen() {
							cs.subject, cs.body = message.Split(msg)
						}
					}
				})
			}
		})
		ui.TextArea(c, &cs.body).Label("Description").Placeholder("Description").Lines(2, 8).FillWidth().Disabled(cs.busy)
		ui.TextInput(c, &cs.coAuthors).Label("Co-authors").Placeholder("Co-authors: Name <email>, …").FillWidth().Disabled(cs.busy)
		ui.Row(c).Gap(12).Children(func() {
			ui.Checkbox(c, &cs.amend, "Amend").Disabled(cs.busy || head.Unborn || len(head.OID) == 0)
			ui.Checkbox(c, &cs.signOff, "Sign off")
			ui.Checkbox(c, &cs.noVerify, "Skip hooks")
		})
		if cs.err != "" {
			ui.Text(c, cs.err).FontSize(12).TextColor(th.Danger).Selectable()
		}
		ready := commitReady(t)
		ui.Row(c).Gap(8).Children(func() {
			branch := strings.TrimPrefix(head.Target, "refs/heads/")
			label := "Commit to " + branch
			if head.Detached {
				label = "Commit"
			}
			if cs.amend {
				label = "Amend Last Commit"
			}
			if ui.PrimaryButton(c, label).Disabled(!ready).Grow(1).Clicked() {
				a.startCommit(t, false)
			}
			if ui.Button(c, "Commit and Push").Disabled(!ready || head.Detached).Clicked() {
				a.startCommit(t, true)
			}
		})
	})
	a.confirmView(c, &cs.confirm)
}

// commitReady reports whether the commit box can commit now.
func commitReady(t *tab) bool {
	cs := &t.commit
	// The model is built off the main thread; until the first one is,
	// nothing counts as staged.
	staged := 0
	if m := t.cv.model; m != nil {
		staged = m.staged
	}
	// The setup names the encoding the message is written in.
	return !cs.busy && cs.encodingRead && strings.TrimSpace(cs.subject) != "" && (staged > 0 || cs.amend)
}

// startCommit checks line endings of what is staged, asks when one
// stands out, and commits.
func (a *App) startCommit(t *tab, push bool) {
	cs := &t.commit
	cs.busy, cs.err = true, ""
	t.s.CheckLineEnds(func(le session.LineEnds) {
		if le.Err != nil {
			// A check that fails does not stop a commit, but says so.
			a.notice = "Line endings not checked: " + errText(le.Err)
		}
		if le.Err != nil || len(le.Findings) == 0 {
			a.commit(t, push)
			return
		}
		cs.confirm = confirmState{
			open:    true,
			title:   "Line endings change in " + countFiles(len(le.Findings)),
			message: lineEndMessage(le),
			button:  "Commit Anyway",
			run:     func() { a.commit(t, push) },
		}
		cs.busy = false
	})
}

func lineEndMessage(le session.LineEnds) string {
	var b strings.Builder
	for i, f := range le.Findings {
		if i == 5 {
			fmt.Fprintf(&b, "and %d more\n", len(le.Findings)-5)
			break
		}
		shown, _ := showPath(f.Path)
		what := "mixes LF and CRLF line endings"
		if f.AddsCRLF {
			what = "gets CRLF line endings; it had LF only"
		}
		b.WriteString(shown + " " + what)
		if f.Attr != "" {
			b.WriteString(" (.gitattributes: " + f.Attr + ")")
		}
		b.WriteByte('\n')
	}
	setting := func(name, v string) string {
		if v == "" {
			v = "not set"
		}
		return name + ": " + v
	}
	b.WriteString("\n" + setting("core.autocrlf", le.AutoCRLF) + ", " + setting("core.eol", le.EOL))
	if !le.Complete {
		b.WriteString("\nSome staged files were too many or too large to check.")
	}
	return b.String()
}

func (a *App) commit(t *tab, push bool) {
	cs := &t.commit
	msg := message.Join(cs.subject, cs.body)
	if s := cs.setup; s != nil && s.Template != "" {
		msg = message.StripTemplate(msg, s.Template, s.Comment)
	}
	opt := write.CommitOptions{Message: msg, Amend: cs.amend, SignOff: cs.signOff, NoVerify: cs.noVerify, CoAuthors: coAuthorList(cs.coAuthors)}
	opt.Encoding = cs.encoding
	cs.busy = true
	seen := t.s.Seen()
	err := t.s.Commit(opt, push, func(err error) {
		cs.busy = false
		if err != nil {
			cs.err = errText(err)
			return
		}
		if opt.Amend {
			a.checkPushed(t, seen)
		}
		cs.subject, cs.body, cs.coAuthors, cs.amend, cs.err = "", "", "", false, ""
		// Recent messages and HEAD's message changed.
		cs.setup = nil
	})
	if err != nil {
		cs.busy, cs.err = false, errText(err)
	}
}
