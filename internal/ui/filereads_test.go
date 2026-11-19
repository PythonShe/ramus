package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A file chosen, another, then the first again: the history shown is
// the last read's, even when the first read of it ends after.
func TestFileHistoryLastReadWins(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	// The first history of one.txt read once ARM is there is held, its
	// output kept, until RELEASE is.
	body := `case " $* " in *" log "*" one.txt "*) if [ -e FLAG/ARM ] && mkdir FLAG/held 2>/dev/null; then ` +
		gitBin + ` "$@" > FLAG/out; s=$?; : > FLAG/started; while [ ! -e FLAG/RELEASE ]; do sleep 0.02; done; cat FLAG/out; exit $s; fi;; esac`
	h := newHarnessWith(t, wrappedGit(t, body, dir))
	r := h.repoWith()
	r.Write("one.txt", "a\n")
	r.Commit("first one")
	r.Write("two.txt", "b\n")
	r.Commit("only two")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No local changes"))
	tb := h.app.current()
	exists := func(name string) bool { _, err := os.Stat(filepath.Join(dir, name)); return err == nil }

	os.WriteFile(filepath.Join(dir, "ARM"), nil, 0o644)
	h.app.ShowFile(filepath.Join(r.Dir, "one.txt"), false)
	h.until("the first read held", func() bool { return exists("started") })
	r.Write("one.txt", "c\n")
	r.Commit("second one")
	h.app.ShowFile(filepath.Join(r.Dir, "two.txt"), false)
	h.until("two.txt", func() bool { return tb.file.path == "two.txt" && hasExact(h, "only two") })
	h.app.ShowFile(filepath.Join(r.Dir, "one.txt"), false)
	h.until("one.txt read again", func() bool { return tb.file.path == "one.txt" && hasExact(h, "second one") })

	reads := func() int {
		n := 0
		for _, rec := range tb.s.Repo.Runner.Journal().For(tb.s.Repo.GitDir) {
			if strings.Contains(rec.Line, " log ") && strings.HasSuffix(rec.Line, " one.txt") {
				n++
			}
		}
		return n
	}
	os.WriteFile(filepath.Join(dir, "RELEASE"), nil, 0o644)
	h.until("the first read ended", func() bool { return reads() >= 2 && len(h.updates) == 0 })
	h.tt.Frame()
	if !hasExact(h, "second one") {
		t.Fatalf("the first read of one.txt, ended last, is shown: %q", h.tt.Texts())
	}
}
