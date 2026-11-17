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
	// parent is the folder a clone goes into: the one picked last, or
	// the last clone went into, else a folder of projects in the home. dir is parent/<the URL's name>,
	// which auto keeps, until the user types another.
	parent, auto string
	looked       bool
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
	a.findCloneParent()
	busy := cs.job != nil || cs.starting
	if !busy && cs.dir == cs.auto && cs.parent != "" {
		// The folder follows the URL until the user types one.
		cs.dir = filepath.Join(cs.parent, defaultCloneDir(cs.url))
		cs.auto = cs.dir
	}
	ready := strings.TrimSpace(cs.url) != "" && strings.TrimSpace(cs.dir) != ""
	th := c.Theme()
	a.modal(c, &cs.open, func() {
		dialogBox(c).Width(dialogMedium).Children(func() {
			ui.Text(c, locales.CloneTitle()).Bold()
			ui.Field(c, locales.CommonURL(), func() {
				if ui.TextInput(c, &cs.url).Placeholder(locales.CloneURLExample()).AutoFocus().Disabled(busy).Submitted() && ready {
					a.startClone()
				}
			})
			ui.Field(c, locales.CommonFolder(), func() {
				ui.Row(c).Gap(spaceM).Children(func() {
					if ui.TextInput(c, &cs.dir).Grow(1).Disabled(busy).Submitted() && ready {
						a.startClone()
					}
					if ui.Button(c, locales.CommonChoose()).Disabled(busy).Clicked() {
						a.pickCloneParent()
					}
				})
			})
			ui.Checkbox(c, &cs.submodules, locales.CloneSubmodules()).Disabled(busy)
			if busy {
				// Of unknown length until git tells how far it is.
				done := -1.0
				if cs.percent >= 0 {
					done = float64(cs.percent) / 100
				}
				ui.Progress(c, done).Label(locales.CloneProgress())
				ui.Text(c, cs.progress).SingleLine().FontSize(textSmall).TextColor(th.TextMuted)
			}
			if cs.err != "" {
				ui.Text(c, cs.err).TextColor(th.Danger).Selectable()
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
				if ui.PrimaryButton(c, locales.CloneClone()).Disabled(!ready).Clicked() {
					a.startClone()
				}
			})
		})
	})
	if busy && !cs.open {
		// Escape or a click outside would hide a clone still running,
		// with nothing left to show its progress or stop it: Stop does.
		cs.open = true
	}
}

// defaultCloneDir is the folder a URL clones into, named as git would;
// "" while the URL names none.
func defaultCloneDir(url string) string {
	name := strings.TrimSuffix(path.Base(strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(url), ":", "/"), "/")), ".git")
	if name == "." || name == "/" {
		return ""
	}
	return name
}

// cloneParents are the folders of projects in the home a clone goes
// into, the first there is, until the user picks one; else the home.
var cloneParents = []string{"Developer", "Projects", "projects", "src", "Code", "code", "dev"}

// findCloneParent finds, off the main thread, the folder a clone goes
// into until the user picks another: the one the last clone went into
// while it is there, else a folder of projects in the home.
func (a *App) findCloneParent() {
	cs := &a.clone
	if cs.looked {
		return
	}
	cs.looked = true
	update, last := a.Update, a.Settings.CloneParent
	a.goWork(nil, func() {
		if fi, err := os.Stat(last); last != "" && err == nil && fi.IsDir() {
			update(func() {
				if cs.parent == "" {
					cs.parent = last
				}
			})
			return
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		p := home
		for _, name := range cloneParents {
			if fi, err := os.Stat(filepath.Join(home, name)); err == nil && fi.IsDir() {
				p = filepath.Join(home, name)
				break
			}
		}
		update(func() {
			if cs.parent == "" {
				cs.parent = p
			}
		})
	})
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
				cs := &a.clone
				cs.parent = p
				cs.dir = filepath.Join(p, defaultCloneDir(cs.url))
				cs.auto = cs.dir
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
		cs.open, cs.url, cs.dir, cs.auto, cs.submodules = false, "", "", "", false
		// The next clone goes beside this one, after a restart too.
		cs.parent = filepath.Dir(dir)
		if a.Settings.CloneParent != cs.parent {
			a.Settings.CloneParent = cs.parent
			a.keepSettings()
		}
		a.Open(dir)
	})
}
