package ui

import (
	"strings"
	"testing"
)

// A commit is written in i18n.commitEncoding and read back from it; a
// message or a file name that is not valid text keeps its bytes, shown as
// escapes and marked.
func TestEncodings(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	r.Git("config", "i18n.commitEncoding", "ISO-8859-1")
	// A message from an older tool, not UTF-8 and naming no encoding.
	tree := strings.TrimSpace(r.Git("rev-parse", "HEAD^{tree}"))
	head := strings.TrimSpace(r.Git("rev-parse", "HEAD"))
	raw := "tree " + tree + "\nparent " + head + "\nauthor A <a@x> 1700000000 +0000\ncommitter A <a@x> 1700000000 +0000\n\nna\xefve bytes\n"
	id, err := r.Run([]byte(raw), "hash-object", "-t", "commit", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	r.Git("update-ref", "HEAD", strings.TrimSpace(id))
	r.Write("f.txt", "x\n")
	r.Git("add", "f.txt")
	// macOS refuses such a name on disk; a repository made elsewhere has
	// it in its index.
	blob, err := r.Run([]byte("y"), "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	r.Git("update-index", "--add", "--cacheinfo", "100644,"+strings.TrimSpace(blob)+",caf\xe9.txt")
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("open", h.shows(`⚠ "caf\xe9.txt"`))
	tb := h.app.current()
	h.until("the commit setup", func() bool { return tb.commit.setup != nil })
	tb.commit.subject = "déjà vu"
	h.tt.Frame()
	h.click("Commit to main")
	h.until("committed", func() bool { return strings.Contains(r.Git("log", "-1", "--format=%s"), "vu") })
	if got := strings.TrimSpace(r.Git("-c", "i18n.logOutputEncoding=UTF-8", "log", "-1", "--format=%s")); got != "déjà vu" {
		t.Fatalf("git reads the message as %q", got)
	}
	if obj := r.Git("cat-file", "commit", "HEAD"); !strings.Contains(obj, "\nencoding ISO-8859-1\n") || !strings.Contains(obj, "d\xe9j\xe0 vu") {
		t.Fatalf("the commit is not in Latin-1: %q", obj)
	}

	h.click("History")
	h.until("both commits", func() bool { return hasExact(h, "déjà vu") && hasExact(h, `na\xefve bytes`) })
	h.click("déjà vu")
	h.until("its encoding", func() bool { return hasExact(h, "ISO-8859-1") })
	h.click(`na\xefve bytes`)
	h.until("marked", h.shows(`Not valid UTF-8: bytes shown as \xNN`))
}
