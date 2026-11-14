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
	"ramus/internal/git/signature"
	"ramus/internal/git/textenc"
	"ramus/internal/session"
	"ramus/locales"
)

// commitView is the detail of the commit chosen in the history: its
// message, people, parents and signature, the files it changed, and the
// patch of the file chosen.
type commitView struct {
	id      []byte
	loading bool
	// cancel stops reading the commit shown before.
	cancel func()
	d      *session.CommitDetail
	files  ui.ListState
	file   int
	split  float32
	// want is a file to choose once the commit is read.
	want string

	patchFor     string
	patchLoading bool
	patch        session.PatchResult
	pv           patchView
	pager        patchPager
	loadPatch    func()
	// sig is git's verdict on the commit's signature, once asked;
	// sigAsked is set from the moment it is.
	sig      *sigResult
	sigAsked bool
}

// sigResult is a verdict on a signature, or why there is none.
type sigResult struct {
	st  signature.Status
	err error
}

// commitDetail shows commit id in cv, reading it when id changes.
func (a *App) commitDetail(c *ui.Context, t *tab, id []byte, cv *commitView) {
	if id == nil {
		emptyState(c, locales.CommitChooseCommit())
		return
	}
	if !bytes.Equal(cv.id, id) {
		if cv.cancel != nil {
			cv.cancel()
		}
		*cv = commitView{id: id, loading: true, file: -1, split: cv.split, want: cv.want}
		cv.cancel = t.s.LoadCommit(id, func(d session.CommitDetail) {
			if bytes.Equal(cv.id, d.OID) {
				cv.loading, cv.d = !d.Done, &d
			}
		})
	}
	if cv.d == nil {
		loading(c, "")
		return
	}
	d := cv.d
	if cv.want != "" && cv.file < 0 && d.Files != nil {
		for i := range d.Files.Len() {
			if string(d.Files.Path(i)) == cv.want {
				cv.file = i
				break
			}
		}
	}
	if d.Err != nil {
		errorMessage(c, errText(d.Err))
		return
	}
	ui.SplitVertical(c, &cv.split, func() {
		ui.Column(c).Fill().Children(func() {
			a.commitHeader(c, t, cv, d)
			a.commitFiles(c, cv, d)
		})
	}, func() { a.commitPatch(c, t, cv, d) }).SplitReset(360).Fill()
}

