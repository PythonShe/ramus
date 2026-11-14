package ui

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/history"
	"ramus/internal/issuelink"
	"ramus/internal/settings"
)

func TestHistoryView(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("tag", "-a", "-m", "first", "v1")
	r.Write("a.txt", "alpha\n")
	r.Commit("add alpha")
	r.Git("branch", "topic")
	r.Write("README", "hello again\n")
	r.Commit("edit readme")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	h.click("History")
	h.until("the commits", h.shows("edit readme"))
	// The labels are built again off the main thread once history comes,
	// with the commit an annotated tag names: v1 may come a frame later.
	for _, s := range []string{"add alpha", "one", "main", "topic", "v1", "3 commits"} {
		h.until(s, h.shows(s))
	}

	h.click("add alpha")
	h.until("the detail", h.shows("1 file changed"))
	h.click("a.txt")
	h.until("its patch", patchHas(&h.app.current().hist.detail.pv, "alpha", ""))
	// The values copy themselves: the hash, the message by its subject,
	// the patch by the count of its files.
	hash := strings.TrimSpace(r.Git("rev-parse", "HEAD~1"))
	h.click(hash)
	if h.tt.Clipboard() != hash {
		t.Fatalf("copied %q, want %s", h.tt.Clipboard(), hash)
	}
	h.click("1 file changed")
	h.until("the patch copied", func() bool { return strings.Contains(h.tt.Clipboard(), "Subject: [PATCH] add alpha") })
	// The subject is the line above the Author row; the list shows the
	// same text, so it is found by place, with the patch closed and the
	// rows each a line.
	// The list's author column is collapsed for Author to name the
	// detail's row alone.
	h.tt.Key(0, ui.KeyEscape)
	h.app.setHistoryColumn(colAuthor, settings.Column{Hidden: true})
	h.tt.Frame()
	h.tt.Advance(time.Second)
	author, _ := h.tt.Find("Author")
	cv := &h.app.current().hist.detail
	x, y := author.X+spaceXS, author.Y-spaceS-cv.subjectH/2
	h.tt.Move(x, y)
	h.tt.Frame()
	if h.tt.Cursor() != ui.CursorPointer {
		t.Fatalf("cursor over the subject is %v, want the pointer", h.tt.Cursor())
	}
	h.tt.ClickAt(x, y)
	h.tt.Frame()
	if got := h.tt.Clipboard(); got != "add alpha\n" {
		t.Fatalf("copied %q, want the message", got)
	}

	// Jumping by ref chooses the commit and shows it.
	tab := h.app.current()
	if err := h.tt.Click("Go to hash or ref"); err != nil {
		t.Fatal(err)
	}
	h.tt.Type("v1")
	h.tt.Key(0, ui.KeyEnter)
	want := strings.TrimSpace(r.Git("rev-parse", "v1^{commit}"))
	h.until("the jump", func() bool {
		return tab.hist.chosen == 2 && h.tt.HasText(want)
	})

	// Only the latest go-to is answered, whichever git answers first.
	for range 10 {
		h.app.jumpTo(tab, "HEAD")
		h.app.jumpTo(tab, "v1")
		h.until("the second jump", func() bool { return hex.EncodeToString(tab.hist.chosenID) == want })
		h.settle()
		if got := hex.EncodeToString(tab.hist.chosenID); got != want || tab.hist.chosen != 2 {
			t.Fatalf("an older go-to won: %s", got)
		}
	}

	// A new commit reloads history and keeps the chosen commit chosen.
	r.Write("b.txt", "beta\n")
	r.Commit("add beta")
	h.until("the new commit", h.shows("add beta"))
	h.until("the choice kept", func() bool { return tab.hist.chosen == 3 })

	if err := h.tt.RightClick("add beta"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Copy Message"); err != nil {
		t.Fatal(err)
	}
	h.until("the message copied", func() bool { return h.tt.Clipboard() == "add beta\n" })
}

