package ui

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/askpass"
	"ramus/internal/git/redact"
	"ramus/internal/git/repo"
	"ramus/locales"
)

// asking are the questions git and ssh asked, the first one shown.
type asking struct {
	list   []*askpass.Ask
	answer string
	// hosts are the hosts the questions name that a remote of the
	// repository asking names too: those the title may name.
	hosts map[*askpass.Ask]string
}

// Ask shows a question from git or ssh, until it is answered or the
// process asking is gone. Call it on the main thread.
func (a *App) Ask(q *askpass.Ask) {
	a.asking.list = append(a.asking.list, q)
	var repos []*repo.Repository
	for _, t := range a.tabs {
		if t.s != nil && t.s.Repo != nil {
			repos = append(repos, t.s.Repo)
		}
	}
	update := a.Update
	a.goWork(nil, func() {
		// Off the main thread: the folders are resolved on disk.
		if h := remoteHost(q.Question, repos); h != "" {
			update(func() {
				if slices.Contains(a.asking.list, q) {
					if a.asking.hosts == nil {
						a.asking.hosts = map[*askpass.Ask]string{}
					}
					a.asking.hosts[q] = h
				}
			})
		}
	})
	a.goWork(nil, func() {
		<-q.Gone()
		update(func() { a.dropAsk(q) })
	})
}

// remoteHost is the host q's prompt names when a remote of the repository
// it was asked in, one of repos, is on that host, else "": the prompt may
// be an ssh server's text, and only a host the app knows is named as the
// one the answer goes to.
func remoteHost(q askpass.Question, repos []*repo.Repository) string {
	host := q.Host()
	if host == "" || q.Dir == "" {
		return ""
	}
	dir := resolved(q.Dir)
	for _, r := range repos {
		if !inFolder(dir, resolved(r.WorkTree)) && !inFolder(dir, resolved(r.GitDir)) {
			continue
		}
		for _, e := range r.Config().Entries() {
			if !strings.HasPrefix(e.Key, "remote.") || !strings.HasSuffix(e.Key, ".url") && !strings.HasSuffix(e.Key, ".pushurl") {
				continue
			}
			if h, ok := redact.Host(e.Value); ok && strings.EqualFold(h, host) {
				return host
			}
		}
	}
	return ""
}

func resolved(p string) string {
	if p == "" {
		return ""
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// inFolder reports dir being root or a folder inside it.
func inFolder(dir, root string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
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
	delete(d.hosts, q)
	d.list = slices.Delete(d.list, i, i+1)
}

// askTitle names what a question asks for, with host, the one the answer
// goes to when the app knows it (remoteHost); the prompt, shown below
// it, says the rest in git's or ssh's words.
func askTitle(q askpass.Question, host string) string {
	host = redact.Control(host)
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
	a.modal(c, &open, func() {
		dialogBox(c).Width(dialogNarrow).Children(func() {
			ui.Text(c, askTitle(q.Question, d.hosts[q])).Bold()
			ui.Text(c, strings.TrimSpace(redact.Shown(q.Prompt))).Selectable().FontSize(textSmall).TextColor(th.TextMuted)
			switch q.Kind {
			case askpass.Secret, askpass.Text:
				in := ui.TextInput(c, &d.answer).Label(askTitle(q.Question, d.hosts[q])).AutoFocus().FillWidth()
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
