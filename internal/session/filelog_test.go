package session

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gittest"
)

func TestFileLogAndBlame(t *testing.T) {
	r := gittest.Init(t)
	// The first name is one git quotes where it cannot use -z.
	a := "a\t\"日本"
	if !gittest.Portable(a) {
		a = "a 日本"
	}
	r.Write(a, "one\n")
	c1 := r.Commit("one")
	r.Git("mv", a, "b")
	r.Commit("move")
	r.Write("b", "one\ntwo\n")
	c3 := r.Commit("two")
	r.Git("switch", "-q", "-c", "side", "HEAD~1")
	r.Write("other", "x")
	r.Commit("side")
	r.Git("switch", "-q", "main")
	r.Git("merge", "-q", "--no-ff", "-m", "merge side", "side")
	r.Write("b", "one\ntwo\nthree\n")
	s, m := openFor(t, r)

	log := func(path string, opt FileLogOptions) []FileCommit {
		var got []FileCommit
		done := false
		s.FileLog(path, opt, func(cs []FileCommit, limited bool, err error) {
			if err != nil || limited {
				t.Error(err, limited)
			}
			got, done = cs, true
		})
		m.until(t, "the log", func() bool { return done })
		return got
	}
	cs := log("b", FileLogOptions{Follow: true})
	var subjects, paths []string
	for _, c := range cs {
		subjects, paths = append(subjects, c.Subject), append(paths, c.Path)
	}
	if strings.Join(subjects, ",") != "two,move,one" || strings.Join(paths, ",") != "b,b,"+a {
		t.Fatalf("followed: %q %q", subjects, paths)
	}
	if cs := log("b", FileLogOptions{}); len(cs) != 2 {
		t.Fatalf("not followed: %d", len(cs))
	}
	if cs := log("other", FileLogOptions{}); len(cs) != 1 || cs[0].Subject != "side" {
		t.Fatalf("other: %+v", cs)
	}

	var text []byte
	var commits []BlameCommit
	lineCommit := map[int]int{}
	done := false
	s.Blame("b", func(b []byte, err error) {
		if err != nil {
			t.Error(err)
		}
		text = b
	}, func(u BlameUpdate) {
		commits = append(commits, u.Commits...)
		for _, g := range u.Groups {
			for l := g.Start; l < g.Start+g.Lines; l++ {
				lineCommit[l] = g.Commit
			}
		}
		if u.Err != nil {
			t.Error(u.Err)
		}
		done = done || u.Done
	})
	m.until(t, "the blame", func() bool { return done })
	if string(text) != "one\ntwo\nthree\n" || len(lineCommit) != 3 {
		t.Fatalf("text %q, lines %v", text, lineCommit)
	}
	hexOf := func(l int) string { return string(commits[lineCommit[l]].OID) }
	if hexOf(0) != string(mustHex(t, c1)) || hexOf(1) != string(mustHex(t, c3)) || !commits[lineCommit[2]].Uncommitted {
		t.Fatalf("blame %+v", commits)
	}
	if c := commits[lineCommit[0]]; c.Path != a || c.Summary != "one" || c.Author == "" {
		t.Fatalf("first line's commit %+v", c)
	}
}

// A blame cancelled, as the file view does before it blames another
// file, calls back no more, even with what it had posted before the
// cancel: its commits would land in the next blame.
func TestCancelledBlameCallsBackNoMore(t *testing.T) {
	r := gittest.Init(t)
	var b strings.Builder
	for i := range 3000 {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	r.Write("a", b.String())
	r.Write("b", "bee\n")
	r.Commit("one")
	s, m := openFor(t, r)
	aCalls := 0
	cancelA := s.Blame("a", func([]byte, error) { aCalls++ }, func(BlameUpdate) { aCalls++ })
	// The main thread is busy; the blame of a posts meanwhile.
	time.Sleep(700 * time.Millisecond)
	cancelA()
	bDone := false
	s.Blame("b", func([]byte, error) {}, func(u BlameUpdate) { bDone = bDone || u.Done })
	m.until(t, "b's blame", func() bool { return bDone })
	if aCalls > 0 {
		t.Errorf("the cancelled blame of a called back %d times after it was cancelled", aCalls)
	}
}

// Blame in a repository not trusted runs no textconv, and blames a file
// whose driver the repository defines, which the hardening empties.
func TestRestrictedBlameRunsNoTextconv(t *testing.T) {
	marks := t.TempDir()
	mark := markWith(t, marks, "textconv")
	r := gittest.Init(t)
	r.Write(".gitattributes", "*.x diff=conv\n")
	r.Write("a.x", "one\ntwo\n")
	r.Commit("one")
	r.Git("config", "diff.conv.textconv", mark+"cat")
	s, m := openFor(t, r)
	if !s.State.Restricted {
		t.Fatal("the repository is trusted")
	}
	var last BlameUpdate
	groups := 0
	s.Blame("a.x", func([]byte, error) {}, func(u BlameUpdate) {
		groups += len(u.Groups)
		last = u
	})
	m.until(t, "the blame", func() bool { return last.Done })
	noMarks(t, marks)
	if last.Err != nil || groups == 0 {
		t.Fatalf("blame: %v, %d groups", last.Err, groups)
	}
}
