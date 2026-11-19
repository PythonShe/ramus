package session

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
		// ok may wait on state set off the main thread after its last
		// post, as the refresher going idle: it is looked at again
		// without a post too.
		select {
		case fn := <-m:
			fn()
		case <-time.After(5 * time.Millisecond):
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
	defer func() { s.Close(); <-s.Done() }()
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
	defer func() { s.Close(); <-s.Done() }()
	m.until(t, "status", func() bool { return s.State.Status != nil })
	if !s.State.Restricted || s.Queue != nil {
		t.Fatalf("untrusted %+v", s.State)
	}
	if _, err := s.Submit(write.Op{Name: "x"}); err == nil || !strings.Contains(err.Error(), "trust") {
		t.Fatalf("write in a restricted repository: %v", err)
	}
	s.LoadHistory()
	m.until(t, "history", func() bool { return s.History.Done })
	loaded := false
	s.LoadCommit(s.State.Head.OID, func(d CommitDetail) { loaded = d.Done })
	m.until(t, "a commit", func() bool { return loaded })
	if s.answers().Len() == 0 {
		t.Fatal("the commit was not kept")
	}
	shown := s.Repo
	if err := s.Trust(); err != nil {
		t.Fatal(err)
	}
	if s.Repo != shown {
		t.Fatal("reopened before the trust was recorded")
	}
	m.until(t, "the trust recorded", func() bool { return s.Repo != shown })
	// What the hardened runner read is read again.
	if s.answers().Len() != 0 {
		t.Fatal("answers kept across the trust")
	}
	// The history read from the closed repository must not outlive it.
	if s.History.Snap != nil || s.History.OID != nil {
		t.Fatal("history kept across the reopen")
	}
	m.until(t, "reopen", func() bool { return s.State.Status != nil && !s.State.Opening })
	if s.State.Restricted || s.Queue == nil || !ts.Trusted(r.GitDir()) {
		t.Fatalf("after trust %+v", s.State)
	}
}

// A quit right after a Trust click holds the main thread while it waits
// for the trust to be in its file: the wait ends once it is, not when
// the main thread is free again to show it.
func TestQuitWaitsNotOnShowingTheTrust(t *testing.T) {
	r := gittest.Init(t)
	r.Commit("one")
	m := make(mainThread, 64)
	held := make(chan struct{})
	var holding atomic.Bool
	update := func(fn func()) {
		if holding.Load() {
			<-held // as Window.Update waits for a main thread a quit holds
		}
		m.update(fn)
	}
	ts := trusted(t)
	s := Open(testrun.Runner(t), ts, r.Dir, update)
	defer func() { s.Close(); <-s.Done() }()
	release := sync.OnceFunc(func() { holding.Store(false); close(held) })
	defer release() // first, should the test fail while held
	m.until(t, "status", func() bool { return s.State.Status != nil })
	holding.Store(true)
	if err := s.Trust(); err != nil {
		t.Fatal(err)
	}
	if !ts.Wait(20 * time.Second) {
		t.Fatal("the quit's wait for the trust ended only with its bound")
	}
	if !ts.Trusted(r.GitDir()) {
		t.Fatal("the wait ended before the trust was recorded")
	}
	release()
	m.until(t, "the reopen", func() bool { return !s.State.Restricted && s.State.Status != nil && !s.State.Opening })
}

