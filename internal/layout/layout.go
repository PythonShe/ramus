// Package layout keeps what was open when the app last ran: its windows,
// their tabs, and in each tab the section shown and what was chosen
// there, to open the same again at launch.
package layout

import (
	"encoding/json"
	"errors"
	"os"
	"sync"

	"ramus/internal/settings"
)

// Bounds on what is kept: a layout is small, and a file grown past them
// is not read whole.
const (
	MaxWindows = 20
	MaxTabs    = 100
	maxFile    = 1 << 20
)

// Window is one window's tabs, Active the one shown.
type Window struct {
	Tabs   []Tab `json:"tabs"`
	Active int   `json:"active"`
}

// Tab is one open repository.
type Tab struct {
	Path string `json:"path"`
	// Section is the sidebar section shown: "changes", "history", ….
	Section string `json:"section,omitempty"`
	// Commit is the commit chosen in the history, hex; File the changed
	// file chosen, slash-separated from the top of the work tree.
	Commit string `json:"commit,omitempty"`
	File   string `json:"file,omitempty"`
}

// Load reads the layout at path; none when there is no file. A file that
// does not parse is an error, with no layout.
func Load(path string) ([]Window, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var ws []Window
	if err := json.NewDecoder(&limited{f, maxFile}).Decode(&ws); err != nil {
		return nil, errors.New("layout: " + path + " is not a layout: " + err.Error())
	}
	return bound(ws), nil
}

func bound(ws []Window) []Window {
	var out []Window
	for _, w := range ws[:min(len(ws), MaxWindows)] {
		var tabs []Tab
		for _, t := range w.Tabs[:min(len(w.Tabs), MaxTabs)] {
			if t.Path != "" {
				tabs = append(tabs, t)
			}
		}
		if len(tabs) == 0 {
			continue
		}
		w.Tabs, w.Active = tabs, max(0, min(w.Active, len(tabs)-1))
		out = append(out, w)
	}
	return out
}

// Save writes the layout to path whole, so that a crash midway leaves the
// old one.
func Save(path string, ws []Window) error {
	data, err := json.MarshalIndent(bound(ws), "", "  ")
	if err != nil {
		return err
	}
	return settings.WriteFile(path, append(data, '\n'))
}

// Keeper writes the layout to its file off the caller's thread: only the
// latest asked for, one write at a time.
type Keeper struct {
	path string
	// OnError hears a write that failed, on the writing goroutine.
	OnError func(error)

	mu   sync.Mutex
	next []Window
	has  bool
	busy bool
	// gen counts what was asked for; a write taken before a later ask is
	// not made.
	gen int
	// write serializes writes, Keep's and Flush's.
	write sync.Mutex
}

// NewKeeper keeps the layout at path.
func NewKeeper(path string) *Keeper { return &Keeper{path: path} }

// Keep has ws written soon; a later Keep before then replaces it.
func (k *Keeper) Keep(ws []Window) {
	k.mu.Lock()
	k.next, k.has = ws, true
	k.gen++
	if k.busy {
		k.mu.Unlock()
		return
	}
	k.busy = true
	k.mu.Unlock()
	go k.run()
}

func (k *Keeper) run() {
	for {
		k.mu.Lock()
		if !k.has {
			k.busy = false
			k.mu.Unlock()
			return
		}
		ws, gen := k.next, k.gen
		k.next, k.has = nil, false
		k.mu.Unlock()
		k.write.Lock()
		k.mu.Lock()
		stale := gen != k.gen
		k.mu.Unlock()
		var err error
		if !stale {
			err = Save(k.path, ws)
		}
		k.write.Unlock()
		if err != nil && k.OnError != nil {
			k.OnError(err)
		}
	}
}

// Flush writes ws now, in place of anything pending, for the app's quit,
// which cannot wait for a goroutine.
func (k *Keeper) Flush(ws []Window) error {
	k.mu.Lock()
	k.next, k.has = nil, false
	k.gen++
	k.mu.Unlock()
	k.write.Lock()
	defer k.write.Unlock()
	return Save(k.path, ws)
}

// limited reads at most n bytes, then fails.
type limited struct {
	f *os.File
	n int64
}

func (l *limited) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, errors.New("file too large")
	}
	p = p[:min(int64(len(p)), l.n)]
	n, err := l.f.Read(p)
	l.n -= int64(n)
	return n, err
}
