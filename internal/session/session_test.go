package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/run"
	"ramus/internal/git/snapshot"
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

func TestLogStripsAndRedacts(t *testing.T) {
	e := &LogEntry{}
	e.add([]string{"\x1b]0;evil title\x07ok \x1b[31mred", "https://user:hunter2@example.com/x.git"})
	if strings.Contains(e.Output[0], "\x1b") || strings.Contains(e.Output[1], "hunter2") {
		t.Fatalf("kept %q", e.Output)
	}
}

func TestCloseWhileOpening(t *testing.T) {
	r := gittest.Init(t)
	s := Open(testrun.Runner(t), nil, r.Dir, func(func()) {})
	s.Close()
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("a session closed while opening did not stop")
	}
}

func TestSnapshotsRestoreAndUndo(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1")
	r.Commit("one")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	t.Cleanup(func() { s.Close(); <-s.Done() })
	m.until(t, "open", func() bool { return s.Queue != nil })

	r.Write("a", "work")
	if _, err := s.SnapshotNow(false); err != nil {
		t.Fatal(err)
	}
	m.until(t, "the snapshot", func() bool { return s.Writes == 1 })
	var list []snapshot.Snapshot
	s.Snapshots(func(l []snapshot.Snapshot, err error) {
		if err != nil {
			t.Error(err)
		}
		list = l
	})
	m.until(t, "the list", func() bool { return list != nil })
	if len(list) != 1 || list[0].Reason != "Snapshot now" {
		t.Fatalf("snapshots %+v", list)
	}
	var files []snapshot.File
	s.SnapshotFiles(list[0], func(f []snapshot.File, err error) { files = f })
	m.until(t, "the files", func() bool { return files != nil })
	if len(files) != 1 || files[0].Path != "a" {
		t.Fatalf("files %+v", files)
	}

	r.Write("a", "lost")
	if _, err := s.Restore(list[0], []string{"a"}); err != nil {
		t.Fatal(err)
	}
	m.until(t, "the restore", func() bool { return s.Writes == 2 })
	if got, _ := os.ReadFile(filepath.Join(r.Dir, "a")); string(got) != "work" {
		t.Fatalf("a is %q", got)
	}

	if _, err := s.Undo(); err != nil {
		t.Fatal(err)
	}
	m.until(t, "the undo", func() bool { return s.Writes == 3 })
	if e := s.Log[len(s.Log)-1]; e.Err == nil || !strings.Contains(e.Err.Error(), "nothing to undo") {
		t.Fatalf("undo with nothing done: %+v", e)
	}

	s.timeNow = func() time.Time { return time.Now().Add(30 * 24 * time.Hour) }
	s.ExpireSnapshots(14 * 24 * time.Hour)
	deadline := time.Now().Add(20 * time.Second)
	for strings.TrimSpace(r.Git("for-each-ref", snapshot.Prefix)) != "" {
		if time.Now().After(deadline) {
			t.Fatal("old snapshots not expired")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
