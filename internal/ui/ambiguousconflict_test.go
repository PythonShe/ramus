package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/conflict"
	"ramus/locales"
)

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A conflicted file the merge editor cannot read, as one whose sides hold
// text like conflict markers and whose conflict was edited by hand, says
// why, and still leaves the way out: the file's menu marks it resolved as
// it is, or takes a side whole.
func TestAmbiguousConflictLeavesAWayOut(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	lines := "a\nb\nc\nd\ne\nf\n"
	example := "<<<<<<< HEAD\nyour change\n=======\ntheir change\n>>>>>>> topic\n"
	r.Write("README.md", lines+"end\n")
	r.Commit("base")
	r.Git("switch", "-q", "-c", "topic")
	r.Write("README.md", lines+"end theirs\n")
	r.Commit("topic")
	r.Git("switch", "-q", "main")
	r.Write("README.md", lines+example)
	r.Commit("main")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	r.Run(nil, "merge", "topic")
	r.Write("README.md", strings.Replace(readFile(t, filepath.Join(r.Dir, "README.md")), "end theirs\n", "end, both\n", 1))
	h.until("the conflict", func() bool { return hasExact(h, "README.md") })
	conflicted := func() bool { return strings.Contains(r.Git("ls-files", "-u", "--", "README.md"), "README.md") }

	h.click("README.md")
	h.until("why it cannot be read", h.shows(errText(conflict.ErrAmbiguous)))

	h.tt.RightClick("README.md")
	if err := h.tt.ChooseMenuItem(locales.ChangesMarkResolved(1)); err != nil {
		t.Fatalf("%v: %q", err, h.tt.Menu())
	}
	h.until("the question", func() bool { return tb.cv.confirm.open })
	h.click("Cancel")
	h.tt.Frame()

	h.tt.RightClick("README.md")
	if err := h.tt.ChooseMenuItem(locales.MergeTakeTheirsFor(1)); err != nil {
		t.Fatalf("%v: %q", err, h.tt.Menu())
	}
	h.until("theirs taken", func() bool { return !conflicted() })
	h.idle(tb, "the write")
	if got := readFile(t, filepath.Join(r.Dir, "README.md")); got != lines+"end theirs\n" {
		t.Fatalf("README.md %q", got)
	}
}
