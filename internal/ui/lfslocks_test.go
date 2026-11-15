package ui

import (
	"os/exec"
	"testing"

	"ramus/internal/git/gittest/lfsserver"
)

// A repository that keeps files in LFS lists its locks: one is taken by
// name, released, and someone else's is forced off once confirmed. A
// changed file's menu offers its lock.
func TestLFSLocksView(t *testing.T) {
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Skip("no git-lfs:", err)
	}
	srv := lfsserver.New(t, "me")
	srv.Add("theirs.psd", "alice")
	h := newHarness(t)
	r := h.repoWith()
	r.Write(".gitattributes", "*.psd filter=lfs diff=lfs merge=lfs -text lockable\n")
	r.Commit("lfs")
	r.Git("config", "lfs.url", srv.URL)
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the repository", h.shows("No changes"))
	h.until("the locks in the sidebar", h.shows("LFS Locks"))
	h.click("LFS Locks")
	h.until("their lock", func() bool { return h.tt.HasText("theirs.psd") && h.tt.HasText("alice") })

	h.click("File to lock")
	h.tt.Type("mine.psd")
	h.tt.Frame()
	h.click("Lock")
	h.until("the lock taken", h.shows("me (you)"))
	if ls := srv.Locks(); len(ls) != 2 || ls[1].Path != "mine.psd" {
		t.Fatalf("server locks %+v", ls)
	}

	menu := func(row, item string) {
		t.Helper()
		if err := h.tt.RightClick(row); err != nil {
			t.Fatal(err)
		}
		if err := h.tt.ChooseMenuItem(item); err != nil {
			t.Fatalf("%v: %q", err, h.tt.Menu())
		}
		h.tt.Frame()
	}
	menu("mine.psd", "Unlock")
	h.until("the lock released", func() bool { return !h.tt.HasText("mine.psd") })

	menu("theirs.psd", "Force Unlock…")
	h.until("the question", h.shows("Force unlock “theirs.psd”?"))
	h.click("Force Unlock")
	h.until("no locks", h.shows("No files are locked."))
	if ls := srv.Locks(); len(ls) != 0 {
		t.Fatalf("server locks %+v", ls)
	}

	// A changed file is locked from its menu in the changes.
	r.Write("art.psd", "pixels")
	h.until("the change counted", h.shows("Changes (1)"))
	h.click("Changes (1)")
	h.until("the change", h.shows("art.psd"))
	menu("art.psd", "Lock File")
	h.until("the lock on the server", func() bool {
		ls := srv.Locks()
		return len(ls) == 1 && ls[0].Path == "art.psd"
	})
}
