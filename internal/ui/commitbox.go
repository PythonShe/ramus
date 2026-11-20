package ui

import (
	"errors"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/drafts"
	"ramus/internal/git/message"
	"ramus/internal/git/redact"
	"ramus/internal/git/refs"
	"ramus/internal/git/repo"
	"ramus/internal/git/status"
	"ramus/internal/git/write"
	"ramus/internal/session"
	"ramus/locales"
)

// commitState is a tab's commit editor.
type commitState struct {
	subject, body string
	// coAuthors are the co-authors chosen, as Name <email> separated by
	// commas, which is how a draft keeps them.
	coAuthors string
	pick      coAuthorPick
	amend     bool
	signOff   bool
	noVerify  bool
	// sign is the Sign box, set from commit.gpgSign as the setup is read.
	sign bool
	// setup is loaded once per tab, and again after a commit changed the
	// recent messages and HEAD's: reload asks for that, the setup before
	// staying shown meanwhile. loading is set while it is read.
	setup           *session.CommitSetup
	loading, reload bool
	// encoding is the setup's commit encoding, kept while the setup is
	// read again after a commit; encodingRead once it was read at all.
	encoding     string
	encodingRead bool
	busy         bool
	// running is the commit's entry in the log while it runs, for its
	// output.
	running *session.LogEntry
	err     string
	// errLog is the commit in the log that err tells of, for all it
	// printed; errChecked once the error bar was told the commit box
	// shows it. withoutHooks offers committing as last tried (lastRun)
	// without the hooks that may have failed it.
	errLog       *session.LogEntry
	errChecked   bool
	withoutHooks bool
	lastRun      commitRun
	// wasAmend is amend as of the last frame, to load HEAD's message
	// when amend is turned on over an empty editor; amendMsg is the
	// message so loaded, which turning amend off takes away again while
	// it is not edited; amendOf is the commit it is of, which the amend
	// replaces: the HEAD shown may still be the one before it, until what
	// follows a commit is read.
	wasAmend bool
	amendMsg string
	amendOf  string
	// recentMsg is the message the Recent menu put in the editor last,
	// which another may replace without asking.
	recentMsg string
	// track is how branch trackOf stood to its upstream, read for the
	// refs trackFor while Amend is ticked: amending a commit already
	// pushed takes a force push.
	track        session.Track
	trackFor     *refs.List
	trackCfg     configRead
	trackOf      string
	trackRead    string
	trackLoading bool
	// prep is what git prepared for the operation prepFor in progress,
	// once read, as of status prepStatus; opMsg is the message the editor
	// got from it, which goes again when the operation ends while it is
	// not edited.
	prep                  session.Prepared
	prepFor               repo.Operation
	prepStatus            *status.Status
	prepRead, prepLoading bool
	opMsg                 string
	confirm               confirmState
	// draftRead is set once the draft kept from the last run was read,
	// draftLoading meanwhile; kept is the draft last handed to the store.
	draftRead, draftLoading bool
	kept                    drafts.Draft
	// draftFrom is the key of an earlier version a draft was found
	// under, "" for the tab's own: kept again under the tab's key, the
	// draft leaves it, so it does not come back once committed.
	draftFrom string
}

// commitRun is how a commit goes beyond what the box says.
type commitRun struct {
	// push, when set, pushes the commit made with these options.
	push *write.PushOptions
	// noHooks commits this once without the hooks, as Skip hooks does.
	noHooks bool
}

// errLines bounds the lines of a failed commit's output the box shows;
// the log has them all.
const errLines = 4

// personsRE finds each person: a name, which may hold a comma or a
// semicolon, as "Doe, John" does, then an address, which ends it.
var personsRE = regexp.MustCompile(`[^<>\n]+<[^<>\s]+>`)

