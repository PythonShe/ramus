package main

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"ramus/internal/statefile"
	"ramus/locales"
)

// unsavedFile names, in the app's data folder, the files a quit gave up
// waiting for, for the next launch to tell once.
const unsavedFile = "unsaved.json"

// unsaved is the content of unsavedFile.
type unsaved struct {
	Files []string `json:"files"`
}

// writeUnsaved records files, those a quit gave up waiting for, in dir;
// none removes the record of an earlier quit, whose files are written
// since.
func writeUnsaved(dir string, files []string) error {
	p := filepath.Join(dir, unsavedFile)
	if len(files) == 0 {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.Marshal(unsaved{Files: files})
	if err != nil {
		return err
	}
	return statefile.WriteFile(p, append(data, '\n'))
}

// takeUnsaved returns the files the last quit gave up waiting for, as
// writeUnsaved recorded them in dir, and removes the record: they are
// told once.
func takeUnsaved(dir string) []string {
	p := filepath.Join(dir, unsavedFile)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	os.Remove(p)
	var u unsaved
	if json.Unmarshal(data, &u) != nil {
		return nil
	}
	return slices.DeleteFunc(u.Files, func(f string) bool { return f == "" })
}

// unsavedNotice tells files, those the last quit gave up waiting for.
func unsavedNotice(files []string) string {
	return locales.AppNotSavedAtQuit(len(files), strings.Join(files, ", "))
}

// removeStale removes the temporary files the writes of an earlier run
// left in dir, the app's data folder, when it gave up on them.
func removeStale(dir string) {
	for _, name := range []string{"settings.json", "layout.json", "library.json", "trust.json", unsavedFile} {
		statefile.RemoveStale(filepath.Join(dir, name))
	}
	statefile.RemoveStaleIn(filepath.Join(dir, "drafts"))
}
