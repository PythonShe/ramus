// Package layout keeps what was open when the app last ran: its windows,
// their tabs, and in each tab the section shown and what was chosen
// there, to open the same again at launch.
package layout

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"os"
	"reflect"
	"strings"
	"sync"

	"ramus/internal/statefile"
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

// Load reads the layout at path; none when there is no file. A value of
// the wrong type costs that value only, and a copy of the file is kept
// aside; a file that does not read at all is moved aside, with no
// layout. The error, a *statefile.LoadError, says which and where the
// file went.
func Load(path string) ([]Window, error) {
	data, err := read(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, statefile.Unreadable(path, err)
	}
	ws, _, _, err := decode(data)
	var partial *json.UnmarshalTypeError
	switch {
	case errors.As(err, &partial):
		return bound(ws), statefile.PartlyRead(path, data, err)
	case err != nil:
		return nil, statefile.Unreadable(path, err)
	}
	return bound(ws), nil
}

// read reads the file at path, up to maxFile.
func read(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(&limited{f, maxFile})
}

// Version is the format of the file written: an object with the
// windows under "windows", room for what a later version adds. 0.x wrote
// the windows as a bare array, which is still read.
//
// A file of a later version is read for what this version knows of it,
// and written back with the rest as it was, at every level: the keys of
// the file, of each window and of each tab this version does not know
// stay, and so does the file's version, never lowered.
const Version = 1

// file is the layout as written.
type file struct {
	Version int      `json:"version"`
	Windows []Window `json:"windows"`
}

// decode reads a layout file's content, the keys of the file a later
// version added, and the file's version, 0 for 0.x's. A value of the
// wrong type is a *json.UnmarshalTypeError, with the rest read.
func decode(data []byte) ([]Window, map[string]json.RawMessage, int, error) {
	var top any
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, nil, 0, err
	}
	switch top.(type) {
	case nil:
		return nil, nil, 0, nil
	case []any: // as 0.x wrote it
		var ws []Window
		err := json.Unmarshal(data, &ws)
		return ws, nil, 0, err
	case map[string]any:
		var f file
		err := json.Unmarshal(data, &f)
		return f.Windows, statefile.Extra(data, reflect.TypeFor[file]()), f.Version, err
	}
	return nil, nil, 0, errors.New("not a layout")
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
// old one. A file there that does not read, which Load could not keep
// aside, is not written over. What a later version wrote in the file
// stays (Version).
func Save(path string, ws []Window) error {
	old, err := read(path)
	var extra map[string]json.RawMessage
	version := Version
	if err == nil {
		var v int
		_, extra, v, err = decode(old)
		version = max(version, v)
	}
	// A value of the wrong type was copied aside when Load met it.
	var partial *json.UnmarshalTypeError
	if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.As(err, &partial) {
		return errors.New("layout: " + path + " does not read, so it is not written over: " + err.Error())
	}
	ws = bound(ws)
	windows, err := json.Marshal(ws)
	if err != nil {
		return err
	}
	if old != nil {
		windows = statefile.KeepUnknown(matching(old, ws), windows, reflect.TypeFor[[]Window]())
	}
	data, err := statefile.WithExtra(struct {
		Version int             `json:"version"`
		Windows json.RawMessage `json:"windows"`
	}{version, windows}, extra)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	json.Indent(&out, data, "", "  ")
	out.WriteByte('\n')
	return statefile.WriteFile(path, out.Bytes())
}

// matching is the windows of old, a layout file's content, as JSON, laid
// out as ws: for each window of ws the old one that had one of its tabs
// open, and in it each tab of ws as the old file had it, by its path; {}
// for one that is new. A window or a tab is matched by what it shows, not
// by its place, which closing one changes.
func matching(old []byte, ws []Window) json.RawMessage {
	var wins []json.RawMessage
	if json.Unmarshal(old, &wins) != nil {
		var f struct{ Windows []json.RawMessage }
		json.Unmarshal(old, &f)
		wins = f.Windows
	}
	type oldWindow struct {
		raw  map[string]json.RawMessage
		tabs map[string]json.RawMessage
	}
	var olds []oldWindow
	anyTab := map[string]json.RawMessage{}
	for _, w := range wins {
		var raw map[string]json.RawMessage
		if json.Unmarshal(w, &raw) != nil || raw == nil {
			continue
		}
		var v struct{ Tabs []json.RawMessage }
		json.Unmarshal(w, &v)
		tabs := map[string]json.RawMessage{}
		for _, t := range v.Tabs {
			var tab struct{ Path string }
			if json.Unmarshal(t, &tab) == nil && tab.Path != "" {
				tabs[tab.Path] = t
				if _, ok := anyTab[tab.Path]; !ok {
					anyTab[tab.Path] = t
				}
			}
		}
		olds = append(olds, oldWindow{raw, tabs})
	}
	used := make([]bool, len(olds))
	out := make([]json.RawMessage, len(ws))
	for i, w := range ws {
		j := -1
		for _, t := range w.Tabs {
			for k, o := range olds {
				if _, ok := o.tabs[t.Path]; ok && !used[k] {
					j = k
					break
				}
			}
			if j >= 0 {
				break
			}
		}
		win := map[string]json.RawMessage{}
		tabs := map[string]json.RawMessage{}
		if j >= 0 {
			used[j] = true
			maps.Copy(win, olds[j].raw)
			tabs = olds[j].tabs
		}
		list := make([]json.RawMessage, len(w.Tabs))
		for n, t := range w.Tabs {
			list[n] = json.RawMessage(`{}`)
			if r, ok := tabs[t.Path]; ok {
				list[n] = r
			} else if r, ok := anyTab[t.Path]; ok {
				list[n] = r
			}
		}
		for k := range win {
			if strings.EqualFold(k, "tabs") {
				delete(win, k)
			}
		}
		win["tabs"], _ = json.Marshal(list)
		out[i], _ = json.Marshal(win)
	}
	data, _ := json.Marshal(out)
	return data
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
