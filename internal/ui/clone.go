package ui

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/trust"
	"ramus/internal/git/write"
	"ramus/locales"
)

type cloneState struct {
	open       bool
	url, dir   string
	submodules bool
	// starting is set while the folder is checked, before the job exists;
	// gen tells a start that was stopped meanwhile.
	starting bool
	gen      int
	job      *write.Job
	progress string
	percent  int
	err      string
}

// cloneDialog asks for a URL and a folder, runs the clone with its
// progress shown, and opens the result.
func (a *App) cloneDialog(c *ui.Context) {
	cs := &a.clone
	if !cs.open {
		return
	}
	th := c.Theme()
	ui.Modal(c, &cs.open, func() {
		dialogBox(c).Width(dialogMedium).Children(func() {
			ui.Text(c, locales.CloneTitle()).Bold()
			busy := cs.job != nil || cs.starting
			ui.Field(c, locales.CommonURL(), func() {
				ui.TextInput(c, &cs.url).Label(locales.CloneURLLabel()).Placeholder("https://github.com/owner/repo.git").AutoFocus().Disabled(busy)
			})
			ui.Field(c, locales.CommonFolder(), func() {
				ui.Row(c).Gap(spaceM).Children(func() {
					ui.TextInput(c, &cs.dir).Label(locales.CloneFolderLabel()).Placeholder(locales.CloneFolderLabel()).Grow(1).Disabled(busy)
					if ui.Button(c, locales.CommonChoose()).Disabled(busy).Clicked() {
						a.pickCloneParent()
					}
				})
			})
			ui.Checkbox(c, &cs.submodules, locales.CloneSubmodules()).Disabled(busy)
			if busy {
				ui.Progress(c, float64(max(cs.percent, 0))/100)
				ui.Text(c, cs.progress).SingleLine().FontSize(textSmall).TextColor(th.TextMuted)
			}
			if cs.err != "" {
				ui.Text(c, cs.err).TextColor(th.Danger).MaxWidth(480)
			}
			ui.Row(c).Gap(spaceM).Justify(ui.End).Children(func() {
				if busy {
					if ui.Button(c, locales.CloneStop()).Clicked() {
						if cs.job != nil {
							cs.job.Cancel()
						}
						cs.starting = false
						cs.gen++
					}
					return
				}
				if ui.Button(c, locales.CommonCancel()).Clicked() {
					cs.open = false
				}
				if ui.PrimaryButton(c, locales.CloneClone()).Disabled(strings.TrimSpace(cs.url) == "" || strings.TrimSpace(cs.dir) == "").Clicked() {
					a.startClone()
				}
			})
		})
	})
}

// defaultCloneDir is the folder a URL clones into, named as git would.
func defaultCloneDir(url string) string {
	name := strings.TrimSuffix(path.Base(strings.TrimRight(strings.ReplaceAll(url, ":", "/"), "/")), ".git")
	if name == "" || name == "." || name == "/" {
		return locales.CloneFolderLabel()
	}
	return name
}

func (a *App) pickCloneParent() {
	if a.PickFolder == nil {
		return
	}
	pick, update := a.PickFolder, a.Update
	a.goWork(nil, func() {
		p, err := pick(locales.CloneInto())
		update(func() {
			if err == nil && p != "" {
				a.clone.dir = filepath.Join(p, defaultCloneDir(a.clone.url))
			}
		})
	})
}

func (a *App) startClone() {
	cs := &a.clone
	dir := strings.TrimSpace(cs.dir)
	if rest, ok := strings.CutPrefix(dir, "~"+string(filepath.Separator)); ok {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, rest)
		}
	}
	if !filepath.IsAbs(dir) {
		cs.err = locales.CloneNeedFolder()
		return
	}
	opt := write.CloneOptions{URL: strings.TrimSpace(cs.url), Dir: dir, Submodules: cs.submodules}
	r, update, ts := a.Runner, a.Update, a.Trust
	cs.err, cs.progress, cs.percent = "", "", -1
	cs.starting = true
	cs.gen++
	gen := cs.gen
	a.goWork(nil, func() {
		// Clone looks at the folder before it starts git.
		j, err := write.Clone(r, opt)
		update(func() {
			if cs.gen != gen {
				if j != nil {
					j.Cancel()
				}
				return
			}
			cs.starting = false
			if err != nil {
				cs.err = failure(err)
				return
			}
			cs.job = j
			a.goWork(nil, func() { a.followClone(j, dir, update, ts) })
		})
	})
}

// followClone shows the clone's progress and opens it when done.
func (a *App) followClone(j *write.Job, dir string, update func(func()), ts *trust.Store) {
	cs := &a.clone
	for ev := range j.Events() {
		update(func() {
			if cs.job == j {
				cs.progress, cs.percent = ev.Line, ev.Percent
			}
		})
	}
	_, err := j.Result()
	if err == nil && ts != nil {
		// A repository the user just cloned here is theirs to trust; only
		// folders that come from elsewhere ask.
		ts.Set(filepath.Join(dir, ".git"), true)
	}
	update(func() {
		if cs.job != j {
			return
		}
		cs.job = nil
		switch {
		case errors.Is(err, context.Canceled):
			return // the user stopped it
		case err != nil && !cs.open:
			// The dialog was closed while git cloned: nothing shows
			// cs.err, so the window says it.
			a.Notify(locales.CloneFailed(dir, oneLine(failure(err))))
			return
		case err != nil:
			cs.err = failure(err)
			return
		}
		cs.open, cs.url, cs.dir, cs.submodules = false, "", "", false
		a.Open(dir)
	})
}
