package ui

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/forge"
	"ramus/internal/git/diff"
	"ramus/internal/git/message"
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
	// infoW is the width of the commit beside its patch, and listW the
	// list's beside them, while a patch is open; both are kept from commit
	// to commit. wasOpen is whether one was open in the last frame.
	// infoRoom and listRoom are the room of the commit's split and of the
	// list's as last laid out.
	infoW, listW       float32
	infoRoom, listRoom float32
	wasOpen            bool
	// msgSet is the message's height set in the last frame: one found at
	// another height was dragged since.
	msgSet float32
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
	// sig is the signature's verdict as shown, with its popover.
	sig sigResult
	// pull is the pull request the commit came in by, once asked
	// (pullAsked) and answered; pullCancel stops the asking while
	// pullWaiting.
	pull                   session.CommitPullResult
	pullAsked, pullWaiting bool
	pullCancel             func()
	// noPull keeps the pull request from being asked for: a stash entry
	// is never pushed.
	noPull bool
	// msgOf is the detail that subject, body and coAuthors were taken
	// from: the message as shown, its Co-authored-by trailers taken out
	// to show as people.
	msgOf         *session.CommitDetail
	subject, body []byte
	coAuthors     []message.Person
	// parentSubjects are the subjects of the commit's parents once read,
	// in their order; parentsAsked is set once they are asked for.
	parentSubjects []string
	parentsAsked   bool
	parentsCancel  func()
	// rel are the refs that contain the commit and its children, once
	// read; relAsked is set once they are asked for. childSubjects are
	// the subjects of the children shown.
	rel           *session.CommitRelations
	relAsked      bool
	relCancel     func()
	childSubjects []string
	childCancel   func()
}

// sigResult is a verdict on a signature, or why there is none.
type sigResult struct {
	st  signature.Status
	err error
	// info keeps open the popover saying why it was not checked.
	info bool
}

