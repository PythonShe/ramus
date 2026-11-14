package ui

import (
	"maps"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"
)

// keysState is the dialog that rebinds shortcuts. A change is kept at
// once.
type keysState struct {
	open   bool
	filter string
	list   ui.ListState
	// recording is the command whose next chord is taken, "" none;
	// focus is set the frame recording starts, for its button to take the
	// focus and with it every key.
	recording string
	focus     bool
	// note says what the last change did besides the obvious.
	note string
}

func (a *App) openKeys() {
	a.keysUI = keysState{open: true}
}

// keyCommand is a command listed in the dialog.
type keyCommand struct{ id, name string }

// keyCommands are the commands a shortcut can run: the app's, then the
// user's actions and the tools found.
func (a *App) keyCommands() []keyCommand {
	var out []keyCommand
	for _, b := range bindable {
		out = append(out, keyCommand(b))
	}
	for _, x := range a.Settings.Actions {
		if x.On == "" {
			out = append(out, keyCommand{actionID(x.Name), x.Name})
		}
	}
	for _, tl := range a.palette.tools {
		out = append(out, keyCommand{toolID(tl.Name), "Open in " + tl.Name})
	}
	return out
}

// allMods are every combination of modifiers a chord may hold.
var allMods = func() []ui.Modifiers {
	var out []ui.Modifiers
	for _, m := range []ui.Modifiers{0, ui.Ctrl, ui.Alt, ui.Super} {
		for _, n := range []ui.Modifiers{0, ui.Ctrl, ui.Alt, ui.Super} {
			for _, s := range []ui.Modifiers{0, ui.Shift} {
				if mods := m | n | s; !slices.Contains(out, mods) {
					out = append(out, mods)
				}
			}
		}
	}
	return out
}()

func (a *App) keysView(c *ui.Context) {
	ks := &a.keysUI
	if !ks.open {
		ks.recording = ""
		return
	}
	th := c.Theme()
	km := a.keys()
	var shown []keyCommand
	words := strings.Fields(strings.ToLower(ks.filter))
	for _, k := range a.keyCommands() {
		name := strings.ToLower(k.name)
		if !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(name, w) }) {
			shown = append(shown, k)
		}
	}
	ui.Modal(c, &ks.open, func() {
		ui.Column(c).Width(620).Gap(12).Padding(20).Children(func() {
			ui.Text(c, "Keyboard Shortcuts").Bold()
			ui.TextInput(c, &ks.filter).Label("Filter").Placeholder("Filter").AutoFocus().FillWidth()
			ks.list.Label = func(i int) string {
				return shown[i].name + ", " + chordsLabel(km.byID[shown[i].id])
			}
			ui.List(c, &ks.list, len(shown), func(i int) {
				k := shown[i]
				ui.Row(c).Padding(3, 8).Gap(8).AlignItems(ui.Center).FillWidth().Children(func() {
					ui.Text(c, k.name).SingleLine().Grow(1)
					a.recorder(c, k, km)
					_, set := a.Settings.Keys[k.id]
					if ui.Button(c, "Clear").Disabled(len(km.byID[k.id]) == 0).Clicked() {
						a.bind(k.id, nil, km)
					}
					if ui.Button(c, "Reset").Disabled(!set).Clicked() {
						user := maps.Clone(a.Settings.Keys)
						delete(user, k.id)
						a.setKeys(user)
						ks.note = ""
					}
				})
			}).Label("Commands").Height(420)
			for _, e := range km.errs {
				ui.Text(c, e).FontSize(12).TextColor(th.Danger).Selectable()
			}
			if ks.note != "" {
				ui.Text(c, ks.note).FontSize(12).TextColor(th.TextMuted)
			}
			ui.Row(c).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, "Done").Clicked() {
					ks.open = false
				}
			})
		})
	})
}

// recorder is the button showing command k's shortcut; pressed, it takes
// the next chord typed as the new one.
func (a *App) recorder(c *ui.Context, k keyCommand, km *keymap) {
	ks := &a.keysUI
	on := ks.recording == k.id
	label := chordsLabel(km.byID[k.id])
	if on {
		label = "Type a Shortcut"
	}
	b := ui.Button(c, label).Width(150)
	if b.Clicked() {
		if on {
			ks.recording = ""
		} else {
			ks.recording, ks.focus, ks.note = k.id, true, ""
		}
		return
	}
	if !on {
		return
	}
	if ks.focus {
		b.Focus()
		ks.focus = false
	} else if !b.Focused() {
		ks.recording = "" // the focus went elsewhere
		return
	}
	for _, mods := range allMods {
		for key := range keyNames {
			if !b.Shortcut(mods, key) {
				continue
			}
			ks.recording = ""
			switch {
			case mods == 0 && key == ui.KeyEscape:
			case mods&^ui.Shift == 0 && key == ui.KeyTab:
			default:
				a.bind(k.id, &chord{mods, key}, km)
			}
			return
		}
	}
}

// bind makes k the one shortcut of command id, taking it from the
// command that had it; nil leaves id with none.
func (a *App) bind(id string, k *chord, km *keymap) {
	user := maps.Clone(a.Settings.Keys)
	if user == nil {
		user = map[string][]string{}
	}
	user[id] = []string{}
	a.keysUI.note = ""
	if k != nil {
		user[id] = []string{k.String()}
		if other := km.owner(*k); other != "" && other != id {
			var left []string
			for _, o := range km.byID[other] {
				if o != *k {
					left = append(left, o.String())
				}
			}
			user[other] = append([]string{}, left...)
			a.keysUI.note = k.label() + " no longer runs " + a.commandName(other)
		}
	}
	a.setKeys(user)
}

func (a *App) setKeys(user map[string][]string) {
	if len(user) == 0 {
		user = nil
	}
	a.Settings.Keys = user
	a.keepSettings()
}

// commandName is what the dialog calls command id.
func (a *App) commandName(id string) string {
	for _, k := range a.keyCommands() {
		if k.id == id {
			return k.name
		}
	}
	return id
}

// chordsLabel lists chords as the platform shows them.
func chordsLabel(ks []chord) string {
	if len(ks) == 0 {
		return "None"
	}
	labels := make([]string, len(ks))
	for i, k := range ks {
		labels[i] = k.label()
	}
	return strings.Join(labels, ", ")
}
