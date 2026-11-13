package session

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/run"
	"ramus/internal/git/trust"
	"ramus/internal/git/write"
)

// mainThread stands in for Window.Update: functions run when the test
// pumps them, on the test's goroutine.
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

func trusted(t *testing.T, dirs ...string) *trust.Store {
	ts, err := trust.Load(filepath.Join(t.TempDir(), "trust.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range dirs {
		ts.Set(d, true)
	}
	return ts
}

func TestSessionFollowsTheRepository(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1")
	r.Commit("one")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	defer s.Close()
	m.until(t, "status", func() bool { return s.State.Status != nil })
	if s.State.Restricted || s.Queue == nil || s.State.Head.Target != "refs/heads/main" || s.State.Status.Len() != 0 {
		t.Fatalf("opened %+v", s.State)
	}

	r.Write("b", "new")
	m.until(t, "the new file", func() bool { return s.State.Status.Len() == 1 })
	if string(s.State.Status.Path(0)) != "b" {
		t.Fatalf("status path %q", s.State.Status.Path(0))
	}

	head := s.State.Head.OID
	j, err := s.Submit(write.Op{Name: "Commit", Cmd: run.Cmd{GitDir: s.Repo.GitDir, WorkTree: r.Dir,
		Args: []string{"commit", "-q", "--allow-empty", "-m", "two"}}})
	if err != nil {
		t.Fatal(err)
	}
	m.until(t, "the commit", func() bool { return string(s.State.Head.OID) != string(head) })
	if _, err := j.Result(); err != nil {
		t.Fatal(err)
	}

	r.Git("checkout", "-q", "-b", "topic")
	m.until(t, "the branch switch", func() bool { return s.State.Head.Target == "refs/heads/topic" })
}

func TestUntrustedOpensRestricted(t *testing.T) {
	r := gittest.Init(t)
	r.Commit("one")
	m := make(mainThread, 64)
	ts := trusted(t)
	s := Open(testrun.Runner(t), ts, r.Dir, m.update)
	defer s.Close()
	m.until(t, "status", func() bool { return s.State.Status != nil })
	if !s.State.Restricted || s.Queue != nil {
		t.Fatalf("untrusted %+v", s.State)
	}
	if _, err := s.Submit(write.Op{Name: "x"}); err == nil || !strings.Contains(err.Error(), "trust") {
		t.Fatalf("write in a restricted repository: %v", err)
	}
	if err := s.Trust(); err != nil {
		t.Fatal(err)
	}
	m.until(t, "reopen", func() bool { return s.State.Status != nil && !s.State.Opening })
	if s.State.Restricted || s.Queue == nil || !ts.Trusted(r.GitDir()) {
		t.Fatalf("after trust %+v", s.State)
	}
}

func TestOpenErrorAndCloseWhileOpening(t *testing.T) {
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), nil, t.TempDir(), m.update)
	m.until(t, "the error", func() bool { return !s.State.Opening })
	if s.State.Err == nil {
		t.Fatal("opened a directory that is no repository")
	}

	r := gittest.Init(t)
	r.Commit("one")
	s = Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	s.Close()
	deadline := time.After(5 * time.Second)
	for s.Repo == nil {
		select {
		case fn := <-m:
			fn()
		case <-deadline:
			return // opened and closed without publishing: fine
		}
		if s.Repo != nil {
			t.Fatal("a closed session took its repository")
		}
	}
}

func TestLog(t *testing.T) {
	r := gittest.Init(t)
	r.Commit("one")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	defer s.Close()
	m.until(t, "status", func() bool { return s.State.Status != nil })
	if _, err := s.Submit(write.Op{Name: "Bad commit", Cmd: run.Cmd{GitDir: s.Repo.GitDir, WorkTree: r.Dir,
		Args: []string{"commit", "-q", "-m", "nothing staged"}}}); err != nil {
		t.Fatal(err)
	}
	m.until(t, "the job", func() bool { return len(s.Log) == 1 && !s.Log[0].Running })
	e := s.Log[0]
	if e.Err == nil || !strings.Contains(e.Command, "commit") {
		t.Fatalf("entry %+v", e)
	}
	sh := "/bin/sh"
	a := s.RunAction("Say", []string{sh, "-c", "for i in 1 2 3; do echo line $i; done; pwd"})
	m.until(t, "the action", func() bool { return !a.Running })
	if a.Err != nil || len(a.Output) != 4 || a.Output[0] != "line 1" {
		t.Fatalf("action %+v", a)
	}
}