func (a *App) commitHeader(c *ui.Context, t *tab, cv *commitView, d *session.CommitDetail) {
	th := c.Theme()
	cm := d.Commit
	subject, body, _ := bytes.Cut(bytes.TrimRight(cm.Message, "\n"), []byte("\n"))
	ui.Column(c).Padding(spaceM, insetX).Gap(spaceS).FillWidth().Children(func() {
		a.loadLinks(t)
		links := t.links.set
		linkedText(c, redact.Control(string(subject)), links).Bold()
		if b := bytes.TrimSpace(body); len(b) > 0 {
			linkedText(c, redact.Control(string(b)), links).MaxHeight(160)
		}
		person := func(role string, p object.Signature) {
			ui.Row(c).Gap(spaceM).Children(func() {
				ui.Text(c, role).FontSize(textSmall).TextColor(th.TextMuted).Width(76)
				ui.Text(c, fmt.Sprintf("%s <%s>", redact.Control(string(p.Name)), redact.Control(string(p.Email)))).FontSize(textSmall).Selectable().Grow(1)
				ui.Text(c, signatureTime(p).Format("2006-01-02 15:04:05 -0700")).FontSize(textSmall).TextColor(th.TextMuted)
			})
		}
		person(locales.CommitAuthor(), cm.Author)
		if !samePerson(cm.Author, cm.Committer) {
			person(locales.CommitCommitter(), cm.Committer)
		}
		ui.Row(c).Gap(spaceM).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.CommitHashLabel()).FontSize(textSmall).TextColor(th.TextMuted).Width(76)
			ui.Text(c, hex.EncodeToString(d.OID)).Font("monospace").FontSize(textSmall).Selectable()
		})
		if cm.Signed {
			if !cv.sigAsked {
				cv.sigAsked = true
				id := cv.id
				t.s.Verify(hex.EncodeToString(d.OID), "", func(st signature.Status, err error) {
					if bytes.Equal(cv.id, id) {
						cv.sig = &sigResult{st, err}
					}
				})
			}
			ui.Row(c).Gap(spaceM).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.CommitSignature()).FontSize(textSmall).TextColor(th.TextMuted).Width(76)
				sigText(c, cv.sig)
			})
		}
		if enc := string(cm.Encoding); !textenc.IsUTF8(enc) || d.Odd {
			ui.Row(c).Gap(spaceM).Children(func() {
				ui.Text(c, locales.CommitEncoding()).FontSize(textSmall).TextColor(th.TextMuted).Width(76)
				name := redact.Control(enc)
				if textenc.IsUTF8(enc) {
					name = "UTF-8"
				}
				if !d.Odd {
					ui.Text(c, name).FontSize(textSmall)
					return
				}
				ui.Text(c, locales.CommitEncodingInvalid(name)).FontSize(textSmall).TextColor(th.Warning)
			})
		}
		if len(d.Parents) > 0 {
			ui.Row(c).Gap(spaceM).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.CommitParents(len(d.Parents))).FontSize(textSmall).TextColor(th.TextMuted).Width(76)
				for _, p := range d.Parents {
					if ui.Button(c, hex.EncodeToString(p)[:10]).Font("monospace").Clicked() {
						a.chooseCommit(t, p)
					}
				}
			})
		}
		if s := signatureText(d, t.s.State.Restricted); s != "" {
			ui.Row(c).Gap(spaceM).Children(func() {
				ui.Text(c, locales.CommitSignature()).FontSize(textSmall).TextColor(th.TextMuted).Width(76)
				col := th.TextMuted
				switch d.Signature {
				case "G":
					col = th.Success
				case "B", "R":
					col = th.Danger
				case "U", "X", "Y":
					col = th.Warning
				}
				ui.Text(c, s).FontSize(textSmall).TextColor(col).Selectable()
			})
		}
		ui.Row(c).Gap(spaceM).Children(func() {
			if ui.Button(c, locales.CommonCopyHash()).Clicked() {
				a.clip = hex.EncodeToString(d.OID)
			}
			if ui.Button(c, locales.CommitCopyMessage()).Clicked() {
				a.clip = string(cm.Message)
			}
			if ui.Button(c, locales.CommitCopyPatch()).Disabled(len(d.Parents) > 1).Clicked() {
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
	if d.Checking {
		return locales.CommitSigChecking()
	}
	// by picks the message that names the signer, when git named one.
	by := func(anon func() string, named func(string) string) string {
		if d.Signer == "" {
			return anon()
		}
		return named(redact.Control(d.Signer))
	}
	switch d.Signature {
	case "G":
		return by(locales.CommitSigGood, locales.CommitSigGoodBy)
	case "B":
		return by(locales.CommitSigBad, locales.CommitSigBadBy)
	case "U":
		return by(locales.CommitSigUntrusted, locales.CommitSigUntrustedBy)
	case "X":
		return by(locales.CommitSigExpired, locales.CommitSigExpiredBy)
	case "Y":
		return by(locales.CommitSigExpiredKey, locales.CommitSigExpiredKeyBy)
	case "R":
		return by(locales.CommitSigRevoked, locales.CommitSigRevokedBy)
	case "E":
		return locales.CommitSigCannotCheck()
	}
	if restricted {
		return locales.CommitSigNotTrusted()
	}
	return locales.CommitSigned()
}

func (a *App) commitFiles(c *ui.Context, cv *commitView, d *session.CommitDetail) {
	th := c.Theme()
	f := d.Files
	if f == nil && !d.Done {
		loading(c, "")
		return
	}
	if f == nil || f.Len() == 0 {
		ui.Text(c, locales.CommitNoFiles()).TextColor(th.TextMuted).Padding(spaceM, insetX)
		return
	}
	ui.Text(c, locales.CommitFilesChanged(f.Len())).FontSize(textSmall).TextColor(th.TextMuted).Padding(rowPadY, insetX)
	cv.files.Selected = &cv.file
	cv.files.Label = func(i int) string { return locales.ChangesFileLabel(fileStatusWord(f.Status(i)), commitFileName(f, i)) }
	ui.List(c, &cv.files, f.Len(), func(i int) {
		ui.Row(c).Padding(rowPadY, insetX).Gap(spaceM).AlignItems(ui.Center).FillWidth().Children(func() {
			ui.Text(c, string(f.Status(i))).Font("monospace").FontSize(textSmall).Width(14).TextColor(fileStatusColor(th, f.Status(i)))
			shown, why := showPath(commitFileName(f, i))
			txt := ui.Text(c, shown).SingleLine().Grow(1)
			if why != "" {
				txt.TextColor(th.Warning).Tooltip(why)
			}
		})
	}).Label(locales.CommitFilesLabel()).Grow(1)
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
	case 'A', 'D', 'R', 'C', 'T':
		return locales.ChangesState(string(st))
	}
	return locales.ChangesState("M")
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
	f := d.Files
	if f == nil || cv.file < 0 || cv.file >= f.Len() {
		emptyState(c, locales.DiffChooseFile())
		return
	}
	paths := []string{string(f.Path(cv.file))}
	if st := f.Status(cv.file); st == 'R' || st == 'C' {
		paths = []string{string(f.OldPath(cv.file)), paths[0]}
	}
	ws := diff.Whitespace(a.diffOpts().ws)
	key := fmt.Sprintf("%x\x00%s\x00%s\x00%d", d.OID, paths[0], paths[len(paths)-1], ws)
	if cv.patchFor != key {
		cv.patchFor, cv.patch, cv.pager = key, session.PatchResult{}, patchPager{}
		_, oldID := f.Old(cv.file)
		_, newID := f.New(cv.file)
		detail := *d
		var load func()
		load = func() {
			cv.patchLoading = true
			sides := session.Sides{Old: oldID, New: newID, Max: cv.pager.sides()}
			t.s.CommitPatch(detail, paths, cv.pager.max(), ws, sides, func(res session.PatchResult) {
				if cv.patchFor != key {
					return
				}
				cv.patch = res
				a.setPatch(&cv.pv, res)
				if cv.pager.more(res) {
					load()
					return
				}
				cv.patchLoading = false
			})
		}
		cv.loadPatch = load
		load()
	}
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(barPadY, insetX).Gap(spaceM).AlignItems(ui.Center).Children(func() {
			shown, _ := showPath(commitFileName(f, cv.file))
			ui.Text(c, shown).Bold().SingleLine().Grow(1)
		})
		a.diffBar(c, func() {
			if ui.Button(c, locales.DiffOpenInTool()).Disabled(t.s.Queue == nil || len(d.Parents) == 0).Clicked() {
				t.s.OpenDiffTool(session.DiffToolRequest{Paths: paths, From: d.Parents[0], To: d.OID}, a.toolDone)
			}
		})
		a.pagerNotice(c, &cv.pager, cv.patch, cv.patchLoading, "", cv.loadPatch)
		a.patchBody(c, t, &cv.pv, cv.patch, nil)
	})
}

