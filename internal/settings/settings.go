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
	"slices"
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
	for _, k := range []string{"theme", "editor", "terminal", "snapshotDays", "recentLimit"} {
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
