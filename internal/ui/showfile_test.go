package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ramus/internal/library"
)

// ramus log and blame, and a second instance, return at once while where
// the file is takes a while to work out, as on a volume that stopped
// answering; an ask made since wins over a slow one.
func TestShowFileLooksOffTheMainThread(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith("a.txt", "b.txt")
	h.trust.Set(r.GitDir(), true)
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(r.Dir, link); err != nil {
		t.Skip(err)
	}
	h.app.Open(r.Dir)
	h.until("the repository", func() bool { tb := h.app.current(); return tb.s.Repo != nil && tb.real != "" })
	tb := h.app.current()

	release := make(chan struct{})
	h.app.resolve = func(p string) string {
		if strings.HasPrefix(p, link) {
			<-release
		}
		return library.Clean(p)
	}
	// The slow ask's answer, once it lands.
	orig, landed := h.app.Update, make(chan struct{})
	h.app.Update = func(fn func()) { orig(func() { fn(); close(landed) }) }
	done := make(chan struct{})
	go func() {
		h.app.ShowFile(filepath.Join(link, "a.txt"), false)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("ShowFile waited for the path to be resolved")
	}
	h.app.Update = orig

	h.app.ShowFile(filepath.Join(r.Dir, "b.txt"), false)
	h.until("b.txt", func() bool { return tb.sel == "file" && tb.file.path == "b.txt" })
	close(release)
	h.until("the slow answer", func() bool {
		select {
		case <-landed:
			return true
		default:
			return false
		}
	})
	h.tt.Frame()
	if tb.file.path != "b.txt" {
		t.Fatalf("the slow ask's %s shown over the later one", tb.file.path)
	}

	// Through the link alone: found once resolved.
	h.app.resolve = nil
	h.app.ShowFile(filepath.Join(link, "a.txt"), true)
	h.until("a.txt", func() bool { return tb.file.path == "a.txt" && tb.file.blame == 1 })
	if len(h.app.tabs) != 1 {
		t.Fatalf("%d tabs", len(h.app.tabs))
	}
}

// ramus log or blame of a file whose repository another window has open
// shows it there, as the window asked hands the repository to that one.
func TestShowFileInTheWindowThatHasIt(t *testing.T) {
	h1, h2 := newHarness(t), newHarness(t)
	r := h2.repoWith("a.txt", "src/b.txt")
	h1.trust.Set(r.GitDir(), true)
	h2.trust.Set(r.GitDir(), true)
	h2.app.Open(r.Dir)
	h2.until("the repository", func() bool { t2 := h2.app.current(); return t2 != nil && t2.s.Repo != nil })
	t2 := h2.app.current()
	// As windows.go wires them.
	handed := 0
	h1.app.ShowOpen = func(p string) bool {
		if h2.app.Has(p) {
			h2.app.Open(p)
			return true
		}
		return false
	}
	h1.app.ShowFileOpen = func(dir, abs string, blame bool) bool {
		if h2.app.Has(dir) || h2.app.HoldsFile(abs) {
			handed++
			h2.app.ShowFile(abs, blame)
			return true
		}
		return false
	}

	for _, c := range []struct {
		path, rel string
		blame     bool
	}{{"a.txt", "a.txt", true}, {"src/b.txt", "src/b.txt", false}, {".", "", false}} {
		before := handed
		h1.app.ShowFile(filepath.Join(r.Dir, filepath.FromSlash(c.path)), c.blame)
		h1.until("handed over "+c.path, func() bool { return handed > before })
		if c.rel == "" {
			// The repository's own folder: its tab shown.
			h2.until("the repository shown", func() bool { return h2.app.current() == t2 })
		} else {
			h2.until(c.path+" shown", func() bool {
				return t2.sel == "file" && t2.file.path == c.rel && (t2.file.blame == 1) == c.blame
			})
		}
		if len(h1.app.tabs) != 0 || h1.app.pendingFile.abs != "" {
			t.Fatalf("%s: window 1 has %d tabs, waits for %+v", c.path, len(h1.app.tabs), h1.app.pendingFile)
		}
	}
}