// commitDetail shows commit id in cv, reading it when id changes.
func (a *App) commitDetail(c *ui.Context, t *tab, id []byte, cv *commitView) {
	if id == nil {
		emptyPane(c)
		return
	}
	if !bytes.Equal(cv.id, id) {
		if cv.cancel != nil {
			cv.cancel()
		}
		for _, cancel := range []func(){cv.parentsCancel, cv.relCancel, cv.childCancel} {
			if cancel != nil {
				cancel()
			}
		}
		cv.stopPull()
		*cv = commitView{id: id, loading: true, file: -1, infoW: cv.infoW, listW: cv.listW, infoRoom: cv.infoRoom, listRoom: cv.listRoom, wasOpen: cv.wasOpen, msgH: cv.msgH, msgSized: cv.msgSized, msgSet: cv.msgSet, want: cv.want, noPull: cv.noPull}
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
	a.syncAvatars(t, [][]byte{d.OID})
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
			if _, body, _ := cv.message(d); len(body) == 0 {
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
	open := cv.patchOpen()
	// Escape closes the patch wherever the focus is; a dialog or a field
	// that takes Escape gets it first.
	if open && c.Shortcut(0, ui.KeyEscape) {
		cv.file, open = -1, false
	}
	if open != cv.wasOpen {
		// The list beside, built before, takes its other width.
		cv.wasOpen = open
		c.AnimationFrame()
	}
	ui.Column(c).Fill().Children(func() {
		// Until a file is chosen there is no patch: the commit takes the
		// column. Its patch opens in a column of its own, beside it.
		if !open {
			top()
			return
		}
		if cv.infoW == 0 {
			cv.infoW = commitInfoW
		}
		keptSplit(c, &cv.infoW, &cv.infoRoom, leastInfoW, leastPatchW, top, func() { a.commitPatch(c, t, cv, d) }).SplitReset(commitInfoW).Fill()
	})
}

// The widths of a commit and of the list beside it while its patch is
// open, until the user sizes them: the patch takes the rest.
const (
	commitInfoW = 320
	commitListW = 360
)

// patchOpen reports whether a file of the commit is chosen, its patch
// shown.
func (cv *commitView) patchOpen() bool {
	return cv.d != nil && cv.d.Files != nil && cv.file >= 0 && cv.file < cv.d.Files.Len()
}

// listPane is the width listBesideCommit gives the list: *w, or the
// list's own while the commit's patch is open, given way for the commit
// to keep its least.
func listPane(w float32, cv *commitView) float32 {
	if cv.patchOpen() {
		w = cv.listW
		if w == 0 {
			w = commitListW
		}
	}
	return fitPane(w, cv.listRoom, leastListW, commitKeep(cv))
}

// commitKeep is the least the commit keeps beside the list: with its
// patch open, room for both.
func commitKeep(cv *commitView) float32 {
	if cv.patchOpen() {
		return leastInfoW + 1 + leastPatchW
	}
	return leastPatchW
}

// listBesideCommit splits a list of commits from the commit chosen in it.
// While the commit's patch is open, the list takes a width of its own,
// narrower, which leaves the patch room; it goes back to *w as the patch
// closes.
func listBesideCommit(c *ui.Context, w *float32, reset float32, cv *commitView, list, detail func()) ui.Element {
	if cv.patchOpen() {
		if cv.listW == 0 {
			cv.listW = commitListW
		}
		w, reset = &cv.listW, commitListW
	}
	return keptSplit(c, w, &cv.listRoom, leastListW, commitKeep(cv), list, detail).SplitReset(reset)
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

// commitBodyMax is the most of a body the message shows until the user
// drags it taller; more scrolls.
const commitBodyMax = 160

// messageParts are a commit message's subject and its body, trimmed.
func messageParts(d *session.CommitDetail) (subject, body []byte) {
	subject, body, _ = bytes.Cut(bytes.TrimRight(d.Commit.Message, "\n"), []byte("\n"))
	return subject, bytes.TrimSpace(body)
}

// message returns d's subject and body as shown, and the co-authors its
// trailers name, whose lines the body leaves out: they show as people.
func (cv *commitView) message(d *session.CommitDetail) (subject, body []byte, coAuthors []message.Person) {
	if cv.msgOf != d {
		msg := d.Commit.Message
		var kept []byte
		at := 0
		cv.coAuthors = nil
		for _, tr := range message.Trailers(msg) {
			if p, ok := message.CoAuthor(tr); ok {
				kept = append(kept, msg[at:tr.Start]...)
				at = tr.End
				cv.coAuthors = append(cv.coAuthors, p)
			}
		}
		if at > 0 {
			msg = append(kept, msg[at:]...)
		}
		cv.subject, cv.body, _ = bytes.Cut(bytes.TrimRight(msg, "\n"), []byte("\n"))
		cv.body = bytes.TrimSpace(cv.body)
		cv.msgOf = d
	}
	return cv.subject, cv.body, cv.coAuthors
}

// fittedMessage is the height that shows the subject and the body, or
// commitBodyMax of a longer one.
func (cv *commitView) fittedMessage() float32 {
	return spaceM + cv.subjectH + spaceS + min(cv.bodyH, commitBodyMax) + spaceS
}

// commitMessage shows the subject and the body; a body taller than the
// space it has scrolls.
func (a *App) commitMessage(c *ui.Context, t *tab, cv *commitView, d *session.CommitDetail) {
	subject, body, _ := cv.message(d)
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
			a.clip = copiedMessage(d.Commit.Message)
		}
		cv.subjectH = s.LayoutBounds().H
		cv.bodyH = 0
		if len(body) > 0 {
			// The body scrolls in its own box: text past an element's
			// height still draws, over the rows below.
			ui.Scroll(c).Key("message").Grow(1).FillWidth().Children(func() {
				b := linkedText(c, redact.Control(reflowBody(string(body))), links).FillWidth()
				cv.bodyH = max(b.LayoutBounds().H, 1)
			})
		}
	})
}

