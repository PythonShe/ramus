package ui

import (
	"strconv"
	"strings"
	"testing"
)

// A file renamed and staged shows what changed against its old path, not
// the whole file as new (GitHub Desktop #5575).
func TestRenamedStagedDiff(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, "line "+strconv.Itoa(i))
	}
	text := strings.Join(lines, "\n") + "\n"
	r.Write("old.txt", text)
	r.Commit("old")
	r.Git("mv", "old.txt", "new.txt")
	r.Write("new.txt", strings.Replace(text, "line 2\n", "line two\n", 1))
	r.Git("add", "new.txt")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the rename", h.shows("Staged (1)"))
	h.click("new.txt")
	tb := h.app.current()
	pv := &tb.cv.diff.pv
	h.until("its patch", patchHas(pv, "line two", "line 2"))
	p := tb.cv.diff.res.Parsed
	if !strings.Contains(string(p.Text), "rename from old.txt") || strings.Contains(string(p.Text), "+line 30") {
		t.Fatalf("patch %q", p.Text)
	}
	if tb.cv.diff.partialOK() {
		t.Fatal("lines of a rename can be unstaged")
	}
}
