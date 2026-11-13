package ui

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/redact"
	"ramus/internal/git/refs"
	"ramus/internal/session"
)

// fileState is a tab's view of one file or folder: its history, or its
// lines blamed on commits.
type fileState struct {
	path  string // relative to the work tree; a folder ends in "/"
	blame int    // 0 history, 1 blame

	follow, noMerges bool
	logKey           string
	logFor           *refs.List
	logLoading       bool
	commits          []session.FileCommit
	limited          bool
	logErr           string
	list             ui.ListState
	chosen           int
	detail           commitView
	detailW          float32

	blameKey   string
	text       []byte
	starts     []int32 // where each line starts in text
	lineCommit []int32 // the commit each line is blamed on, -1 not yet
	bcommits   []session.BlameCommit
	blameDone  bool
	blameErr   string
	blameList  ui.ListState
	cancel     func()
}

func newFileState() fileState {
	return fileState{chosen: -1, follow: true, detailW: 560, detail: commitView{split: 300}}
}

// showFile opens the view of path (relative to t's work tree).
func (a *App) showFile(t *tab, path string, blame bool) {
	fs := &t.file
	if fs.path != path {
		fs.path, fs.chosen, fs.commits = path, -1, nil
		fs.detail.want = path
	}
	fs.blame = 0
	if blame {
		fs.blame = 1
	}
	t.sel = "file"
}

// ShowFile opens the repository holding abs, a file or folder, at its
// history, or its blame when blame is set: the tab of a repository open
// already when one holds it.
func (a *App) ShowFile(abs string, blame bool) {
	for i, t := range a.tabs {
		if r := t.s.Repo; r != nil {
			if rel, ok := within(r.WorkTree, abs); ok {
				a.active = i
				a.showFile(t, rel, blame)
				return
			}
		}
	}
	dir := abs
	if st, err := statDir(abs); err != nil || !st {
		dir = filepath.Dir(abs)
	}
	a.Open(dir)
	a.pendingFile = pendingFile{abs: abs, blame: blame}
}

type pendingFile struct {
	abs   string
	blame bool
}

// resolvePending shows a file asked for before its repository was open.
func (a *App) resolvePending(t *tab) {
	p := a.pendingFile
	if p.abs == "" || t.s.Repo == nil {
		return
	}
	if rel, ok := within(t.s.Repo.WorkTree, p.abs); ok {
		a.pendingFile = pendingFile{}
		a.showFile(t, rel, p.blame)
	}
}

// within is abs relative to root, with "/" for a folder, when it is in it.
func within(root, abs string) (string, bool) {
	for _, fix := range []func(string) (string, error){func(p string) (string, error) { return p, nil }, filepath.EvalSymlinks} {
		r, err1 := fix(root)
		p, err2 := fix(abs)
		if err1 != nil || err2 != nil {
			continue
		}
		rel, err := filepath.Rel(r, p)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		rel = filepath.ToSlash(rel)
		if isDir, _ := statDir(abs); isDir {
			rel += "/"
		}
		return rel, true
	}
	return "", false
}

func (a *App) fileView(c *ui.Context, t *tab) {
	fs := &t.file
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(6, 8).Gap(8).AlignItems(ui.Center).Children(func() {
			shown, _ := showPath(fs.path)
			ui.Text(c, shown).Bold().SingleLine().Grow(1)
			if fs.blame == 0 {
				if !strings.HasSuffix(fs.path, "/") {
					ui.Checkbox(c, &fs.follow, "Follow Renames")
				}
				ui.Checkbox(c, &fs.noMerges, "Hide Merges")
			}
			if !strings.HasSuffix(fs.path, "/") {
				ui.Segmented(c, &fs.blame, "History", "Blame")
			}
		})
		if fs.blame == 1 && !strings.HasSuffix(fs.path, "/") {
			a.blameView(c, t)
			return
		}
		a.fileHistory(c, t)
	})
}