// coAuthorList reads co-authors written one per line or separated by
// commas or semicolons.
func coAuthorList(s string) []string {
	var out []string
	for _, m := range personsRE.FindAllString(s, -1) {
		// What separates a person from the one before starts the match.
		if p := strings.TrimSpace(strings.TrimLeft(m, ",; \t")); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// commitBox is the message editor under the changes list.
func (a *App) commitBox(c *ui.Context, t *tab) {
	th := c.Theme()
	cs := &t.commit
	if t.s.Queue == nil {
		return
	}
	if (cs.setup == nil || cs.reload) && !cs.loading {
		cs.loading, cs.reload = true, false
		t.s.LoadCommitSetup(func(setup session.CommitSetup) {
			cs.loading, cs.setup = false, &setup
			cs.encoding, cs.encodingRead = setup.Encoding, true
			cs.sign = setup.Sign
			cs.fillTemplate()
		})
	}
	a.loadDraft(t)
	followOperation(t)
	switch {
	case cs.amend && !cs.wasAmend && cs.untouched():
		// HEAD as it is now: a commit made in a terminal or a switch
		// since the setup was read moved it. It takes the template's
		// place, which an amend would otherwise make HEAD's message.
		t.s.HeadMessage(func(id, msg string, err error) {
			if err != nil || !cs.amend || !cs.untouched() {
				return
			}
			cs.subject, cs.body = message.Split(msg)
			cs.amendMsg, cs.amendOf = msg, id
		})
	case !cs.amend && cs.wasAmend && cs.amendMsg != "":
		if cs.holds(cs.amendMsg) {
			cs.subject, cs.body = "", ""
			cs.fillTemplate()
		}
		cs.amendMsg = ""
	}
	if !cs.amend {
		cs.amendOf = ""
	}
	cs.wasAmend = cs.amend
	followTrack(t)
	a.keepDraft(t)
	a.confirmView(c, &cs.confirm)
	if commitHidden(t) {
		return
	}
	if e := cs.errLog; e != nil && !cs.errChecked && !e.Running {
		// The box tells of the failure, with its way out; the error bar
		// tells it again only with one of its own, as a lock to remove.
		cs.errChecked = true
		if len(a.diagnose(t, e).fixes) == 0 {
			t.failSeen = e
		}
	}
	head := t.s.State.Head
	op := t.s.State.Operation
	ui.Column(c).Padding(spaceM, insetX).Gap(spaceS).Background(th.Surface).FillWidth().Children(func() {
		ui.Row(c).Gap(spaceS).AlignItems(ui.Center).Children(func() {
			ui.TextInput(c, &cs.subject).Label(locales.CommitBoxSummary()).Placeholder(locales.CommitBoxSummary()).Grow(1).Disabled(cs.busy)
			a.coAuthorButton(c, t)
			if cs.setup != nil && len(cs.setup.Recent) > 0 {
				recent := cs.setup.Recent
				ui.PopoverMenuButton(c, locales.CommitBoxRecent(), func(m *ui.Menu) {
					for _, msg := range recent {
						subject, _ := message.Split(msg)
						if m.Item(redact.Control(subject)).Chosen() {
							cs.chooseRecent(msg)
						}
					}
				})
			}
		})
		ui.TextArea(c, &cs.body).Label(locales.CommitBoxDescription()).Placeholder(locales.CommitBoxDescription()).Lines(2, 8).FillWidth().Disabled(cs.busy)
		coAuthorChips(c, cs)
		ui.Row(c).Gap(spaceL).Children(func() {
			// git amends nothing while a merge, cherry-pick or revert
			// waits for its commit.
			ui.Checkbox(c, &cs.amend, locales.CommitBoxAmend()).Disabled(cs.busy || head.Unborn || len(head.OID) == 0 || operationCommit(op))
			ui.Checkbox(c, &cs.signOff, locales.CommitBoxSignOff())
			if cs.setup != nil && cs.setup.CanSign {
				ui.Checkbox(c, &cs.sign, locales.CommitBoxSign())
			}
			ui.Checkbox(c, &cs.noVerify, locales.CommitBoxSkipHooks())
		})
		if cs.err != "" {
			a.commitError(c, t)
		}
		ready, why := commitWhyNot(t)
		upstream, pushed := amendsPushed(t)
		ui.Row(c).Gap(spaceM).Children(func() {
			branch := strings.TrimPrefix(head.Target, "refs/heads/")
			label := locales.CommitBoxCommitTo(redact.Control(branch))
			switch {
			case cs.busy:
				label = locales.CommitBoxCommitting()
			case cs.amend && pushed:
				label = locales.CommitBoxAmendPushed()
			case cs.amend:
				label = locales.CommitBoxAmendLast()
			case operationCommit(op):
				label = locales.CommitBoxCommitOperation(operationKey(op))
			case head.Detached:
				label = locales.CommitBoxCommit()
			}
			// A disabled button shows no tooltip of its own in the mygo
			// pinned: the row around it says why it is. egoist/mygo#1982
			// fixes that: once a mygo release with it is pinned, the row
			// goes and the tip is the button's.
			ui.Row(c).Grow(1).Tooltip(why).Children(func() {
				if ui.PrimaryButton(c, label).Description(why).Disabled(!ready).Grow(1).Clicked() {
					a.startCommit(t, commitRun{})
				}
			})
			second := locales.CommitBoxCommitAndPush()
			switch {
			case cs.amend && pushed:
				second = locales.CommitBoxAmendAndForcePush()
			case cs.amend:
				second = locales.CommitBoxAmendAndPush()
			}
			if ui.Button(c, second).Disabled(!ready || head.Detached).Clicked() {
				a.commitAndPush(t, upstream, pushed)
			}
		})
		if cs.busy {
			ui.Row(c).Gap(spaceS).AlignItems(ui.Center).FillWidth().Children(func() {
				ui.Spinner(c).FontSize(textSmall).Label(locales.CommitBoxCommitting())
				if line := runningLine(cs.running); line != "" {
					ui.Text(c, line).FontSize(textSmall).TextColor(th.TextMuted).SingleLine().TruncateMiddle().Shrink(1)
				}
			})
		}
	})
}

// commitError is why the last commit failed, in its last lines, with
// what can be done about it.
func (a *App) commitError(c *ui.Context, t *tab) {
	th := c.Theme()
	cs := &t.commit
	text, cut := lastLines(cs.err, errLines)
	ui.Text(c, text).FontSize(textSmall).TextColor(th.Danger).Selectable().MaxLines(errLines).FillWidth()
	inLog := cut && cs.errLog != nil
	if !cs.withoutHooks && !inLog {
		return
	}
	ui.Row(c).Gap(spaceS).Children(func() {
		if cs.withoutHooks && ui.Button(c, locales.CommitBoxWithoutHooks()).Disabled(cs.busy).Clicked() {
			// The push is decided again: Amend may have been ticked or
			// unticked since, which a force turns on.
			r := commitRun{noHooks: true}
			if cs.lastRun.push == nil {
				a.startCommit(t, r)
			} else {
				upstream, pushed := amendsPushed(t)
				a.commitAndPushRun(t, upstream, pushed, r)
			}
		}
		if inLog && ui.Button(c, locales.RepoShowInLog()).Clicked() {
			a.showLogEntry(t, cs.errLog)
		}
	})
}

// lastLines is the last n lines of s that hold something, and whether
// there were more: a hook prints its progress first and its verdict
// last.
func lastLines(s string, n int) (string, bool) {
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) <= n {
		return strings.Join(lines, "\n"), false
	}
	return strings.Join(lines[len(lines)-n:], "\n"), true
}

// runningLine is the last line a write running printed, "" none.
func runningLine(e *session.LogEntry) string {
	if e == nil || !e.Running {
		return ""
	}
	if e.Progress != "" {
		return e.Progress
	}
	for i := len(e.Output) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(e.Output[i]); l != "" {
			return l
		}
	}
	return ""
}

