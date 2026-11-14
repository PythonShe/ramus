package ui

import (
	"errors"
	"maps"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
)

// chord is a key pressed with exactly mods held.
type chord struct {
	mods ui.Modifiers
	key  ui.Key
}

// keyNames name the keys a shortcut may use, as accelerators spell them.
var keyNames = func() map[ui.Key]string {
	m := map[ui.Key]string{
		ui.KeyEnter: "Enter", ui.KeyEscape: "Escape", ui.KeyBackspace: "Backspace", ui.KeyTab: "Tab",
		ui.KeySpace: "Space", ui.KeyDelete: "Delete", ui.KeyInsert: "Insert", ui.KeyHome: "Home", ui.KeyEnd: "End",
		ui.KeyPageUp: "PageUp", ui.KeyPageDown: "PageDown", ui.KeyLeft: "Left", ui.KeyRight: "Right",
		ui.KeyUp: "Up", ui.KeyDown: "Down", ui.KeyMinus: "-", ui.KeyEqual: "=", ui.KeyComma: ",",
		ui.KeyPeriod: ".", ui.KeySlash: "/", ui.KeySemicolon: ";", ui.KeyQuote: "'", ui.KeyBracketLeft: "[",
		ui.KeyBracketRight: "]", ui.KeyBackslash: "\\", ui.KeyBackquote: "`",
	}
	for i, k := range []ui.Key{ui.KeyF1, ui.KeyF2, ui.KeyF3, ui.KeyF4, ui.KeyF5, ui.KeyF6, ui.KeyF7, ui.KeyF8, ui.KeyF9, ui.KeyF10, ui.KeyF11, ui.KeyF12} {
		m[k] = "F" + strconv.Itoa(i+1)
	}
	for i, k := range []ui.Key{ui.Key0, ui.Key1, ui.Key2, ui.Key3, ui.Key4, ui.Key5, ui.Key6, ui.Key7, ui.Key8, ui.Key9} {
		m[k] = strconv.Itoa(i)
	}
	for i, k := range []ui.Key{ui.KeyA, ui.KeyB, ui.KeyC, ui.KeyD, ui.KeyE, ui.KeyF, ui.KeyG, ui.KeyH, ui.KeyI, ui.KeyJ, ui.KeyK, ui.KeyL, ui.KeyM,
		ui.KeyN, ui.KeyO, ui.KeyP, ui.KeyQ, ui.KeyR, ui.KeyS, ui.KeyT, ui.KeyU, ui.KeyV, ui.KeyW, ui.KeyX, ui.KeyY, ui.KeyZ} {
		m[k] = string(rune('A' + i))
	}
	return m
}()

var namedKeys = func() map[string]ui.Key {
	m := map[string]ui.Key{"return": ui.KeyEnter, "esc": ui.KeyEscape, "del": ui.KeyDelete}
	for k, name := range keyNames {
		m[strings.ToLower(name)] = k
	}
	return m
}()

var mac = runtime.GOOS == "darwin"

// parseChord reads a shortcut as the settings file writes it, in the
// accelerator syntax of the app menu: "CmdOrCtrl+Shift+P". CmdOrCtrl is
// Command on macOS and Control elsewhere.
func parseChord(s string) (chord, error) {
	var k chord
	parts := strings.Split(s, "+")
	for i, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if i < len(parts)-1 {
			switch p {
			case "cmdorctrl", "commandorcontrol":
				k.mods |= ui.Cmd
			case "cmd", "command", "super", "meta":
				k.mods |= ui.Super
			case "ctrl", "control":
				k.mods |= ui.Ctrl
			case "alt", "option":
				k.mods |= ui.Alt
			case "shift":
				k.mods |= ui.Shift
			default:
				return chord{}, errors.New("unknown modifier " + strconv.Quote(parts[i]) + " in " + strconv.Quote(s))
			}
			continue
		}
		key, ok := namedKeys[p]
		if !ok {
			return chord{}, errors.New("unknown key " + strconv.Quote(parts[i]) + " in " + strconv.Quote(s))
		}
		k.key = key
	}
	return k, nil
}

// String is the chord as the settings file and the app menu spell it.
func (k chord) String() string {
	var b strings.Builder
	if k.mods&ui.Cmd != 0 {
		b.WriteString("CmdOrCtrl+")
	}
	for _, m := range []struct {
		mod  ui.Modifiers
		name string
	}{{ui.Ctrl, "Ctrl"}, {ui.Super, "Super"}, {ui.Alt, "Alt"}, {ui.Shift, "Shift"}} {
		if m.mod != ui.Cmd && k.mods&m.mod != 0 {
			b.WriteString(m.name + "+")
		}
	}
	return b.String() + keyNames[k.key]
}

