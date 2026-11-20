package main

import (
	"bytes"
	"context"
	_ "embed"

	"github.com/egoist/mygo"
	mygoui "github.com/egoist/mygo/ui"

	"ramus/internal/ui"
	"ramus/locales"
)

// The app's icon, as the About window and the window with no repository
// open show it.
var (
	//go:embed assets/app-icon/monogram/svg/icon-light.svg
	iconLight []byte
	//go:embed assets/app-icon/monogram/svg/icon-dark.svg
	iconDark []byte
)

// iconCanvas is the icon's canvas and, within it, the box of its shape:
// the rest is the margin a Dock wants around an icon.
const (
	iconCanvas = `viewBox="0 0 1024 1024"`
	iconShape  = `viewBox="100 100 824 824"`
)

// edgeIcons is the icon without its margin, for the window with no
// repository open, where its edge lines up with the text under it.
func edgeIcons() (light, dark *mygoui.SVG) {
	crop := func(svg []byte) *mygoui.SVG {
		s, err := mygoui.ParseSVG(bytes.Replace(svg, []byte(iconCanvas), []byte(iconShape), 1))
		if err != nil {
			return nil
		}
		return s
	}
	return crop(iconLight), crop(iconDark)
}

// aboutWindow is the About window, nil while it is not open.
type aboutWindow struct {
	win   *mygo.Window
	about *ui.About
}

// showAbout opens the About window, or brings it forward.
func (ws *windows) showAbout() {
	if a := ws.about; a != nil {
		a.win.Restore()
		a.win.Focus()
		return
	}
	a := &aboutWindow{about: ui.NewAbout(mygo.App.Version())}
	ab := a.about
	if s, err := mygoui.ParseSVG(iconLight); err == nil {
		ab.Icon = s
	}
	if s, err := mygoui.ParseSVG(iconDark); err == nil {
		ab.IconDark = s
	}
	ab.Check = checkUpdate
	ab.Crash = ws.deps.Crash
	ab.Update = func(fn func()) {
		mygo.RunOnMain(func() {
			if a.win != nil {
				a.win.Update(fn)
			}
		})
	}
	ab.Relaunch = func() { mygo.App.Relaunch() }
	a.win = mygo.NewWindow(mygo.WindowOptions{
		Title:             locales.MenuAbout(),
		Width:             380,
		Height:            480,
		MinWidth:          340,
		MinHeight:         440,
		DisableMinimize:   true,
		DisableFullScreen: true,
		Content:           mygoui.View(ab.View),
	})
	a.win.OnClosed(func() {
		ab.Close()
		a.win = nil
		ws.about = nil
	})
	ws.about = a
	a.win.Focus()
}

// checkUpdate asks the releases on GitHub for a newer version: the one
// use of the network that is the app's own, made only when the user asks.
func checkUpdate(ctx context.Context) (ui.Update, error) {
	if !mygo.Updater.Enabled() {
		return nil, ui.ErrUpdatesOff
	}
	u, err := mygo.Updater.Check(ctx)
	if err != nil || u == nil {
		return nil, err
	}
	return update{u}, nil
}

// update is a newer version found on GitHub.
type update struct{ u *mygo.Update }

func (u update) Version() string { return u.u.Version }

func (u update) Install(ctx context.Context, progress func(done, total int64)) error {
	return u.u.Install(ctx, progress)
}
