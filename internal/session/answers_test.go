package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/diff"
	"ramus/internal/git/gittest"
)

// A patch or a commit read once is handed over again with no process
// until what it was read from changes (GitHub Desktop #21440).
func TestAnswersKept(t *testing.T) {
	runner, logged := loggedRunner(t)
	r := gittest.Init(t)
	r.Write("a.txt", "one\n")
	r.Commit("one")
	r.Write("a.txt", "two\n")
	r.Git("add", "a.txt")
	file := filepath.Join(r.Dir, "a.txt")
	edit := func(text string, age time.Duration) {
		t.Helper()
		r.Write("a.txt", text)
		if age > 0 {
			at := time.Now().Add(-age)
			if err := os.Chtimes(file, at, at); err != nil {
				t.Fatal(err)
			}
		}
	}
	edit("three\n", time.Hour)
	m := make(mainThread, 64)
	s := Open(runner, trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "status", func() bool { return s.State.Status != nil && s.State.Status.Len() == 1 })
	m.pump(4 * Debounce)

	sides := func(src diff.Source) Sides {
		st := s.State.Status
		ids, modes := st.OIDs(0), st.Modes(0)
		if src == diff.Staged {
			return Sides{Old: ids[0], New: ids[1], Modes: [2]uint32{modes[0], modes[1]}}
		}
		return Sides{Old: ids[1], WorkTreePath: "a.txt", Modes: [2]uint32{modes[1], modes[2]}}
	}
	// diffs counts the git diff commands run, which a refresh the edits
	// set off does not run.
	diffs := func() int {
		n := 0
		for _, l := range logged() {
			if strings.HasPrefix(l, "diff ") || strings.Contains(l, " diff ") {
				n++
			}
		}
		return n
	}
	// read reports whether git diff ran and whether the patch came at
	// once.
	read := func(src diff.Source, want string) (ran, atOnce bool) {
		t.Helper()
		before := diffs()
		var res PatchResult
		got := false
		s.Patch(diff.PatchRequest{Source: src, Paths: []string{"a.txt"}, Context: -1}, sides(src), func(p PatchResult) { res, got = p, true })
		atOnce = got
		m.until(t, "the patch", func() bool { return got })
		if res.Err != nil || !strings.Contains(string(res.Parsed.Text), "\n+"+want+"\n") {
			t.Fatalf("patch %q, %v; want +%s", res.Parsed.Text, res.Err, want)
		}
		return diffs() > before, atOnce
	}

	if ran, _ := read(diff.Staged, "two"); !ran {
		t.Fatal("the first staged patch ran no git")
	}
	if ran, atOnce := read(diff.Staged, "two"); ran || !atOnce {
		t.Fatalf("the staged patch again: git ran %v, at once %v", ran, atOnce)
	}
	if ran, _ := read(diff.Unstaged, "three"); !ran {
		t.Fatal("the first work tree patch ran no git")
	}
	if ran, _ := read(diff.Unstaged, "three"); ran {
		t.Fatal("the work tree patch again ran git")
	}
	edit("four\n", 30*time.Minute)
	if ran, _ := read(diff.Unstaged, "four"); !ran {
		t.Fatal("an edited file's patch was not read again")
	}
	// A file changed just now may change again within its timestamp
	// tick: its patch is not kept.
	edit("five\n", 0)
	read(diff.Unstaged, "five")
	if ran, _ := read(diff.Unstaged, "five"); !ran {
		t.Fatal("kept the patch of a file changed just now")
	}

	head := s.State.Head.OID
	loadCommit := func() (ran, atOnce bool) {
		before := len(logged())
		var d CommitDetail
		got := false
		s.LoadCommit(head, func(cd CommitDetail) { d, got = cd, cd.Done })
		atOnce = got
		m.until(t, "the commit", func() bool { return got })
		if d.Err != nil || string(d.Commit.Message) != "one\n" || d.Files.Len() != 1 {
			t.Fatalf("commit %q, %v", d.Commit.Message, d.Err)
		}
		return len(logged()) > before, atOnce
	}
	if ran, _ := loadCommit(); !ran {
		t.Fatal("the first commit read ran no git")
	}
	if ran, atOnce := loadCommit(); ran || !atOnce {
		t.Fatalf("the commit again: git ran %v, at once %v", ran, atOnce)
	}
	var d CommitDetail
	s.LoadCommit(head, func(cd CommitDetail) { d = cd })
	commitPatch := func() (ran, atOnce bool) {
		before := len(logged())
		got := false
		s.CommitPatch(d, []string{"a.txt"}, 0, 0, Sides{}, func(p PatchResult) {
			got = true
			if p.Err != nil || !strings.Contains(string(p.Parsed.Text), "\n+one\n") {
				t.Errorf("commit patch %q, %v", p.Parsed.Text, p.Err)
			}
		})
		atOnce = got
		m.until(t, "the commit patch", func() bool { return got })
		return len(logged()) > before, atOnce
	}
	commitPatch()
	if ran, atOnce := commitPatch(); ran || !atOnce {
		t.Fatalf("the commit patch again: git ran %v, at once %v", ran, atOnce)
	}

	s.DropAnswers()
	if ran, _ := loadCommit(); !ran {
		t.Fatal("a commit was kept past DropAnswers")
	}
	s.keep("large", answer{patch: &PatchResult{Old: make([]byte, maxAnswer)}})
	if _, ok := s.answers().Peek("large"); ok {
		t.Fatal("kept an answer past maxAnswer")
	}
}