// holds reports whether the editor holds msg as it is.
func (cs *commitState) holds(msg string) bool {
	s, b := message.Split(msg)
	return s == cs.subject && b == cs.body
}

// untouched reports whether the editor holds nothing the user wrote:
// nothing, or the commit template.
func (cs *commitState) untouched() bool {
	if cs.subject == "" && cs.body == "" {
		return true
	}
	return cs.setup != nil && cs.setup.Template != "" && cs.holds(cs.setup.Template)
}

// chooseRecent puts recent message msg in the editor, asking first when
// that would replace a message the user wrote.
func (cs *commitState) chooseRecent(msg string) {
	fill := func() {
		cs.subject, cs.body = message.Split(msg)
		cs.recentMsg = msg
	}
	filled := func(m string) bool { return m != "" && cs.holds(m) }
	if cs.untouched() || filled(cs.recentMsg) || filled(cs.amendMsg) || filled(cs.opMsg) {
		fill()
		return
	}
	cs.confirm = confirmState{open: true, title: locales.CommitBoxReplaceTitle(), message: locales.CommitBoxReplaceMessage(),
		button: locales.CommitBoxReplace(), run: fill}
}

// fillTemplate puts the commit template in an empty editor, once the
// draft from the last run, which comes first, was read. Not while Amend
// is ticked: HEAD's message, still being read, goes there, and a setup
// read again after a commit lands as the user ticks it right after.
func (cs *commitState) fillTemplate() {
	if s := cs.setup; s != nil && !cs.amend && cs.draftRead && cs.subject == "" && cs.body == "" && s.Template != "" {
		cs.subject, cs.body = message.Split(s.Template)
	}
}

