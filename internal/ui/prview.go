package ui

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"ramus/internal/forge"
	"ramus/internal/git/diff"
	"ramus/internal/git/redact"
	"ramus/internal/session"
	"ramus/locales"
)

// sidePulls is the sidebar key of the pull requests page.
const sidePulls = "pulls"

// prListMaxAge is how old the list may be before the page, shown again,
// reads it again; nothing reads on a timer.
const prListMaxAge = 2 * time.Minute

// prState is a tab's pull requests page: the open ones, and the one
// under review.
type prState struct {
	listing bool
	// listAt is when the list was read, zero to read it again.
	listAt  time.Time
	repo    forge.Repo
	list    []forge.Listed
	more    bool
	listErr string
	table   tableState
	chosen  int

	// rv is the pull request under review, nil for the list.
	rv *prReview
}

func newPRState() prState { return prState{chosen: -1} }

// prReview is one pull request under review: what the host says of it,
// its commits fetched, and the review being written.
type prReview struct {
	n    int
	repo forge.Repo

	loading bool
	read    bool
	r       forge.Review
	err     string

	// fetchedFor is the head and base branch the commits were fetched
	// for (fetchKey); a review read with others fetches again.
	fetchedFor string
	// mismatched counts the fetches in a row whose head was not the one
	// the review was read at, which reread it.
	mismatched int
	fetching   bool
	// fetchN numbers the fetches: one left behind as the page was left
	// goes on to nothing.
	fetchN    int
	fetchErr  string
	rng       session.PullRange
	detail    *session.CommitDetail
	cancelCmp func()

	page  int // 0 files, 1 conversation
	files ui.ListState
	file  int
	listW float32

	patchFor     string
	patch        session.PatchResult
	pv           patchView
	pager        patchPager
	patchLoading bool
	loadPatch    func()
	// diffW is the viewer's width in the last frame, which the comments
	// under its lines are measured at.
	diffW float32

	drafts []forge.Draft
	// draftsAt are the commits the drafts' lines are of: a head read
	// since moves lines the drafts do not follow.
	draftsAt forge.Revs
	// dropDraft is the draft to delete, from 1, once no note under the
	// patch's lines is being built.
	dropDraft int
	compose   *prCompose
	replies   map[string]*string
	opened    map[string]bool // resolved threads shown whole
	sending   bool

	conv    ui.ListState
	comment string
	submit  prSubmit
}

// prCompose is a comment being written on lines of a file.
type prCompose struct {
	path, oldPath string
	side          forge.Side
	start, line   int
	across        int // see forge.Draft
	body          string
	// oid is the commit compared, whose lines these are.
	oid string
}

func (cp *prCompose) draft(body string) forge.Draft {
	return forge.Draft{Path: cp.path, OldPath: cp.oldPath, Side: cp.side, Line: cp.line, StartLine: cp.start, Across: cp.across, Body: body}
}

// prSubmit is the dialog that submits the review.
type prSubmit struct {
	open    bool
	verdict int
	body    string
	err     string
}

// errForge says a failure of the host's tool in the app's words where it
// has them.
func errForge(err error) string {
	switch {
	case errors.Is(err, forge.ErrNoCLI):
		return locales.PullsNoCLI()
	case errors.Is(err, forge.ErrNoReview):
		return locales.PrsNoReview()
	case errors.Is(err, forge.ErrHeadMoved):
		return locales.PrsHeadMoved()
	case errors.Is(err, forge.ErrUnknownHost):
		return locales.PrsUnknownHost()
	case errors.Is(err, forge.ErrNotFound):
		return locales.PrsNotFound()
	case errors.Is(err, forge.ErrTimeout):
		return locales.PrsTimeout()
	case errors.Is(err, session.ErrReadOnly):
		return locales.PrsReadOnly()
	}
	var login *forge.LoginError
	if errors.As(err, &login) {
		return locales.PrsNotLoggedIn(login.Program, login.Host)
	}
	return errText(err)
}

// hasPulls reports whether the tab offers its pull requests.
func (a *App) hasPulls(t *tab) bool {
	if !a.Settings.PullRequests || a.Runner == nil && a.forgeFind == nil {
		return false
	}
	_, ok := t.s.ReviewRepo()
	return ok
}

// kindName is how a message selects on a host's kind.
func kindName(k forge.Kind) string {
	if k == forge.GitLab {
		return "gitlab"
	}
	return "github"
}

func (a *App) pullsView(c *ui.Context, t *tab) {
	if rv := t.pr.rv; rv != nil {
		a.prReviewView(c, t, rv)
		return
	}
	a.prListView(c, t)
}

func (a *App) loadPRList(t *tab) {
	ps := &t.pr
	if ps.listing || !ps.listAt.IsZero() && a.Now().Sub(ps.listAt) < prListMaxAge {
		return
	}
	ps.listing = true
	t.s.OpenPulls(a.findForge(), func(res session.OpenPullsResult) {
		ps.listing, ps.listAt = false, a.Now()
		ps.repo, ps.list, ps.more, ps.listErr = res.Repo, res.List, res.More, ""
		if res.Err != nil {
			ps.listErr = errForge(res.Err)
		}
	})
}

