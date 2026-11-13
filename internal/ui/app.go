// Package ui holds the app's state and the views built from it.
package ui

import "github.com/egoist/mygo/ui"

// App is the state the main window shows. View reads it on the main thread;
// other goroutines change it through Window.Update.
type App struct{}

// View builds the main window.
func (a *App) View(c *ui.Context) {
	ui.Column(c).Fill().Center().Children(func() {
		ui.Text(c, "Ramus").FontSize(26).Bold()
	})
}
