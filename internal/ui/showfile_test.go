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
	t.Cleanup(h1.closeAll) // it makes no repository, which would register it
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

// ramus log or blame of a file in a folder of a repository whose tab is
// still opening (the tab of the last session at a cold start, or one
// just opened) opens that folder, whose tab closes for the repository's:
// the file is shown there.
func TestShowFileInAFolderOfATabStillOpening(t *testing.T) {
	for _, beforeGit := range []bool{true, false} {
		name := "opening"
		if beforeGit {
			name = "before git"
		}
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			r := h.repoWith("src/b.go")
			h.trust.Set(r.GitDir(), true)
			runner := h.app.Runner
			if beforeGit {
				h.app.Runner = nil
			}
			h.app.Open(r.Dir)
			if tb := h.app.current(); tb != nil && tb.s.Repo != nil {
				t.Skip("the repository opened at once")
			}
			h.app.ShowFile(filepath.Join(r.Dir, "src", "b.go"), false)
			if beforeGit {
				h.until("the repository asked for", func() bool { return len(h.app.pending) > 1 })
				h.app.SetRunner(runner, nil)
			}
			h.until("the file", func() bool {
				tb := h.app.current()
				return tb != nil && tb.sel == "file" && tb.file.path == "src/b.go"
			})
			if len(h.app.tabs) != 1 || h.app.pendingFile.abs != "" {
				t.Fatalf("%d tabs, waits for %+v", len(h.app.tabs), h.app.pendingFile)
			}
		})
	}
}

// A file asked for while one asked before waits for its repository to
// open replaces it: the one asked first is not shown over it when the
// repository opens and its answer lands last.
func TestShowFileALaterAskReplacesOneWaiting(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith("a.txt", "b.txt")
	h.trust.Set(r.GitDir(), true)
	orig := h.app.Update
	pump := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.After(20 * time.Second)
		for !ok() {
			select {
			case fn := <-h.updates:
				fn()
			case <-deadline:
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}
	h.app.ShowFile(filepath.Join(r.Dir, "a.txt"), false)
	pump("a.txt waiting", func() bool { return h.app.pendingFile.abs != "" })
	if len(h.app.tabs) != 1 || h.app.tabs[0].s.Repo != nil {
		t.Skipf("the repository opened before the second ask: %d tabs", len(h.app.tabs))
	}
	tb := h.app.tabs[0]
	// The later ask, its answer held.
	heldB := make(chan func(), 1)
	h.app.Update = func(fn func()) { heldB <- fn }
	h.app.ShowFile(filepath.Join(r.Dir, "b.txt"), false)
	var landB func()
	select {
	case landB = <-heldB:
	case <-time.After(20 * time.Second):
		t.Fatal("the second ask found nothing")
	}
	// The repository opens; what is found for a file still waiting is
	// held too.
	var heldA []func()
	hold := true
	h.app.Update = func(fn func()) {
		if hold {
			heldA = append(heldA, fn)
			return
		}
		orig(fn)
	}
	h.until("the repository", func() bool { return tb.s.Repo != nil })
	h.tt.Frame()
	time.Sleep(300 * time.Millisecond)
	hold = false
	landB()
	h.until("b.txt", func() bool { return tb.sel == "file" && tb.file.path == "b.txt" })
	for _, fn := range heldA {
		fn()
	}
	h.tt.Frame()
	if tb.file.path != "b.txt" {
		t.Fatalf("%s, asked for first, is shown over b.txt, asked for after it", tb.file.path)
	}
}

// The tab of a file's folder that closes for the repository's tab in
// another window hands that window the file, not only the repository.
func TestShowFileHandedOnWithTheTabOfItsFolder(t *testing.T) {
	h1, h2 := newHarness(t), newHarness(t)
	r := h2.repoWith("src/b.go")
	t.Cleanup(h1.closeAll)
	h1.trust.Set(r.GitDir(), true)
	h2.trust.Set(r.GitDir(), true)
	// As windows.go wires them.
	h1.app.ShowOpen = func(p string) bool {
		if h2.app.Has(p) {
			h2.app.Open(p)
			return true
		}
		return false
	}
	h1.app.ShowFileOpen = func(dir, abs string, blame bool) bool {
		if h2.app.Has(dir) || h2.app.HoldsFile(abs) {
			h2.app.ShowFile(abs, blame)
			return true
		}
		return false
	}
	h1.app.ShowFile(filepath.Join(r.Dir, "src", "b.go"), true)
	deadline := time.After(20 * time.Second)
	for h1.app.pendingFile.abs == "" {
		select {
		case fn := <-h1.updates:
			fn()
		case <-deadline:
			t.Fatal("the folder was not opened for the file")
		}
	}
	// The other window opens the repository while this one's tab of the
	// folder has yet to find it is in it.
	h2.app.Open(r.Dir)
	h1.until("the folder's tab handed on", func() bool { return len(h1.app.tabs) == 0 })
	h2.until("the file", func() bool {
		tb := h2.app.current()
		return tb != nil && tb.sel == "file" && tb.file.path == "src/b.go" && tb.file.blame == 1
	})
	if len(h2.app.tabs) != 1 || h1.app.pendingFile.abs != "" || h2.app.pendingFile.abs != "" {
		t.Fatalf("window 2 has %d tabs; window 1 waits for %+v, window 2 for %+v", len(h2.app.tabs), h1.app.pendingFile, h2.app.pendingFile)
	}
}
