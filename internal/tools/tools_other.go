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

// Preferred is the index in found of the tool of kind the user prefers,
// -1 when there is no telling, as here: the first found then serves.
func Preferred(found []Tool, kind Kind) int { return -1 }