func (a *App) prListView(c *ui.Context, t *tab) {
	ps := &t.pr
	a.loadPRList(t)
	th := c.Theme()
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(bannerPadY, insetX).Gap(spaceL).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.PrsSection(kindName(ps.repo.Kind))).Bold()
			ui.Text(c, ps.repo.Path).TextColor(th.TextMuted).Grow(1).SingleLine()
			if ui.Button(c, locales.MenuRefresh()).Disabled(ps.listing).Clicked() {
				ps.listAt = time.Time{}
			}
		})
		list := ps.list
		switch {
		case ps.listErr != "":
			errorMessage(c, ps.listErr)
			return
		case list == nil && ps.listing:
			loading(c, "")
			return
		case len(list) == 0:
			emptyState(c, locales.PrsNone(kindName(ps.repo.Kind)))
			return
		}
		if ps.more {
			ui.Text(c, locales.PrsMore(kindName(ps.repo.Kind), len(list))).FontSize(textSmall).TextColor(th.TextMuted).Padding(0, insetX, spaceS, insetX)
		}
		ps.table.Selected = &ps.chosen
		ps.table.Label = func(i int) string {
			return locales.PrsRow(list[i].Ref(ps.repo.Kind), redact.Control(list[i].Title), list[i].Author)
		}
		var act func()
		ps.table.CellMenu = func(m *ui.Menu, i, _ int) {
			if m.Item(locales.PrsOpenInBrowser()).Chosen() {
				url := list[i].URL
				act = func() { a.openURL = url }
			}
		}
		cols := []ui.TableColumn{
			{Title: "#", ID: "number", Width: 64, MinWidth: 40},
			{Title: locales.ColumnTitle(), ID: "title", MinWidth: 120},
			{Title: locales.ColumnAuthor(), ID: "author", Width: 120, MinWidth: 60},
			{Title: locales.ColumnBranch(), ID: "branch", Width: 160, MinWidth: 60},
			{Title: locales.ColumnReview(), ID: "review", Width: 150, MinWidth: 60},
			{Title: locales.ColumnLines(), ID: "lines", Width: 100, MinWidth: 60},
			{Title: locales.ColumnUpdated(), ID: "updated", Width: 100, MinWidth: 60},
		}
		now := a.Now()
		a.savedTable(c, "pulls", &ps.table, cols, len(list), func(i, col int) {
			p := list[i]
			k := ps.table.ink(c, i)
			switch col {
			case 0:
				mutedCell(c, k, p.Ref(ps.repo.Kind))
			case 1:
				ui.Row(c).Gap(spaceS).AlignItems(ui.Center).Children(func() {
					ui.Text(c, redact.Control(p.Title)).SingleLine()
					if p.Draft {
						mutedCell(c, k, locales.PullsDraft())
					}
				})
			case 2:
				mutedCell(c, k, p.Author)
			case 3:
				mutedCell(c, k, p.Branch)
			case 4:
				ui.Text(c, prStatus(p.Decision, p.Checks)).FontSize(textSmall).TextColor(decisionColor(k, p.Decision)).SingleLine()
			case 5:
				inkLineCounts(c, k, p.Added, p.Deleted)
			case 6:
				mutedCell(c, k, ago(now, p.Updated))
			}
		}).Label(locales.PrsList(kindName(ps.repo.Kind))).Grow(1)
		if act != nil {
			act()
		}
		if ps.chosen >= 0 && ps.chosen < len(list) {
			a.openReview(t, ps.repo, list[ps.chosen].Number)
			ps.chosen = -1
		}
	})
}

// prStatus says what the reviews and checks of a pull request add up to.
func prStatus(d forge.ReviewState, ch forge.Checks) string {
	var parts []string
	if s := decisionText(d); s != "" {
		parts = append(parts, s)
	}
	switch ch {
	case forge.ChecksPending:
		parts = append(parts, locales.PullsChecksRunning())
	case forge.ChecksPassing:
		parts = append(parts, locales.PullsChecksPassed())
	case forge.ChecksFailing:
		parts = append(parts, locales.PullsChecksFailed())
	}
	return strings.Join(parts, ", ")
}

func decisionText(d forge.ReviewState) string {
	switch d {
	case forge.ReviewApproved:
		return locales.PrsDecision("approved")
	case forge.ReviewChangesRequested:
		return locales.PrsDecision("changes")
	case forge.ReviewRequired:
		return locales.PrsDecision("required")
	}
	return ""
}

func decisionColor(k ink, d forge.ReviewState) ui.Color {
	switch d {
	case forge.ReviewApproved:
		return k.Success
	case forge.ReviewChangesRequested:
		return k.Danger
	}
	return k.Muted
}

// openReview shows pull request n of repo for review.
func (a *App) openReview(t *tab, repo forge.Repo, n int) {
	t.pr.rv = &prReview{n: n, repo: repo, file: -1, listW: 300, replies: map[string]*string{}, opened: map[string]bool{}}
}

// leaveReview goes back to the list, once the user agrees to drop the
// comments not sent.
func (a *App) leaveReview(t *tab, rv *prReview) {
	n := len(rv.drafts)
	if cp := rv.compose; cp != nil && strings.TrimSpace(cp.body) != "" {
		n++
	}
	if strings.TrimSpace(rv.comment) != "" {
		n++
	}
	for _, r := range rv.replies {
		if r != nil && strings.TrimSpace(*r) != "" {
			n++
		}
	}
	if n == 0 {
		a.closeReview(t)
		return
	}
	a.confirm = confirmState{open: true, title: locales.PrsDropDraftsTitle(n),
		message: locales.PrsDropDraftsMessage(),
		button:  locales.PrsDropDrafts(),
		run: func() {
			if t.pr.rv == rv {
				a.closeReview(t)
			}
		}}
}

