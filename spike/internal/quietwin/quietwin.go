// Package quietwin opens the spike programs' windows without disturbing
// whoever uses the machine: no Dock icon, no activation, no keyboard
// focus, in a corner of the screen.
package quietwin

import "github.com/egoist/mygo"

// Prepare must run before mygo.App.Run.
func Prepare() {
	mygo.App.SetActivationPolicy(mygo.ActivationPolicyAccessory)
}

// Open creates the window hidden, then shows it without activating it.
// mygo draws no frames for a window out of sight, so it must be shown.
func Open(opts mygo.WindowOptions) *mygo.Window {
	opts.Hidden = true
	if opts.X == 0 && opts.Y == 0 {
		opts.X, opts.Y = 40, 40
	}
	w := mygo.NewWindow(opts)
	w.ShowInactive()
	return w
}
