package ui

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/diff"
	"ramus/internal/git/object"
	"ramus/internal/git/redact"
	"ramus/internal/session"
)

// commitView is the detail of the commit chosen in the history: its
// message, people, parents and signature, the files it changed, and the
// patch of the file chosen.
type commitView struct {
	id      []byte
	loading bool
	d       *session.CommitDetail
	files   ui.ListState
	file    int
	split   float32

	patchFor     string
	patchLoading bool
	patch        session.PatchResult
	rows         []int32
	plist        ui.ListState
}

// commitDetail shows commit id in cv, reading it when id changes.
func (a *App) commitDetail(c *ui.Context, t *tab, id []byte, cv *commitView) {
	th := c.Theme()
	if id == nil {
		ui.Column(c).Fill().Center().Children(func() {
			ui.Text(c, "Choose a commit to see it").TextColor(th.TextMuted)
		})
		return
	}
	if !bytes.Equal(cv.id, id) {
		*cv = commitView{id: id, loading: true, file: -1, split: cv.split}
		t.s.LoadCommit(id, func(d session.CommitDetail) {
			if bytes.Equal(cv.id, d.OID) {
				cv.loading, cv.d = false, &d
			}
		})
	}
	if cv.d == nil {
		ui.Column(c).Fill().Center().Children(func() { ui.Spinner(c) })
		return
	}
	d := cv.d
	if d.Err != nil {
		ui.Text(c, errText(d.Err)).TextColor(th.Danger).Padding(12).Selectable()
		return
	}
	ui.SplitVertical(c, &cv.split, func() {
		ui.Column(c).Fill().Children(func() {
			a.commitHeader(c, t, d)
			a.commitFiles(c, cv, d)
		})
	}, func() { a.commitPatch(c, t, cv, d) }).SplitReset(360).Fill()
}

func (a *App) commitHeader(c *ui.Context, t *tab, d *session.CommitDetail) {
	th := c.Theme()
	cm := d.Commit
	subject, body, _ := bytes.Cut(bytes.TrimRight(cm.Message, "\n"), []byte("\n"))
	ui.Column(c).Padding(10, 12).Gap(6).FillWidth().Children(func() {
		a.loadLinks(t)
		links := t.links.set
		linkedText(c, redact.Control(string(subject)), links).Bold()
		if b := bytes.TrimSpace(body); len(b) > 0 {
			linkedText(c, redact.Control(string(b)), links).MaxHeight(160)
		}
		person := func(role string, p object.Signature) {
			ui.Row(c).Gap(8).Children(func() {
				ui.Text(c, role).FontSize(12).TextColor(th.TextMuted).Width(76)
				ui.Text(c, fmt.Sprintf("%s <%s>", redact.Control(string(p.Name)), redact.Control(string(p.Email)))).FontSize(12).Selectable().Grow(1)
				ui.Text(c, signatureTime(p).Format("2006-01-02 15:04:05 -0700")).FontSize(12).TextColor(th.TextMuted)
			})
		}
		person("Author", cm.Author)
		if !samePerson(cm.Author, cm.Committer) {
			person("Committer", cm.Committer)
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "Commit").FontSize(12).TextColor(th.TextMuted).Width(76)
			ui.Text(c, hex.EncodeToString(d.OID)).Font("monospace").FontSize(12).Selectable()
		})
		if len(d.Parents) > 0 {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				label := "Parent"
				if len(d.Parents) > 1 {
					label = "Parents"
				}
				ui.Text(c, label).FontSize(12).TextColor(th.TextMuted).Width(76)
				for _, p := range d.Parents {
					if ui.Button(c, hex.EncodeToString(p)[:10]).Font("monospace").Clicked() {
						a.chooseCommit(t, p)
					}
				}
			})
		}
		if s := signatureText(d, t.s.State.Restricted); s != "" {
			ui.Row(c).Gap(8).Children(func() {
				ui.Text(c, "Signature").FontSize(12).TextColor(th.TextMuted).Width(76)
				col := th.TextMuted
				switch d.Signature {
				case "G":
					col = th.Success
				case "B", "R":
					col = th.Danger
				case "U", "X", "Y":
					col = th.Warning
				}
				ui.Text(c, s).FontSize(12).TextColor(col).Selectable()
			})
		}
		ui.Row(c).Gap(8).Children(func() {
			if ui.Button(c, "Copy Hash").Clicked() {
				a.clip = hex.EncodeToString(d.OID)
			}
			if ui.Button(c, "Copy Message").Clicked() {
				a.clip = string(cm.Message)
			}
			if ui.Button(c, "Copy Patch").Disabled(len(d.Parents) > 1).Clicked() {
				a.copyPatch(t, d.OID)
			}
		})
	})
}

func signatureTime(p object.Signature) time.Time {
	return time.Unix(p.When, 0).In(time.FixedZone("", int(p.Offset)*60))
}

func samePerson(a, b object.Signature) bool {
	return bytes.Equal(a.Name, b.Name) && bytes.Equal(a.Email, b.Email) && a.When == b.When && a.Offset == b.Offset
}