// closeReview goes back to the list, dropping what was being read.
func (a *App) closeReview(t *tab) {
	if rv := t.pr.rv; rv != nil && rv.cancelCmp != nil {
		rv.cancelCmp()
	}
	t.pr.rv = nil
	t.pr.listAt = time.Time{}
}

// loadPRReview reads the pull request, and its commits when they are not
// the ones fetched.
func (a *App) loadPRReview(t *tab, rv *prReview) {
	if rv.read || rv.loading {
		return
	}
	rv.loading = true
	t.s.PullReview(a.findForge(), rv.repo, rv.n, func(r forge.Review, err error) {
		if t.pr.rv != rv {
			return
		}
		rv.loading, rv.read, rv.err = false, true, ""
		if err != nil {
			rv.err = errForge(err)
			return
		}
		rv.r = r
		if fetchKey(r) != rv.fetchedFor {
			a.fetchReview(t, rv)
		}
	})
}

// fetchReview fetches the pull request's commits and reads what they
// change.
// fetchKey is what the commits fetched for r depend on: its head, and
// the base branch it is diffed against.
func fetchKey(r forge.Review) string { return r.HeadOID + "\x00" + r.Base }

func (a *App) fetchReview(t *tab, rv *prReview) {
	rv.fetching, rv.fetchErr, rv.fetchedFor = true, "", fetchKey(rv.r)
	rv.fetchN++
	n := rv.fetchN
	// A fetch that failed is tried again when the review is read again.
	failed := func(err error) { rv.fetching, rv.fetchErr, rv.fetchedFor = false, errForge(err), "" }
	j, err := t.s.FetchPull(rv.repo, rv.n, rv.r.Base)
	if err != nil {
		failed(err)
		return
	}
	update := a.Update
	a.goWork(t, func() {
		<-j.Done()
		_, err := j.Result()
		update(func() {
			if t.pr.rv != rv || rv.fetchN != n {
				return
			}
			if err != nil {
				failed(err)
				return
			}
			t.s.LoadPullRange(rv.n, func(pr session.PullRange, err error) {
				if t.pr.rv != rv || rv.fetchN != n {
					return
				}
				if err != nil {
					failed(err)
					return
				}
				// The host moved the head between the review read and the
				// fetch: its threads are of the head read, so it is read
				// again, a few times at most, as the host's ref and its
				// API may disagree for a while.
				if fmt.Sprintf("%x", pr.Head) != rv.r.HeadOID && rv.mismatched < 2 {
					rv.mismatched++
					rv.fetching, rv.fetchedFor = false, ""
					rv.reread()
					return
				}
				rv.mismatched = 0
				rv.rng = pr
				a.compareReview(t, rv)
			})
		})
	})
}

func (a *App) compareReview(t *tab, rv *prReview) {
	if rv.cancelCmp != nil {
		rv.cancelCmp()
	}
	rv.fetching = true
	rv.cancelCmp = t.s.Compare(rv.rng.MergeBase, rv.rng.Head, func(d session.CommitDetail) {
		if t.pr.rv != rv {
			return
		}
		// The list comes first, then again with its lines counted: the
		// patch shown stays.
		listed := rv.detail == nil || rv.detail.Files != d.Files
		rv.fetching, rv.detail = false, &d
		if d.Done {
			rv.cancelCmp = nil
		}
		if d.Err != nil {
			rv.fetchErr = errText(d.Err)
		}
		if listed {
			rv.patchFor = ""
		}
	})
}

// reread reads the pull request again after a write to it.
func (rv *prReview) reread() { rv.read = false }

