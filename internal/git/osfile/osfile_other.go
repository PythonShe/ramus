//go:build !unix && !windows

package osfile

import (
	"io/fs"
	"os"
)

func open(path string) (*os.File, error) { return os.Open(path) }

func regular(fi fs.FileInfo) bool { return fi.Mode().IsRegular() }
