package ui

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/askpass"
	"ramus/internal/git/redact"
)

// asking are the questions git and ssh asked, the first one shown.
type asking struct {
	list   []*askpass.Ask
	answer string
}

// Ask shows a question from git or ssh, until it is answered or the
// process asking is gone. Call it on the main thread.
func (a *App) Ask(q *askpass.Ask) {
	a.asking.list = append(a.asking.list, q)
	update := a.Update
	go func() {
		<-q.Gone()
		update(func() { a.dropAsk(q) })
	}()
}

func (a *App) dropAsk(q *askpass.Ask) {
	d := &a.asking
	i := slices.Index(d.list, q)
	if i < 0 {
		return
	}
	if i == 0 {
		d.answer = ""
	}
	d.list = slices.Delete(d.list, i, i+1)
}

// askTitle names what a question asks for, with the host the answer
// goes to: the one the prompt names, whatever the rest of it says.
func askTitle(q askpass.Question) string {
	host := redact.Control(q.Host())
	of := func(what string) string {
		if host == "" {
			return what
		}
		return what + " for " + host
	}
	switch q.Kind {
	case askpass.Text:
		return of("User name")
	case askpass.HostKey:
		if host == "" {
			return "Trust this host?"
		}
		return "Trust host " + host + "?"
	case askpass.Confirm:
		return "Allow this?"
	case askpass.Notice:
		return "Security key"
	}
	if strings.Contains(strings.ToLower(q.Prompt), "passphrase") || strings.Contains(q.Prompt, "PIN") {
		return of("Passphrase")
	}
	return of("Password")
}

func (a *App) askView(c *ui.Context) {
	d := &a.asking
	if len(d.list) == 0 {
		return
	}
	q := d.list[0]
	th := c.Theme()
	done := func(answer string, ok bool) {
		if ok {
			q.Answer(answer)
		} else {
			q.Cancel()
		}
		a.dropAsk(q)
	}
	open := true
	ui.Modal(c, &open, func() {
		ui.Column(c).Width(460).Gap(12).Padding(20).Children(func() {
			ui.Text(c, askTitle(q.Question)).Bold()
			ui.Text(c, strings.TrimSpace(redact.Shown(q.Prompt))).Selectable().FontSize(12).TextColor(th.TextMuted)
			switch q.Kind {
			case askpass.Secret, askpass.Text:
				in := ui.TextInput(c, &d.answer).Label(askTitle(q.Question)).AutoFocus().FillWidth()
				if q.Kind == askpass.Secret {
					in.Password()
				}
				ui.Row(c).Gap(8).Children(func() {
					ui.Spacer(c)
					if ui.Button(c, "Cancel").Clicked() {
						done("", false)
					}
					if ui.PrimaryButton(c, "OK").Clicked() || in.Submitted() {
						done(d.answer, true)
					}
				})
			case askpass.HostKey, askpass.Confirm:
				yes := "Allow"
				if q.Kind == askpass.HostKey {
					yes = "Connect"
				}
				ui.Row(c).Gap(8).Children(func() {
					ui.Spacer(c)
					if ui.Button(c, "Cancel").Clicked() {
						done("no", q.Kind == askpass.HostKey)
					}
					if ui.PrimaryButton(c, yes).Clicked() {
						done("yes", true)
					}
				})
			case askpass.Notice:
				ui.Row(c).Children(func() {
					ui.Spacer(c)
					if ui.Button(c, "Hide").Clicked() {
						done("", false)
					}
				})
			}
		})
	})
	if !open && len(d.list) > 0 && d.list[0] == q {
		done("", false)
	}
}