func (a *App) prReviewView(c *ui.Context, t *tab, rv *prReview) {
	if i := rv.dropDraft - 1; i >= 0 {
		if i < len(rv.drafts) {
			rv.drafts = slices.Delete(rv.drafts, i, i+1)
		}
		rv.dropDraft = 0
	}
	a.loadPRReview(t, rv)
	th := c.Theme()
	r := &rv.r
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(bannerPadY, insetX, 0, insetX).Gap(spaceM).AlignItems(ui.Center).Children(func() {
			// Esc leaves a comment being written to its text area.
			if ui.Button(c, "‹ "+locales.PrsSection(kindName(rv.repo.Kind))).Clicked() || rv.compose == nil && c.Shortcut(0, ui.KeyEscape) {
				a.leaveReview(t, rv)
			}
			title := locales.PrsTitle(r.Ref(rv.repo.Kind), redact.Control(r.Title))
			ui.Text(c, title).Bold().SingleLine().Grow(1).Selectable()
			if ui.Button(c, locales.MenuRefresh()).Disabled(rv.loading).Clicked() {
				rv.reread()
			}
			if ui.Button(c, locales.PrsOpenInBrowser()).Disabled(r.URL == "").Clicked() {
				a.openURL = r.URL
			}
			if ui.PrimaryButton(c, locales.PrsSubmitReview(len(rv.drafts))).Disabled(rv.r.HeadOID == "").Clicked() {
				rv.submit = prSubmit{open: true}
			}
		})
		if t.pr.rv != rv {
			return
		}
		// Read again, the pull request stays shown as it was.
		have := rv.r.Number != 0
		switch {
		case rv.err != "" && !have:
			errorMessage(c, rv.err)
			return
		case !have:
			loading(c, locales.PrsReading(kindName(rv.repo.Kind)))
			return
		case rv.err != "":
			ui.Text(c, rv.err).FontSize(textSmall).TextColor(th.Danger).Padding(0, insetX).Selectable()
		}
		ui.Row(c).Padding(spaceXS, insetX).Gap(spaceM).Children(func() {
			ui.Text(c, locales.PrsMeta(r.Author, r.Branch, r.Base)).FontSize(textSmall).TextColor(th.TextMuted).SingleLine()
			if r.Draft {
				ui.Text(c, locales.PullsDraft()).FontSize(textSmall).TextColor(th.TextMuted)
			}
			if s := prStatus(r.Decision, r.Checks); s != "" {
				ui.Text(c, s).FontSize(textSmall).TextColor(decisionColor(plainInk(th), r.Decision)).SingleLine()
			}
			lineCounts(c, r.Added, r.Deleted)
		})
		if r.More {
			ui.Text(c, locales.PrsMoreOnHost(kindName(rv.repo.Kind))).FontSize(textSmall).TextColor(th.Warning).Padding(0, insetX)
		}
		nFiles := 0
		if rv.detail != nil && rv.detail.Files != nil {
			nFiles = rv.detail.Files.Len()
		}
		ui.Row(c).Padding(spaceS, insetX).Children(func() {
			ui.Segmented(c, &rv.page, locales.PrsFiles(nFiles), locales.PrsConversation(len(prTimeline(r))))
		})
		if rv.page == 1 {
			a.prConversation(c, t, rv)
		} else {
			a.prFiles(c, t, rv)
		}
	})
	a.prSubmitView(c, t, rv)
}

// prFiles lists the files the pull request changes, beside the patch of
// the one chosen with the review's threads under its lines.
func (a *App) prFiles(c *ui.Context, t *tab, rv *prReview) {
	th := c.Theme()
	switch {
	case rv.fetchErr != "":
		errorMessage(c, rv.fetchErr)
		return
	case rv.detail == nil || rv.detail.Files == nil:
		loading(c, locales.PrsFetching(kindName(rv.repo.Kind)))
		return
	}
	f := rv.detail.Files
	if f.Len() == 0 {
		emptyState(c, locales.CommitNoFiles())
		return
	}
	threads := map[string]int{}
	for _, th := range rv.r.Threads {
		threads[th.Path]++
	}
	for _, d := range rv.drafts {
		threads[d.Path]++
	}
	ui.Split(c, &rv.listW, func() {
		rv.files.Selected = &rv.file
		rv.files.Label = func(i int) string {
			return fileLabel(fileStatusWord(f.Status(i)), commitFileName(f, i), lineAt(rv.detail.Lines, i))
		}
		ui.List(c, &rv.files, f.Len(), func(i int) {
			ui.Row(c).Padding(rowPadY, insetX).Gap(spaceM).AlignItems(ui.Center).FillWidth().Children(func() {
				ui.Text(c, string(f.Status(i))).Font("monospace").FontSize(textSmall).Width(14).TextColor(fileStatusColor(th, f.Status(i)))
				fileIcon(c, string(f.Path(i)))
				shown, _ := showPath(commitFileName(f, i))
				pathLabel(c, shown).Grow(1)
				if n := threads[string(f.Path(i))]; n > 0 {
					ui.Text(c, fmt.Sprint(n)).FontSize(textSmall).TextColor(th.Accent).Tooltip(locales.PrsCommentCount(n))
				}
				fileLines(c, lineAt(rv.detail.Lines, i))
			})
		}).Label(locales.CommitFilesLabel()).Fill()
	}, func() { a.prPatch(c, t, rv) }).SplitReset(300).Fill()
}

func (a *App) prPatch(c *ui.Context, t *tab, rv *prReview) {
	d := rv.detail
	f := d.Files
	if rv.file < 0 || rv.file >= f.Len() {
		emptyPane(c)
		return
	}
	path := string(f.Path(rv.file))
	paths := []string{path}
	if st := f.Status(rv.file); st == 'R' || st == 'C' {
		paths = []string{string(f.OldPath(rv.file)), path}
	}
	// GitHub places comments on lines of a diff with all white space
	// kept; a patch leaving some out would number them otherwise.
	key := fmt.Sprintf("%x\x00%s\x00%s", d.OID, paths[0], path)
	if rv.patchFor != key {
		if cp := rv.compose; cp != nil && cp.oid != string(d.OID) {
			// Its lines are of the head shown before.
			rv.compose = nil
		}
		rv.patchFor, rv.patch, rv.pager = key, session.PatchResult{}, patchPager{}
		_, oldID := f.Old(rv.file)
		_, newID := f.New(rv.file)
		detail := *d
		var load func()
		load = func() {
			rv.patchLoading = true
			sides := session.Sides{Old: oldID, New: newID, Max: rv.pager.sides()}
			t.s.CommitPatch(detail, paths, rv.pager.max(), diff.Whitespace(0), sides, func(res session.PatchResult) {
				if rv.patchFor != key {
					return
				}
				rv.patch = res
				a.setPatch(&rv.pv, res)
				if rv.pager.more(res) {
					load()
					return
				}
				rv.patchLoading = false
			})
		}
		rv.loadPatch = load
		load()
	}
	ui.Column(c).Fill().Children(func() {
		ui.Row(c).Padding(barPadY, insetX).Gap(spaceM).AlignItems(ui.Center).Children(func() {
			shown, _ := showPath(commitFileName(f, rv.file))
			ui.Text(c, shown).Bold().SingleLine().Grow(1)
			fileLines(c, lineAt(d.Lines, rv.file))
			if closeButton(c, locales.CommitClosePatch()).Clicked() {
				rv.file = -1
			}
		})
		a.diffBar(c, false, nil)
		a.pagerNotice(c, &rv.pager, rv.patch, rv.patchLoading, "", rv.loadPatch)
		el := a.patchBodyWith(c, t, &rv.pv, rv.patch, nil, func(dv *editor.DiffState) { a.decorateReview(c, t, rv, paths[0], path, dv) })
		if w := el.Bounds().W; w > 0 && w != rv.diffW {
			rv.diffW = w
			c.AnimationFrame() // the comments, measured anew
		}
	})
}