func (a *App) fileHistory(c *ui.Context, t *tab) {
	th := c.Theme()
	fs := &t.file
	key := fs.path + "\x00" + strconv.FormatBool(fs.follow) + strconv.FormatBool(fs.noMerges)
	if (key != fs.logKey || t.s.State.Refs != fs.logFor) && !fs.logLoading {
		fs.logKey, fs.logFor, fs.logLoading = key, t.s.State.Refs, true
		t.s.FileLog(fs.path, session.FileLogOptions{Follow: fs.follow, NoMerges: fs.noMerges}, func(cs []session.FileCommit, limited bool, err error) {
			fs.logLoading = false
			var chosen []byte
			if fs.chosen >= 0 && fs.chosen < len(fs.commits) {
				chosen = fs.commits[fs.chosen].OID
			}
			fs.commits, fs.limited, fs.logErr, fs.chosen = cs, limited, "", -1
			for i, c := range cs {
				if bytes.Equal(c.OID, chosen) {
					fs.chosen = i
				}
			}
			if err != nil {
				fs.logErr = errText(err)
			}
		})
	}
	ui.Split(c, &fs.detailW, func() {
		ui.Column(c).Fill().Children(func() {
			switch {
			case fs.logErr != "":
				ui.Text(c, fs.logErr).TextColor(th.Danger).Padding(12).Selectable()
				return
			case fs.commits == nil:
				ui.Column(c).Fill().Center().Children(func() {
					if fs.logLoading {
						ui.Spinner(c)
						return
					}
					ui.Text(c, "No commits changed this path").TextColor(th.TextMuted)
				})
				return
			}
			if fs.limited {
				ui.Text(c, "The latest "+strconv.Itoa(session.MaxFileLog)+" commits").FontSize(12).TextColor(th.TextMuted).Padding(0, 12)
			}
			cs := fs.commits
			now := a.Now()
			fs.list.Selected = &fs.chosen
			fs.list.Label = func(i int) string {
				return redact.Control(cs[i].Subject) + ", " + redact.Control(cs[i].Author) + ", " + ago(now, cs[i].Time)
			}
			ui.List(c, &fs.list, len(cs), func(i int) {
				fc := cs[i]
				row := ui.Row(c).Padding(3, 12).Gap(8).AlignItems(ui.Center).FillWidth()
				row.Children(func() {
					ui.Text(c, redact.Control(fc.Subject)).SingleLine().Grow(1)
					if fc.Path != "" && fc.Path != fs.path {
						shown, _ := showPath(fc.Path)
						ui.Text(c, "as "+shown).FontSize(12).TextColor(th.TextMuted).SingleLine().MaxWidth(200)
					}
					ui.Text(c, redact.Control(fc.Author)).FontSize(12).Width(120).TextColor(th.TextMuted).SingleLine()
					ui.Text(c, ago(now, fc.Time)).FontSize(12).Width(88).TextColor(th.TextMuted).SingleLine()
					ui.Text(c, hex.EncodeToString(fc.OID)[:8]).Font("monospace").FontSize(12).Width(64).TextColor(th.TextMuted)
				})
				row.ContextMenu(func(m *ui.Menu) {
					if m.Item("Show in History").Chosen() {
						t.sel = "history"
						a.chooseCommit(t, fc.OID)
					}
					if m.Item("Copy Hash").Chosen() {
						a.clip = hex.EncodeToString(fc.OID)
					}
					a.commitActions(m, t, fc.OID)
				})
			}).Grow(1)
		})
	}, func() {
		var id []byte
		if fs.chosen >= 0 && fs.chosen < len(fs.commits) {
			id = fs.commits[fs.chosen].OID
			if p := fs.commits[fs.chosen].Path; p != "" && !bytes.Equal(id, fs.detail.id) {
				fs.detail.want = p
			}
		}
		a.commitDetail(c, t, id, &fs.detail)
	}).SplitReset(560).Fill()
}

