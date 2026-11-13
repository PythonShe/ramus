//go:build !unix

package refs

import "errors"

func syscallMkfifo(string) error { return errors.New("no FIFOs here") }
