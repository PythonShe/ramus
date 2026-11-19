// Package settings reads and writes the app's settings: one JSON file the
// user may edit by hand, which the settings view edits too.
package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	"ramus/internal/statefile"
)

// Settings are the user's choices. The zero value of a field means its
// default, so a file that leaves a field out keeps it current.
type Settings struct {
	// Theme is "system", "light" or "dark".
	Theme string `json:"theme,omitempty"`
	// Editor opens files: a command and its arguments, the file appended.
	Editor []string `json:"editor,omitempty"`
	// Terminal opens a repository: a command and its arguments, the
	// directory appended.
	Terminal []string `json:"terminal,omitempty"`
	// SnapshotDays is how long snapshots of dropped work are kept.
	SnapshotDays int `json:"snapshotDays,omitempty"`
	// RecentLimit bounds the recent repositories listed.
	RecentLimit int `json:"recentLimit,omitempty"`
	// Actions are the user's own commands.
	Actions []Action `json:"actions,omitempty"`
	// Emails are the user's own addresses besides each repository's
	// user.email, for statistics of their own commits.
	Emails []string `json:"emails,omitempty"`
	// DiffSplit shows patches side by side; DiffNoWrap leaves their long
	// lines unwrapped, which they are not by default. ReviewUnified shows
	// the patches of the review one above the other: it has a layout of
	// its own, side by side by default, as it has the window's width.
	DiffSplit     bool `json:"diffSplit,omitempty"`
	DiffNoWrap    bool `json:"diffNoWrap,omitempty"`
	ReviewUnified bool `json:"reviewUnified,omitempty"`
	// PictureCompare is how a picture's two sides compare: side by side
	// (0), swiped (1), faded one over the other (2) or by their
	// difference (3). SVGAsPicture shows a picture git diffs as text, as
	// SVG, as a picture rather than as its text.
	PictureCompare int  `json:"pictureCompare,omitempty"`
	SVGAsPicture   bool `json:"svgAsPicture,omitempty"`
	// ShowDependencyFolders lists the folders package managers and tools
	// fill (node_modules, .venv) that git does not track among the
	// changes; by default they are left out of all a repository runs.
	ShowDependencyFolders bool `json:"showDependencyFolders,omitempty"`
	// NoFocusRefresh turns off reading the repository again when the
	// window gains focus, the fallback for changes the watcher missed.
	NoFocusRefresh bool `json:"noFocusRefresh,omitempty"`
	// NoFetch keeps open repositories from fetching every remote in the
	// background, which they do every FetchMinutes: DefaultFetchMinutes
	// when 0, and at least MinFetchMinutes.
	NoFetch      bool `json:"noFetch,omitempty"`
	FetchMinutes int  `json:"fetchMinutes,omitempty"`
	// PullRequests shows each branch's pull request, its state and an
	// open one's checks in the branches view, and the pull request a
	// commit came in by in its detail, asked of the user's own gh or glab.
	PullRequests bool `json:"pullRequests,omitempty"`
	// HiddenSections are the sidebar's items and sections left out, by
	// key ("reflog", "tags").
	HiddenSections []string `json:"hiddenSections,omitempty"`
	// NoSidebar leaves the repository sidebar out.
	NoSidebar bool `json:"noSidebar,omitempty"`
	// HistoryColumns are the history list's columns the user resized or
	// collapsed, by key: "graph", "author", "date", "hash".
	HistoryColumns map[string]Column `json:"historyColumns,omitempty"`
	// HistoryMessage is the width the user gave the history's message
	// column, which it keeps however wide the list, leaving the rest free
	// at the end of the rows; 0 for the message to fill them.
	HistoryMessage float32 `json:"historyMessage,omitempty"`
	// Tables are the order and the widths the user gave the columns of
	// the other lists, by list ("branches", "reflog"): each column by its
	// key, never its title, which changes with the language.
	Tables map[string]TableLayout `json:"tables,omitempty"`
	// Keys are the user's shortcuts by command, in place of the app's
	// own: accelerators such as "CmdOrCtrl+Shift+P"; an empty list
	// leaves a command without one.
	Keys map[string][]string `json:"keys,omitempty"`
	// CloneParent is the folder the last clone went into, which the next
	// one offers.
	CloneParent string `json:"cloneParent,omitempty"`
}

// Column is a list column as the user left it: its width, 0 for the
// list's own, and whether it is shut, or, for the history's graph, in its
// thin form.
type Column struct {
	Width  float32 `json:"width,omitempty"`
	Hidden bool    `json:"hidden,omitempty"`
	Thin   bool    `json:"thin,omitempty"`
}