// operationCommit reports whether operation op stopped waiting for the
// commit that ends it, which the commit box makes.
func operationCommit(op repo.Operation) bool {
	return op == repo.Merge || op == repo.CherryPick || op == repo.Revert
}

// operationKey is op as the catalog selects on it: a select case is a
// word.
func operationKey(op repo.Operation) string {
	if op == repo.CherryPick {
		return "cherryPick"
	}
	return string(op)
}

// followOperation reads what git prepared for the operation in
// progress, and puts the message it prepared for a merge, cherry-pick
// or revert in an editor holding nothing written; once the operation
// ends, that message goes again unless edited.
func followOperation(t *tab) {
	cs := &t.commit
	op := t.s.State.Operation
	if !operationCommit(op) && op != repo.Rebase {
		if cs.opMsg != "" && cs.holds(cs.opMsg) {
			cs.subject, cs.body = "", ""
			cs.fillTemplate()
		}
		cs.prepFor, cs.prepStatus, cs.prepRead, cs.opMsg = "", nil, false, ""
		return
	}
	// Read again as the status is: an operation can end and another of
	// its kind start between two reads of the state.
	st := t.s.State.Status
	if cs.prepLoading || cs.setup == nil || (cs.prepFor == op && cs.prepStatus == st) {
		return
	}
	if cs.prepFor != op {
		cs.prepRead = false
		if operationCommit(op) {
			cs.amend = false
		}
	}
	cs.prepFor, cs.prepStatus, cs.prepLoading = op, st, true
	t.s.LoadPrepared(cs.setup.Comment, func(p session.Prepared) {
		cs.prepLoading = false
		if t.s.State.Operation != op {
			cs.prepFor = "" // ended or changed meanwhile: read again
			return
		}
		cs.prep, cs.prepRead = p, true
		// The message of an operation before, not edited, gives way: one
		// can end and the next stop between two reads of the state.
		unwritten := cs.opMsg == "" && cs.untouched() || cs.opMsg != "" && cs.holds(cs.opMsg)
		if operationCommit(op) && p.Message != "" && unwritten {
			cs.subject, cs.body = message.Split(p.Message)
			cs.opMsg = p.Message
		}
	})
}

// commitHidden reports whether the commit box is left out: while an
// am, or a rebase but where it stopped to edit a commit, waits on its
// continue, which makes the commit.
func commitHidden(t *tab) bool {
	cs := &t.commit
	switch t.s.State.Operation {
	case repo.Am:
		return true
	case repo.Rebase:
		return !(cs.prepRead && cs.prepFor == repo.Rebase && cs.prep.Edit)
	}
	return false
}

// followTrack reads how the branch stands to its upstream while Amend
// is ticked, as the refs change.
func followTrack(t *tab) {
	cs := &t.commit
	head := t.s.State.Head
	cfg := t.configRead()
	if !cs.amend || cs.trackLoading || (cs.trackFor == t.s.State.Refs && cs.trackCfg == cfg && cs.trackOf == head.Target) {
		return
	}
	// Until the new read lands, the old one says nothing: a commit
	// since may have left the upstream behind.
	cs.trackFor, cs.trackCfg, cs.trackOf, cs.trackLoading, cs.trackRead = t.s.State.Refs, cfg, head.Target, true, ""
	t.s.LoadTrack(func(tr session.Track) {
		cs.trackLoading = false
		cs.track, cs.trackRead = tr, tr.Branch
	})
}

