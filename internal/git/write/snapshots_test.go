package write

import (
	"os"
	"path/filepath"
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