// sigText shows a verdict on a signature, colored by what it means: one
// that checks from a key git trusts, one that checks from another key,
// and one that does not check.
func sigText(c *ui.Context, r *sigResult) {
	th := c.Theme()
	if r == nil {
		ui.Text(c, locales.CommitSigCheckingVerdict()).FontSize(textSmall).TextColor(th.TextMuted)
		return
	}
	if r.err != nil {
		ui.Text(c, locales.CommitSigNotChecked(errText(r.err))).FontSize(textSmall).TextColor(th.TextMuted).Selectable()
		return
	}
	st := r.st
	color := th.Warning
	switch st.Code {
	case signature.Good:
		color = th.Success
	case signature.Bad, signature.Revoked:
		color = th.Danger
	case signature.CannotCheck, signature.None:
		color = th.TextMuted
	}
	text := st.Text()
	signer, key := redact.Control(st.Signer), redact.Control(st.Key)
	switch {
	case st.Signer != "" && st.Key != "":
		text = locales.CommitSigVerdictSignerKey(text, signer, key)
	case st.Signer != "":
		text = locales.CommitSigVerdictSigner(text, signer)
	case st.Key != "":
		text = locales.CommitSigVerdictKey(text, key)
	}
	ui.Text(c, text).FontSize(textSmall).TextColor(color).Selectable()
}
