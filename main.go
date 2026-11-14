package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	mygoui "github.com/egoist/mygo/ui"

	"ramus/internal/cli"
	"ramus/internal/crashlog"
	"ramus/internal/drafts"
	"ramus/internal/git/gitexe"
	"ramus/internal/git/lifetime"
	"ramus/internal/git/run"
	"ramus/internal/git/trust"
	"ramus/internal/git/userenv"
	"ramus/internal/layout"
	"ramus/internal/library"
	"ramus/internal/session"
	"ramus/internal/settings"
	"ramus/internal/ui"
)

// maxProcesses bounds the git processes running at once, across every
// repository.
const maxProcesses = 8

func main() {
	mainStart = time.Now()
	cwd, _ := os.Getwd()
	req, err := cli.Parse(os.Args[1:], cwd)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		fmt.Fprintln(os.Stderr, cli.Usage)
		os.Exit(2)
	}
	mygo.App.SetName("Ramus")
	if req.Wait {
		// git waits on this process, so it runs on its own instead of
		// handing the request to a running instance.
		runTool(req)
		return
	}
	probe := startProbe()
	if probe == "" && !mygo.App.RequestSingleInstanceLock() {
		return // the running instance has the request
	}
	dir, err := mygo.App.Path(mygo.PathUserData)
	if err != nil {
		log.Fatal(err)
	}
	cacheDir, _ := mygo.App.Path(mygo.PathCache) // none is only slower
	if probe != "" {
		dir, cacheDir = probe, probe
	}
	crash := crashlog.New(filepath.Join(dir, "crashes"), mygo.App.Version())
	if probe == "" {
		go crash.CatchFatal() // a crash it cannot keep is only not kept
		session.OnPanic = func(path, gitDir string, p lifetime.Panic) {
			go crash.Panic(path, gitDir, p)
		}
	}
	cfgPath := filepath.Join(dir, "settings.json")
	cfg, cfgErr := settings.Load(cfgPath)
	mygo.Theme.SetSource(mygo.ThemeSource(cfg.Theme))
	ts, err := trust.Load(filepath.Join(dir, "trust.json"))
	if err != nil {
		log.Fatal(err)
	}
	lib, libErr := library.Load(filepath.Join(dir, "library.json"))

	program, _ := os.Executable() // none only leaves the tool config out
	ws := &windows{probe: probe, deps: ui.Deps{
		Trust: ts, Library: lib, Settings: cfg, SettingsPath: cfgPath, CacheDir: cacheDir, Program: program, Crash: crash,
		ApplyTheme: func(t string) { mygo.Theme.SetSource(mygo.ThemeSource(t)) },
	}}
	var saved []layout.Window
	var layoutErr error
	if probe == "" {
		layoutPath := filepath.Join(dir, "layout.json")
		saved, layoutErr = layout.Load(layoutPath)
		ws.keep = layout.NewKeeper(layoutPath)
		ws.keep.OnError = func(err error) { log.Print(err) }
		ws.deps.Drafts = drafts.New(filepath.Join(dir, "drafts"))
		ws.deps.Drafts.OnError = func(err error) { log.Print(err) }
	}
	first := ws.add()
	// What was open when the app last ran opens again, before the
	// request it was started with.
	for i, l := range saved {
		w := first
		if i > 0 {
			w = ws.add()
		}
		w.app.Restore(l)
	}
	ws.focused = first
	for _, e := range []error{cfgErr, libErr, layoutErr} {
		if e != nil {
			first.app.Notify(e.Error())
		}
	}
	// handle carries out a request in the window it belongs in, and
	// returns that window.
	handle := func(r cli.Request) *window {
		w := ws.focused
		if r.Kind == cli.Open {
			if o := ws.showing(r.Paths[0]); o != nil {
				w = o
			}
		}
		if w == nil {
			w = ws.target()
		}
		switch r.Kind {
		case cli.Open:
			w.app.Open(r.Paths[0])
		case cli.Diff:
			w.app.Compare(r.Paths[0], r.Paths[1], nil)
		case cli.Log, cli.Blame:
			w.app.ShowFile(r.Paths[0], r.Kind == cli.Blame)
		case cli.Merge:
			w.app.MergeTool(r.Paths[0], r.Paths[1], r.Paths[2], r.Paths[3], nil)
		}
		return w
	}
	handle(req)
	mygo.App.OnSecondInstance(func(args []string, wd string) {
		r, err := cli.Parse(args, wd)
		if err != nil {
			w := ws.target()
			w.app.Notify(err.Error())
			ws.raise(w)
			return
		}
		ws.raise(handle(r))
	})
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) {
		ws.flush()
		ws.quitting = true
	})
	mygo.App.OnQuitCanceled(func() { ws.quitting = false })
	mygo.App.OnOpenFile(func(p string) {
		ws.raise(handle(cli.Request{Kind: cli.Open, Paths: []string{p}}))
	})
	if probe != "" {
		probeMark("app")
	}
	mygo.App.WhenReady(func() {
		if probe != "" {
			probeMark("ready")
		}
		ws.ready = true
		mygo.App.SetMenu(menu(ws))
		for _, w := range ws.list[1:] {
			ws.show(w)
		}
		ws.show(first)
		findGit(ws, crash)
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// findGit finds git off the main thread and hands it to every window.
// The user's PATH comes from their login shell when the app was not
// started from a terminal; asking it takes a moment, so the window opens
// first.
func findGit(ws *windows, crash *crashlog.Log) {
	go func() {
		r, err := lookGit()
		if err == nil && crash != nil {
			crash.SetGit(r.Git(), r.Journal())
		}
		mygo.RunOnMain(func() {
			ws.git, ws.gitErr, ws.found = r, err, true
			for _, w := range ws.list {
				if w.win == nil {
					w.app.SetRunner(r, err)
				} else {
					w.win.Update(func() { w.app.SetRunner(r, err) })
				}
			}
		})
	}()
}

// lookGit finds git on the user's PATH and makes the runner of every
// git process.
func lookGit() (*run.Runner, error) {
	env := os.Environ()
	if userenv.NeedsResolving() {
		env = userenv.Resolve(context.Background()).Env
	}
	g, err := gitexe.Find(context.Background(), lookup(env, "PATH"))
	if err != nil {
		return nil, err
	}
	return run.New(g, env, maxProcesses), nil
}

// runTool runs the app as git's difftool or mergetool: one window with
// the comparison or the merge, and an exit code git reads, 0 for a merge
// resolved and saved.
func runTool(req cli.Request) {
	cfg := settings.Defaults
	if dir, err := mygo.App.Path(mygo.PathUserData); err == nil {
		cfg, _ = settings.Load(filepath.Join(dir, "settings.json"))
	}
	mygo.Theme.SetSource(mygo.ThemeSource(cfg.Theme))
	code := 0
	if req.Kind == cli.Merge {
		code = 1
	}
	var win *mygo.Window
	app := ui.NewApp(ui.Deps{Settings: cfg, Update: func(fn func()) {
		mygo.RunOnMain(func() {
			if win == nil {
				fn()
				return
			}
			win.Update(fn)
		})
	}})
	title := "Ramus"
	switch req.Kind {
	case cli.Diff:
		title = "Compare " + filepath.Base(req.Paths[1])
		app.Compare(req.Paths[0], req.Paths[1], func() { mygo.App.Exit(code) })
	case cli.Merge:
		title = "Merge " + filepath.Base(req.Paths[3])
		app.MergeTool(req.Paths[0], req.Paths[1], req.Paths[2], req.Paths[3], func(resolved bool) {
			if resolved {
				code = 0
			}
			mygo.App.Exit(code)
		})
	}
	mygo.App.WhenReady(func() {
		win = mygo.NewWindow(mygo.WindowOptions{
			Title: title, Width: 1100, Height: 760, MinWidth: 640, MinHeight: 400,
			StateKey: "tool", Content: mygoui.View(app.ToolView),
		})
		win.OnClosed(func() { mygo.App.Exit(code) })
		go func() {
			r, err := lookGit()
			win.Update(func() { app.SetRunner(r, err) })
		}()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
	os.Exit(code)
}

func lookup(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
			return v
		}
	}
	return ""
}

// menu is the app's menu; an item acts in the window it was chosen in,
// else the one last focused.
func menu(ws *windows) *mygo.Menu {
	key := func(id string) string { return ui.Accelerator(ws.deps.Settings.Keys, id) }
	act := func(fn func(*ui.App)) func(*mygo.MenuItem, *mygo.Window) {
		return func(_ *mygo.MenuItem, win *mygo.Window) {
			w := ws.of(win)
			if w == nil {
				w = ws.target()
			}
			fn(w.app)
			if w.win != nil {
				w.win.Invalidate()
			}
		}
	}
	return mygo.NewMenu([]*mygo.MenuItem{
		{Role: mygo.RoleAppMenu},
		{Label: "File", Submenu: []*mygo.MenuItem{
			{Label: "New Window", Accelerator: key("new-window"), Click: func(*mygo.MenuItem, *mygo.Window) { ws.open(nil) }},
			{Label: "Open Repository…", Accelerator: key("open"), Click: act((*ui.App).PickAndOpen)},
			{Label: "Clone Repository…", Accelerator: key("clone"), Click: act((*ui.App).ShowClone)},
			{Label: "Repositories…", Accelerator: key("repositories"), Click: act((*ui.App).ShowManager)},
			{Role: mygo.RoleOpenRecent},
			mygo.Separator(),
			{Label: "Settings…", Accelerator: key("settings"), Click: act((*ui.App).ShowSettings)},
			{Label: "Command Palette", Accelerator: key("palette"), Click: act((*ui.App).ShowPalette)},
			{Label: "Refresh", Accelerator: key("refresh"), Click: act((*ui.App).Reload)},
			mygo.Separator(),
			{Role: mygo.RoleClose},
		}},
		{Role: mygo.RoleEditMenu},
		{Role: mygo.RoleViewMenu},
		{Role: mygo.RoleWindowMenu},
	})
}
