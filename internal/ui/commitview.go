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
	// split is the height above the patch while a file is open. It fits
	// the message, the rows below it and the first files until the user
	// sizes it (sized), which is kept from commit to commit.
	split float32
	sized bool
	// splitSet and msgSet are the heights set in the last frame: a split
	// found at another height was dragged since.
	splitSet, msgSet float32
	// msgH is the height of a message with a body, above a divider of
	// its own; it fits the message, up to commitBodyMax of body, until
	// the user drags it (msgSized). subjectH, bodyH, msgColH and metaH
	// are what the message's parts and the rows below it took when last
	// laid out.
	msgH, subjectH, bodyH, msgColH, metaH float32
	msgSized                              bool
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
		*cv = commitView{id: id, loading: true, file: -1, split: cv.split, sized: cv.sized, splitSet: cv.splitSet, msgH: cv.msgH, msgSized: cv.msgSized, msgSet: cv.msgSet, want: cv.want}
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
	top := func() {
		ui.Column(c).Fill().Clip().Children(func() {
			if !hasBody(d) {
				a.commitMessage(c, t, cv, d)
				a.commitMeta(c, t, cv, d)
				a.commitFiles(c, cv, d, a.patchCopier(t, d))
				return
			}
			// The message is as tall as the user drags it.
			fit := cv.fittedMessage()
			fitSplit(&cv.msgH, &cv.msgSet, &cv.msgSized, fit)
			ui.SplitVertical(c, &cv.msgH, func() { a.commitMessage(c, t, cv, d) }, func() {
				ui.Column(c).Fill().Children(func() {
					a.commitMeta(c, t, cv, d)
					a.commitFiles(c, cv, d, a.patchCopier(t, d))
				})
			}).SplitReset(fit).Fill()
			if fit != cv.fittedMessage() {
				c.AnimationFrame() // measured anew: lay out once more
			}
		})
	}
	open := d.Files != nil && cv.file >= 0 && cv.file < d.Files.Len()
	// Escape closes the patch wherever the focus is; a dialog or a field
	// that takes Escape gets it first.
	if open && c.Shortcut(0, ui.KeyEscape) {
		cv.file, open = -1, false
	}
	col := ui.Column(c).Fill()
	colH := col.LayoutBounds().H
	col.Children(func() {
		// Until a file is chosen there is no patch: the header and the
		// whole file list take the column.
		if !open {
			top()
			return
		}
		fit := cv.fittedSplit(d.Files.Len(), colH)
		fitSplit(&cv.split, &cv.splitSet, &cv.sized, fit)
		ui.SplitVertical(c, &cv.split, top, func() { a.commitPatch(c, t, cv, d) }).SplitReset(fit).Fill()
	})
}

// fitSplit keeps a split's size at fit until the user drags it. A drag,
// or a double click back to the fitted size, changes *size between
// frames, away from *set, the size this last gave it (0 before it gave
// any).
func fitSplit(size, set *float32, sized *bool, fit float32) {
	if *set != 0 && *size != *set {
		*sized = *size != fit
	}
	if !*sized {
		*size = fit
	}
	*set = *size
}

// commitFilesShown is how many files stay in sight above a patch that
// was just opened.
const commitFilesShown = 4

// fittedSplit is the height above the patch that shows the header, the
// count of files and the first of them, but leaves the patch most of a
// column colH high.
func (cv *commitView) fittedSplit(files int, colH float32) float32 {
	header := cv.msgColH + cv.metaH
	if cv.bodyH > 0 {
		header = cv.msgH + cv.metaH
	}
	h := header + rowHeight*float32(1+min(files, commitFilesShown))
	if colH > 0 {
		h = min(h, colH*0.6)
	}
	return max(h, 4*rowHeight)
}

// commitBodyMax is the most of a body the message shows until the user
// drags it taller; more scrolls.
const commitBodyMax = 160

// messageParts are a commit message's subject and its body, trimmed.
func messageParts(d *session.CommitDetail) (subject, body []byte) {
	subject, body, _ = bytes.Cut(bytes.TrimRight(d.Commit.Message, "\n"), []byte("\n"))
	return subject, bytes.TrimSpace(body)
}

func hasBody(d *session.CommitDetail) bool { _, b := messageParts(d); return len(b) > 0 }

// fittedMessage is the height that shows the subject and the body, or
// commitBodyMax of a longer one.
func (cv *commitView) fittedMessage() float32 {
	return spaceM + cv.subjectH + spaceS + min(cv.bodyH, commitBodyMax) + spaceS
}

// commitMessage shows the subject and the body; a body taller than the
// space it has scrolls.
func (a *App) commitMessage(c *ui.Context, t *tab, cv *commitView, d *session.CommitDetail) {
	subject, body := messageParts(d)
	col := ui.Column(c).Padding(spaceM, insetX, 0, insetX).Gap(spaceS).FillWidth()
	if len(body) > 0 {
		col.Fill()
	}
	cv.msgColH = col.LayoutBounds().H
	col.Children(func() {
		a.loadLinks(t)
		links := t.links.set
		// The subject stands for the message: a click copies it whole.
		s := clickable(linkedText(c, redact.Control(string(subject)), links).Bold()).Tooltip(locales.CommitCopyMessage())
		if s.Clicked() {
			a.clip = string(d.Commit.Message)
		}
		cv.subjectH = s.LayoutBounds().H
		cv.bodyH = 0
		if len(body) > 0 {
			// The body scrolls in its own box: text past an element's
			// height still draws, over the rows below.
			ui.Scroll(c).Key("message").Grow(1).FillWidth().Children(func() {
				b := linkedText(c, redact.Control(string(body)), links).FillWidth()
				cv.bodyH = max(b.LayoutBounds().H, 1)
			})
		}
	})
}

// commitMeta shows who made the commit, its id, signature and parents.
func (a *App) commitMeta(c *ui.Context, t *tab, cv *commitView, d *session.CommitDetail) {
	th := c.Theme()
	cm := d.Commit
	meta := ui.Column(c).Padding(spaceS, insetX, spaceM, insetX).Gap(spaceS).FillWidth()
	cv.metaH = meta.LayoutBounds().H
	meta.Children(func() {
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
			id := hex.EncodeToString(d.OID)
			if clickable(ui.Text(c, id).Font("monospace").FontSize(textSmall)).Tooltip(locales.CommonCopyHash()).Clicked() {
				a.clip = id
			}
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
						t.sel = "history"
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

// patchCopier copies commit d's patch, nil for a merge, which has no one
// patch.
func (a *App) patchCopier(t *tab, d *session.CommitDetail) func() {
	if len(d.Parents) > 1 {
		return nil
	}
	return func() { a.copyPatch(t, d.OID) }
}

// commitFiles lists the files d changed under their count; onCopy, when
// not nil, makes the count copy their patch, which it stands for.
func (a *App) commitFiles(c *ui.Context, cv *commitView, d *session.CommitDetail, onCopy func()) {
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
	count := ui.Text(c, locales.CommitFilesChanged(f.Len())).FontSize(textSmall).TextColor(th.TextMuted).Padding(rowPadY, insetX)
	if onCopy != nil && clickable(count).Tooltip(locales.CommitCopyPatch()).Clicked() {
		onCopy()
	}
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
			if ui.Button(c, "×").Label(locales.CommitClosePatch()).Padding(0, spaceXS).Clicked() {
				cv.file = -1
			}
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