// label is the chord as the platform shows shortcuts: symbols on macOS.
func (k chord) label() string {
	name := keyNames[k.key]
	if !mac {
		return strings.ReplaceAll(k.String(), "CmdOrCtrl", "Ctrl")
	}
	switch k.key {
	case ui.KeyEnter:
		name = "↩"
	case ui.KeyEscape:
		name = "⎋"
	case ui.KeyBackspace:
		name = "⌫"
	case ui.KeyDelete:
		name = "⌦"
	case ui.KeyTab:
		name = "⇥"
	case ui.KeyLeft:
		name = "←"
	case ui.KeyRight:
		name = "→"
	case ui.KeyUp:
		name = "↑"
	case ui.KeyDown:
		name = "↓"
	}
	var b strings.Builder
	for _, m := range []struct {
		mod ui.Modifiers
		sym string
	}{{ui.Ctrl, "⌃"}, {ui.Alt, "⌥"}, {ui.Shift, "⇧"}, {ui.Super, "⌘"}} {
		if k.mods&m.mod != 0 {
			b.WriteString(m.sym)
		}
	}
	return b.String() + name
}

// textSafe reports a chord that typing in a text field cannot mean:
// one with Command, Control or Option held, other than the keys a field
// takes for itself (moving by words, deleting them, Undo, Select All and
// the clipboard).
func (k chord) textSafe() bool {
	if k.mods&(ui.Ctrl|ui.Alt|ui.Super) == 0 {
		return false
	}
	switch k.key {
	case ui.KeyLeft, ui.KeyRight, ui.KeyUp, ui.KeyDown, ui.KeyHome, ui.KeyEnd, ui.KeyBackspace, ui.KeyDelete:
		return false
	case ui.KeyZ, ui.KeyA, ui.KeyC, ui.KeyV, ui.KeyX:
		return k.mods&ui.Cmd == 0
	}
	return true
}

// bindable is every command a shortcut can run, in the order the
// settings list them, with the name they show there.
var bindable = []struct{ id, name string }{
	{"palette", "Command Palette"},
	{"open", "Open Repository…"},
	{"clone", "Clone Repository…"},
	{"new", "New Repository…"},
	{"repositories", "Repositories…"},
	{"settings", "Settings…"},
	{"library-stats", "Library Statistics…"},
	{"close-tab", "Close Tab"},
	{"refresh", "Refresh"},
	{"undo", "Undo"},
	{"redo", "Redo"},
	{"show-changes", "Show Changes"},
	{"show-history", "Show History"},
	{"show-reflog", "Show Reflog"},
	{"show-stats", "Show Statistics"},
	{"lost-commits", "Find Lost Commits"},
	{"show-refs", "Show Branches and Tags"},
	{"show-stashes", "Show Stashes"},
	{"show-log", "Show Command Log"},
	{"show-snapshots", "Show Snapshots"},
	{"commit", "Commit"},
	{"commit-push", "Commit and Push"},
	{"stage-all", "Stage All"},
	{"unstage-all", "Unstage All"},
	{"fetch", "Fetch All Remotes"},
	{"pull-merge", "Pull, Merging"},
	{"pull-rebase", "Pull, Rebasing"},
	{"pull-ff", "Pull, Fast-Forward Only"},
	{"push", "Push"},
	{"new-branch", "New Branch…"},
	{"new-tag", "New Tag…"},
	{"stash", "Stash Changes…"},
	{"clean", "Clean Untracked Files…"},
	{"apply-patch", "Apply Patch File…"},
	{"snapshot", "Snapshot Now"},
	{"trust", "Trust This Repository"},
	{"reveal", "Reveal in File Manager"},
	{"issue-links", "Issue Links…"},
	{"toggle-sidebar", "Show or Hide the Sidebar"},
	{"toggle-status-bar", "Show or Hide the Status Bar"},
	{"tab-1", "Go to Tab 1"}, {"tab-2", "Go to Tab 2"}, {"tab-3", "Go to Tab 3"},
	{"tab-4", "Go to Tab 4"}, {"tab-5", "Go to Tab 5"}, {"tab-6", "Go to Tab 6"},
	{"tab-7", "Go to Tab 7"}, {"tab-8", "Go to Tab 8"}, {"tab-9", "Go to Tab 9"},
}

