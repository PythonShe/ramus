//go:build !windows

package osfile

import "os"

func open(path string) (*os.File, error) { return os.Open(path) }
