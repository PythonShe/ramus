package write

import (
	"path/filepath"
	"strings"
)

// terminalTools are git's built-in merge and diff tools that run in a
// terminal, from git's mergetools folder: those whose help does not say
// they need a graphical session.
var terminalTools = map[string]bool{
	"emerge":  true,
	"vimdiff": true, "vimdiff1": true, "vimdiff2": true, "vimdiff3": true,
	"nvimdiff": true, "nvimdiff1": true, "nvimdiff2": true, "nvimdiff3": true,
}

// guiVims are the vims that open a window of their own: git's vimdiff
// runs one given as its mergetool.<name>.path or difftool.<name>.path.
var guiVims = map[string]bool{"gvim": true, "mvim": true}

// TerminalTool reports whether the merge or diff tool name, whose
// mergetool.<name>.cmd or difftool.<name>.cmd is cmd and whose .path is
// path, is one of git's that runs in a terminal. The app runs git with
// none, on every system (a hidden console on Windows, no tty elsewhere):
// such a tool would wait unseen for input, and hold the queue up to
// MergeToolTime, so it is refused before it starts. A cmd of the user's
// own overrides git's tool of that name, and is run as it is; so is a
// vimdiff whose path is a vim with a window, as gvim or mvim.
func TerminalTool(name, cmd, path string) bool {
	if cmd != "" || !terminalTools[name] {
		return false
	}
	if path != "" && strings.Contains(name, "vimdiff") {
		base := strings.ToLower(filepath.Base(strings.ReplaceAll(path, `\`, "/")))
		if guiVims[strings.TrimSuffix(base, ".exe")] {
			return false
		}
	}
	return true
}
