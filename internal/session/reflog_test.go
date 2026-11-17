package session

import (
	"encoding/hex"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

func TestReflogAndLostCommits(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1")
	r.Commit("one")
	r.Write("a", "2")
	lostID := r.Commit("lost work")
	r.Git("reset", "-q", "--hard", "HEAD~1")

	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "open", func() bool { return s.Queue != nil && s.State.Status != nil })
	if seen := s.Seen(); seen.Branch != "refs/heads/main" || seen.Head != strings.TrimSpace(r.Git("rev-parse", "HEAD")) {
		t.Fatalf("seen %+v", seen)
	}

	var log []ReflogEntry
	s.Reflog("HEAD", func(es []ReflogEntry, err error) {
		if err != nil {
			t.Error(err)
		}
		log = es
	})
	m.until(t, "the reflog", func() bool { return log != nil })
	if len(log) != 3 || log[0].Selector != "HEAD@{0}" || !strings.HasPrefix(log[0].Message, "reset: moving to HEAD~1") ||
		hex.EncodeToString(log[1].OID) != lostID || log[1].Time.IsZero() || log[1].Merges != 0 {
		t.Fatalf("reflog %+v", log)
	}

	var lost []LostCommit
	s.LostCommits(func(l []LostCommit, err error) {
		if err != nil {
			t.Error(err)
		}
		lost = l
		if lost == nil {
			lost = []LostCommit{}
		}
	})
	m.until(t, "the lost commits", func() bool { return lost != nil })
	if len(lost) != 1 || hex.EncodeToString(lost[0].OID) != lostID || lost[0].Subject != "lost work" {
		t.Fatalf("lost %+v", lost)
	}
}

// A merge's entry counts its parents past the first, for the menu to
// offer each.
func TestParseReflogMerge(t *testing.T) {
	a, b, c := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	out := a + "\x00" + b + " " + c + "\x00HEAD@{1700000000 +0000}\x00merge side: Merge made\n" +
		b + "\x00" + c + "\x00HEAD@{1600000000 +0000}\x00commit: one\n"
	es := parseReflog([]byte(out), "HEAD")
	if len(es) != 2 || es[0].Merges != 1 || es[1].Merges != 0 || es[0].Message != "merge side: Merge made" ||
		es[1].Selector != "HEAD@{1}" || es[0].Time.Unix() != 1700000000 {
		t.Fatalf("%+v", es)
	}
}
