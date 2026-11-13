package write

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/run"
	"ramus/internal/git/snapshot"
)

func discard(dir string, gitDir string, paths ...string) Op {
	return Op{Name: "Discard", Cmd: run.Cmd{GitDir: gitDir, WorkTree: dir, Args: append([]string{"checkout", "--"}, paths...)}}
}

func TestExpectRefusesStaleState(t *testing.T) {
	r, q, _ := newQueue(t)
	r.Write("a", "1")
	head := r.Commit("one")
	r.Write("a", "seen")
	op := discard(r.Dir, r.GitDir(), "a")
	op.Expect = &Expect{Branch: "refs/heads/main", Head: head, Files: observe(t, r.Dir, "a")}

	// Something else edits the file after the user confirmed.
	time.Sleep(10 * time.Millisecond)
	r.Write("a", "edited by an agent")
	_, err := wait(t, q.Submit(op))
	var se *StaleError
	if !errors.As(err, &se) || se.What != "a" {
		t.Fatalf("file changed: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(r.Dir, "a")); string(b) != "edited by an agent" {
		t.Fatal("ran anyway")
	}

	op.Expect = &Expect{Branch: "refs/heads/main", Head: head, Files: observe(t, r.Dir, "a")}
	r.Commit("two")
	if _, err := wait(t, q.Submit(op)); !errors.As(err, &se) || se.What != "HEAD" {
		t.Fatalf("HEAD moved: %v", err)
	}

	head = strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	r.Git("branch", "topic")
	del := Op{Name: "Delete branch", Cmd: run.Cmd{GitDir: r.GitDir(), Args: []string{"branch", "-D", "topic"}},
		Expect: &Expect{Branch: "refs/heads/main", Head: head, Refs: map[string]string{"refs/heads/topic": strings.TrimSpace(r.Git("rev-parse", "topic"))}}}
	r.Git("update-ref", "refs/heads/topic", "HEAD~")
	if _, err := wait(t, q.Submit(del)); !errors.As(err, &se) || se.What != "refs/heads/topic" {
		t.Fatalf("ref moved: %v", err)
	}
	del.Expect.Refs["refs/heads/topic"] = strings.TrimSpace(r.Git("rev-parse", "topic"))
	if _, err := wait(t, q.Submit(del)); err != nil {
		t.Fatal(err)
	}
	r.Git("branch", "topic")
	if _, err := wait(t, q.Submit(Op{Name: "Create", Cmd: run.Cmd{GitDir: r.GitDir(), Args: []string{"branch", "-D", "topic"}},
		Expect: &Expect{Branch: "refs/heads/main", Head: head, Refs: map[string]string{"refs/heads/topic": ""}}})); !errors.As(err, &se) {
		t.Fatalf("ref appeared: %v", err)
	}
}

func observe(t *testing.T, dir string, paths ...string) []FileState {
	t.Helper()
	fs, err := Observe(dir, paths)
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func TestExpectBranchAndFolders(t *testing.T) {
	r, q, _ := newQueue(t)
	r.Write("dir/a", "1")
	head := r.Commit("one")
	r.Git("branch", "other")
	r.Write("dir/a", "seen")
	x := &Expect{Branch: "refs/heads/main", Head: head, Files: observe(t, r.Dir, "dir")}

	// Same commit, another branch: a reset would move the wrong one.
	r.Git("checkout", "-q", "other")
	op := discard(r.Dir, r.GitDir(), "dir")
	op.Expect = x
	var se *StaleError
	if _, err := wait(t, q.Submit(op)); !errors.As(err, &se) || se.What != "the current branch" {
		t.Fatalf("branch switch: %v", err)
	}
	r.Git("checkout", "-q", "main")

	// An edit inside the folder leaves the folder's own times alone.
	time.Sleep(10 * time.Millisecond)
	r.Write("dir/a", "edited")
	if _, err := wait(t, q.Submit(op)); !errors.As(err, &se) || se.What != "dir/a" {
		t.Fatalf("edit in folder: %v", err)
	}
	r.Write("dir/new", "x")
	op.Expect = &Expect{Branch: "refs/heads/main", Head: head, Files: observe(t, r.Dir, "dir")}
	os.Remove(filepath.Join(r.Dir, "dir/new"))
	if _, err := wait(t, q.Submit(op)); !errors.As(err, &se) || !strings.HasPrefix(se.What, "dir") {
		t.Fatalf("file gone from folder: %v", err)
	}
}

func TestSnapshotBeforeDiscard(t *testing.T) {
	r, q, _ := newQueue(t)
	r.Write("a", "1")
	r.Commit("one")
	r.Write("a", "work")
	op := discard(r.Dir, r.GitDir(), "a")
	op.Snapshot = &snapshot.Options{Reason: "Discard changes", Paths: []string{"a"}}
	j := q.Submit(op)
	if _, err := wait(t, j); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(r.Dir, "a")); string(b) != "1" {
		t.Fatal("not discarded")
	}
	s, _ := j.Kept()
	if s.Commit == "" || r.Git("show", s.Commit+":a") != "work" {
		t.Fatalf("snapshot %+v", s)
	}

	// A snapshot that cannot be taken stops the write.
	r.Write("a", "more work")
	op.Snapshot = &snapshot.Options{Reason: "x", Paths: []string{"a"}}
	os.WriteFile(filepath.Join(r.GitDir(), "objects", "info", "alternates"), []byte("/nonexistent\n"), 0o644)
	os.Chmod(filepath.Join(r.GitDir(), "objects"), 0o555)
	defer os.Chmod(filepath.Join(r.GitDir(), "objects"), 0o755)
	if _, err := wait(t, q.Submit(op)); err == nil || !strings.Contains(err.Error(), "snapshot") {
		t.Fatalf("failed snapshot: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(r.Dir, "a")); string(b) != "more work" {
		t.Fatal("discarded without a snapshot")
	}
}

func TestBackupBeforeRewrite(t *testing.T) {
	r, q, _ := newQueue(t)
	r.Commit("one")
	tip := r.Commit("two")
	j := q.Submit(Op{Name: "Reset", Cmd: run.Cmd{GitDir: r.GitDir(), WorkTree: r.Dir, Args: []string{"reset", "-q", "--hard", "HEAD~"}},
		Backup: []string{"refs/heads/main", "refs/heads/missing"}})
	if _, err := wait(t, j); err != nil {
		t.Fatal(err)
	}
	_, dir := j.Kept()
	if !strings.HasPrefix(dir, BackupPrefix) {
		t.Fatalf("backup dir %q", dir)
	}
	if got := strings.TrimSpace(r.Git("rev-parse", dir+"heads/main")); got != tip {
		t.Fatalf("backup %s, want %s", got, tip)
	}
	if out := r.Git("for-each-ref", "--format=%(refname)", dir); strings.Count(out, "\n") != 1 {
		t.Fatalf("backed up %q", out)
	}
}
