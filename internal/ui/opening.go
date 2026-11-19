package ui

// goOpen opens path with open (tools.Reveal, tools.OpenDefault) off the
// main thread, and tells the user why when it cannot: in t's tab, or the
// window's with none. A Linux desktop without xdg-open has nothing to
// open with, and the button would otherwise do nothing and say nothing.
func (a *App) goOpen(t *tab, open func(string) error, path string) {
	update := a.Update
	a.goWork(t, func() {
		if err := open(path); err != nil {
			update(func() { a.tell(t, errText(err)) })
		}
	})
}
