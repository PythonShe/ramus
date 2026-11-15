package history

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
)

// historyOf is one load of history from the refs as they are: its order,
// the commits it holds and its walk's starts.
type historyOf struct {
	src    *Source
	order  []string
	in     map[uint32]bool
	starts []uint32
}

func loadHistory(t *testing.T, r *gittest.Repo, src *Source) historyOf {
	t.Helper()
	ids, err := src.Resolve(context.Background(), startCat(t, r), startTips(t, r), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	h := historyOf{src: src, in: map[uint32]bool{}}
	w := NewTopoWalk(src, ids)
	for {
		id, _, ok := w.Next()
		if !ok {
			break
		}
		h.in[id] = true
		h.order = append(h.order, hex.EncodeToString(src.OID(id)))
	}
	h.starts = w.Starts()
	return h
}

// grow is Above from h to the refs as they are now.
func (h historyOf) grow(t *testing.T, r *gittest.Repo) ([]string, bool) {
	t.Helper()
	tips := startTips(t, r)
	ids, err := h.src.Resolve(context.Background(), startCat(t, r), tips, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	rows, starts, ok, err := Above(h.src, func(id uint32) bool { return h.in[id] }, h.starts, ids, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		return nil, false
	}
	var out []string
	for _, id := range rows {
		out = append(out, hex.EncodeToString(h.src.OID(id)))
	}
	// The starts are those a walk of the whole new history finds.
	_, w := walkAll(t, h.src, ids)
	if fmt.Sprint(starts) != fmt.Sprint(w.Starts()) {
		t.Fatalf("starts %v, a walk finds %v", starts, w.Starts())
	}
	return append(out, h.order...), true
}

// Above lays commits on top of a history ordered already exactly when git
// orders the new history so, and says so: commits on the newest branch,
// merges of branches made of new commits; not a commit on an older
// branch, a branch rewound, or one pointing at an old commit.
func TestAbove(t *testing.T) {
	for _, graph := range []bool{false, true} {
		t.Run(fmt.Sprintf("graph=%v", graph), func(t *testing.T) {
			r := gittest.Init(t)
			r.Write("a", "1")
			r.Commit("one")
			r.Git("branch", "side")
			r.Write("a", "2")
			r.Commit("two")
			r.Git("tag", "-a", "-m", "t", "v1")
			if graph {
				r.Git("commit-graph", "write", "--reachable")
			}
			load := func() historyOf {
				var src *Source
				if graph {
					src = NewSource(openGraph(t, r.GitDir()), nil, 20)
				} else {
					src = NewSource(nil, nil, 20)
				}
				return loadHistory(t, r, src)
			}
			step := func(what string, wantOK bool, change func()) {
				t.Helper()
				h := load()
				change()
				got, ok := h.grow(t, r)
				want := gitOrder(t, r, startTips(t, r))
				if ok != wantOK {
					t.Fatalf("%s: above %v, want %v", what, ok, wantOK)
				}
				if ok {
					sameOrder(t, what, got, want)
				} else if len(want) > len(h.order) && strings.HasSuffix(strings.Join(want, " "), strings.Join(h.order, " ")) {
					t.Errorf("%s: git keeps the old order below the new commits", what)
				}
			}
			step("a commit on top", true, func() { r.Write("a", "3"); r.Commit("three") })
			step("a merge of new commits", true, func() {
				r.Git("checkout", "-q", "-b", "topic")
				r.Write("t", "1")
				r.Commit("topic 1")
				r.Write("t", "2")
				r.Commit("topic 2")
				r.Git("checkout", "-q", "main")
				r.Write("a", "4")
				r.Commit("four")
				r.Env = []string{fmt.Sprintf("GIT_COMMITTER_DATE=@%d +0000", r.Tick())}
				r.Git("merge", "-q", "--no-ff", "-m", "merge topic", "topic")
				r.Env = nil
				r.Git("branch", "-D", "topic")
			})
			step("a branch at an old commit", true, func() { r.Git("branch", "old", "main~3") })
			step("a ref gone that left nothing", true, func() { r.Git("branch", "-D", "old") })
			step("a commit on a branch from an old commit", true, func() {
				r.Git("checkout", "-q", "side")
				r.Write("s", "1")
				r.Commit("side work")
			})
			step("a commit on top again", true, func() { r.Write("s", "2"); r.Commit("side again") })
			step("a commit on a branch older than the newest", false, func() {
				r.Git("checkout", "-q", "main")
				r.Write("a", "5")
				r.Commit("five")
			})
			step("a branch rewound", false, func() { r.Git("reset", "-q", "--hard", "HEAD~1") })
			step("a merge of the newest branch", true, func() {
				r.Env = []string{fmt.Sprintf("GIT_COMMITTER_DATE=@%d +0000", r.Tick())}
				r.Git("merge", "-q", "--no-ff", "-m", "merge side", "side")
				r.Env = nil
			})
		})
	}
}
