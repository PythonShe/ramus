package session

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
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
		s.EditRange(id, func(w write.Rewrite, cs []EditCommit, e error) { rw, got, err, done = w, cs, e, true })
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
