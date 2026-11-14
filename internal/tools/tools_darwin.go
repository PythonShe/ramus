package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
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

// Preferred is the index in found of the tool of kind the user prefers:
// the one the system opens source code or text with, as set in the
// Finder, else the one used last; -1 when there is no telling.
func Preferred(found []Tool, kind Kind) int {
	if kind == Editor {
		if ids := defaultHandlers(); len(ids) > 0 {
			for _, id := range ids {
				for i, t := range found {
					if t.Kind == kind && strings.EqualFold(bundleID(t.Program()), id) {
						return i
					}
				}
			}
		}
	}
	best, last := -1, ""
	for i, t := range found {
		if t.Kind != kind {
			continue
		}
		// The dates sort as text: "2026-11-14 21:52:03 +0000".
		if used := lastUsed(t.Program()); used > last {
			best, last = i, used
		}
	}
	return best
}

// editorTypes are the kinds of file whose default app is the editor,
// most telling first.
var editorTypes = []string{"public.source-code", "public.plain-text", "public.script", "public.text"}

// defaultHandlers are the bundle ids of the apps the user chose to open
// editorTypes, in their order.
func defaultHandlers() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	out, err := quick("/usr/bin/plutil", "-convert", "json", "-o", "-",
		filepath.Join(home, "Library/Preferences/com.apple.LaunchServices/com.apple.launchservices.secure.plist"))
	if err != nil {
		return nil
	}
	var ls struct {
		LSHandlers []struct {
			ContentType string `json:"LSHandlerContentType"`
			All         string `json:"LSHandlerRoleAll"`
			Editor      string `json:"LSHandlerRoleEditor"`
		}
	}
	if json.Unmarshal(out, &ls) != nil {
		return nil
	}
	var ids []string
	for _, ct := range editorTypes {
		for _, h := range ls.LSHandlers {
			if h.ContentType != ct {
				continue
			}
			for _, id := range []string{h.Editor, h.All} {
				if id != "" && id != "-" {
					ids = append(ids, id)
				}
			}
		}
	}
	return ids
}

// bundleID is the identifier of the app bundle at app.
func bundleID(app string) string {
	out, err := quick("/usr/bin/plutil", "-extract", "CFBundleIdentifier", "raw", "-o", "-", filepath.Join(app, "Contents", "Info.plist"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// lastUsed is when the app at app was last opened, as Spotlight keeps
// it, "" when it does not know.
func lastUsed(app string) string {
	out, err := quick("/usr/bin/mdls", "-name", "kMDItemLastUsedDate", "-raw", app)
	if s := strings.TrimSpace(string(out)); err == nil && s != "(null)" {
		return s
	}
	return ""
}

// quick runs a system tool that answers at once, giving up after a
// moment rather than hold up the search for tools.
func quick(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}