// prNote is what shows under a line of the patch: a thread, a draft or
// the comment being written.
type prNote struct {
	thread  *forge.Thread
	draft   int // index in drafts, -1 for none
	compose bool
}

func editorSide(s forge.Side) editor.DiffSide {
	if s == forge.Left {
		return editor.DiffOld
	}
	return editor.DiffNew
}

func forgeSide(s editor.DiffSide) forge.Side {
	if s == editor.DiffOld {
		return forge.Left
	}
	return forge.Right
}

// decorateReview puts the review's threads, its drafts and the comment
// being written under their lines of path's patch, and the gutter's +
// to start a comment.
func (a *App) decorateReview(c *ui.Context, t *tab, rv *prReview, oldPath, path string, d *editor.DiffState) {
	pv := &rv.pv
	notes := map[any]prNote{}
	add := func(side editor.DiffSide, line int, key any, n prNote, h float32) {
		if v := pv.viewLine(side, line); v >= 0 {
			d.Annotations = append(d.Annotations, editor.DiffAnnotation{Side: side, Line: v, Height: h, Key: key})
			notes[key] = n
		}
	}
	w := rv.noteWidth(d)
	for i := range rv.r.Threads {
		th := &rv.r.Threads[i]
		if th.Path != path || th.Outdated || th.Line <= 0 {
			continue
		}
		add(editorSide(th.Side), th.Line, "t:"+th.ID, prNote{thread: th, draft: -1}, rv.threadHeight(c, th, w))
	}
	stale := rv.draftsStale()
	for i, dr := range rv.drafts {
		if dr.Path != path || stale {
			continue
		}
		add(editorSide(dr.Side), dr.Line, fmt.Sprintf("d:%d", i), prNote{draft: i}, rv.draftHeight(c, dr.Body, w))
	}
	if cp := rv.compose; cp != nil && cp.path == path {
		add(editorSide(cp.side), cp.line, "compose", prNote{draft: -1, compose: true}, composeHeight)
	}
	d.LineSelection = true
	d.OnGutterButton = func(r editor.DiffLineRange) {
		start, line := pv.fileLine(r.Side, r.Start), pv.fileLine(r.Side, r.End)
		if line == 0 || !pv.inHunk(r.Side, line) || start > 0 && !pv.inHunk(r.Side, start) {
			a.tell(t, locales.PrsNotInDiff(kindName(rv.repo.Kind)))
			return
		}
		if start == line {
			start = 0
		}
		if cp := rv.compose; cp != nil && strings.TrimSpace(cp.body) != "" {
			// One comment written at a time: its text is not dropped.
			a.tell(t, locales.PrsFinishComment())
			return
		}
		rv.compose = &prCompose{path: path, oldPath: oldPath, side: forgeSide(r.Side), start: start, line: line, across: pv.across(r.Side, line)}
		if rv.detail != nil {
			rv.compose.oid = string(rv.detail.OID)
		}
	}
	d.RenderAnnotation = func(c *ui.Context, an editor.DiffAnnotation) {
		n, ok := notes[an.Key]
		if !ok {
			return
		}
		ui.Column(c).Height(an.Height).FillWidth().Padding(spaceXS, spaceM).Children(func() {
			box := ui.Column(c).Fill().Gap(spaceS).Padding(spaceM).Background(c.Theme().Surface).Radius(6).Clip()
			box.Children(func() {
				switch {
				case n.compose:
					a.composeNote(c, t, rv)
				case n.draft >= 0:
					a.draftNote(c, rv, n.draft)
				case n.thread != nil:
					a.threadNote(c, t, rv, n.thread)
				}
			})
		})
	}
}

// Heights of what shows under a line.
const (
	composeHeight  = 190
	noteHeaderH    = 18
	noteMaxHeight  = 480
	noteBodySize   = 13
	noteFrame      = 2*spaceXS + 2*spaceM
	resolvedHeight = 32 + noteFrame
)

// noteWidth is the width the text under a line wraps at.
func (rv *prReview) noteWidth(d *editor.DiffState) float32 {
	w := rv.diffW
	if w <= 0 {
		w = 600
	}
	if d.Layout == editor.DiffSplit {
		w /= 2
	}
	// The gutter's numbers, the frame and the scroll bar.
	return max(120, w-80-2*spaceM-2*spaceM)
}