// metaLabelW is the width of the labels before what commitMeta shows, so
// that what they label lines up.
const metaLabelW = 76

// commitMeta shows who made the commit, its id, signature and parents.
func (a *App) commitMeta(c *ui.Context, t *tab, cv *commitView, d *session.CommitDetail) {
	th := c.Theme()
	cm := d.Commit
	meta := ui.Column(c).Padding(spaceS, insetX, spaceM, insetX).Gap(spaceS).FillWidth()
	cv.metaH = meta.LayoutBounds().H
	meta.Children(func() {
		label := func(text string) {
			ui.Text(c, text).FontSize(textSmall).TextColor(th.TextMuted).Width(metaLabelW)
		}
		// The time goes under the person it is theirs, rather than beside
		// them, where a narrow panel would break it over lines.
		person := func(role string, p object.Signature) {
			ui.Row(c).Gap(spaceM).AlignItems(ui.Start).Children(func() {
				label(role)
				a.personLine(c, t, string(p.Name), string(p.Email), avatarMeta, func() {
					at := signatureTime(p)
					ui.Text(c, zoneTime(at)).FontSize(textSmall).TextColor(th.TextMuted).Tooltip(since(a.Now(), at))
				})
			})
		}
		person(locales.CommitAuthor(), cm.Author)
		if _, _, co := cv.message(d); len(co) > 0 {
			ui.Row(c).Gap(spaceM).AlignItems(ui.Start).Children(func() {
				label(locales.CommitCoAuthors(len(co)))
				ui.Column(c).Gap(spaceXS).Grow(1).Shrink(1).MinWidth(0).Children(func() {
					for i, p := range co {
						c.Key(i)
						a.personLine(c, t, p.Name, p.Email, avatarSmall, nil)
					}
				})
			})
		}
		if !samePerson(cm.Author, cm.Committer) {
			person(locales.CommitCommitter(), cm.Committer)
		}
		ui.Row(c).Gap(spaceM).AlignItems(ui.Start).Children(func() {
			ui.Text(c, locales.CommitHashLabel()).FontSize(textSmall).TextColor(th.TextMuted).Width(metaLabelW)
			id := hex.EncodeToString(d.OID)
			// A hash is one word: it breaks anywhere to fit a narrow panel,
			// rather than run past its edge.
			if clickable(ui.Text(c, id).Font("monospace").FontSize(textSmall).Grow(1).Shrink(1).MinWidth(0)).Tooltip(locales.CommonCopyHash()).Clicked() {
				a.clip = id
			}
		})
		// The row shows only once the host names a pull request: nothing
		// stands in for it while asking, so nothing moves when it does
		// not.
		a.loadCommitPull(t, cv, d)
		if p := cv.pull.Pull; a.Settings.PullRequests && cv.pull.Found {
			ui.Row(c).Gap(spaceM).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.CommitPullRequest(kindName(p.Repo.Kind))).FontSize(textSmall).TextColor(th.TextMuted).Width(metaLabelW)
				ui.Row(c).Gap(spaceS).AlignItems(ui.Center).Grow(1).Children(func() {
					ui.Link(c, p.Ref(p.Repo.Kind), p.URL).FontSize(textSmall).Tooltip(redact.Control(p.Title))
					pullState(c, plainInk(th), p.Pull)
				})
			})
		}
		// The commit's own signature, under its hash: the kind of key
		// that made it, then what git made of it.
		if cm.Signed {
			ui.Row(c).Gap(spaceM).AlignItems(ui.Center).Children(func() {
				ui.Text(c, locales.CommitSignature()).FontSize(textSmall).TextColor(th.TextMuted).Width(metaLabelW)
				ui.Row(c).Gap(spaceS).AlignItems(ui.Center).Grow(1).Children(func() {
					if k := sigKindName(cm.SigKind); k != "" {
						ui.Badge(c, k).FontSize(textCaption)
					}
					commitSigText(c, cv, d, t.s.State.Restricted)
				})
			})
		}
		if enc := string(cm.Encoding); !textenc.IsUTF8(enc) || d.Odd {
			ui.Row(c).Gap(spaceM).Children(func() {
				ui.Text(c, locales.CommitEncoding()).FontSize(textSmall).TextColor(th.TextMuted).Width(metaLabelW)
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
			if !cv.parentsAsked {
				cv.parentsAsked = true
				id := cv.id
				cv.parentsCancel = t.s.LoadSubjects(d.Parents, func(subjects []string) {
					if bytes.Equal(cv.id, id) {
						cv.parentSubjects = subjects
					}
				})
			}
			ui.Row(c).Gap(spaceM).AlignItems(ui.Start).Children(func() {
				label(locales.CommitParents(len(d.Parents)))
				a.commitLinks(c, t, d.Parents, cv.parentSubjects, locales.CommitGoToParent())
			})
		}
		a.loadRelations(t, cv)
		// Nothing stands in for these while they are read, as for the pull
		// request: a commit no ref contains has neither row.
		rel := cv.rel
		if rel == nil || rel.Err != nil {
			return
		}
		if n := len(rel.Children); n > 0 {
			ui.Row(c).Gap(spaceM).AlignItems(ui.Start).Children(func() {
				label(locales.CommitChildren(n))
				ui.Column(c).Gap(space2XS).Grow(1).Shrink(1).MinWidth(0).Children(func() {
					a.commitLinks(c, t, rel.Children[:min(n, shownChildren)], cv.childSubjects, locales.CommitGoToChild())
					if n > shownChildren {
						ui.Text(c, locales.CommitMoreChildren(n-shownChildren)).FontSize(textSmall).TextColor(th.TextMuted)
					}
				})
			})
		}
		if len(rel.Refs) > 0 {
			ui.Row(c).Gap(spaceM).AlignItems(ui.Start).Children(func() {
				label(locales.CommitContainedIn())
				ui.Row(c).Gap(spaceXS).Wrap().AlignItems(ui.Center).Grow(1).Shrink(1).MinWidth(0).Children(func() {
					shown := min(len(rel.Refs), shownRefs)
					for i, name := range rel.Refs[:shown] {
						c.Key(i)
						refChip(c, containedLabel(t, name))
					}
					if more := len(rel.Refs) - shown + rel.More; more > 0 {
						ui.Text(c, locales.CommitMoreRefs(more)).FontSize(textCaption).TextColor(th.TextMuted)
					}
				})
			})
		}
	})
}

