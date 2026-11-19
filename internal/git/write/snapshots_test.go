package write

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/catfile"
	"ramus/internal/git/refs"
	"ramus/internal/git/snapshot"
)

func TestSnapshotNowRestoreExpire(t *testing.T) {
	r, q, life := newQueue(t)
	r.Write("a", "one")
	r.Commit("one")
	r.Write("a", "edited")
	r.Write("new", "untracked")
	j := q.SnapshotNow(r.Dir, snapshot.Options{Reason: "Before an agent run"})
	if _, err := wait(t, j); err != nil {
		t.Fatal(err)
	}
	s, _ := j.Kept()
	if s.Ref == "" || !s.All {
		t.Fatalf("kept %+v", s)
	}

	r.Write("a", "agent wrote this")
	os.Remove(filepath.Join(r.Dir, "new"))
	cat, err := catfile.Start(life.Context(), q.r, r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	files, err := snapshot.Files(cat, 20, s)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, f := range files {
		paths = append(paths, f.Path)
	}
	j = q.Restore(r.Dir, s, paths)
	if _, err := wait(t, j); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(r.Dir, "a")); string(b) != "edited" {
		t.Fatalf("a is %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(r.Dir, "new")); string(b) != "untracked" {
		t.Fatalf("new is %q", b)
	}
	if before, _ := j.Kept(); before.Ref == "" {
		t.Fatal("restore kept nothing of what it replaced")
	}
	if _, err := wait(t, q.Restore(r.Dir, s, nil)); err == nil {
		t.Fatal("restoring a whole-tree snapshot with no files named")
	}

	rd := refs.New(r.GitDir(), r.GitDir(), 20)
	list, err := snapshot.List(rd, cat)
	if err != nil || len(list) != 2 {
		t.Fatalf("list %d %v", len(list), err)
	}
	if _, err := wait(t, q.Expire(r.Dir, list, time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if list, _ = snapshot.List(rd, cat); len(list) != 0 {
		t.Fatalf("%d left after expiry", len(list))
	}
}

// Backups of history rewrites expire with the snapshots, but those an
// undo or redo the History holds still needs.
func TestExpireBackups(t *testing.T) {
	h := newHarness(t)
	h.r.Write("a", "1")
	h.r.Commit("one")
	old := BackupPrefix + strconv.FormatInt(time.Now().Add(-30*24*time.Hour).UnixNano(), 10) + "/heads/main"
	recent := BackupPrefix + strconv.FormatInt(time.Now().Add(-time.Hour).UnixNano(), 10) + "/heads/main"
	h.r.Git("update-ref", old, "HEAD")
	h.r.Git("update-ref", recent, "HEAD")
	op, err := Commit(h.w, CommitOptions{Message: "amended", Amend: true, Seen: h.rev("HEAD"), Branch: "refs/heads/main"})
	h.do(op, err, true)
	backups := func() string {
		return strings.TrimSpace(h.r.Git("for-each-ref", "--format=%(refname)", BackupPrefix))
	}
	amend := ""
	for name := range strings.SplitSeq(backups(), "\n") {
		if name != old && name != recent {
			amend = name
		}
	}
	if amend == "" {
		t.Fatal("the amend made no backup")
	}
	if _, err := wait(t, h.q.Expire(h.r.Dir, nil, time.Now().Add(-14*24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if got := backups(); got != recent+"\n"+amend && got != amend+"\n"+recent {
		t.Fatalf("after expiry %q", got)
	}
	// Past the cutoff, the amend's backup stays while Undo can take the
	// amend back, which needs the commit it holds.
	if _, err := wait(t, h.q.Expire(h.r.Dir, nil, time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if got := backups(); got != amend {
		t.Fatalf("after expiry %q", got)
	}
	h.undo()
	if _, err := wait(t, h.q.Expire(h.r.Dir, nil, time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if got := backups(); got != amend {
		t.Fatalf("a backup redo needs expired: %q", got)
	}
}