// amendsPushed reports whether Amend is ticked over a commit already on
// the branch's upstream, which it names: pushing the amend takes a
// force.
func amendsPushed(t *tab) (upstream string, pushed bool) {
	cs := &t.commit
	h := t.s.State.Head
	tr := cs.track
	if !cs.amend || h.Detached || cs.trackRead != h.Target || tr.Upstream == "" || tr.Gone || tr.Ahead > 0 {
		return "", false
	}
	return strings.TrimPrefix(tr.Upstream, "refs/remotes/"), true
}

// loadDraft reads the message left unfinished in the repository when
// the app last ran, off the main thread, into an editor still empty.
func (a *App) loadDraft(t *tab) {
	cs := &t.commit
	if cs.draftRead || cs.draftLoading {
		return
	}
	if a.Drafts == nil {
		cs.draftRead = true
		return
	}
	if t.real == "" {
		// Keyed as the folder is spelled on disk, which resolveTab is
		// working out: one folder reached by two spellings has one draft.
		return
	}
	cs.draftLoading = true
	store, key, given, update := a.Drafts, t.key(), t.s.Path, a.Update
	a.goWork(t, func() {
		d, err := store.Load(key)
		from := ""
		if err == nil && d.Empty() {
			d, from, err = olderDraft(store, key, given)
		}
		update(func() {
			cs.draftLoading, cs.draftRead = false, true
			if err != nil {
				a.tell(t, LoadNotice(err))
			}
			if cs.subject == "" && cs.body == "" && cs.coAuthors == "" {
				cs.subject, cs.body, cs.coAuthors = d.Subject, d.Body, d.CoAuthors
			}
			cs.kept, cs.draftFrom = d, from
			cs.fillTemplate()
		})
	})
}

// olderDraft looks for the draft of the repository with key, opened as
// given, under the keys earlier versions kept it by: the path with its
// symbolic links resolved but spelled as given (0.6), and the path as
// given. It returns the draft and the key it was under, "" for none. It
// reads the disk.
func olderDraft(store *drafts.Store, key, given string) (drafts.Draft, string, error) {
	keys := []string{given}
	if real, err := filepath.EvalSymlinks(given); err == nil {
		keys = append(keys, filepath.Clean(real))
	}
	for _, k := range keys {
		if k == key {
			continue
		}
		if d, err := store.Load(k); err != nil || !d.Empty() {
			return d, k, err
		}
	}
	return drafts.Draft{}, "", nil
}

// keepDraft hands the message to the store when it changed. The commit
// template alone is not a draft, nor a message the app put in the editor
// for Amend or an operation, unedited: read back after a restart without
// what brought it, it would be committed as a new commit's.
func (a *App) keepDraft(t *tab) {
	cs := &t.commit
	if a.Drafts == nil || !cs.draftRead {
		return
	}
	d := drafts.Draft{Subject: cs.subject, Body: cs.body, CoAuthors: cs.coAuthors}
	if loaded := func(m string) bool { return m != "" && cs.holds(m) }; loaded(cs.amendMsg) || loaded(cs.opMsg) {
		d.Subject, d.Body = "", ""
	}
	if s := cs.setup; s != nil && s.Template != "" && d.CoAuthors == "" && message.Join(d.Subject, d.Body) == message.Join(message.Split(s.Template)) {
		d = drafts.Draft{}
	}
	if d == cs.kept {
		return
	}
	cs.kept = d
	if cs.draftFrom != "" {
		a.Drafts.Move(cs.draftFrom, t.key(), d)
		cs.draftFrom = ""
	} else {
		a.Drafts.Keep(t.key(), d)
	}
}

// commitReady reports whether the commit box can commit now.
func commitReady(t *tab) bool {
	ready, _ := commitWhyNot(t)
	return ready
}