// shownChildren and shownRefs are the most children and containing refs
// a commit lists; it counts the rest.
const (
	shownChildren = 10
	shownRefs     = 24
)

// loadRelations asks once per commit shown for the refs that contain it
// and its children, then for the subjects of the children it lists.
func (a *App) loadRelations(t *tab, cv *commitView) {
	if cv.relAsked {
		return
	}
	cv.relAsked = true
	id := cv.id
	cv.relCancel = t.s.LoadCommitRelations(id, func(rel session.CommitRelations) {
		if !bytes.Equal(cv.id, id) {
			return
		}
		cv.rel = &rel
		if n := len(rel.Children); n > 0 {
			cv.childCancel = t.s.LoadSubjects(rel.Children[:min(n, shownChildren)], func(subjects []string) {
				if bytes.Equal(cv.id, id) {
					cv.childSubjects = subjects
				}
			})
		}
	})
}

// containedLabel is the chip of a ref named in full that contains a
// commit, as the history labels it.
func containedLabel(t *tab, name string) refLabel {
	st := t.s.State
	switch {
	case strings.HasPrefix(name, "refs/heads/"):
		kind := labelBranch
		if name == st.Head.Target && !st.Head.Detached {
			kind = labelCurrent
		}
		return refLabel{strings.TrimPrefix(name, "refs/heads/"), kind}
	case strings.HasPrefix(name, "refs/tags/"):
		return refLabel{strings.TrimPrefix(name, "refs/tags/"), labelTag}
	}
	return refLabel{strings.TrimPrefix(name, "refs/remotes/"), labelRemote}
}

