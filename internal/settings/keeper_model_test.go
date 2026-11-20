package settings

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ramus/internal/statefile"
)

// model is the app as it uses a Keeper: its settings, changed on its
// main thread (the test's goroutine) and kept, and the readings of the
// file it hears, which it takes when the test says.
type model struct {
	t    *testing.T
	p    string
	k    *Keeper
	app  Settings
	news chan FromFile
	errs chan error
}

// newModel starts an app on the settings file at p, as the app does.
func newModel(t *testing.T, p string) *model {
	t.Helper()
	s := load(t, p)
	m := &model{t: t, p: p, k: NewKeeper(p), app: s, news: make(chan FromFile, 64), errs: make(chan error, 64)}
	m.k.Started(s)
	m.k.OnFile = func(f FromFile) { m.news <- f }
	t.Cleanup(m.k.Close)
	return m
}

// load is Load, which may read a file in part.
func load(t *testing.T, p string) Settings {
	t.Helper()
	s, err := Load(p)
	if le, ok := err.(*statefile.LoadError); err != nil && (!ok || !le.Partial) {
		t.Fatal(err)
	}
	return s
}

// keep changes the app's settings and has them written.
func (m *model) keep(change func(*Settings)) {
	change(&m.app)
	m.k.Keep(m.app, func(err error) { m.errs <- err })
}

// heard waits for the keeper and returns the readings it handed on.
func (m *model) heard() []FromFile {
	m.k.Wait()
	var out []FromFile
	for {
		select {
		case f := <-m.news:
			out = append(out, f)
		default:
			return out
		}
	}
}

func (m *model) take(fs ...FromFile) {
	for _, f := range fs {
		m.app, _ = m.k.Take(f, m.app)
	}
}

// focus is a window gaining focus: the file is read again, and what is
// heard taken.
func (m *model) focus() {
	m.k.Check(func(err error) { m.errs <- err })
	m.take(m.heard()...)
}

// failures are the errors heard since the last call.
func (m *model) failures() int {
	n := 0
	for {
		select {
		case <-m.errs:
			n++
		default:
			return n
		}
	}
}

func hand(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// agrees has what is heard taken, a focus, and then the app and the file
// holding the same settings: nothing the app shows differs from the file
// with nothing left to bring them together.
func (m *model) agrees() {
	m.t.Helper()
	m.take(m.heard()...)
	m.focus()
	onDisk := load(m.t, m.p)
	if !same(onDisk, m.app) {
		m.t.Fatalf("the app and the file differ:\napp  %+v\nfile %+v", m.app, onDisk)
	}
}

func split(s *Settings)  { s.DiffSplit = true }
func noWrap(s *Settings) { s.DiffNoWrap = true }
func dark(s *Settings)   { s.Theme = "dark" }

// TestKeeperInterleavings runs Keep, writes that fail, readings taken
// late, hand edits, a second window and a second copy of the app in the
// orders that once lost or undid a change, and checks the invariants on
// Keeper: the change the user made last is the one in force, in the app
// and in the file, and the two end the same.
func TestKeeperInterleavings(t *testing.T) {
	cases := []struct {
		name string
		file string
		run  func(t *testing.T, m *model)
		want func(s Settings) bool
	}{{
		// The first focus after launch reads the file; the user changes a
		// setting before the reading is taken.
		name: "reading older than a save",
		run: func(t *testing.T, m *model) {
			m.k.Check(nil)
			old := m.heard()
			m.keep(dark)
			m.k.Wait()
			m.take(old...)
		},
		want: func(s Settings) bool { return s.Theme == "dark" },
	}, {
		name: "reading made while a save waits to be written",
		run: func(t *testing.T, m *model) {
			m.k.file.Lock()
			m.keep(split)
			f, err := m.k.read()
			m.k.file.Unlock()
			if err != nil || f == nil {
				t.Fatalf("read %v %v", f, err)
			}
			m.k.Wait()
			m.take(*f)
		},
		want: func(s Settings) bool { return s.DiffSplit },
	}, {
		// A change of the app's not yet written is not the file's: a
		// second reading taken does not undo it.
		name: "change not yet written across two readings",
		run: func(t *testing.T, m *model) {
			hand(t, m.p, `{"emails": ["me@x"]}`)
			m.k.Check(nil)
			first := m.heard()
			m.app.NoSidebar = true
			// The reading taken queues the change's write, which is not
			// to land before the hand edit: the edit would then undo it.
			m.k.file.Lock()
			m.take(first...)
			hand(t, m.p, `{"emails": ["me@x", "work@x"]}`)
			m.k.file.Unlock()
			m.focus()
			if !m.app.NoSidebar || len(m.app.Emails) != 2 {
				t.Fatalf("after the second reading: %+v", m.app)
			}
			m.keep(func(*Settings) {})
		},
		want: func(s Settings) bool { return s.NoSidebar && len(s.Emails) == 2 },
	}, {
		name: "failed write, then a change of another setting",
		run: func(t *testing.T, m *model) {
			hand(t, m.p, `{"theme": `)
			m.keep(noWrap)
			m.k.Wait()
			if m.failures() != 1 {
				t.Fatal("the write into a broken file did not fail")
			}
			hand(t, m.p, `{}`)
			m.keep(split)
		},
		want: func(s Settings) bool { return s.DiffNoWrap && s.DiffSplit },
	}, {
		name: "failed write, then a hand edit read on focus",
		run: func(t *testing.T, m *model) {
			hand(t, m.p, `{"theme": `)
			m.keep(noWrap)
			m.k.Wait()
			hand(t, m.p, `{"emails": ["me@x"]}`)
			m.focus()
			if !m.app.DiffNoWrap || len(m.app.Emails) != 1 {
				t.Fatalf("after the focus: %+v", m.app)
			}
			m.keep(split)
		},
		want: func(s Settings) bool { return s.DiffNoWrap && s.DiffSplit && len(s.Emails) == 1 },
	}, {
		name: "hand edit while a save waits to be written",
		run: func(t *testing.T, m *model) {
			m.k.file.Lock()
			m.keep(split)
			hand(t, m.p, `{"emails": ["me@x"]}`)
			m.k.file.Unlock()
		},
		want: func(s Settings) bool { return s.DiffSplit && len(s.Emails) == 1 },
	}, {
		name: "hand edit between a save's reading and its rename",
		run: func(t *testing.T, m *model) {
			edited := false
			beforeReplace = func() {
				if !edited {
					edited = true
					hand(t, m.p, `{"emails": ["me@x"]}`)
				}
			}
			t.Cleanup(func() { beforeReplace = nil })
			m.keep(split)
			m.k.Wait()
			if !edited {
				t.Fatal("no edit made")
			}
		},
		want: func(s Settings) bool { return s.DiffSplit && len(s.Emails) == 1 },
	}, {
		// Two windows share the keeper and the app's settings.
		name: "two windows",
		run: func(t *testing.T, m *model) {
			m.k.Check(nil)
			old := m.heard()
			m.keep(dark)
			m.keep(split)
			m.k.Wait()
			m.take(old...)
		},
		want: func(s Settings) bool { return s.Theme == "dark" && s.DiffSplit },
	}, {
		name: "a key deleted by hand while a save waits",
		file: `{"emails": ["me@x"]}`,
		run: func(t *testing.T, m *model) {
			m.k.file.Lock()
			m.keep(split)
			hand(t, m.p, `{}`)
			m.k.file.Unlock()
		},
		want: func(s Settings) bool { return s.DiffSplit && len(s.Emails) == 0 },
	}, {
		// A value this version cannot read stays as the user wrote it.
		name: "a value that does not read",
		file: `{"actions": [{"name": "Lint", "command": "make"}]}`,
		run: func(t *testing.T, m *model) {
			m.keep(split)
			m.k.Wait()
			if data, _ := os.ReadFile(m.p); !strings.Contains(string(data), `"command": "make"`) {
				t.Fatalf("the action written over: %s", data)
			}
		},
		want: func(s Settings) bool { return s.DiffSplit && len(s.Actions) == 0 },
	}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "settings.json")
			if c.file != "" {
				hand(t, p, c.file)
			}
			m := newModel(t, p)
			c.run(t, m)
			m.agrees()
			if !c.want(m.app) {
				t.Fatalf("app ends with %+v", m.app)
			}
		})
	}
}