// loadBlame blames the file again when it, or the refs, changed.
func (a *App) loadBlame(t *tab) {
	fs := &t.file
	key := fs.path + "\x00" + strconv.Itoa(t.s.Writes)
	if key == fs.blameKey {
		return
	}
	fs.blameKey = key
	if fs.cancel != nil {
		fs.cancel()
	}
	fs.text, fs.starts, fs.lineCommit, fs.bcommits, fs.blameDone, fs.blameErr = nil, nil, nil, nil, false, ""
	fs.cancel = t.s.Blame(fs.path, func(text []byte, err error) {
		if err != nil {
			fs.blameErr, fs.blameDone = errText(err), true
			return
		}
		fs.text = text
		fs.starts = fs.starts[:0]
		for i := 0; i < len(text); {
			fs.starts = append(fs.starts, int32(i))
			j := bytes.IndexByte(text[i:], '\n')
			if j < 0 {
				break
			}
			i += j + 1
		}
		fs.lineCommit = make([]int32, len(fs.starts))
		for i := range fs.lineCommit {
			fs.lineCommit[i] = -1
		}
	}, func(u session.BlameUpdate) {
		fs.bcommits = append(fs.bcommits, u.Commits...)
		for _, g := range u.Groups {
			for l := g.Start; l < g.Start+g.Lines && l < len(fs.lineCommit); l++ {
				fs.lineCommit[l] = int32(g.Commit)
			}
		}
		if u.Done {
			fs.blameDone = true
			if u.Err != nil {
				fs.blameErr = errText(u.Err)
			}
		}
	})
}

func (a *App) blameView(c *ui.Context, t *tab) {
	th := c.Theme()
	fs := &t.file
	a.loadBlame(t)
	if fs.blameErr != "" {
		ui.Text(c, fs.blameErr).TextColor(th.Danger).Padding(12).Selectable()
		return
	}
	if fs.text == nil {
		ui.Column(c).Fill().Center().Children(func() { ui.Spinner(c) })
		return
	}
	if !fs.blameDone {
		ui.Row(c).Padding(0, 12).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c)
			ui.Text(c, "Blaming").FontSize(12).TextColor(th.TextMuted)
		})
	}
	text, starts, lc, cs := fs.text, fs.starts, fs.lineCommit, fs.bcommits
	line := func(i int) string {
		end := len(text)
		if i+1 < len(starts) {
			end = int(starts[i+1])
		}
		return strings.TrimRight(redact.Control(string(text[starts[i]:end])), "\r\n")
	}
	now := a.Now()
	numW := float32(len(strconv.Itoa(len(starts))))*8 + 8
	fs.blameList.Label = func(i int) string {
		who := "not blamed yet"
		if k := lc[i]; k >= 0 && int(k) < len(cs) {
			who = blameWho(cs[k], now)
		}
		return "Line " + strconv.Itoa(i+1) + ", " + who + ": " + line(i)
	}
	ui.List(c, &fs.blameList, len(starts), func(i int) {
		k := lc[i]
		first := i == 0 || lc[i-1] != k
		row := ui.Row(c).Padding(0, 8).Gap(8).AlignItems(ui.Center).FillWidth()
		row.Children(func() {
			who := ""
			if first && k >= 0 && int(k) < len(cs) {
				who = blameWho(cs[k], now)
			}
			ui.Text(c, who).FontSize(11).Width(260).TextColor(th.TextMuted).SingleLine()
			ui.Text(c, strconv.Itoa(i+1)).Font("monospace").FontSize(12).Width(numW).TextColor(th.TextMuted)
			ui.Text(c, line(i)).Font("monospace").FontSize(12).SingleLine().Grow(1)
		})
		if k >= 0 && int(k) < len(cs) && !cs[k].Uncommitted {
			id := cs[k].OID
			row.ContextMenu(func(m *ui.Menu) {
				if m.Item("Show Commit in History").Chosen() {
					t.sel = "history"
					a.chooseCommit(t, id)
				}
				if m.Item("Copy Hash").Chosen() {
					a.clip = hex.EncodeToString(id)
				}
			})
		}
	}).Grow(1)
}

// blameWho is the short line naming a blamed commit.
func blameWho(bc session.BlameCommit, now time.Time) string {
	if bc.Uncommitted {
		return "not committed"
	}
	return hex.EncodeToString(bc.OID)[:8] + "  " + redact.Control(bc.Author) + ", " + ago(now, bc.Time)
}

// statDir reports whether p is a folder.
func statDir(p string) (bool, error) {
	st, err := os.Stat(p)
	if err != nil {
		return false, err
	}
	return st.IsDir(), nil
}
