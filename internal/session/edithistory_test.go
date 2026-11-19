package session

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/write"
)

func TestEditRange(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "a\n")
	a := r.Commit("a")
	r.Write("b", "b\n")
	b := r.Commit("b")
	r.Git("branch", "at-b")
	r.Write("c", "c\n")
	c := r.Commit("c")
	s, m := openFor(t, r)
	m.until(t, "the refs", func() bool { return s.State.Refs != nil && s.State.Refs.Len() >= 2 })

	read := func(from string) ([]EditCommit, write.Rewrite, error) {
		id, _ := hex.DecodeString(from)
		var got []EditCommit
		var rw write.Rewrite
		var err error
		done := false
		s.EditRange(id, func(w write.Rewrite, _ write.Seen, cs []EditCommit, e error) { rw, got, err, done = w, cs, e, true })
		m.until(t, "the range", func() bool { return done })
		return got, rw, err
	}
	cs, rw, err := read(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 || hex.EncodeToString(cs[0].OID) != b || hex.EncodeToString(cs[1].OID) != c || rw.Parent != a || rw.Commit != b {
		t.Fatalf("range %+v, rewrite %+v", cs, rw)
	}
	if len(cs[0].Branches) != 1 || cs[0].Branches[0] != "refs/heads/at-b" || len(cs[1].Branches) != 0 {
		t.Fatalf("branches %q %q", cs[0].Branches, cs[1].Branches)
	}
	if cs, rw, err := read(a); err != nil || len(cs) != 3 || rw.Parent != "" {
		t.Fatalf("from the root: %v %d %q", err, len(cs), rw.Parent)
	}

	r.Git("switch", "-q", "-c", "side", a)
	r.Write("s", "s\n")
	side := r.Commit("side")
	r.Git("switch", "-q", "main")
	r.Git("merge", "-q", "--no-ff", "-m", "merge", "side")
	// The merge itself: the state passes through HEAD at a and at side on
	// the way there.
	merge := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	m.until(t, "the merge", func() bool { return hex.EncodeToString(s.State.Head.OID) == merge })
	if _, _, err := read(b); !errors.Is(err, ErrMergeInRange) {
		t.Fatalf("over a merge: %v", err)
	}
	if _, _, err := read(side); err == nil {
		t.Fatal("a commit off the first-parent line taken")
	}
}

// An edit of history is written against HEAD as its commits were read:
// a commit made while the editor was open is not in the plan, so the
// write is refused rather than replaying the plan without it.
func TestEditHistoryRefusesAMovedHead(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "a\n")
	r.Commit("add a")
	r.Write("b", "b\n")
	b := r.Commit("add b")
	r.Write("c", "c\n")
	r.Commit("add c")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "the status", func() bool { return s.State.Status != nil })
	id, _ := hex.DecodeString(b)
	var rw write.Rewrite
	var seen write.Seen
	var cs []EditCommit
	done := false
	s.EditRange(id, func(w write.Rewrite, sn write.Seen, got []EditCommit, err error) {
		if err != nil {
			t.Error(err)
		}
		rw, seen, cs, done = w, sn, got, true
	})
	m.until(t, "the range", func() bool { return done })
	old := string(s.State.Head.OID)
	r.Write("e", "e\n")
	e := r.Commit("add e")
	m.until(t, "HEAD moved", func() bool { return string(s.State.Head.OID) != old })
	// Swap b and c.
	steps := []write.Step{{Commit: hex.EncodeToString(cs[1].OID)}, {Commit: hex.EncodeToString(cs[0].OID)}}
	op, err := write.EditHistory(s.Where(), rw, steps, seen)
	if err != nil {
		t.Fatal(err)
	}
	j, err := s.Submit(op)
	if err == nil {
		m.until(t, "the write", func() bool {
			select {
			case <-j.Done():
				return true
			default:
				return false
			}
		})
		_, err = j.Result()
	}
	if err == nil {
		t.Error("the edit was written over a HEAD moved since its commits were read")
	}
	if got := strings.TrimSpace(r.Git("rev-parse", "HEAD")); got != e {
		t.Fatalf("HEAD is %s, not the commit made meanwhile", got)
	}
}

// A record of git log the edit cannot read fails the range: one skipped
// would be left out of the rebase todo, and the commit dropped. git
// prints a signature check before a signed commit when log.showSignature
// is set, whatever the format.
func TestEditRangeFailsOnARecordItCannotRead(t *testing.T) {
	r := gittest.Init(t)
	gittest.SSHSigning(t, r)
	r.Write("a", "a\n")
	from := r.Commit("first, unsigned")
	r.Write("b", "b\n")
	r.Git("add", "b")
	r.Git("commit", "-q", "-S", "-m", "second, signed")
	r.Write("c", "c\n")
	head := r.Commit("third, unsigned")
	id, _ := hex.DecodeString(from)
	cs, _, err := readEditRange(t.Context(), testrun.Runner(t), r.GitDir(), head, id)
	if err != nil || len(cs) != 3 {
		t.Fatalf("plain: %d commits, %v", len(cs), err)
	}
	r.Git("config", "log.showSignature", "true")
	cs, _, err = readEditRange(t.Context(), testrun.Runner(t), r.GitDir(), head, id)
	if err == nil && len(cs) != 3 {
		t.Fatalf("with signatures shown: %d commits of 3, and no error", len(cs))
	}
}