func bodyHeight(c *ui.Context, body string, w float32) float32 {
	_, h := c.MeasureText(w, ui.Span{Text: shownBody(body), Size: noteBodySize})
	return max(h, noteHeaderH)
}

func (rv *prReview) threadHeight(c *ui.Context, th *forge.Thread, w float32) float32 {
	if th.Resolved && !rv.opened[th.ID] {
		return resolvedHeight
	}
	h := float32(noteFrame)
	for _, cm := range th.Comments {
		h += noteHeaderH + spaceXS + bodyHeight(c, cm.Body, w) + spaceS
	}
	h += controlHeight + spaceS
	return min(h+spaceS, noteMaxHeight)
}

func (rv *prReview) draftHeight(c *ui.Context, body string, w float32) float32 {
	return min(noteFrame+noteHeaderH+spaceXS+bodyHeight(c, body, w)+spaceS+controlHeight+spaceS, noteMaxHeight)
}

// htmlComment is what GitHub's pages hide of a text: the comments of a
// pull request template.
var htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)

// shownBody is a comment's text as shown: without what its page hides,
// control characters and the white space around it.
func shownBody(s string) string {
	return strings.TrimSpace(redact.Control(htmlComment.ReplaceAllString(strings.ReplaceAll(s, "\r\n", "\n"), "")))
}

func commentHead(c *ui.Context, author string, at time.Time, now time.Time) {
	ui.Row(c).Gap(spaceM).Children(func() {
		ui.Text(c, author).Bold().FontSize(textSmall)
		ui.Text(c, ago(now, at)).FontSize(textSmall).TextColor(c.Theme().TextMuted)
	})
}

func (a *App) threadNote(c *ui.Context, t *tab, rv *prReview, th *forge.Thread) {
	now := a.Now()
	if th.Resolved && !rv.opened[th.ID] {
		ui.Row(c).Gap(spaceM).AlignItems(ui.Center).Children(func() {
			ui.Text(c, locales.PrsResolvedThread(len(th.Comments))).FontSize(textSmall).TextColor(c.Theme().TextMuted).Grow(1)
			if ui.Button(c, locales.PrsShow()).Clicked() {
				rv.opened[th.ID] = true
			}
		})
		return
	}
	ui.Scroll(c).Grow(1).FillWidth().Children(func() {
		ui.Column(c).FillWidth().Gap(spaceS).Children(func() {
			for _, cm := range th.Comments {
				ui.Column(c).Key(cm.ID).FillWidth().Gap(spaceXS).Children(func() {
					commentHead(c, cm.Author, cm.At, now)
					ui.Text(c, shownBody(cm.Body)).FontSize(noteBodySize).Selectable()
				})
			}
		})
	})
	reply := rv.replies[th.ID]
	if reply == nil {
		reply = new(string)
		rv.replies[th.ID] = reply
	}
	ui.Row(c).Gap(spaceM).AlignItems(ui.Center).Children(func() {
		in := ui.TextInput(c, reply).Label(locales.PrsReplyPlaceholder()).Placeholder(locales.PrsReplyPlaceholder()).Grow(1).Disabled(!th.CanReply || rv.sending)
		body := strings.TrimSpace(*reply)
		if (ui.Button(c, locales.PrsReply()).Disabled(!th.CanReply || body == "" || rv.sending).Clicked() || in.Submitted() && body != "") && len(th.Comments) > 0 {
			thr := *th
			a.prSend(t, rv, func(ctx context.Context, cl *forge.CLI) error { return cl.Reply(ctx, rv.repo, rv.n, thr, body) }, func() { *reply = "" })
		}
		switch {
		case th.Resolved && th.CanUnresolve:
			if ui.Button(c, locales.PrsUnresolve()).Disabled(rv.sending).Clicked() {
				thr := *th
				a.prSend(t, rv, func(ctx context.Context, cl *forge.CLI) error { return cl.Resolve(ctx, rv.repo, rv.n, thr, false) }, nil)
			}
		case !th.Resolved && th.CanResolve:
			if ui.Button(c, locales.PrsResolve()).Disabled(rv.sending).Clicked() {
				thr := *th
				a.prSend(t, rv, func(ctx context.Context, cl *forge.CLI) error { return cl.Resolve(ctx, rv.repo, rv.n, thr, true) }, nil)
			}
		}
		if th.Resolved && ui.Button(c, locales.PrsHide()).Clicked() {
			delete(rv.opened, th.ID)
		}
	})
}

func (a *App) draftNote(c *ui.Context, rv *prReview, i int) {
	dr := rv.drafts[i]
	ui.Text(c, locales.PrsPending()).Bold().FontSize(textSmall).TextColor(c.Theme().Warning)
	ui.Text(c, shownBody(dr.Body)).FontSize(noteBodySize).Selectable().Grow(1)
	ui.Row(c).Children(func() {
		ui.Spacer(c)
		if ui.Button(c, locales.CommonDelete()).Disabled(rv.sending).Clicked() {
			rv.dropDraft = i + 1
		}
	})
}

