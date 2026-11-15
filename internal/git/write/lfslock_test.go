package write

import (
	"os/exec"
	"testing"

	"ramus/internal/git/gittest/lfsserver"
)

// Locking and unlocking go to the LFS server; someone else's lock comes
// off only when forced.
func TestLFSLock(t *testing.T) {
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Skip("no git-lfs:", err)
	}
	h := newHarness(t)
	srv := lfsserver.New(t, "me")
	theirs := srv.Add("their.psd", "alice")
	h.r.Git("config", "lfs.url", srv.URL)

	op, err := LFSLock(h.w, "dir/my file.psd")
	h.do(op, err, true)
	locks := srv.Locks()
	if len(locks) != 2 || locks[1].Path != "dir/my file.psd" || locks[1].Owner.Name != "me" {
		t.Fatalf("locks %+v", locks)
	}
	// Locked already: the server refuses.
	op, err = LFSLock(h.w, "their.psd")
	h.do(op, err, false)

	op, err = LFSUnlock(h.w, "dir/my file.psd", locks[1].ID, false)
	h.do(op, err, true)
	op, err = LFSUnlock(h.w, "their.psd", theirs.ID, false)
	h.do(op, err, false)
	if n := len(srv.Locks()); n != 1 {
		t.Fatalf("%d locks after unlocking ours", n)
	}
	op, err = LFSUnlock(h.w, "their.psd", theirs.ID, true)
	h.do(op, err, true)
	if n := len(srv.Locks()); n != 0 {
		t.Fatalf("%d locks after forcing", n)
	}

	if _, err := LFSLock(h.w, ""); err == nil {
		t.Error("locked no file")
	}
	if _, err := LFSUnlock(h.w, "x", "--force", false); err == nil {
		t.Error("an id read as an option")
	}
}