// signatureText says what git found of a commit's signature.
func signatureText(d *session.CommitDetail, restricted bool) string {
	if !d.Commit.Signed {
		return ""
	}
	who := ""
	if d.Signer != "" {
		who = " by " + redact.Control(d.Signer)
	}
	switch d.Signature {
	case "G":
		return "Good signature" + who
	case "B":
		return "Bad signature" + who
	case "U":
		return "Good signature" + who + ", from a key not trusted"
	case "X":
		return "Good signature" + who + ", expired"
	case "Y":
		return "Good signature" + who + ", from an expired key"
	case "R":
		return "Good signature" + who + ", from a revoked key"
	case "E":
		return "Signed; the signature cannot be checked here"
	}
	if restricted {
		return "Signed; not checked until you trust this repository"
	}
	return "Signed"
}

func (a *App) commitFiles(c *ui.Context, cv *commitView, d *session.CommitDetail) {
	th := c.Theme()
	f := d.Files
	if f == nil || f.Len() == 0 {
		ui.Text(c, "No files changed").TextColor(th.TextMuted).Padding(8, 12)
		return
	}
	ui.Text(c, countFiles(f.Len())+" changed").FontSize(12).TextColor(th.TextMuted).Padding(4, 12)
	cv.files.Selected = &cv.file
	cv.files.Label = func(i int) string { return fileStatusWord(f.Status(i)) + " " + commitFileName(f, i) }
	ui.List(c, &cv.files, f.Len(), func(i int) {
		ui.Row(c).Padding(2, 12).Gap(8).AlignItems(ui.Center).FillWidth().Children(func() {
			ui.Text(c, string(f.Status(i))).Font("monospace").FontSize(12).Width(14).TextColor(fileStatusColor(th, f.Status(i)))
			shown, why := showPath(commitFileName(f, i))
			txt := ui.Text(c, shown).FontSize(13).SingleLine().Grow(1)
			if why != "" {
				txt.TextColor(th.Warning).Tooltip(why)
			}
		})
	}).Grow(1)
}

func commitFileName(f *diff.Raw, i int) string {
	switch f.Status(i) {
	case 'R', 'C':
		return string(f.OldPath(i)) + " → " + string(f.Path(i))
	}
	return string(f.Path(i))
}

func fileStatusWord(st byte) string {
	switch st {
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'R':
		return "renamed"
	case 'C':
		return "copied"
	case 'T':
		return "type changed"
	}
	return "modified"
}

func fileStatusColor(th *ui.Theme, st byte) ui.Color {
	switch st {
	case 'A':
		return th.Success
	case 'D':
		return th.Danger
	case 'R', 'C':
		return th.Accent
	}
	return th.Warning
}

func (a *App) commitPatch(c *ui.Context, t *tab, cv *commitView, d *session.CommitDetail) {
	th := c.Theme()
	f := d.Files
	if f == nil || cv.file < 0 || cv.file >= f.Len() {
		ui.Column(c).Fill().Center().Children(func() {
			ui.Text(c, "Choose a file to see its changes").TextColor(th.TextMuted)
		})
		return
	}
	paths := []string{string(f.Path(cv.file))}
	if st := f.Status(cv.file); st == 'R' || st == 'C' {
		paths = []string{string(f.OldPath(cv.file)), paths[0]}
	}
	key := string(d.OID) + "\x00" + paths[0] + "\x00" + paths[len(paths)-1]
	if cv.patchFor != key {
		cv.patchFor, cv.patchLoading = key, true
		cv.patch, cv.rows, cv.plist = session.PatchResult{}, nil, ui.ListState{}
		t.s.CommitPatch(*d, paths, MaxShownPatch, func(res session.PatchResult) {
			if cv.patchFor == key {
				cv.patchLoading, cv.patch, cv.rows = false, res, patchRows(res.Parsed)
			}
		})
	}
	switch {
	case cv.patchLoading:
		ui.Column(c).Fill().Center().Children(func() { ui.Spinner(c) })
		return
	case cv.patch.Err != nil:
		ui.Text(c, errText(cv.patch.Err)).TextColor(th.Danger).Padding(12).Selectable()
		return
	}
	p := cv.patch.Parsed
	if p == nil || len(p.Files) == 0 || len(cv.rows) == 0 {
		msg := "No differences in content"
		if p != nil && len(p.Files) > 0 && p.Files[0].Binary {
			msg = "Binary file"
		}
		ui.Column(c).Fill().Center().Children(func() { ui.Text(c, msg).TextColor(th.TextMuted) })
		return
	}
	ui.Column(c).Fill().Children(func() {
		if cv.patch.Truncated || p.Cut {
			ui.Text(c, fmt.Sprintf("Only the first %d MB of this patch are shown", MaxShownPatch>>20)).
				FontSize(12).TextColor(th.Warning).Padding(4, 12)
		}
		rows := cv.rows
		cv.plist.Header = func(i int) bool { return rows[i] < 0 }
		cv.plist.Label = func(i int) string {
			if k := rows[i]; k < 0 {
				return string(p.Bytes(p.Hunks[-1-k].Header))
			} else {
				return lineLabel(p, p.Lines[k])
			}
		}
		ui.List(c, &cv.plist, len(rows), func(i int) {
			k := rows[i]
			if k < 0 {
				ui.Row(c).Padding(4, 12).FillWidth().Background(th.Surface).Children(func() {
					ui.Text(c, redact.Control(string(p.Bytes(p.Hunks[-1-k].Header)))).Font("monospace").FontSize(12).TextColor(th.TextMuted).SingleLine().Grow(1)
				})
				return
			}
			patchLine(c, p, p.Lines[k])
		}).Grow(1)
	})
}
