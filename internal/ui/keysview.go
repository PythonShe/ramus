package ui

import (
	"maps"
	"slices"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/locales"
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

// keyCommand is a command listed in the dialog; one with a heading and
// no id is the heading of the commands after it.
type keyCommand struct{ id, name, heading string }

// keyCommands are the commands a shortcut can run: the app's, then the
// user's actions and the tools found.
func (a *App) keyCommands() []keyCommand {
	var out []keyCommand
	for _, kc := range a.keyRows() {
		if kc.id != "" {
			out = append(out, kc)
		}
	}
	return out
}

// keyRows are the dialog's rows: the commands of keyCommands under the
// headings of their groups.
func (a *App) keyRows() []keyCommand {
	var out []keyCommand
	for _, g := range commandGroups {
		out = append(out, keyCommand{heading: g.heading()})
		for _, b := range g.cmds {
			out = append(out, keyCommand{id: b.id, name: b.name()})
		}
	}
	var own []keyCommand
	for _, x := range a.Settings.Actions {
		if x.On == "" {
			own = append(own, keyCommand{id: actionID(x.Name), name: x.Name})
		}
	}
	if len(own) > 0 {
		out = append(append(out, keyCommand{heading: locales.KeysGroupActions()}), own...)
	}
	if len(a.palette.tools) > 0 {
		out = append(out, keyCommand{heading: locales.KeysGroupTools()})
		for _, tl := range a.palette.tools {
			out = append(out, keyCommand{id: toolID(tl.Name), name: locales.PaletteOpenIn(toolName(tl.Name))})
		}
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
	// The commands the filter matches, under the headings of theirs.
	var shown []keyCommand
	words := strings.Fields(strings.ToLower(ks.filter))
	for _, k := range a.keyRows() {
		name := strings.ToLower(k.name)
		if k.id == "" || !slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(name, w) }) {
			shown = append(shown, k)
		}
	}
	for i := len(shown) - 1; i >= 0; i-- {
		// A heading whose commands the filter left out goes too.
		if shown[i].id == "" && (i == len(shown)-1 || shown[i+1].id == "") {
			shown = slices.Delete(shown, i, i+1)
		}
	}
	a.modal(c, &ks.open, func() {
		dialogBox(c).Width(dialogWide).Children(func() {
			ui.Text(c, locales.KeysTitle()).Bold()
			ui.TextInput(c, &ks.filter).Label(locales.CommonFilter()).Placeholder(locales.CommonFilter()).AutoFocus().FillWidth()
			ks.list.Label = func(i int) string {
				if shown[i].id == "" {
					return shown[i].heading
				}
				return locales.KeysListLabel(shown[i].name, chordsLabel(km.byID[shown[i].id]))
			}
			ui.List(c, &ks.list, len(shown), func(i int) {
				k := shown[i]
				if k.id == "" {
					ui.Text(c, k.heading).FontSize(textSmall).Bold().TextColor(th.TextMuted).Padding(spaceM, insetX, spaceXS, insetX)
					return
				}
				ui.Row(c).Padding(rowPadY, insetX).Gap(spaceM).AlignItems(ui.Center).FillWidth().Children(func() {
					ui.Text(c, k.name).SingleLine().Grow(1)
					// Reset only where the user changed the default, and the
					// x only where there is a shortcut to take away: most rows
					// have neither.
					if _, set := a.Settings.Keys[k.id]; set && ui.Button(c, locales.KeysReset()).Clicked() {
						user := maps.Clone(a.Settings.Keys)
						delete(user, k.id)
						a.setKeys(user)
						ks.note = ""
					}
					a.recorder(c, k, km)
					ui.Row(c).Width(rowHeight).Justify(ui.Center).Children(func() {
						if len(km.byID[k.id]) > 0 && closeButton(c, locales.KeysClearShortcut(k.name)).Clicked() {
							a.bind(k.id, nil, km)
						}
					})
				})
			}).Label(locales.KeysCommands()).Height(420)
			for _, e := range km.errs {
				ui.Text(c, e).FontSize(textSmall).TextColor(th.Danger).Selectable()
			}
			if ks.note != "" {
				ui.Text(c, ks.note).FontSize(textSmall).TextColor(th.TextMuted)
			}
			ui.Row(c).Children(func() {
				ui.Spacer(c)
				if ui.Button(c, locales.CommonDone()).Clicked() {
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
	if len(km.byID[k.id]) == 0 {
		label = locales.KeysAddShortcut()
	}
	if on {
		label = locales.KeysTypeShortcut()
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
			a.keysUI.note = locales.KeysNoLongerRuns(k.label(), a.commandName(other))
		} else if item := a.roleOf(*k); item != "" {
			a.keysUI.note = locales.KeysNoLongerRuns(k.label(), item)
		}
	}
	a.setKeys(user)
}

// roleOf is the label of the app menu's item with a role whose shortcut
// is k, "" none: a command bound to k takes the key from it.
func (a *App) roleOf(k chord) string {
	if a.RoleKeys == nil {
		return ""
	}
	roles := a.RoleKeys()
	for _, acc := range slices.Sorted(maps.Keys(roles)) {
		// mygo's Plus is the key of = and +, without Shift.
		parts := strings.Split(acc, "+")
		if parts[len(parts)-1] == "Plus" {
			parts[len(parts)-1] = "="
		}
		if rk, err := parseChord(strings.Join(parts, "+")); err == nil && rk == k {
			return roles[acc]
		}
	}
	return ""
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
		return locales.KeysNone()
	}
	labels := make([]string, len(ks))
	for i, k := range ks {
		labels[i] = k.label()
	}
	return strings.Join(labels, ", ")
}