// loadCommitPull asks GitHub or GitLab once per commit shown, when the settings
// turn pull requests on, for the pull request that brought it in.
func (a *App) loadCommitPull(t *tab, cv *commitView, d *session.CommitDetail) {
	if !a.Settings.PullRequests || cv.noPull || cv.pullAsked || a.Runner == nil && a.forgeFind == nil {
		return
	}
	cv.pullAsked, cv.pullWaiting = true, true
	id := cv.id
	subject, _ := messageParts(d)
	cancel := t.s.LoadCommitPull(a.findForge(), id, string(subject), func(res session.CommitPullResult) {
		if !bytes.Equal(cv.id, id) {
			return
		}
		cv.pull, cv.pullWaiting = res, false
		// A missing gh is the branches view's to say.
		if res.Err != nil && !errors.Is(res.Err, forge.ErrNoCLI) {
			a.forgeNotice(res.Err)
		}
	})
	if cv.pullWaiting {
		cv.pullCancel = cancel
	}
}

// stopPull stops asking for the commit's pull request, so that showing
// it again asks again.
func (cv *commitView) stopPull() {
	if cv.pullWaiting && cv.pullCancel != nil {
		cv.pullCancel()
	}
	if cv.pullWaiting {
		cv.pullAsked, cv.pullWaiting = false, false
	}
	cv.pullCancel = nil
}

// The sizes of the people's pictures: the author's and committer's, a
// co-author's, beside one line, and an author's in a history row.
const (
	avatarMeta  = 28
	avatarSmall = 18
	avatarRow   = 16
)

// personLine shows a person: their picture, their name, which shows the
// history of their commits, their email, and under them what more is
// given.
func (a *App) personLine(c *ui.Context, t *tab, name, email string, size float32, more func()) {
	th := c.Theme()
	name, email = redact.Control(name), redact.Control(email)
	ui.Row(c).Gap(spaceS).AlignItems(ui.Center).Grow(1).Shrink(1).MinWidth(0).Children(func() {
		if a.avatar(c, t, name, email, size).Cursor(ui.CursorPointer).Tooltip(locales.CommitShowPersonHistory(name)).Clicked() {
			a.showPersonHistory(t, email)
		}
		ui.Column(c).Gap(space2XS).Grow(1).Shrink(1).MinWidth(0).Children(func() {
			ui.Row(c).Gap(spaceS).Wrap().AlignItems(ui.Center).Children(func() {
				if clickable(ui.Text(c, name).FontSize(textSmall).FontWeight(600)).Tooltip(locales.CommitShowPersonHistory(name)).Clicked() {
					a.showPersonHistory(t, email)
				}
				ui.Text(c, email).FontSize(textSmall).TextColor(th.TextMuted).Selectable()
			})
			if more != nil {
				more()
			}
		})
	})
}

// showPersonHistory shows the history of the commits the person with
// email authored, through the history's author filter, where it can be
// seen and cleared. The email in brackets matches it whole.
func (a *App) showPersonHistory(t *tab, email string) {
	hf := &t.hist.filter
	hf.author = "<" + email + ">"
	hf.open = true
	t.sel = "history"
	a.applyHistoryFilter(t)
}

