package ui

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/write"
)

type cloneState struct {
	open     bool
	url, dir string
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
		ui.Column(c).Width(520).Gap(12).Padding(20).Children(func() {
			ui.Text(c, "Clone a repository").Bold()
			busy := cs.job != nil
			ui.Field(c, "URL", func() {
				ui.TextInput(c, &cs.url).Placeholder("https://github.com/owner/repo.git").AutoFocus().Disabled(busy)
			})
			ui.Field(c, "Folder", func() {
				ui.Row(c).Gap(8).Children(func() {
					ui.TextInput(c, &cs.dir).Placeholder(defaultCloneDir(cs.url)).Grow(1).Disabled(busy)
					if ui.Button(c, "Choose…").Disabled(busy).Clicked() {
						a.pickCloneParent()
					}
				})
			})
			if busy {
				ui.Progress(c, float64(max(cs.percent, 0))/100)
				ui.Text(c, cs.progress).SingleLine().FontSize(12).TextColor(th.TextMuted)
			}
			if cs.err != "" {
				ui.Text(c, cs.err).TextColor(th.Danger).MaxWidth(480)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if busy {
					if ui.Button(c, "Stop").Clicked() {
						cs.job.Cancel()
					}
					return
				}
				if ui.Button(c, "Cancel").Clicked() {
					cs.open = false
				}
				if ui.PrimaryButton(c, "Clone").Disabled(strings.TrimSpace(cs.url) == "").Clicked() {
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
		return "Folder to clone into"
	}
	return name
}

func (a *App) pickCloneParent() {
	if a.PickFolder == nil {
		return
	}
	pick, update := a.PickFolder, a.Update
	go func() {
		p, err := pick("Clone Into")
		update(func() {
			if err == nil && p != "" {
				a.clone.dir = filepath.Join(p, defaultCloneDir(a.clone.url))
			}
		})
	}()
}

func (a *App) startClone() {
	cs := &a.clone
	dir := strings.TrimSpace(cs.dir)
	if dir == "" {
		cs.err = "Choose a folder to clone into"
		return
	}
	j, err := write.Clone(a.Runner, write.CloneOptions{URL: strings.TrimSpace(cs.url), Dir: dir})
	if err != nil {
		cs.err = err.Error()
		return
	}
	cs.job, cs.err, cs.progress, cs.percent = j, "", "", -1
	update := a.Update
	go func() {
		for ev := range j.Events() {
			update(func() {
				if cs.job == j {
					cs.progress, cs.percent = ev.Line, ev.Percent
				}
			})
		}
		_, err := j.Result()
		update(func() {
			if cs.job != j {
				return
			}
			cs.job = nil
			if err != nil {
				cs.err = fmt.Sprint(err)
				return
			}
			cs.open, cs.url, cs.dir = false, "", ""
			// A repository the user just cloned here is theirs to trust;
			// only folders that come from elsewhere ask.
			ts := a.Trust
			go func() {
				if ts != nil {
					ts.Set(filepath.Join(dir, ".git"), true)
				}
				update(func() { a.Open(dir) })
			}()
		})
	}()
}
