package ui

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/pathsafe"
	"ramus/internal/git/redact"
	"ramus/internal/git/refs"
	"ramus/internal/git/write"
	"ramus/internal/session"
	"ramus/locales"
)

// fileState is a tab's view of one file or folder: its history, or its
// lines blamed on commits.
type fileState struct {
	path  string // relative to the work tree; a folder ends in "/"
	blame int    // 0 history, 1 blame, linesMode a line history

	follow, noMerges bool
	logKey           string
	logFor           *refs.List
	logLoading       bool
	commits          []session.FileCommit
	limited          bool
	logErr           string
	list             tableState
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
	bpicked    ui.Selection[int] // blame rows chosen together
	cancel     func()

	// stamp is the blame's key as of stampFor, once read off the main
	// thread: the file, HEAD and the file's stat (blameStamp).
	stamp, stampFor string

	lines lineHistory
}

func newFileState() fileState {
	return fileState{chosen: -1, follow: true, detailW: 560, detail: commitView{}}
}

// showFile opens the view of path (relative to t's work tree).
func (a *App) showFile(t *tab, path string, blame bool) {
	fs := &t.file
	if fs.path != path {
		fs.path, fs.chosen, fs.commits = path, -1, nil
		fs.detail.want = path
		fs.lines = lineHistory{}
		fs.bpicked.Clear()
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
	a.pendingFile = pendingFile{abs: abs, blame: blame, dir: cleanPath(dir)}
}

type pendingFile struct {
	abs   string
	blame bool
	// dir is the folder opened for it, as its tab names it: the tab
	// that alone can show it, which may not be open yet when git is
	// still being looked for.
	dir string
}

// resolvePending shows a file asked for before its repository was open,
// once that repository is: where it is in the work tree is worked out
// off the main thread, which resolving symbolic links may hold up, and
// once, so a file the repository turned out not to hold is not looked
// for again each frame.
func (a *App) resolvePending(t *tab) {
	p := a.pendingFile
	if p.abs == "" || !t.is(p.dir) || t.s.Repo == nil {
		return
	}
	a.pendingFile = pendingFile{}
	root, update := t.s.Repo.WorkTree, a.Update
	a.goWork(t, func() {
		if rel, ok := within(root, p.abs); ok {
			update(func() { a.showFile(t, rel, p.blame) })
		}
	})
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
		ui.Row(c).Padding(barPadY, insetX).MinHeight(barHeight).Gap(spaceM).AlignItems(ui.Center).Children(func() {
			shown, _ := showPath(fs.path)
			pathLabel(c, shown).Bold().Grow(1)
			if fs.blame == 0 {
				if !strings.HasSuffix(fs.path, "/") {
					ui.Checkbox(c, &fs.follow, locales.FileHistoryFollowRenames())
				}
				ui.Checkbox(c, &fs.noMerges, locales.FileHistoryHideMerges())
			}
			if !strings.HasSuffix(fs.path, "/") {
				modes := []string{locales.RepoSectionHistory(), locales.FileHistoryBlame()}
				if fs.lines.from > 0 {
					modes = append(modes, fs.lines.label())
				}
				ui.Segmented(c, &fs.blame, modes...)
			}
		})
		switch {
		case strings.HasSuffix(fs.path, "/"):
		case fs.blame == 1:
			a.blameView(c, t)
			return
		case fs.blame == linesMode && fs.lines.from > 0:
			a.lineHistoryView(c, t)
			return
		case fs.blame == linesMode:
			fs.blame = 0
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
	listBesideCommit(c, &fs.detailW, 560, &fs.detail, fs.chosen >= 0 && fs.chosen < len(fs.commits), &fs.list.ListState, func() {
		ui.Column(c).Fill().Children(func() {
			switch {
			case fs.logErr != "":
				errorMessage(c, fs.logErr)
				return
			case fs.commits == nil:
				ui.Column(c).Fill().Center().Children(func() {
					if fs.logLoading {
						ui.Spinner(c).Label(locales.CommonLoading())
						return
					}
					ui.Text(c, locales.FileHistoryNoCommits()).TextColor(th.TextMuted)
				})
				return
			}
			if fs.limited {
				ui.Text(c, locales.FileHistoryLatest(session.MaxFileLog)).FontSize(textSmall).TextColor(th.TextMuted).Padding(0, insetX)
			}
			cs := fs.commits
			now := a.Now()
			fs.list.Selected = &fs.chosen
			fs.list.Label = func(i int) string {
				return locales.FileHistoryCommit(redact.Control(cs[i].Subject), redact.Control(cs[i].Author), ago(now, cs[i].Time))
			}
			fs.list.CellMenu = func(m *ui.Menu, i, _ int) {
				fc := cs[i]
				if m.Item(locales.FileHistoryShowInHistory()).Chosen() {
					t.sel = "history"
					a.chooseCommit(t, fc.OID)
				}
				if m.Item(locales.CommonCopyHash()).Chosen() {
					a.clip = hex.EncodeToString(fc.OID)
				}
				if fc.Path == "" || fc.Path == fs.path {
					a.restoreFromItem(m, t, fs.path, hex.EncodeToString(fc.OID), "")
				}
				a.commitItems(m, t, fc.OID, fc.Merges)
			}
			a.savedTable(c, "fileHistory", &fs.list, commitColumns(), len(cs), func(i, col int) {
				fileCommitCell(c, fs.list.ink(c, i), cs[i], fs.path, now, col)
			}).Label(locales.FileHistoryFileCommits()).Grow(1)
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
	}).Fill()
}

// fileCommitCell builds column col (commitColumns) of a commit in the
// history of the file at path, which says where the file was when the
// commit had it at another path.
func fileCommitCell(c *ui.Context, k ink, fc session.FileCommit, path string, now time.Time, col int) {
	switch col {
	case 0:
		ui.Row(c).Grow(1).Gap(spaceM).AlignItems(ui.Center).Children(func() {
			ui.Text(c, redact.Control(fc.Subject)).SingleLine().Grow(1)
			if fc.Path != "" && fc.Path != path {
				shown, _ := showPath(fc.Path)
				mutedCell(c, k, locales.FileHistoryAsPath(shown)).MaxWidth(200)
			}
		})
	case 1:
		mutedCell(c, k, redact.Control(fc.Author))
	case 2:
		mutedCell(c, k, ago(now, fc.Time))
	case 3:
		hashCell(c, k, fc.OID)
	}
}

// loadBlame blames the file again when it, or HEAD, changed.
func (a *App) loadBlame(t *tab) {
	fs := &t.file
	key, ok := a.blameStamp(t)
	if !ok || key == fs.blameKey {
		return
	}
	fs.blameKey = key
	fs.bpicked.Clear()
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

// dropBlame stops the blame and lets go of it, as the view is left: a
// blamed file's text can be tens of megabytes, kept for no one. It is
// blamed again when shown.
func (fs *fileState) dropBlame() {
	if fs.cancel != nil {
		fs.cancel()
		fs.cancel = nil
	}
	fs.blameKey = ""
	fs.text, fs.starts, fs.lineCommit, fs.bcommits, fs.blameDone, fs.blameErr = nil, nil, nil, nil, false, ""
}

// blameStamp is what the blame of the file shown is of: the file, HEAD
// and the file's stat, read off the main thread as each write ends and
// HEAD moves; not ok while it is read. A write that leaves them, as a
// fetch, keeps the blame and the lines picked in it. Another file is
// blamed at once, none of the last one's blame showing meanwhile.
func (a *App) blameStamp(t *tab) (string, bool) {
	fs := &t.file
	head := hex.EncodeToString(t.s.State.Head.OID)
	asked := fs.path + "\x00" + head + "\x00" + strconv.Itoa(t.s.Writes)
	if asked != fs.stampFor {
		fs.stampFor, fs.stamp = asked, ""
		if r := t.s.Repo; r != nil {
			path, root, update := fs.path, r.WorkTree, a.Update
			a.goWork(t, func() {
				st := fileStat(root, path)
				update(func() {
					if fs.stampFor != asked {
						return
					}
					fs.stamp = path + "\x00" + head + "\x00" + st
					if fs.blameKey == path+"\x00" {
						// Blamed as it was chosen, about as the stat was read.
						fs.blameKey = fs.stamp
					}
				})
			})
		}
	}
	if fs.stamp == "" && !strings.HasPrefix(fs.blameKey, fs.path+"\x00") {
		return fs.path + "\x00", true
	}
	return fs.stamp, fs.stamp != ""
}

// fileStat says the size, time and mode of the file at path in the work
// tree root, "" when there is none.
func fileStat(root, path string) string {
	abs, err := pathsafe.Target(root, path)
	if err != nil {
		return ""
	}
	st, err := os.Lstat(abs)
	if err != nil {
		return ""
	}
	return fmt.Sprint(st.Size(), st.ModTime().UnixNano(), st.Mode())
}

func (a *App) blameView(c *ui.Context, t *tab) {
	th := c.Theme()
	fs := &t.file
	a.loadBlame(t)
	if fs.blameErr != "" {
		errorMessage(c, fs.blameErr)
		return
	}
	if fs.text == nil {
		loading(c, "")
		return
	}
	if !fs.blameDone {
		ui.Row(c).Padding(0, insetX).Gap(spaceM).AlignItems(ui.Center).Children(func() {
			ui.Spinner(c).Label(locales.FileHistoryBlaming())
			ui.Text(c, locales.FileHistoryBlaming()).FontSize(textSmall).TextColor(th.TextMuted)
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
		who := locales.FileHistoryNotBlamed()
		if k := lc[i]; k >= 0 && int(k) < len(cs) {
			who = blameWho(cs[k], now)
		}
		return locales.FileHistoryBlameLine(i+1, who, line(i))
	}
	fs.blameList.Selection, fs.blameList.DragSelect = &fs.bpicked, true
	ui.List(c, &fs.blameList, len(starts), func(i int) {
		k := lc[i]
		first := i == 0 || lc[i-1] != k
		row := ui.Row(c).Padding(0, insetX).Gap(spaceM).AlignItems(ui.Center).FillWidth()
		row.Children(func() {
			who := ""
			if first && k >= 0 && int(k) < len(cs) {
				who = blameWho(cs[k], now)
			}
			ui.Text(c, who).FontSize(textCaption).Width(260).TextColor(th.TextMuted).SingleLine()
			ui.Text(c, strconv.Itoa(i+1)).Font("monospace").FontSize(textSmall).Width(numW).TextColor(th.TextMuted)
			ui.Text(c, line(i)).Font("monospace").FontSize(textSmall).SingleLine().Grow(1)
		})
		var id []byte
		if k >= 0 && int(k) < len(cs) && !cs[k].Uncommitted {
			id = cs[k].OID
		}
		row.ContextMenu(func(m *ui.Menu) {
			a.lineHistoryItem(m, t, i)
			if id == nil {
				return
			}
			m.Separator()
			if m.Item(locales.FileHistoryShowCommit()).Chosen() {
				t.sel = "history"
				a.chooseCommit(t, id)
			}
			if m.Item(locales.CommonCopyHash()).Chosen() {
				a.clip = hex.EncodeToString(id)
			}
		})
	}).Label(locales.FileHistoryBlameLines()).Grow(1)
}

// blameWho is the short line naming a blamed commit.
func blameWho(bc session.BlameCommit, now time.Time) string {
	if bc.Uncommitted {
		return locales.FileHistoryNotCommitted()
	}
	return locales.FileHistoryBlameWho(hex.EncodeToString(bc.OID)[:8], redact.Control(bc.Author), ago(now, bc.Time))
}

// statDir reports whether p is a folder.
func statDir(p string) (bool, error) {
	st, err := os.Stat(p)
	if err != nil {
		return false, err
	}
	return st.IsDir(), nil
}

// restoreFromItem offers to put path in the work tree back as it is in
// commit, which keeps it in a snapshot first. asOf names the commit in
// the item, "" for "this commit".
func (a *App) restoreFromItem(m *ui.Menu, t *tab, path, commit, asOf string) {
	folder := strings.HasSuffix(path, "/")
	var label string
	switch {
	case asOf != "" && folder:
		label = locales.FileHistoryRestoreFolderAsOf(redact.Control(asOf))
	case asOf != "":
		label = locales.FileHistoryRestoreFileAsOf(redact.Control(asOf))
	case folder:
		label = locales.FileHistoryRestoreFolder()
	default:
		label = locales.FileHistoryRestoreFile()
	}
	if !m.Item(label).Disabled(t.s.Queue == nil).Chosen() {
		return
	}
	op, err := write.RestoreFrom(t.s.Where(), path, commit)
	if err != nil {
		a.notice = errText(err)
		return
	}
	shown, _ := showPath(path)
	a.confirm = confirmState{open: true, title: locales.FileHistoryRestoreTitle(shown),
		message: locales.FileHistoryRestoreMessage(commit[:8]),
		button:  locales.FileHistoryRestore(), run: func() { a.submit(t, op, nil) }}
}
