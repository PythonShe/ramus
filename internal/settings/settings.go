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
	"path/filepath"
	"reflect"
	"slices"
	"strings"
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
	// DiffSplit shows patches side by side, DiffWrap wraps their long
	// lines.
	DiffSplit bool `json:"diffSplit,omitempty"`
	DiffWrap  bool `json:"diffWrap,omitempty"`
	// NoFocusRefresh turns off reading the repository again when the
	// window gains focus, the fallback for changes the watcher missed.
	NoFocusRefresh bool `json:"noFocusRefresh,omitempty"`
	// FetchMinutes is how often open repositories fetch every remote in
	// the background; 0 never, and at least MinFetchMinutes.
	FetchMinutes int `json:"fetchMinutes,omitempty"`
	// PullRequests shows each branch's open pull request and its checks
	// in the branches view, asked of the user's own gh or glab.
	PullRequests bool `json:"pullRequests,omitempty"`
	// HiddenSections are the sidebar's items and sections left out, by
	// key ("reflog", "tags").
	HiddenSections []string `json:"hiddenSections,omitempty"`
	// NoSidebar leaves the repository sidebar out; NoStatusBar the
	// status bar, but while it has a warning.
	NoSidebar   bool `json:"noSidebar,omitempty"`
	NoStatusBar bool `json:"noStatusBar,omitempty"`
	// Keys are the user's shortcuts by command, in place of the app's
	// own: accelerators such as "CmdOrCtrl+Shift+P"; an empty list
	// leaves a command without one.
	Keys map[string][]string `json:"keys,omitempty"`
}

// MinFetchMinutes is the shortest wait between background fetches.
const MinFetchMinutes = 5

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

// WithDefaults returns s with every unset or invalid field at its default.
func (s Settings) WithDefaults() Settings {
	if !slices.Contains(Themes, s.Theme) {
		s.Theme = Defaults.Theme
	}
	if s.SnapshotDays <= 0 {
		s.SnapshotDays = Defaults.SnapshotDays
	}
	if s.RecentLimit <= 0 {
		s.RecentLimit = Defaults.RecentLimit
	}
	if s.FetchMinutes < 0 {
		s.FetchMinutes = 0
	} else if s.FetchMinutes > 0 {
		s.FetchMinutes = max(s.FetchMinutes, MinFetchMinutes)
	}
	return s
}

// Load reads the file at path. A missing file gives the defaults; a file
// that does not parse gives the defaults and an error saying where, so
// the app starts and can show what is wrong.
func Load(path string) (Settings, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Defaults, nil
	}
	if err != nil {
		return Defaults, err
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return Defaults, fmt.Errorf("settings: %s: %w", path, describe(data, err))
	}
	return s.WithDefaults(), nil
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
// half of it.
func Save(path string, s Settings) error {
	merged := map[string]json.RawMessage{}
	if old, err := os.ReadFile(path); err == nil {
		json.Unmarshal(old, &merged) // a broken file is replaced whole
	}
	known, err := json.Marshal(s)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	json.Unmarshal(known, &fields)
	// Every key of this version is dropped first: one left out of the
	// new file is at its default and must not keep its old value.
	for _, k := range knownKeys() {
		delete(merged, k)
	}
	for k, v := range fields {
		merged[k] = v
	}
	data, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return err
	}
	return WriteFile(path, append(data, '\n'))
}

// knownKeys are the JSON names of the fields of Settings.
func knownKeys() []string {
	var keys []string
	t := reflect.TypeFor[Settings]()
	for i := range t.NumField() {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
			keys = append(keys, name)
		}
	}
	return keys
}

// WriteFile replaces path with data through a temporary file beside it.
func WriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
