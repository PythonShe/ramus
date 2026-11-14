package libstats

import (
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

type mainThread chan func()

func (m mainThread) update(fn func()) { m <- fn }

func (m mainThread) until(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for !ok() {
		select {
		case fn := <-m:
			fn()
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestLoad(t *testing.T) {
	mine := gittest.Init(t)
	mine.Git("config", "user.email", "TEST@example.com")
	for i := range 3 {
		mine.Write("a", strings.Repeat("x", i))
		mine.Commit("c")
	}
	// git counts whole KiB, of the bytes stored where the system has no
	// block counts, as on Windows: some kilobytes that do not compress.
	noise := make([]byte, 16<<10)
	rand.NewChaCha8([32]byte{}).Read(noise)
	mine.Write("noise", string(noise))
	mine.Git("add", "noise")
	other := gittest.Init(t)
	other.Write("b", "1")
	other.Commit("one")
	other.Env = []string{"GIT_AUTHOR_EMAIL=someone@example.com"}
	other.Write("b", "2")
	other.Git("commit", "-q", "-a", "-m", "two")
	other.Env = nil

	dir := t.TempDir()
	m := make(mainThread, 64)
	load := func() []Repo {
		t.Helper()
		l := New(testrun.Runner(t), nil, dir, m.update)
		defer l.Close()
		got := make([]Repo, 3)
		l.Load([]string{mine.Dir, other.Dir, filepath.Join(dir, "missing")}, []string{"mine", "other", "missing"},
			func(i int, r Repo) { got[i] = r })
		m.until(t, "every repository", func() bool { return got[0].Done && got[1].Done && got[2].Done })
		return got
	}
	got := load()
	if got[0].Err != nil || got[1].Err != nil || got[2].Err == nil {
		t.Fatalf("errors %v, %v, %v", got[0].Err, got[1].Err, got[2].Err)
	}
	if got[0].Records.Len() != 3 || got[1].Records.Len() != 2 || got[0].Size <= 0 {
		t.Fatalf("%d and %d commits, size %d", got[0].Records.Len(), got[1].Records.Len(), got[0].Size)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.activity"))
	if len(files) != 2 {
		t.Fatalf("cached %v", files)
	}
	// A new loader reads the records from the cache directory.
	for _, f := range files {
		os.Chtimes(f, time.Unix(1, 0), time.Unix(1, 0))
	}
	again := load()
	if again[0].Records.Len() != 3 {
		t.Fatal("cached records lost commits")
	}
	for _, f := range files {
		if fi, _ := os.Stat(f); fi.ModTime().Unix() == 1 {
			t.Errorf("%s was not read", f)
		}
	}

	all := Summarize(got, Filter{}, 0)
	if all.Commits != 5 || all.Contributors != 2 || len(all.Rows) != 3 || all.Rows[1].Totals.Commits != 2 {
		t.Fatalf("all %+v", all)
	}
	own := Summarize(got, Filter{Mine: true}, 0)
	if own.Commits != 4 || own.Contributors != 1 || own.Rows[1].Totals.Commits != 1 {
		t.Fatalf("own %+v", own.Rows)
	}
	extra := Summarize(got, Filter{Mine: true, Emails: []string{"Someone@example.com"}}, 0)
	if extra.Commits != 5 {
		t.Fatalf("with an added address %+v", extra.Rows)
	}
	// The range leaves the first commit out; how old the repository is
	// does not change.
	first, _ := got[0].Records.Span()
	if since := Summarize(got, Filter{Since: first + 1}, 0); since.Rows[0].Totals.Commits != 2 || since.Rows[0].Born != first {
		t.Fatalf("since %+v", since.Rows)
	}
}

func TestTrim(t *testing.T) {
	dir := t.TempDir()
	for i, name := range []string{"a.activity", "b.activity", "c.activity", "write-1"} {
		p := filepath.Join(dir, name)
		os.WriteFile(p, make([]byte, 100), 0o600)
		at := time.Now().Add(time.Duration(i-10) * time.Hour)
		os.Chtimes(p, at, at)
	}
	trim(dir, 200)
	left, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(left) != 2 || filepath.Base(left[0]) != "b.activity" {
		t.Fatalf("left %v", left)
	}
}
