package tools

import (
	"os"
	"path/filepath"
)

var macEditors = []string{
	"Visual Studio Code", "Cursor", "Zed", "Sublime Text", "BBEdit", "Nova", "Xcode",
	"GoLand", "IntelliJ IDEA", "IntelliJ IDEA CE", "WebStorm", "PyCharm", "PyCharm CE", "CLion", "RustRover", "Fleet",
	"TextEdit",
}

var macTerminals = []string{"Terminal", "iTerm", "Ghostty", "WezTerm", "kitty", "Alacritty", "Warp"}

func appDirs() []string {
	dirs := []string{"/Applications", "/System/Applications", "/System/Applications/Utilities", "/Applications/Utilities"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "Applications"))
	}
	return dirs
}

// macApp opens through open(1), which starts the app or hands the target
// to the running one, as the Finder does.
func macApp(name string, kind Kind, bundle string) Tool {
	return Tool{Name: name, Kind: kind, Argv: []string{"/usr/bin/open", "-a", bundle}}
}

// Detect lists the editors and terminals installed.
func Detect() []Tool {
	var out []Tool
	find := func(names []string, kind Kind) {
		for _, n := range names {
			var cands []string
			for _, d := range appDirs() {
				cands = append(cands, filepath.Join(d, n+".app"))
			}
			if p, ok := first(cands...); ok {
				out = append(out, macApp(n, kind, p))
			}
		}
	}
	find(macEditors, Editor)
	find(macTerminals, Terminal)
	return out
}

// Reveal shows path selected in the Finder.
func Reveal(path string) error {
	return Tool{Name: "Finder", Argv: []string{"/usr/bin/open", "-R"}}.Open(path, 0)
}

// OpenDefault opens path in the app the system has for it.
func OpenDefault(path string) error {
	return Tool{Name: "default", Argv: []string{"/usr/bin/open"}}.Open(path, 0)
}
