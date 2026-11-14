package ui

import (
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/askpass"
	"ramus/internal/git/redact"
	"ramus/locales"
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
	of := func(what func() string, forHost func(string) string) string {
		if host == "" {
			return what()
		}
		return forHost(host)
	}
	switch q.Kind {
	case askpass.Text:
		return of(locales.AskpassUserName, locales.AskpassUserNameFor)
	case askpass.HostKey:
		return of(locales.AskpassTrustHost, locales.AskpassTrustHostNamed)
	case askpass.Confirm:
		return locales.AskpassAllowThis()
	case askpass.Notice:
		return locales.AskpassSecurityKey()
	}
	if strings.Contains(strings.ToLower(q.Prompt), "passphrase") || strings.Contains(q.Prompt, "PIN") {
		return of(locales.AskpassPassphrase, locales.AskpassPassphraseFor)
	}
	return of(locales.AskpassPassword, locales.AskpassPasswordFor)
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
		dialogBox(c).Width(dialogNarrow).Children(func() {
			ui.Text(c, askTitle(q.Question)).Bold()
			ui.Text(c, strings.TrimSpace(redact.Shown(q.Prompt))).Selectable().FontSize(textSmall).TextColor(th.TextMuted)
			switch q.Kind {
			case askpass.Secret, askpass.Text:
				in := ui.TextInput(c, &d.answer).Label(askTitle(q.Question)).AutoFocus().FillWidth()
				if q.Kind == askpass.Secret {
					in.Password()
				}
				ui.Row(c).Gap(spaceM).Children(func() {
					ui.Spacer(c)
					if ui.Button(c, locales.CommonCancel()).Clicked() {
						done("", false)
					}
					if ui.PrimaryButton(c, locales.CommonOK()).Clicked() || in.Submitted() {
						done(d.answer, true)
					}
				})
			case askpass.HostKey, askpass.Confirm:
				yes := locales.AskpassAllow()
				if q.Kind == askpass.HostKey {
					yes = locales.AskpassConnect()
				}
				ui.Row(c).Gap(spaceM).Children(func() {
					ui.Spacer(c)
					if ui.Button(c, locales.CommonCancel()).Clicked() {
						done("no", q.Kind == askpass.HostKey)
					}
					if ui.PrimaryButton(c, yes).Clicked() {
						done("yes", true)
					}
				})
			case askpass.Notice:
				ui.Row(c).Children(func() {
					ui.Spacer(c)
					if ui.Button(c, locales.AskpassHide()).Clicked() {
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