// defaultKeys are the shortcuts the app ships with.
var defaultKeys = map[string][]string{
	"palette":      {"CmdOrCtrl+Shift+P", "CmdOrCtrl+K"},
	"open":         {"CmdOrCtrl+O"},
	"repositories": {"CmdOrCtrl+Shift+O"},
	"settings":     {"CmdOrCtrl+,"},
	"close-tab":    {"CmdOrCtrl+W"},
	"refresh":      {"CmdOrCtrl+R"},
	"undo":         {"CmdOrCtrl+Z"},
	"redo":         {"CmdOrCtrl+Shift+Z"},
	"commit":       {"CmdOrCtrl+Enter"},
	"tab-1":        {"CmdOrCtrl+1"}, "tab-2": {"CmdOrCtrl+2"}, "tab-3": {"CmdOrCtrl+3"},
	"tab-4": {"CmdOrCtrl+4"}, "tab-5": {"CmdOrCtrl+5"}, "tab-6": {"CmdOrCtrl+6"},
	"tab-7": {"CmdOrCtrl+7"}, "tab-8": {"CmdOrCtrl+8"}, "tab-9": {"CmdOrCtrl+9"},
}

// actionID is the command id of the user's action named name, and
// toolID of opening the repository in a tool.
func actionID(name string) string { return "action:" + name }
func toolID(name string) string   { return "open-in:" + name }

// binding is one chord and the command it runs.
type binding struct {
	chord
	id string
}

// keymap is the bindings in force: the defaults, with the user's own
// in place of a command's where the settings name it (an empty list
// unbinds it). A chord bound twice runs the command the user bound it
// to, else the first listed.
type keymap struct {
	from     map[string][]string // the settings it was made from
	bindings []binding
	byID     map[string][]chord
	errs     []string
}

func newKeymap(user map[string][]string) *keymap {
	km := &keymap{from: user, byID: map[string][]chord{}}
	order := map[string]int{}
	for i, b := range bindable {
		order[b.id] = i
	}
	sorted := func(ids []string) []string {
		slices.SortFunc(ids, func(a, b string) int {
			oa, okA := order[a]
			ob, okB := order[b]
			switch {
			case okA && okB:
				return oa - ob
			case okA != okB:
				if okA {
					return -1
				}
				return 1
			}
			return strings.Compare(a, b)
		})
		return ids
	}
	var rest []string
	for id := range defaultKeys {
		if _, ok := user[id]; !ok {
			rest = append(rest, id)
		}
	}
	taken := map[chord]bool{}
	bind := func(id string, keys []string) {
		for _, s := range keys {
			k, err := parseChord(s)
			if err != nil {
				km.errs = append(km.errs, err.Error())
				continue
			}
			if !taken[k] {
				taken[k] = true
				km.bindings = append(km.bindings, binding{k, id})
				km.byID[id] = append(km.byID[id], k)
			}
		}
	}
	for _, id := range sorted(slices.Collect(maps.Keys(user))) {
		bind(id, user[id])
	}
	for _, id := range sorted(rest) {
		bind(id, defaultKeys[id])
	}
	return km
}

// owner is the command chord k runs, "" none.
func (km *keymap) owner(k chord) string {
	for _, b := range km.bindings {
		if b.chord == k {
			return b.id
		}
	}
	return ""
}

// keys returns the keymap of the settings, made again when they changed.
func (a *App) keys() *keymap {
	if a.keymap == nil || !maps.EqualFunc(a.keymap.from, a.Settings.Keys, slices.Equal) {
		changed := a.keymap != nil
		a.keymap = newKeymap(maps.Clone(a.Settings.Keys))
		if changed && a.KeysChanged != nil {
			a.KeysChanged()
		}
	}
	return a.keymap
}

// Accelerator is the first shortcut of command id, for the app menu; ""
// when it has none.
func (a *App) Accelerator(id string) string {
	if ks := a.keys().byID[id]; len(ks) > 0 {
		return ks[0].String()
	}
	return ""
}

// shortcuts runs the command of every chord pressed this frame.
func (a *App) shortcuts(c *ui.Context) {
	for _, b := range a.keys().bindings {
		if !c.Shortcut(b.mods, b.key) {
			continue
		}
		if c.FocusTakesText() && !b.textSafe() {
			continue // the field takes the key, as an Undo there undoes the typing
		}
		a.runCommand(b.id)
	}
}

// runCommand runs command id if it can run now.
func (a *App) runCommand(id string) {
	for _, cmd := range a.commands() {
		if cmd.id == id {
			cmd.run()
			return
		}
	}
}
