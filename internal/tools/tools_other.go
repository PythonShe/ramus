//go:build !darwin && !linux && !windows

package tools

func macApp(name string, kind Kind, bundle string) Tool {
	return Tool{Name: name, Kind: kind, Argv: []string{bundle}}
}

// Detect finds nothing on this system.
func Detect() []Tool { return nil }

// Reveal is not supported here.
func Reveal(path string) error { return errNoOpener }

// OpenDefault is not supported here.
func OpenDefault(path string) error { return errNoOpener }