func (a *App) composeNote(c *ui.Context, t *tab, rv *prReview) {
	cp := rv.compose
	label := locales.PrsCommentLine(cp.line)
	if cp.start > 0 {
		label = locales.PrsCommentLines(cp.start, cp.line)
	}
	ui.Text(c, label).Bold().FontSize(textSmall)
	ui.TextArea(c, &cp.body).Label(label).Placeholder(locales.PrsCommentPlaceholder()).Lines(3, 5).FillWidth().AutoFocus()
	if rv.draftsStale() {
		ui.Text(c, locales.PrsDraftsStale(len(rv.drafts))).FontSize(textSmall).TextColor(c.Theme().TextMuted)
	}
	body := strings.TrimSpace(cp.body)
	ui.Row(c).Gap(spaceM).Children(func() {
		ui.Spacer(c)
		if ui.Button(c, locales.CommonCancel()).Clicked() {
			rv.compose = nil
		}
		if ui.Button(c, locales.PrsCommentNow()).Disabled(body == "" || rv.sending).Clicked() {
			dr, at := cp.draft(body), rv.revs()
			a.prSend(t, rv, func(ctx context.Context, cl *forge.CLI) error {
				return cl.SubmitReview(ctx, rv.repo, rv.n, at, forge.VerdictComment, "", []forge.Draft{dr})
			}, func() {
				if rv.compose == cp {
					rv.compose = nil
				}
			})
		}
		// A review being sent takes the drafts it was given away when it
		// is through.
		if ui.PrimaryButton(c, locales.PrsAddToReview()).Disabled(body == "" || rv.sending || rv.draftsStale()).Clicked() {
			if len(rv.drafts) == 0 {
				rv.draftsAt = rv.revs()
			}
			rv.drafts = append(rv.drafts, cp.draft(body))
			rv.compose = nil
		}
	})
}

// draftsStale reports drafts written on a head the review no longer
// shows: their lines are of it, so they are not shown on these, and go
// with a review of that head only.
func (rv *prReview) draftsStale() bool {
	return len(rv.drafts) > 0 && rv.draftsAt.Head != rv.revs().Head
}

// revs are the commits comments are placed on: the head the patch shown
// is of, and what the host diffs it against.
func (rv *prReview) revs() forge.Revs {
	at := forge.Revs{Base: rv.r.BaseOID, Start: rv.r.StartOID, Head: rv.r.HeadOID}
	if len(rv.rng.Head) > 0 {
		at.Head = fmt.Sprintf("%x", rv.rng.Head)
	}
	return at
}

// prSend runs a write to the host, then reads the pull request again;
// ok runs once it succeeded.
func (a *App) prSend(t *tab, rv *prReview, fn func(ctx context.Context, c *forge.CLI) error, ok func()) {
	rv.sending = true
	t.s.ForgeDo(a.findForge(), rv.repo, fn, func(err error) {
		rv.sending = false
		if err != nil {
			a.tell(t, errForge(err))
			return
		}
		if ok != nil {
			ok()
		}
		if t.pr.rv == rv {
			rv.reread()
		}
	})
}

// prEvent is one entry of a pull request's conversation.
type prEvent struct {
	author string
	at     time.Time
	body   string
	// what says what the entry is: opened, commented, a verdict, or a
	// thread's place.
	what   string
	state  forge.ReviewState
	thread *forge.Thread
}

// prTimeline is the conversation: the description, the comments, the
// reviews with something to say and the threads whose lines are gone
// from the patch, oldest first.
func prTimeline(r *forge.Review) []prEvent {
	if r.Number == 0 {
		return nil
	}
	ev := []prEvent{{author: r.Author, at: r.Created, body: r.Body, what: locales.PrsOpened()}}
	for _, c := range r.Comments {
		ev = append(ev, prEvent{author: c.Author, at: c.At, body: c.Body, what: locales.PrsCommented()})
	}
	for _, s := range r.Reviews {
		if s.State == forge.ReviewCommented && strings.TrimSpace(s.Body) == "" || s.State == forge.ReviewPending {
			continue
		}
		ev = append(ev, prEvent{author: s.Author, at: s.At, body: s.Body, what: reviewedText(s.State), state: s.State})
	}
	for i := range r.Threads {
		th := &r.Threads[i]
		if !th.Outdated && th.Line > 0 || len(th.Comments) == 0 {
			continue
		}
		ev = append(ev, prEvent{author: th.Comments[0].Author, at: th.Comments[0].At, thread: th,
			what: locales.PrsOutdatedOn(th.Path, th.OriginalLine)})
	}
	slices.SortStableFunc(ev[1:], func(x, y prEvent) int { return x.at.Compare(y.at) })
	return ev
}

func reviewedText(s forge.ReviewState) string {
	switch s {
	case forge.ReviewApproved:
		return locales.PrsReviewed("approved")
	case forge.ReviewChangesRequested:
		return locales.PrsReviewed("changes")
	case forge.ReviewDismissed:
		return locales.PrsReviewed("dismissed")
	}
	return locales.PrsReviewed("other")
}

