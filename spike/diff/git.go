package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

// gitDiff returns git's patch between the two texts, as Ramus gets one
// for a changed file.
func gitDiff(old, new string) (string, error) {
	dir, err := os.MkdirTemp("", "ramus-spike-diff")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	a, b := filepath.Join(dir, "a.c"), filepath.Join(dir, "b.c")
	if err := os.WriteFile(a, []byte(old), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(b, []byte(new), 0o644); err != nil {
		return "", err
	}
	cmd := exec.Command("git", "diff", "--no-index", "--no-color", "--no-ext-diff", "a.c", "b.c")
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		err = nil // differences found
	}
	return out.String(), err
}