// TableLayout is a table's columns as the user left them: the order of
// those moved, and the widths of those resized, by key. Columns it does
// not name keep the table's own order and width.
type TableLayout struct {
	Order  []string           `json:"order,omitempty"`
	Widths map[string]float32 `json:"widths,omitempty"`
}

// MinFetchMinutes is the shortest wait between background fetches,
// DefaultFetchMinutes the one unless the user sets another.
const (
	MinFetchMinutes     = 5
	DefaultFetchMinutes = 15
)

// FetchEvery is the minutes between background fetches, 0 for none.
func (s Settings) FetchEvery() int {
	if s.NoFetch {
		return 0
	}
	if s.FetchMinutes <= 0 {
		return DefaultFetchMinutes
	}
	return min(max(s.FetchMinutes, MinFetchMinutes), maxFetchMinutes)
}

// Action is a command of the user's, shown in the command palette and in
// the context menus of what it acts on.
type Action struct {
	Name string `json:"name"`
	// Command is the program, by its full path, and its arguments. In
	// each argument ${repo}, ${branch}, ${commit} and ${file} stand for
	// what the action runs on, and ${prompt:Question} for what the user
	// answers when it starts. No shell runs it.
	Command []string `json:"command"`
	// On is where it shows besides the palette: "file", "branch" or
	// "commit"; empty for the repository only.
	On string `json:"on,omitempty"`
}

// Themes are the values Theme takes.
var Themes = []string{"system", "light", "dark"}

// Defaults fill fields left out.
var Defaults = Settings{Theme: "system", SnapshotDays: 14, RecentLimit: 20}

// Bounds on the numbers a file edited by hand may hold: past them a
// number means nothing more, and a time made of it overflows (days as a
// time.Duration past 106751), which could expire every snapshot of
// dropped work at once.
const (
	maxSnapshotDays = 36500
	maxRecentLimit  = 1000
	maxFetchMinutes = 7 * 24 * 60
	// maxWidth is more than any screen has.
	maxWidth = 1 << 14
	// pictureCompareLast is the last way pictures compare.
	pictureCompareLast = 3
)

// WithDefaults returns s with every unset or invalid field at its default,
// and every number within its bounds.
func (s Settings) WithDefaults() Settings {
	if !slices.Contains(Themes, s.Theme) {
		s.Theme = Defaults.Theme
	}
	if s.SnapshotDays <= 0 {
		s.SnapshotDays = Defaults.SnapshotDays
	}
	s.SnapshotDays = min(s.SnapshotDays, maxSnapshotDays)
	if s.RecentLimit <= 0 {
		s.RecentLimit = Defaults.RecentLimit
	}
	s.RecentLimit = min(s.RecentLimit, maxRecentLimit)
	if s.FetchMinutes <= 0 {
		s.FetchMinutes = 0
	} else {
		s.FetchMinutes = min(max(s.FetchMinutes, MinFetchMinutes), maxFetchMinutes)
	}
	if s.PictureCompare < 0 || s.PictureCompare > pictureCompareLast {
		s.PictureCompare = 0
	}
	if !(s.HistoryMessage > 0 && s.HistoryMessage <= maxWidth) {
		s.HistoryMessage = 0
	}
	s.HistoryColumns = validColumns(s.HistoryColumns)
	s.Tables = validTables(s.Tables)
	return s
}

// validColumns returns columns with a width no column can take at the
// list's own. The map is new: the one given may be shared with another
// window.
func validColumns(cols map[string]Column) map[string]Column {
	if cols == nil {
		return nil
	}
	out := make(map[string]Column, len(cols))
	for k, c := range cols {
		if !(c.Width > 0 && c.Width <= maxWidth) {
			c.Width = 0
		}
		out[k] = c
	}
	return out
}

// validTables returns tables without the widths no column can take, as a
// file edited by hand may give, nor the tables left with nothing. The
// maps are new: the ones given may be shared with another window.
func validTables(tables map[string]TableLayout) map[string]TableLayout {
	var out map[string]TableLayout
	for key, l := range tables {
		var widths map[string]float32
		for id, w := range l.Widths {
			if w > 0 && w <= maxWidth {
				if widths == nil {
					widths = map[string]float32{}
				}
				widths[id] = w
			}
		}
		if len(l.Order) == 0 && widths == nil {
			continue
		}
		if out == nil {
			out = map[string]TableLayout{}
		}
		out[key] = TableLayout{Order: l.Order, Widths: widths}
	}
	return out
}