// commitLinks lists commits by their short ids and subjects, once read;
// a click goes to one in the history.
func (a *App) commitLinks(c *ui.Context, t *tab, ids [][]byte, subjects []string, tip string) {
	th := c.Theme()
	ui.Column(c).Gap(space2XS).Grow(1).Shrink(1).MinWidth(0).Children(func() {
		for i, id := range ids {
			c.Key(i)
			row := ui.Row(c).Gap(spaceS).AlignItems(ui.Center).FillWidth().MinWidth(0).Cursor(ui.CursorPointer).Focusable().Role(ui.RoleButton).Tooltip(tip)
			row.Children(func() {
				h := ui.Text(c, shortID(id)).Font("monospace").FontSize(textSmall).Shrink(0)
				if row.Hovered() {
					h.Underline().UnderlineOffset(3)
				}
				if i < len(subjects) && subjects[i] != "" {
					ui.Text(c, redact.Control(subjects[i])).FontSize(textSmall).TextColor(th.TextMuted).SingleLine().Grow(1).Shrink(1).MinWidth(0)
				}
			})
			if row.Clicked() {
				t.sel = "history"
				a.chooseCommit(t, id)
			}
		}
	})
}

// since says how long before now t was, as words, for a tooltip beside
// the date itself.
func since(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < 60*24*time.Hour:
		return ago(now, t)
	case d < 365*24*time.Hour:
		return locales.TimeMonthsAgo(int(d / (30 * 24 * time.Hour)))
	}
	return locales.TimeYearsAgo(int(d / (365 * 24 * time.Hour)))
}

func signatureTime(p object.Signature) time.Time {
	return time.Unix(p.When, 0).In(time.FixedZone("", int(p.Offset)*60))
}

func samePerson(a, b object.Signature) bool {
	return bytes.Equal(a.Name, b.Name) && bytes.Equal(a.Email, b.Email) && a.When == b.When && a.Offset == b.Offset
}

// sigKindName names the kind of key a signature was made with, "" when
// it is not known.
func sigKindName(k object.SigKind) string {
	switch k {
	case object.SigSSH:
		return locales.CommitSigKind("ssh")
	case object.SigOpenPGP:
		return locales.CommitSigKind("openpgp")
	case object.SigX509:
		return locales.CommitSigKind("x509")
	}
	return ""
}