func (a *App) prConversation(c *ui.Context, t *tab, rv *prReview) {
	th := c.Theme()
	ev := prTimeline(&rv.r)
	now := a.Now()
	ui.Column(c).Fill().Children(func() {
		rv.conv.Label = func(i int) string { return ev[i].author + ", " + ev[i].what }
		ui.List(c, &rv.conv, len(ev), func(i int) {
			e := ev[i]
			ui.Column(c).FillWidth().Padding(spaceM, insetX).Gap(spaceXS).Children(func() {
				ui.Row(c).Gap(spaceM).Children(func() {
					ui.Text(c, e.author).Bold().FontSize(textSmall)
					col := th.TextMuted
					switch e.state {
					case forge.ReviewApproved:
						col = th.Success
					case forge.ReviewChangesRequested:
						col = th.Danger
					}
					ui.Text(c, e.what).FontSize(textSmall).TextColor(col)
					ui.Text(c, ago(now, e.at)).FontSize(textSmall).TextColor(th.TextMuted)
				})
				if e.thread != nil {
					for _, cm := range e.thread.Comments {
						ui.Column(c).Key(cm.ID).Padding(0, 0, 0, spaceL).Gap(space2XS).Children(func() {
							commentHead(c, cm.Author, cm.At, now)
							ui.Text(c, shownBody(cm.Body)).FontSize(noteBodySize).Selectable()
						})
					}
					return
				}
				body := shownBody(e.body)
				if body == "" && i == 0 {
					ui.Text(c, locales.PrsNoDescription()).FontSize(noteBodySize).TextColor(th.TextMuted)
				} else if body != "" {
					ui.Text(c, body).FontSize(noteBodySize).Selectable()
				}
			})
		}).Label(locales.PrsConversationLabel()).Grow(1)
		ui.Column(c).Padding(spaceM, insetX).Gap(spaceS).Children(func() {
			ui.TextArea(c, &rv.comment).Label(locales.PrsCommentPlaceholder()).Placeholder(locales.PrsCommentPlaceholder()).Lines(2, 6).FillWidth()
			body := strings.TrimSpace(rv.comment)
			ui.Row(c).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, locales.PrsAddComment()).Disabled(body == "" || rv.sending).Clicked() {
					a.prSend(t, rv, func(ctx context.Context, cl *forge.CLI) error { return cl.AddComment(ctx, rv.repo, rv.n, body) },
						func() { rv.comment = "" })
				}
			})
		})
	})
}

func (a *App) prSubmitView(c *ui.Context, t *tab, rv *prReview) {
	d := &rv.submit
	if !d.open {
		return
	}
	th := c.Theme()
	own := rv.r.Viewer != "" && strings.EqualFold(rv.r.Viewer, rv.r.Author)
	ui.Modal(c, &d.open, func() {
		dialogBox(c).Width(dialogMedium).Children(func() {
			ui.Text(c, locales.PrsSubmitTitle(rv.r.Ref(rv.repo.Kind))).Bold()
			ui.TextArea(c, &d.body).Label(locales.PrsSummary()).Placeholder(locales.PrsSummary()).Lines(3, 8).FillWidth().AutoFocus()
			if own {
				// The host takes no verdict from a pull request's author.
				d.verdict = 0
			} else {
				ui.Segmented(c, &d.verdict, locales.PrsVerdictComment(), locales.PrsVerdictApprove(), locales.PrsVerdictRequestChanges())
			}
			if n := len(rv.drafts); n > 0 {
				ui.Text(c, locales.PrsPendingCount(n)).FontSize(textSmall).TextColor(th.TextMuted)
				if rv.draftsStale() {
					// Not shown under the lines of the head shown, where
					// each has its Delete.
					ui.Text(c, locales.PrsDraftsStaleSent()).FontSize(textSmall).TextColor(th.TextMuted)
					if ui.Button(c, locales.PrsDiscardDrafts(n)).Disabled(rv.sending).Clicked() {
						rv.drafts = nil
					}
				}
			}
			if d.err != "" {
				ui.Text(c, d.err).FontSize(textSmall).TextColor(th.Danger).Selectable()
			}
			body := strings.TrimSpace(d.body)
			empty := body == "" && len(rv.drafts) == 0 && d.verdict != 1
			ui.Row(c).Gap(spaceM).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, locales.CommonCancel()).Clicked() {
					d.open = false
				}
				if ui.PrimaryButton(c, locales.PullsSubmit()).Disabled(empty || rv.sending).Clicked() {
					drafts, at, v := slices.Clone(rv.drafts), rv.revs(), forge.Verdict(d.verdict)
					if len(drafts) > 0 {
						at = rv.draftsAt
					}
					rv.sending, d.err = true, ""
					t.s.ForgeDo(a.findForge(), rv.repo, func(ctx context.Context, cl *forge.CLI) error {
						return cl.SubmitReview(ctx, rv.repo, rv.n, at, v, body, drafts)
					}, func(err error) {
						rv.sending = false
						var sent *forge.SentError
						if errors.As(err, &sent) {
							// What was sent is not sent again: what is
							// left to do is approve.
							rv.drafts = nil
							if strings.TrimSpace(d.body) == body {
								// Not what was typed since it was sent.
								d.body = ""
							}
							d.err = locales.PrsSentNotApproved(errForge(sent.Err))
							if t.pr.rv == rv {
								rv.reread()
							}
							return
						}
						var left *forge.LeftError
						if errors.As(err, &left) {
							d.err = locales.PrsDraftsLeft(left.Left, errForge(left.Err))
							return
						}
						if err != nil {
							d.err = errForge(err)
							return
						}
						d.open, rv.drafts = false, nil
						if t.pr.rv == rv {
							rv.reread()
						}
					})
				}
			})
		})
	})
}

// pullsSection is the sidebar's name of the pull requests page, as the
// repository's host names them.
func (a *App) pullsSection(t *tab) string {
	r, _ := t.s.ReviewRepo()
	return locales.PrsSection(kindName(r.Kind))
}