// commitWhyNot reports whether the commit box can commit now, and when
// not, why in words the user can act on; "" while it waits on what is
// being read or a commit running.
func commitWhyNot(t *tab) (ready bool, why string) {
	cs := &t.commit
	// The model is built off the main thread; until the first one is,
	// nothing counts as staged.
	m := t.cv.model
	// The setup names the encoding the message is written in.
	if cs.busy || !cs.encodingRead || m == nil || t.s.Queue == nil || commitHidden(t) {
		return false, ""
	}
	op := t.s.State.Operation
	switch {
	case t.s.State.Status != nil && t.s.State.Status.Unmerged() > 0:
		return false, locales.CommitBoxWhyConflicts()
	// A merge's commit records the branch merged in, though the result
	// may hold no change.
	case m.staged == 0 && !cs.amend && op != repo.Merge:
		return false, locales.CommitBoxWhyNothingStaged()
	case strings.TrimSpace(cs.subject) == "":
		return false, locales.CommitBoxWhySummary()
	}
	return true, ""
}

// commitAndPush commits and pushes; an amend of a commit already on
// upstream, after asking, with a force push.
func (a *App) commitAndPush(t *tab, upstream string, pushed bool) {
	a.commitAndPushRun(t, upstream, pushed, commitRun{})
}

// commitAndPushRun is commitAndPush of r, whose push it sets.
func (a *App) commitAndPushRun(t *tab, upstream string, pushed bool, r commitRun) {
	if !pushed {
		r.push = &write.PushOptions{}
		a.startCommit(t, r)
		return
	}
	t.commit.confirm = confirmState{open: true, title: locales.RefsForcePushTitle(redact.Control(upstream)),
		message: locales.RefsForcePushMessage(), button: locales.CommitBoxAmendForcePush(),
		run: func() {
			r.push = &write.PushOptions{ForceWithLease: true}
			a.startCommit(t, r)
		}}
}

// startCommit checks line endings of what is staged, asks when one
// stands out, and commits.
func (a *App) startCommit(t *tab, r commitRun) {
	cs := &t.commit
	cs.busy, cs.err, cs.errLog, cs.withoutHooks = true, "", nil, false
	t.s.CheckLineEnds(func(le session.LineEnds) {
		if le.Err != nil {
			// A check that fails does not stop a commit, but says so.
			a.tell(t, locales.CommitBoxLineEndsNotChecked(errText(le.Err)))
		}
		if le.Err != nil || len(le.Findings) == 0 {
			a.commit(t, r)
			return
		}
		cs.confirm = confirmState{
			open:    true,
			title:   locales.CommitBoxLineEndsTitle(len(le.Findings)),
			message: lineEndMessage(le),
			button:  locales.CommitBoxCommitAnyway(),
			// Committing loses nothing: no Danger color.
			safe: true,
			run:  func() { a.commit(t, r) },
		}
		cs.busy = false
	})
}

func lineEndMessage(le session.LineEnds) string {
	var b strings.Builder
	for i, f := range le.Findings {
		if i == 5 {
			b.WriteString(locales.ChangesAndMore(len(le.Findings)-5) + "\n")
			break
		}
		shown, _ := showPath(f.Path)
		switch {
		case f.AddsCRLF && f.Attr != "":
			b.WriteString(locales.CommitBoxAddsCRLFAttr(shown, f.Attr))
		case f.AddsCRLF:
			b.WriteString(locales.CommitBoxAddsCRLF(shown))
		case f.Attr != "":
			b.WriteString(locales.CommitBoxMixedEndingsAttr(shown, f.Attr))
		default:
			b.WriteString(locales.CommitBoxMixedEndings(shown))
		}
		b.WriteByte('\n')
	}
	setting := func(v string) string {
		if v == "" {
			return locales.CommitBoxNotSet()
		}
		return v
	}
	b.WriteString("\n" + locales.CommitBoxEndingSettings(setting(le.AutoCRLF), setting(le.EOL)))
	if !le.Complete {
		b.WriteString("\n" + locales.CommitBoxNotAllChecked())
	}
	return b.String()
}