func TestIssueLinks(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("remote", "add", "origin", "git@github.com:octo/proj.git")
	r.Write("a.txt", "a\n")
	r.Commit("fix crash\n\nCloses #12 and PROJ-7.")
	h.trust.Set(r.GitDir(), true)
	h.lib.Opened(r.Dir, h.app.Now())
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	h.click("History")
	h.until("the commits", h.shows("fix crash"))
	h.click("fix crash")
	exact := func(s string) func() bool {
		return func() bool { return slices.Contains(h.tt.Texts(), s) }
	}
	h.until("the link from the remote", exact("#12"))
	if exact("PROJ-7")() {
		t.Fatal("PROJ-7 linked before a rule names it")
	}

	tab := h.app.current()
	h.app.openLinks(tab)
	h.tt.Frame()
	h.app.links.rules[0] = issuelink.Rule{Pattern: `\bPROJ-(\d+)\b`, URL: "https://jira.example.com/browse/PROJ-$1"}
	h.click("Save")
	h.until("the rule used", exact("PROJ-7"))
	if got := h.lib.IssueLinks(r.Dir); len(got) != 1 {
		t.Fatalf("saved %+v", got)
	}
}

func TestReflogView(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "lost\n")
	lost := r.Commit("lost work")
	r.Git("reset", "-q", "--hard", "HEAD~1")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	h.click("Reflog")
	h.until("the reflog", h.shows("reset: moving to HEAD~1"))

	h.click("Lost Commits")
	h.click("Find Lost Commits")
	h.until("the lost commit", h.shows("lost work"))
	h.click("lost work")
	h.until("its detail", h.shows("1 file changed"))
	if err := h.tt.RightClick("lost work"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Create Branch Here…"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	h.tt.Type("rescued")
	h.click("Create")
	h.until("the branch", func() bool {
		return strings.TrimSpace(r.Git("rev-parse", "--abbrev-ref", "HEAD")) == "rescued"
	})
	if got := strings.TrimSpace(r.Git("rev-parse", "rescued")); got != lost {
		t.Fatalf("rescued at %s, want %s", got, lost)
	}

	// Reset the branch back, hard, after a confirmation.
	h.app.current().reflog.lostShown = 0
	h.until("the new entry", h.shows("checkout: moving from main to rescued"))
	if err := h.tt.RightClick("reset: moving to HEAD~1"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Reset rescued to Here", "Hard: Drop Local Changes…"); err != nil {
		t.Fatal(err)
	}
	h.until("the question", h.shows("Reset"))
	h.app.confirm.run()
	h.until("the reset", func() bool {
		return strings.TrimSpace(r.Git("rev-parse", "rescued")) != lost
	})
}

