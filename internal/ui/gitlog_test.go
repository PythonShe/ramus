package ui

import (
	"strings"
	"testing"

	"ramus/internal/git/run"
)

func TestGitCommandLog(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith("a.txt")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("a.txt"))
	tb := h.app.current()
	h.app.ShowFile(r.Dir+"/README", true)
	h.until("the blame", func() bool { return hasExact(h, "hello") && !h.tt.HasText("Blaming") })
	h.click("Command Log")
	h.click("Git Commands")
	// The blame is a read, which the log of actions leaves out.
	h.until("the blame in the log", h.shows("blame"))
	gl := &tb.gitLog
	k := -1
	for i, rec := range gl.recs {
		if strings.HasPrefix(rec.Short, "git blame") {
			k = i
		}
	}
	if k < 0 || !gl.recs[k].Read || gl.recs[k].Err != "" {
		t.Fatalf("blame record %d in %+v", k, gl.recs)
	}
	for _, rec := range gl.recs {
		if rec.GitDir != r.GitDir() {
			t.Fatalf("a command of another repository: %+v", rec)
		}
	}
	// The blame's output streamed to the app; status's the runner kept.
	if gl.recs[k].StdoutLen != -1 {
		t.Fatalf("blame %+v", gl.recs[k])
	}
	st := -1
	for i, rec := range gl.recs {
		if strings.HasPrefix(rec.Short, "git status") && rec.StdoutLen > 0 {
			st = i
		}
	}
	if st < 0 {
		t.Fatal("no status run")
	}
	h.click(gl.recs[st].Short)
	h.until("its output", func() bool {
		return h.tt.HasText(gl.recs[st].Line) && h.tt.HasText("? a.txt") && h.tt.HasText("# branch.head main")
	})

	// A command that fails shows how.
	n := len(gl.recs)
	tb.s.Repo.Runner.Run(t.Context(), &run.Cmd{GitDir: r.GitDir(), Read: true, Args: []string{"rev-parse", "--verify", "nope"}})
	h.until("the failure", func() bool { return len(gl.recs) > n && h.tt.HasText("exit 128") })
	h.click("Actions")
	h.until("the actions", h.shows("Commands the app runs here show up in this log"))
}
