package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	mygoui "github.com/egoist/mygo/ui"

	"ramus/internal/cli"
	"ramus/internal/git/gitexe"
	"ramus/internal/git/run"
	"ramus/internal/git/trust"
	"ramus/internal/git/userenv"
	"ramus/internal/library"
	"ramus/internal/settings"
	"ramus/internal/ui"
)

// maxProcesses bounds the git processes running at once, across every
// repository.
const maxProcesses = 8

func main() {
	cwd, _ := os.Getwd()
	req, err := cli.Parse(os.Args[1:], cwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, cli.Usage)
		os.Exit(2)
	}
	mygo.App.SetName("Ramus")
	if !mygo.App.RequestSingleInstanceLock() {
		return // the running instance has the request
	}
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		log.Fatal(err)
	}
	cacheDir, _ := mygo.App.Path(mygo.PathCache) // none is only slower
	cfgPath := filepath.Join(dir, "settings.json")
	cfg, cfgErr := settings.Load(cfgPath)
	mygo.Theme.SetSource(mygo.ThemeSource(cfg.Theme))
	ts, err := trust.Load(filepath.Join(dir, "trust.json"))
	if err != nil {
		log.Fatal(err)
	}
	lib, libErr := library.Load(filepath.Join(dir, "library.json"))

	var win *mygo.Window // set and read on the main thread
	app := ui.NewApp(ui.Deps{
		Trust: ts, Library: lib, Settings: cfg, SettingsPath: cfgPath, CacheDir: cacheDir,
		ApplyTheme: func(t string) { mygo.Theme.SetSource(mygo.ThemeSource(t)) },
		Update: func(fn func()) {
			mygo.RunOnMain(func() {
				if win == nil {
					fn() // no view yet to rebuild
					return
				}
				win.Update(fn)
			})
		},
		PickFolder: func(title string) (string, error) {
			paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: win, Title: title, Directory: true, CreateDirectories: true})
			if err != nil || len(paths) == 0 {
				return "", err
			}
			return paths[0], nil
		},
		PickFile: func(title string) (string, error) {
			paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: win, Title: title})
			if err != nil || len(paths) == 0 {
				return "", err
			}
			return paths[0], nil
		},
		SaveFile: func(title, name string) (string, error) {
			return mygo.Dialog.Save(mygo.SaveDialogOptions{Parent: win, Title: title, DefaultPath: name, CreateDirectories: true})
		},
	})
	for _, e := range []error{cfgErr, libErr} {
		if e != nil {
			app.Notify(e.Error())
		}
	}
	handle := func(r cli.Request) {
		switch r.Kind {
		case cli.Open:
			app.Open(r.Paths[0])
		case cli.Diff:
			app.Compare(r.Paths[0], r.Paths[1], nil)
		case cli.Log, cli.Blame:
			app.ShowFile(r.Paths[0], r.Kind == cli.Blame)
		}
	}
	handle(req)
	mygo.App.OnSecondInstance(func(args []string, wd string) {
		r, err := cli.Parse(args, wd)
		if err != nil {
			app.Notify(err.Error())
		} else {
			handle(r)
		}
		if win != nil {
			win.Invalidate()
			win.Restore()
			win.Focus()
		}
	})
	mygo.App.OnOpenFile(func(p string) {
		app.Open(p)
		if win != nil {
			win.Invalidate()
		}
	})
	mygo.App.WhenReady(func() {
		mygo.App.SetMenu(menu(app))
		win = mygo.NewWindow(mygo.WindowOptions{
			Title:     "Ramus",
			Width:     1200,
			Height:    800,
			MinWidth:  720,
			MinHeight: 480,
			StateKey:  "main",
			Content:   mygoui.View(app.View),
		})
		win.OnClosed(app.CloseAll)
		// The user's PATH comes from their login shell when the app was not
		// started from a terminal; asking it takes a moment, so the window
		// opens first.
		go func() {
			env := os.Environ()
			if userenv.NeedsResolving() {
				env = userenv.Resolve(context.Background()).Env
			}
			g, err := gitexe.Find(context.Background(), lookup(env, "PATH"))
			var r *run.Runner
			if err == nil {
				r = run.New(g, env, maxProcesses)
			}
			win.Update(func() { app.SetRunner(r, err) })
		}()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

func lookup(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
			return v
		}
	}
	return ""
}

func menu(app *ui.App) *mygo.Menu {
	return mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu},
		{Label: "File", Submenu: []*mygo.MenuItem{
			{Label: "Open Repository…", Accelerator: "CmdOrCtrl+O", Click: func(_ *mygo.MenuItem, w *mygo.Window) {
				app.PickAndOpen()
				w.Invalidate()
			}},
			{Label: "Clone Repository…", Click: func(_ *mygo.MenuItem, w *mygo.Window) {
				app.ShowClone()
				w.Invalidate()
			}},
			{Label: "Repositories…", Accelerator: "CmdOrCtrl+Shift+O", Click: func(_ *mygo.MenuItem, w *mygo.Window) {
				app.ShowManager()
				w.Invalidate()
			}},
			{Role: mygo.RoleOpenRecent},
			mygo.Separator(),
			{Label: "Settings…", Accelerator: "CmdOrCtrl+,", Click: func(_ *mygo.MenuItem, w *mygo.Window) {
				app.ShowSettings()
				w.Invalidate()
			}},
			{Label: "Command Palette", Accelerator: "CmdOrCtrl+Shift+P", Click: func(_ *mygo.MenuItem, w *mygo.Window) {
				app.ShowPalette()
				w.Invalidate()
			}},
			mygo.Separator(),
			{Role: mygo.RoleClose},
		}},
		{Role: mygo.RoleEditMenu},
		{Role: mygo.RoleViewMenu},
		{Role: mygo.RoleWindowMenu},
	})
}