func (a *App) commit(t *tab, r commitRun) {
	cs := &t.commit
	msg := message.Join(cs.subject, cs.body)
	if s := cs.setup; s != nil && s.Template != "" {
		msg = message.StripTemplate(msg, s.Template, s.Comment)
	}
	opt := write.CommitOptions{Message: msg, Amend: cs.amend, SignOff: cs.signOff, NoVerify: cs.noVerify || r.noHooks, CoAuthors: coAuthorList(cs.coAuthors)}
	opt.Encoding = cs.encoding
	if cs.amend {
		// The commit whose message the editor took, when it took one.
		opt.Seen = cs.amendOf
	}
	if s := cs.setup; s != nil && s.CanSign && cs.sign != s.Sign {
		opt.Sign = write.SignNo
		if cs.sign {
			opt.Sign = write.SignYes
		}
	}
	cs.busy, cs.lastRun = true, r
	seen := t.s.Seen()
	written := struct{ subject, body, coAuthors string }{cs.subject, cs.body, cs.coAuthors}
	// A commit that ends a merge, cherry-pick or revert: undoing it
	// moves the branch back but leaves the operation ended, and its
	// message committed again would make a plain commit of it.
	ends := operationCommit(t.s.State.Operation)
	var entry *session.LogEntry
	err := t.s.Commit(opt, r.push, func(err error) {
		cs.busy, cs.running = false, nil
		var notPushed *session.NotPushedError
		if err != nil && !errors.As(err, &notPushed) {
			var hooks *session.HookError
			cs.err, cs.errLog, cs.errChecked = failure(err), entry, false
			cs.withoutHooks = errors.As(err, &hooks)
			return
		}
		// A force push replaces the commit on the upstream itself.
		if opt.Amend && (r.push == nil || !r.push.ForceWithLease) {
			a.checkPushed(t, seen)
		}
		// Skip hooks is for the commit it was ticked for, as Amend is:
		// left on, it would skip every hook after it unseen.
		cs.subject, cs.body, cs.coAuthors, cs.amend, cs.noVerify, cs.err = "", "", "", false, false, ""
		if notPushed != nil {
			// The commit is made: its message goes, or a second click
			// would commit or amend it again.
			reason := failure(notPushed.Err)
			if errors.Is(notPushed.Err, session.ErrNoRemote) {
				reason = locales.CommitBoxNoRemote()
			}
			cs.err = locales.CommitBoxNotPushed(reason)
		} else {
			var id uint64
			if entry != nil {
				id = entry.Recorded()
			}
			a.toastCommit(t, opt, r.push == nil && !ends, id, written.subject, written.body, written.coAuthors)
		}
		// Recent messages and HEAD's message changed.
		cs.reload = true
	})
	if err != nil {
		cs.busy, cs.err = false, failure(err)
		return
	}
	entry = lastEntry(t)
	cs.running = entry
}

// toastCommit tells of a commit made, with Undo while it is the write
// Undo would take back: entry is the History entry the commit recorded,
// 0 for none. One pushed is not taken back from the remote, and one
// that ended a merge, cherry-pick or revert would not be that again, so
// neither has Undo.
func (a *App) toastCommit(t *tab, opt write.CommitOptions, undoable bool, entry uint64, subject, body, coAuthors string) {
	title := locales.CommitBoxCommitted(firstLine(opt.Message))
	if opt.Amend {
		title = locales.CommitBoxAmended(firstLine(opt.Message))
	}
	toast := ui.Toast{ID: "commit", Title: title}
	q := t.s.Queue
	if q != nil && undoable && entry != 0 {
		toast.Action = locales.CommitBoxUndo()
		toast.OnAction = func() {
			if !a.undoWrite(t, q, entry) {
				return
			}
			// The commit's changes are staged again; its message comes
			// back with them, to commit them again, unless the user has
			// started another.
			cs := &t.commit
			if !opt.Amend && cs.untouched() && cs.coAuthors == "" {
				cs.subject, cs.body, cs.coAuthors = subject, body, coAuthors
			}
		}
	}
	a.tabToast(t, toast)
}

// firstLine is a message's first line.
func firstLine(msg string) string {
	s, _ := message.Split(msg)
	return s
}