// commitSigText says what git made of a signed commit's signature.
func commitSigText(c *ui.Context, cv *commitView, d *session.CommitDetail, restricted bool) {
	switch {
	case restricted:
		ui.Text(c, locales.CommitSigNotTrusted()).FontSize(textSmall).TextColor(c.Theme().TextMuted)
	case d.Sig.Code == 0 && d.SigErr == nil:
		sigText(c, plainInk(c.Theme()), nil)
	default:
		r := &cv.sig
		r.st, r.err = d.Sig, d.SigErr
		sigText(c, plainInk(c.Theme()), r)
	}
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
	ui.Row(c).Padding(rowPadY, insetX).Gap(spaceM).AlignItems(ui.Center).Children(func() {
		count := ui.Text(c, locales.CommitFilesChanged(f.Len())).FontSize(textSmall).TextColor(th.TextMuted)
		if onCopy != nil && clickable(count).Tooltip(locales.CommitCopyPatch()).Clicked() {
			onCopy()
		}
		lineCounts(c, int(d.Sum.Added), int(d.Sum.Deleted))
	})
	cv.files.Selected = &cv.file
	cv.files.Label = func(i int) string {
		return fileLabel(fileStatusWord(f.Status(i)), commitFileName(f, i), lineAt(d.Lines, i))
	}
	ui.List(c, &cv.files, f.Len()).Rows(func(r ui.ListRow) {
		i, k := r.Index, rowInk(r)
		ui.Row(c).Padding(rowPadY, insetX).Gap(spaceM).AlignItems(ui.Center).FillWidth().Children(func() {
			ui.Text(c, string(f.Status(i))).Font("monospace").FontSize(textSmall).Width(14).TextColor(fileStatusColor(k, f.Status(i)))
			fileIcon(c, string(f.Path(i)))
			shown, why := showPath(commitFileName(f, i))
			txt := pathLabel(c, shown).Grow(1)
			if why != "" {
				txt.TextColor(k.Warning).Tooltip(why)
			}
			inkFileLines(c, k, lineAt(d.Lines, i))
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

func fileStatusColor(k ink, st byte) ui.Color {
	switch st {
	case 'A':
		return k.Success
	case 'D':
		return k.Danger
	case 'R', 'C':
		return k.Accent
	}
	return k.Warning
}

func (a *App) commitPatch(c *ui.Context, t *tab, cv *commitView, d *session.CommitDetail) {
	f := d.Files
	if f == nil || cv.file < 0 || cv.file >= f.Len() {
		emptyPane(c)
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
			pathLabel(c, shown).Bold().Grow(1)
			fileLines(c, lineAt(d.Lines, cv.file))
			if closeButton(c, locales.CommitClosePatch()).Clicked() {
				cv.file = -1
			}
		})
		a.diffBar(c, false, func() {
			if ui.Button(c, locales.DiffOpenInTool()).Disabled(t.s.Queue == nil || len(d.Parents) == 0).Clicked() {
				t.s.OpenDiffTool(session.DiffToolRequest{Paths: paths, From: d.Parents[0], To: d.OID, Own: a.ownDiffTool(shortID(d.Parents[0]), shortID(d.OID))}, a.toolDone)
			}
		})
		a.pagerNotice(c, &cv.pager, cv.patch, cv.patchLoading, "", cv.loadPatch)
		a.patchBody(c, t, &cv.pv, cv.patch, nil)
	})
}

// sigText shows a verdict on a signature, colored by what it means: one
// that checks from a key git trusts, one that checks from another key,
// and one that does not check.
func sigText(c *ui.Context, k ink, r *sigResult) {
	if r == nil {
		ui.Text(c, locales.CommitSigCheckingVerdict()).FontSize(textSmall).TextColor(k.Muted)
		return
	}
	// Why it was not checked is git's own word, often long: a popover
	// holds it.
	why := func(short, reason string) {
		ui.Row(c).Gap(spaceXS).AlignItems(ui.Center).Children(func() {
			ui.Text(c, short).FontSize(textSmall).TextColor(k.Muted)
			if reason != "" {
				infoButton(c, &r.info, locales.CommitSigWhy(), reason, k.Warning)
			}
		})
	}
	if r.err != nil {
		why(locales.CommitSigNotChecked(), errText(r.err))
		return
	}
	if r.st.Code == signature.CannotCheck {
		why(locales.CommitSigCannotCheck(), redact.Shown(r.st.Reason))
		return
	}
	st := r.st
	color := k.Warning
	switch st.Code {
	case signature.Good:
		color = k.Success
	case signature.Bad, signature.Revoked:
		color = k.Danger
	case signature.None:
		color = k.Muted
	}
	ui.Text(c, verdictText(st)).FontSize(textSmall).TextColor(color).Selectable()
}

// verdictText says git's verdict on a signature, naming the signer when
// the checking program did.
func verdictText(st signature.Status) string {
	by := func(anon func() string, named func(string) string) string {
		if st.Signer == "" {
			return anon()
		}
		return named(redact.Control(st.Signer))
	}
	switch st.Code {
	case signature.Good:
		return by(locales.CommitSigGood, locales.CommitSigGoodBy)
	case signature.Bad:
		return by(locales.CommitSigBad, locales.CommitSigBadBy)
	case signature.Untrusted:
		return by(locales.CommitSigUntrusted, locales.CommitSigUntrustedBy)
	case signature.Expired:
		return by(locales.CommitSigExpired, locales.CommitSigExpiredBy)
	case signature.KeyExpired:
		return by(locales.CommitSigExpiredKey, locales.CommitSigExpiredKeyBy)
	case signature.Revoked:
		return by(locales.CommitSigRevoked, locales.CommitSigRevokedBy)
	}
	return locales.CommitSigNone()
}