// Load reads the file at path. A missing file gives the defaults. A
// field of the wrong type costs that field only: it is at its default,
// the file keeps it (Save does not write over it), and the error says
// which. A file that does not read at all is moved aside, so that saving
// cannot lose it, and gives the defaults; the error, a *statefile.LoadError,
// says where it went, for the app to tell the user.
func Load(path string) (Settings, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Defaults, nil
	}
	if err != nil {
		return Defaults, statefile.Unreadable(path, err)
	}
	s, partial, err := decode(data)
	switch {
	case err == nil:
		return s, nil
	case partial:
		return s, &statefile.LoadError{Path: path, Err: err, Partial: true}
	}
	return Defaults, statefile.Unreadable(path, err)
}

// decode reads the content of a settings file. One that is not a JSON
// object is an error with nothing read; partial is set when only some
// fields did not decode, the others read.
func decode(data []byte) (s Settings, partial bool, err error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return Defaults, false, describe(data, err)
	}
	// A value of the wrong type leaves its field as it was and the rest
	// decodes: encoding/json carries on past it.
	if err := json.Unmarshal(data, &s); err != nil {
		err = badFields(&s, top, err)
		return s.WithDefaults(), true, err
	}
	return s.WithDefaults(), false, nil
}

// badFields says which of the fields in top do not decode, err when it
// cannot tell, and leaves each of them unset in s. encoding/json fills a
// list or a map up to the value that does not fit: the half read would
// be in force, and a save would write it over the user's text, which it
// keeps for a field left unset.
func badFields(s *Settings, top map[string]json.RawMessage, err error) error {
	var msgs []string
	t := reflect.TypeFor[Settings]()
	v := reflect.ValueOf(s).Elem()
	for i := range t.NumField() {
		key, raw, ok := statefile.LookupKey(top, jsonName(t.Field(i)))
		if !ok {
			continue
		}
		if ferr := json.Unmarshal(raw, reflect.New(t.Field(i).Type).Interface()); ferr != nil {
			msgs = append(msgs, fmt.Sprintf("%q: %v", key, ferr))
			v.Field(i).SetZero()
		}
	}
	if len(msgs) == 0 {
		return err
	}
	return errors.New(strings.Join(msgs, "; "))
}

func jsonName(f reflect.StructField) string {
	name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if name == "-" {
		return ""
	}
	return name
}

func describe(data []byte, err error) error {
	var se *json.SyntaxError
	if errors.As(err, &se) {
		line := 1 + bytes.Count(data[:min(int(se.Offset), len(data))], []byte{'\n'})
		return fmt.Errorf("line %d: %w", line, err)
	}
	return err
}

// Save writes s to path, keeping keys the file has that this version
// does not know, and replacing the file at once so a reader never sees
// half of it. A file there that does not read is not written over: Save
// fails and leaves it for the user to mend.
func Save(path string, s Settings) error {
	_, _, err := save(path, s, nil)
	return err
}

// save is Save. With base, the settings s was changed from, a field s
// has as base had it is left as the file has it, edited by hand since;
// got is then what the file holds once written, data its content.
//
// Two copies of the app keep one file under its lock (statefile.Lock), so
// that each reads what the other wrote before it replaces the file: both
// end with every change, the one written last winning a setting both
// changed. A writer that takes no lock, an editor, is guarded against by
// the check before the rename only, which leaves a moment between the
// check and the rename where its save is replaced.
func save(path string, s Settings, base *Settings) (got Settings, data []byte, err error) {
	if failSave != nil {
		if err := failSave(); err != nil {
			return s, nil, err
		}
	}
	unlock, err := statefile.Lock(path, lockWait)
	if err != nil {
		return s, nil, err
	}
	defer unlock()
	for range maxMerges {
		got, data, err = saveOnce(path, s, base)
		if !errors.Is(err, statefile.ErrChanged) {
			return got, data, err
		}
	}
	return s, nil, fmt.Errorf("settings: %s kept changing while it was written: %w", path, err)
}

// maxMerges bounds the merges of a save whose file changes each time
// before it is replaced.
const maxMerges = 5

// lockWait bounds how long a save waits for another copy of the app's: a
// save holds the lock for a read and a small write.
const lockWait = 2 * time.Second

