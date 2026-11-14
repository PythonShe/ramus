package ui

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/history"
	"ramus/internal/issuelink"
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
	h.click("Copy Hash")
	if want := strings.TrimSpace(r.Git("rev-parse", "HEAD~1")); h.tt.Clipboard() != want {
		t.Fatalf("copied %q, want %s", h.tt.Clipboard(), want)
	}
	h.click("Copy Patch")
	h.until("the patch copied", func() bool { return strings.Contains(h.tt.Clipboard(), "Subject: [PATCH] add alpha") })

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
	if h.tt.HasText("Choose a file to see its changes") || h.tt.HasText("Open in Diff Tool") {
		t.Fatal("a patch pane before a file is chosen")
	}

	h.click("a.txt")
	h.until("the patch", h.shows("Open in Diff Tool"))
	b, _ := h.tt.Find("b.txt")
	bar, _ := h.tt.Find("Open in Diff Tool")
	if b.H == 0 || b.Y+b.H > bar.Y {
		t.Fatalf("the other file at %v is not above the patch at %v", b, bar)
	}

	h.tt.Key(0, ui.KeyEscape)
	h.until("closed by Escape", func() bool { return !h.tt.HasText("Open in Diff Tool") })
	h.click("a.txt")
	h.until("the patch again", h.shows("Open in Diff Tool"))
	h.click("Close Patch")
	h.until("closed by its button", func() bool { return !h.tt.HasText("Open in Diff Tool") })
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

	h.click("long")
	h.until("the detail", h.shows("Author"))
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