// A trust that cannot be recorded says so in the log, and leaves the
// repository as it was.
func TestTrustNotRecorded(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("folder permissions")
	}
	r := gittest.Init(t)
	r.Commit("one")
	m := make(mainThread, 64)
	dir := t.TempDir()
	ts, err := trust.Load(filepath.Join(dir, "trust.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := Open(testrun.Runner(t), ts, r.Dir, m.update)
	defer func() { s.Close(); <-s.Done() }()
	m.until(t, "status", func() bool { return s.State.Status != nil })
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	shown := s.Repo
	if err := s.Trust(); err != nil {
		t.Fatal(err)
	}
	m.until(t, "the failure", func() bool { return len(s.Log) == 1 && !s.Log[0].Running })
	if s.Log[0].Err == nil || s.Repo != shown || !s.State.Restricted || s.trusting {
		t.Fatalf("err %v, reopened %v, restricted %v", s.Log[0].Err, s.Repo != shown, s.State.Restricted)
	}
}

// A trust given just before a quit is in the file once the quit has
// waited for the store's saves: the save counts from the click, not from
// when the goroutine making it starts.
func TestTrustThenQuitIsSaved(t *testing.T) {
	r := gittest.Init(t)
	r.Commit("one")
	m := make(mainThread, 64)
	path := filepath.Join(t.TempDir(), "trust.json")
	ts, err := trust.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	s := Open(testrun.Runner(t), ts, r.Dir, m.update)
	defer func() { s.Close(); <-s.Done() }()
	m.until(t, "status", func() bool { return s.State.Status != nil })
	dir := s.Repo.TrustDir
	if err := s.Trust(); err != nil {
		t.Fatal(err)
	}
	// The quit, on the main thread at once.
	if !ts.Wait(20 * time.Second) {
		t.Fatal("the save never ended")
	}
	again, err := trust.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Trusted(dir) {
		t.Fatal("the trust given just before the quit was not saved")
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
	// Done, it posts nothing more: what it posted is all waiting here.
	select {
	case <-s.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("a session closed while opening did not stop")
	}
	for len(m) > 0 {
		(<-m)()
	}
	if s.Repo != nil {
		t.Fatal("a closed session took its repository")
	}
}

func TestLog(t *testing.T) {
	r := gittest.Init(t)
	r.Commit("one")
	m := make(mainThread, 64)
	s := Open(testrun.Runner(t), trusted(t, r.GitDir()), r.Dir, m.update)
	defer func() { s.Close(); <-s.Done() }()
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
	sh := gittest.Shell(t)
	argv := []string{sh, "-c", "for i in 1 2 3; do echo line $i; done; pwd"}
	a := s.RunActionShown("Say", strings.Join(argv, " "), argv)
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
	m.until(t, "the change", func() bool { return s.State.Status != nil && s.State.Status.Len() == 1 })
	// Named as asked, else by where it was taken.
	if _, err := s.SnapshotNow("  Before the agent  ", false); err != nil {
		t.Fatal(err)
	}
	m.until(t, "the snapshot", func() bool { return s.Writes == 1 })
	if _, err := s.SnapshotNow("", false); err != nil {
		t.Fatal(err)
	}
	m.until(t, "the second snapshot", func() bool { return s.Writes == 2 })
	var list []snapshot.Snapshot
	s.Snapshots(func(l []snapshot.Snapshot, err error) {
		if err != nil {
			t.Error(err)
		}
		list = l
	})
	m.until(t, "the list", func() bool { return list != nil })
	branch := strings.TrimPrefix(strings.TrimSpace(r.Git("symbolic-ref", "HEAD")), "refs/heads/")
	auto := branch + " at " + strings.TrimSpace(r.Git("rev-parse", "--short=8", "HEAD")) + ", 1 changed file"
	var reasons []string
	for _, sn := range list {
		reasons = append(reasons, sn.Reason)
	}
	if !slices.Contains(reasons, "Before the agent") || !slices.Contains(reasons, auto) || len(list) != 2 {
		t.Fatalf("snapshots %q, want %q", reasons, auto)
	}
	// Both hold the work tree as it was.
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
	m.until(t, "the restore", func() bool { return s.Writes == 3 })
	if got, _ := os.ReadFile(filepath.Join(r.Dir, "a")); string(got) != "work" {
		t.Fatalf("a is %q", got)
	}

	if _, err := s.Undo(); err != nil {
		t.Fatal(err)
	}
	m.until(t, "the undo", func() bool { return s.Writes == 4 })
	if e := s.Log[len(s.Log)-1]; e.Err == nil || !strings.Contains(e.Err.Error(), "nothing to undo") {
		t.Fatalf("undo with nothing done: %+v", e)
	}

	// Every snapshot is older than a nanosecond by now. The clock is not
	// moved instead: the session's goroutines read it.
	time.Sleep(time.Millisecond)
	s.ExpireSnapshots(time.Nanosecond)
	deadline := time.Now().Add(20 * time.Second)
	for strings.TrimSpace(r.Git("for-each-ref", snapshot.Prefix)) != "" {
		if time.Now().After(deadline) {
			t.Fatal("old snapshots not expired")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