// saveOnce is one try of save, which fails with statefile.ErrChanged when
// the file changes between its reading and its replacement.
func saveOnce(path string, s Settings, base *Settings) (got Settings, data []byte, err error) {
	merged := map[string]json.RawMessage{}
	old, err := os.ReadFile(path)
	existed := err == nil
	switch {
	case errors.Is(err, fs.ErrNotExist):
		old = nil
	case err != nil:
		return s, nil, notOverwritten(path, err)
	default:
		if err := json.Unmarshal(old, &merged); err != nil {
			return s, nil, notOverwritten(path, describe(old, err))
		}
		if merged == nil { // the file says null
			merged = map[string]json.RawMessage{}
		}
	}
	known, err := json.Marshal(s)
	if err != nil {
		return s, nil, err
	}
	var fields map[string]json.RawMessage
	json.Unmarshal(known, &fields)
	unset := fieldsOf(Defaults.WithDefaults())
	now := fieldsOf(s.WithDefaults())
	var was map[string]json.RawMessage
	if base != nil {
		was = fieldsOf(base.WithDefaults())
	}
	t := reflect.TypeFor[Settings]()
	for i := range t.NumField() {
		name := jsonName(t.Field(i))
		if name == "" {
			continue
		}
		// Not changed in the app: the file's, whatever it says now.
		if base != nil && bytes.Equal(now[name], was[name]) {
			continue
		}
		// A value Load could not read is in force as the default: while s
		// leaves it there, the file keeps what the user wrote.
		if key, raw, ok := statefile.LookupKey(merged, name); ok && bytes.Equal(now[name], unset[name]) &&
			json.Unmarshal(raw, reflect.New(t.Field(i).Type).Interface()) != nil {
			if key != name {
				delete(merged, key)
				merged[name] = raw
			}
			continue
		}
		// Every other key of this version is dropped first: one left out
		// of the new file is at its default and must not keep its old
		// value.
		_, was, _ := statefile.LookupKey(merged, name)
		for k := range merged {
			if strings.EqualFold(k, name) {
				delete(merged, k)
			}
		}
		if v, ok := fields[name]; ok {
			if was != nil {
				v = statefile.KeepUnknown(was, v, t.Field(i).Type)
			}
			merged[name] = v
		}
	}
	data, err = json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return s, nil, err
	}
	data = append(data, '\n')
	if !bytes.Equal(data, old) {
		// An edit saved between the reading and the rename would be lost.
		unchanged := func() bool {
			if beforeReplace != nil {
				beforeReplace()
			}
			now, err := os.ReadFile(path)
			if !existed {
				return errors.Is(err, fs.ErrNotExist)
			}
			return err == nil && bytes.Equal(now, old)
		}
		if err := statefile.WriteFileIf(path, data, unchanged); err != nil {
			return s, nil, err
		}
	}
	got, _, _ = decode(data)
	return got, data, nil
}

// same reports whether a and b are the same settings, a field left at
// its default and one set to it alike.
func same(a, b Settings) bool {
	fa, fb := fieldsOf(a.WithDefaults()), fieldsOf(b.WithDefaults())
	if len(fa) != len(fb) {
		return false
	}
	for k, v := range fa {
		if !bytes.Equal(v, fb[k]) {
			return false
		}
	}
	return true
}

// merge is ours with what theirs changed since base: each field ours has
// as base had it is theirs.
func merge(base, ours, theirs Settings) Settings {
	was, now := fieldsOf(base.WithDefaults()), fieldsOf(ours.WithDefaults())
	out := reflect.ValueOf(&ours).Elem()
	from := reflect.ValueOf(theirs)
	t := out.Type()
	for i := range t.NumField() {
		if name := jsonName(t.Field(i)); name != "" && bytes.Equal(now[name], was[name]) {
			out.Field(i).Set(from.Field(i))
		}
	}
	return ours.WithDefaults()
}

// notOverwritten is the error of a save refused because the file there
// does not read.
func notOverwritten(path string, err error) error {
	return fmt.Errorf("settings: %s does not read, so it is not written over until it is mended: %w", path, err)
}

// fieldsOf is s as JSON, by key.
func fieldsOf(s Settings) map[string]json.RawMessage {
	data, _ := json.Marshal(s)
	var m map[string]json.RawMessage
	json.Unmarshal(data, &m)
	return m
}

// beforeReplace, when set, runs as a save is about to replace its file:
// a test edits the file there.
var beforeReplace func()

// failSave, when set, runs as a save starts, which fails with its error:
// a test's disk that is full.
var failSave func() error