// Two copies of the app on one file, as an app and a second one started
// apart (a test's, or one a lock did not stop): each keeps what the other
// changed, and both end with every change.
func TestKeeperTwoCopies(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	a, b := newModel(t, p), newModel(t, p)
	a.keep(dark)
	a.k.Wait()
	b.keep(split)
	b.agrees()
	a.agrees()
	for _, m := range []*model{a, b} {
		if m.app.Theme != "dark" || !m.app.DiffSplit {
			t.Fatalf("%+v", m.app)
		}
	}
}

// twoCopiesRuns is how many times TestKeeperTwoCopiesAtOnce races: once
// lost a change about one run in twelve.
const twoCopiesRuns = 100

// Two copies of the app save different settings at the same moment, many
// times over: the file ends with both changes every time, and each copy
// then reads the other's.
func TestKeeperTwoCopiesAtOnce(t *testing.T) {
	for i := range twoCopiesRuns {
		p := filepath.Join(t.TempDir(), "settings.json")
		hand(t, p, `{}`)
		a, b := newModel(t, p), newModel(t, p)
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() { <-start; a.keep(dark); a.k.Wait() })
		wg.Go(func() { <-start; b.keep(split); b.k.Wait() })
		close(start)
		wg.Wait()
		if s := load(t, p); s.Theme != "dark" || !s.DiffSplit {
			t.Fatalf("run %d: one change lost: %+v", i, s)
		}
		a.agrees()
		b.agrees()
		if a.app.Theme != "dark" || !a.app.DiffSplit || b.app.Theme != "dark" || !b.app.DiffSplit {
			t.Fatalf("run %d: the copies end with %+v and %+v", i, a.app, b.app)
		}
	}
}

// A Save with no base, as the probe's and a test's, writes a value it
// could not read back as the user wrote it, not the half of it read.
func TestSaveKeepsAValueThatDoesNotRead(t *testing.T) {
	p := filepath.Join(t.TempDir(), "settings.json")
	hand(t, p, `{"actions": [{"name": "Lint", "command": "make"}]}`)
	s, err := Load(p)
	if err == nil || len(s.Actions) != 0 {
		t.Fatalf("loaded %+v, %v", s.Actions, err)
	}
	s.DiffSplit = true
	if err := Save(p, s); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(p); !strings.Contains(string(data), `"command": "make"`) {
		t.Fatalf("the action written over: %s", data)
	}
}