func TestStatsView(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	for i := range 5 {
		r.Write("a.txt", strings.Repeat("x", i))
		r.Commit("work")
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	h.click("Statistics")
	h.until("the counts", h.shows("Ramus Test <test@example.com>"))
	tab := h.app.current()
	if got := tab.stats.res.Stats.Commits; got != 6 || !h.tt.HasText("Commits per week") || !h.tt.HasText("By weekday") {
		t.Fatalf("%d commits; texts %q", got, h.tt.Texts())
	}

	// The treemaps: a folder's tile opens it.
	r.Write("src/main.go", strings.Repeat("x", 4000))
	r.Commit("src")
	// A read under way says what it reads, as Activity does.
	tab.stats.sizes = treemapState{loading: true}
	tab.stats.mode = 1
	h.until("the loading label", h.shows("Reading file sizes…"))
	tab.stats.sizes = treemapState{}
	h.app.countStats(tab)
	h.until("the size treemap", h.shows("src/"))
	h.click("src/")
	h.until("the folder", h.shows("main.go"))
	if !h.tt.HasText("3.9 KB") {
		t.Fatalf("texts %q", h.tt.Texts())
	}
	tab.stats.mode = 2
	h.until("the change treemap", h.shows("Changes over 7 commits"))
	h.until("the change counts", h.shows("a.txt: 5 changes, 71.4%"))
	tab.stats.mode = 0

	// A range with no commits in it.
	tab.stats.from = "2030-01-01"
	h.app.countStats(tab)
	h.until("the empty range", h.shows("No commits in this range"))
	tab.stats.from = "next week"
	h.app.countStats(tab)
	h.until("the bad day", h.shows(`"next week" is not a day like 2024-03-31`))
}

// Scrolling through a long history keeps the row text within its budget:
// the chunks used longest ago go.
func TestHistoryTextBudget(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	var stream strings.Builder
	const n = 3000
	body := strings.Repeat("a long line of a commit message ", 4)
	for i := range n {
		msg := fmt.Sprintf("commit %d %s\n", i, body)
		fmt.Fprintf(&stream, "commit refs/heads/main\ncommitter A U Thor <a@example.com> %d +0000\ndata %d\n%s", 1700000000+i, len(msg), msg)
		if i == 0 {
			stream.WriteString("from refs/heads/main^0\n")
		}
		fmt.Fprintf(&stream, "M 644 inline f.txt\ndata %d\n%d\n\n", len(strconv.Itoa(i))+1, i)
	}
	if out, err := r.Run([]byte(stream.String()), "fast-import", "--quiet"); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	r.Git("reset", "-q", "--hard")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	tb := h.app.current()
	h.click("History")
	h.until("history", func() bool { return tb.s.History.Done && tb.s.History.Snap != nil && tb.s.History.Snap.Len() == n+1 })
	const budget = 160 << 10
	hv := &tb.hist
	hv.textBudget = budget
	peak := 0
	for row := 0; row <= n; row += 128 {
		hv.list.ScrollTo(row, ui.Start)
		h.until(fmt.Sprintf("row %d", row), func() bool { return hv.loaded(row) != nil })
		total := 0
		for _, c := range hv.text {
			total += c.Bytes()
		}
		peak = max(peak, total)
		if total > budget {
			t.Fatalf("at row %d the text holds %d bytes in %d chunks, over %d", row, total, len(hv.text), budget)
		}
	}
	if len(hv.text) < 2 || len(hv.text) >= (n+1)/history.TextChunkRows {
		t.Fatalf("%d chunks kept of %d", len(hv.text), (n+1)/history.TextChunkRows)
	}
	t.Logf("peak %d bytes, %d chunks kept", peak, len(hv.text))
}

// A commit message longer than the header gives it scrolls in its own box,
// instead of being drawn over the rows below it.
func TestLongCommitMessageStaysInItsBox(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	var lines []string
	for i := range 30 {
		lines = append(lines, fmt.Sprintf("body line %d", i+1))
	}
	r.Write("a.txt", "alpha\n")
	r.Commit("long message\n\n" + strings.Join(lines, "\n"))
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	h.click("History")
	h.until("the commits", h.shows("long message"))
	h.click("long message")
	h.until("the detail", h.shows("Author"))

	// Between the end of the word Author and the name after it, the row
	// shows nothing; a message drawn past its box would cross it.
	a, _ := h.tt.Find("Author")
	img := h.tt.Image()
	bg := img.RGBAAt(img.Bounds().Max.X-4, img.Bounds().Max.Y-4)
	ink := 0
	for y := int(a.Y); y < int(a.Y+a.H); y++ {
		for x := int(a.X) + 56; x < int(a.X)+82; x++ {
			c := img.RGBAAt(x, y)
			if d := int(c.R) + int(c.G) + int(c.B) - int(bg.R) - int(bg.G) - int(bg.B); d > 60 || d < -60 {
				ink++
			}
		}
	}
	if ink > 0 {
		t.Fatalf("%d pixels drawn beside the Author label", ink)
	}
}

// A commit shows its header and files alone; a patch opens below them only
// for a chosen file, leaving the files in sight, and closes again.
func TestCommitPatchOpensForAChosenFile(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	var lines []string
	for i := range 30 {
		lines = append(lines, fmt.Sprintf("body line %d", i+1))
	}
	r.Write("a.txt", "alpha\n")
	r.Write("b.txt", "beta\n")
	r.Commit("two files\n\n" + strings.Join(lines, "\n"))
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	h.click("History")
	h.until("the commits", h.shows("two files"))
	h.click("two files")
	h.until("the files", h.shows("b.txt"))
	if h.tt.HasText("Choose a file to see its changes") || h.tt.HasText("Close Patch") {
		t.Fatal("a patch pane before a file is chosen")
	}
	listW := h.app.current().hist.detailW

	// The patch opens in a column of its own, right of the files, which
	// keep the whole height; the list narrows to make room.
	h.click("a.txt")
	h.until("the patch", h.shows("Close Patch"))
	b, _ := h.tt.Find("b.txt")
	closer, _ := h.tt.Find("Close Patch")
	if b.H == 0 || b.X+b.W > closer.X || closer.Y > b.Y {
		t.Fatalf("the patch at %v is not right of the files at %v", closer, b)
	}
	cv := &h.app.current().hist.detail
	if cv.listW != commitListW || h.app.current().hist.detailW != listW {
		t.Fatalf("list %v beside the patch, %v without it", cv.listW, h.app.current().hist.detailW)
	}

	h.tt.Key(0, ui.KeyEscape)
	h.until("closed by Escape", func() bool { return !h.tt.HasText("Close Patch") })
	h.click("a.txt")
	h.until("the patch again", h.shows("Close Patch"))
	h.click("Close Patch")
	h.until("closed by its button", func() bool { return !h.tt.HasText("Close Patch") })
}

// The message has a divider of its own: it starts fitted to a short body,
// drags taller, and keeps the height dragged for the next commit.
func TestCommitMessageDragsTaller(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	var lines []string
	for i := range 30 {
		lines = append(lines, fmt.Sprintf("body line %d", i+1))
	}
	r.Write("a.txt", "alpha\n")
	r.Commit("short\n\njust one line")
	r.Write("b.txt", "beta\n")
	r.Commit("long\n\n" + strings.Join(lines, "\n"))
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	h.click("History")
	h.until("the commits", h.shows("short"))
	cv := &h.app.current().hist.detail

	h.click("short")
	h.until("the detail", h.shows("just one line"))
	h.tt.Frame()
	if cv.msgH >= commitBodyMax || cv.msgH != cv.fittedMessage() {
		t.Fatalf("a one-line body takes %v, fitted %v", cv.msgH, cv.fittedMessage())
	}

	// The list's author column is collapsed for Author to name the
	// detail's row alone.
	h.app.setHistoryColumn(colAuthor, settings.Column{Hidden: true})
	h.tt.Frame()
	h.tt.Advance(time.Second)
	h.click("long")
	h.until("the detail", h.shows("body line 30"))
	h.tt.Frame()
	before, _ := h.tt.Find("Author")
	// The divider is msgH below the top of the message: its padding, the
	// subject and the gap above the body.
	body, _ := h.tt.Find(strings.Join(lines, "\n"))
	x, y := body.X+40, body.Y-spaceS-cv.subjectH-spaceM+cv.msgH
	h.tt.Press(x, y)
	h.tt.Frame()
	for _, dy := range []float32{25, 50, 75, 100} {
		h.tt.Move(x, y+dy)
		h.tt.Frame()
	}
	h.tt.Release(x, y+100)
	h.tt.Frame()
	after, _ := h.tt.Find("Author")
	if after.Y-before.Y < 90 || !cv.msgSized {
		t.Fatalf("Author moved from %v to %v; sized %v", before.Y, after.Y, cv.msgSized)
	}
	dragged := cv.msgH

	h.click("short")
	h.until("the other commit", h.shows("just one line"))
	h.tt.Frame()
	if cv.msgH != dragged {
		t.Fatalf("height %v after choosing another commit, dragged to %v", cv.msgH, dragged)
	}
}

// The history's columns resize from the dividers in their header, the
// room coming from the message. A column follows the pointer all the way
// and settles when let go: at its least width, or shut when dragged under
// half of it; the header's menu brings it back. A list too narrow for
// the message shuts the columns at the end first.
func TestHistoryColumns(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Write("a.txt", "alpha\n")
	r.Commit("add alpha")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	h.click("History")
	h.until("the rows", h.shows("Ramus Test"))
	h.tt.Frame()
	hv := &h.app.current().hist
	head, _ := h.tt.Find("Author")
	cell, _ := h.tt.Find("Ramus Test")
	if head.X != cell.X || head.W != cell.W {
		t.Fatalf("author header at %v, its cells at %v", head, cell)
	}

	// drag moves the divider left of the header title by dx, checking
	// that the title keeps up with the pointer, and lets the column
	// settle.
	drag := func(title string, dx float32) {
		b, _ := h.tt.Find(title)
		x, y := b.X-spaceM/2, b.Y+b.H/2
		h.tt.Press(x, y)
		h.tt.Frame()
		for _, f := range []float32{0.25, 0.5, 0.75, 1} {
			h.tt.Move(x+dx*f, y)
			h.tt.Frame()
			if now, _ := h.tt.Find(title); now.X != b.X+dx*f {
				t.Fatalf("%s at %v with the pointer moved %v, from %v", title, now.X, dx*f, b.X)
			}
		}
		h.tt.Release(x+dx, y)
		h.tt.Frame()
		h.tt.Advance(time.Second)
	}
	drag("Author", -60)
	if w := h.app.Settings.HistoryColumns["author"].Width; w != 180 {
		t.Fatalf("author %v wide, dragged to 180", w)
	}
	if cell, _ := h.tt.Find("Ramus Test"); cell.W != 180 {
		t.Fatalf("author cells %v wide", cell.W)
	}

	// Let go under its least width, but not under half, it settles at
	// its least.
	drag("Commit", 20)
	if b, _ := h.tt.Find("Commit"); b.W != histColumnMin[colHash] || h.app.Settings.HistoryColumns["hash"].Width != histColumnMin[colHash] {
		t.Fatalf("hash column %v, kept %+v", b, h.app.Settings.HistoryColumns["hash"])
	}
	drag("Commit", 40)
	if hasExact(h, "Commit") || !h.app.Settings.HistoryColumns["hash"].Hidden {
		t.Fatalf("hash column %+v not shut", h.app.Settings.HistoryColumns["hash"])
	}
	// Shut, it keeps its divider, past the date's, to drag it out again.
	date, _ := h.tt.Find("Date")
	x, y := date.X+date.W+spaceM/2, date.Y+date.H/2
	h.tt.Move(x, y)
	h.tt.Frame()
	if h.tt.Cursor() != ui.CursorResizeEW {
		t.Fatalf("cursor over a shut column's divider is %v", h.tt.Cursor())
	}
	h.tt.Press(x, y)
	h.tt.Frame()
	for _, dx := range []float32{-40, -80} {
		h.tt.Move(x+dx, y)
		h.tt.Frame()
	}
	h.tt.Release(x-80, y)
	h.tt.Frame()
	h.tt.Advance(time.Second)
	if b, _ := h.tt.Find("Commit"); b.W != 80 || h.app.Settings.HistoryColumns["hash"].Hidden {
		t.Fatalf("hash column dragged out to %v, kept %+v", b, h.app.Settings.HistoryColumns["hash"])
	}
	if cell, _ := h.tt.Find("Ramus Test"); cell.W != 180 {
		t.Fatalf("author cells %v wide after the hash came back", cell.W)
	}

	// Or the header's menu brings it back.
	drag("Commit", 80)
	if hasExact(h, "Commit") {
		t.Fatal("hash column not shut again")
	}
	if err := h.tt.RightClick("Message"); err != nil {
		t.Fatal(err)
	}
	if err := h.tt.ChooseMenuItem("Commit"); err != nil {
		t.Fatal(err)
	}
	h.tt.Frame()
	h.tt.Advance(time.Second)
	if b, _ := h.tt.Find("Commit"); b.W != 80 {
		t.Fatalf("hash column back at %v", b)
	}

	// Narrowed, the list shuts the hash, then the date, then the
	// author, each whole, and keeps the message.
	h.app.Settings.HistoryColumns = nil
	for _, c := range []struct {
		w     float32
		shown [histColumns]bool
	}{
		{640, [histColumns]bool{true, true, true, true}},
		{560, [histColumns]bool{true, true, true, false}},
		{480, [histColumns]bool{true, true, false, false}},
		{360, [histColumns]bool{true, false, false, false}},
	} {
		hv.detailW = c.w
		h.tt.Frame()
		h.tt.Advance(time.Second)
		if hv.cols.shown != c.shown {
			t.Logf("rowW %v cols %+v key %+v", hv.rowW, hv.cols, hv.widthKey)
			t.Fatalf("a list %v wide shows columns %v, want %v", c.w, hv.cols.shown, c.shown)
		}
		if msg, _ := h.tt.Find("Message"); msg.W < historyMessageMin-1 {
			t.Fatalf("a list %v wide leaves the message %v", c.w, msg.W)
		}
	}

	// Widened in a list that shut the date and the hash, a column takes
	// the room they left to the message, no more.
	hv.detailW = 480
	h.tt.Frame()
	h.tt.Advance(time.Second)
	drag("Author", -20)
	if cell, _ := h.tt.Find("Ramus Test"); cell.W != 140 {
		t.Fatalf("author widened to %v in a narrow list, want 140", cell.W)
	}
	// Past the room it stops, short of the pointer.
	b, _ := h.tt.Find("Author")
	x, y = b.X-spaceM/2, b.Y+b.H/2
	h.tt.Press(x, y)
	h.tt.Frame()
	h.tt.Move(x-200, y)
	h.tt.Frame()
	h.tt.Release(x-200, y)
	h.tt.Frame()
	h.tt.Advance(time.Second)
	if msg, _ := h.tt.Find("Message"); msg.W < historyMessageMin-1 || !hasExact(h, "Author") {
		t.Fatalf("author widened past the room: message %v", msg.W)
	}
}

// The graph collapses to a thin form rather than shutting: by a click on
// its divider, which expands it again, or dragged most of the way there.
// Thin, its lanes are drawn closer together, all of them still shown.
func TestHistoryGraphCollapses(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	for b := range 3 {
		r.Git("checkout", "-q", "-b", fmt.Sprintf("topic%d", b), "main")
		r.Write(fmt.Sprintf("t%d.txt", b), "x\n")
		r.Commit(fmt.Sprintf("topic %d", b))
	}
	r.Git("checkout", "-q", "main")
	for b := range 3 {
		r.Git("merge", "-q", "--no-ff", "-m", fmt.Sprintf("merge %d", b), fmt.Sprintf("topic%d", b))
	}
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows("No changes"))
	h.click("History")
	h.until("the rows", h.shows("merge 2"))
	h.tt.Frame()
	hv := &h.app.current().hist
	snap := h.app.current().s.History.Snap
	full, thin := graphWidth(snap), graphThin(snap)
	if hv.cols.w[colGraph] != full || thin >= full {
		t.Fatalf("graph %v wide, fitted %v, thin %v", hv.cols.w[colGraph], full, thin)
	}
	// divider is the graph's divider, left of the message's title.
	divider := func() (float32, float32) {
		m, _ := h.tt.Find("Message")
		return m.X - spaceM/2, m.Y + m.H/2
	}
	settle := func() {
		h.tt.Frame()
		h.tt.Advance(time.Second)
	}

	h.tt.ClickAt(divider())
	settle()
	if hv.cols.w[colGraph] != thin || !h.app.Settings.HistoryColumns["graph"].Thin {
		t.Fatalf("clicked, graph %v wide, want thin %v", hv.cols.w[colGraph], thin)
	}
	if st := hv.cols.graphStyle(); st.Columns(thin) < snap.Width() {
		t.Fatalf("thin, %d of %d lanes drawn", st.Columns(thin), snap.Width())
	}
	h.tt.ClickAt(divider())
	settle()
	if hv.cols.w[colGraph] != full {
		t.Fatalf("clicked again, graph %v wide, want %v", hv.cols.w[colGraph], full)
	}

	// A list too narrow for the message, once the author, the date and
	// the hash are shut, has the graph go thin, and full again as it
	// widens.
	for _, c := range []struct{ w, graph float32 }{{360, full}, {260, thin}, {640, full}} {
		hv.detailW = c.w
		settle()
		if hv.cols.w[colGraph] != c.graph || c.w < 640 && hv.cols.shown[colAuthor] {
			t.Fatalf("a list %v wide: graph %v wide, want %v; columns %v", c.w, hv.cols.w[colGraph], c.graph, hv.cols.shown)
		}
	}
	if h.app.Settings.HistoryColumns["graph"].Thin {
		t.Fatal("thin for want of room kept as the user's")
	}

	// Dragged to the end, it settles thin; dragged out, full again.
	drag := func(dx float32) {
		x, y := divider()
		h.tt.Press(x, y)
		h.tt.Frame()
		for _, f := range []float32{0.5, 1} {
			h.tt.Move(x+dx*f, y)
			h.tt.Frame()
		}
		h.tt.Release(x+dx, y)
		settle()
	}
	drag(-full)
	if hv.cols.w[colGraph] != thin {
		t.Fatalf("dragged to the end, graph %v wide, want thin %v", hv.cols.w[colGraph], thin)
	}
	drag(full)
	if p := h.app.Settings.HistoryColumns["graph"]; p.Thin || hv.cols.w[colGraph] != thin+full {
		t.Fatalf("dragged out, graph %v wide, kept %+v", hv.cols.w[colGraph], p)
	}
}
