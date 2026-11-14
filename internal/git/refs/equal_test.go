package refs

import (
	"testing"

	"ramus/internal/git/gittest"
)

func TestListEqual(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1")
	r.Commit("one")
	s := New(r.GitDir(), r.GitDir(), 20)
	read := func() *List {
		l, err := s.List()
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	a, b := read(), read()
	if a == b || !a.Equal(b) {
		t.Fatal("two reads of the same refs differ")
	}
	r.Write("a", "2")
	r.Commit("two")
	if c := read(); a.Equal(c) {
		t.Fatal("a moved branch reads equal")
	}
	if a.Equal(nil) || (*List)(nil).Equal(a) {
		t.Fatal("a list equals nothing")
	}
}
