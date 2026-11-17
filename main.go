package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/egoist/mygo"
	mygoui "github.com/egoist/mygo/ui"

	"ramus/internal/askpass"
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
	"ramus/locales"
)

// maxProcesses bounds the git processes running at once, across every
// repository.
const maxProcesses = 8

func main() {
	mainStart = time.Now()
	if askpass.IsHelper(os.Args[1:]) {
		// git or ssh asks the running app a question through this process.
		os.Exit(askpass.Helper(os.Args[1:], os.Stdout))
	}
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
	if program != "" {
		program = cli.Program(program, os.Getenv)
	}
	ws := &windows{probe: probe, deps: ui.Deps{
		Trust: ts, Library: lib, Settings: cfg, SettingsPath: cfgPath, SettingsKeeper: settings.NewKeeper(cfgPath),
		CacheDir: cacheDir, Program: program, Crash: crash,
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
	// handle carries out a request, made in folder wd, in the window it
	// belongs in, and returns that window.
	handle := func(r cli.Request, wd string) *window {
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
			w.app.CompareNamed(r.Paths[0], r.Paths[1], r.Name, toolLabels(r, wd), nil)
		case cli.Log, cli.Blame:
			w.app.ShowFile(r.Paths[0], r.Kind == cli.Blame)
		case cli.Merge:
			w.app.MergeTool(r.Paths[0], r.Paths[1], r.Paths[2], r.Paths[3], nil)
		}
		return w
	}
	handle(req, cwd)
	mygo.App.OnSecondInstance(func(args []string, wd string) {
		r, err := cli.Parse(args, wd)
		if err != nil {
			w := ws.target()
			w.app.Notify(err.Error())
			ws.raise(w)
			return
		}
		ws.raise(handle(r, wd))
	})
	mygo.App.OnBeforeQuit(func(e *mygo.QuitEvent) {
		if ws.beforeQuit(e) {
			e.PreventDefault()
			return
		}
		ws.flush()
		ws.quitting = true
	})
	mygo.App.OnQuitCanceled(func() { ws.quitting, ws.quitAnyway = false, false })
	mygo.App.OnOpenFile(func(p string) {
		ws.raise(handle(cli.Request{Kind: cli.Open, Paths: []string{p}}, ""))
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
		// The menu's own labels are set once; the roles' follow by
		// themselves.
		mygo.App.OnLocaleChanged(func(string) { mygo.App.SetMenu(menu(ws)) })
		for _, w := range ws.list[1:] {
			ws.show(w)
		}
		ws.show(first)
		findGit(ws, crash, listenAsk(ws))
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// findGit finds git off the main thread and hands it to every window.
// The user's PATH comes from their login shell when the app was not
// started from a terminal; asking it takes a moment, so the window opens
// first.
func findGit(ws *windows, crash *crashlog.Log, ask *askpass.Server) {
	go func() {
		r, env, shellErr, err := lookGit()
		// An askpass of the user's own stays theirs.
		if err == nil && ask != nil && lookup(env, "SSH_ASKPASS") == "" {
			if program, perr := os.Executable(); perr == nil {
				r.SetPrompt(ask.Env(program), ask.Asking)
			}
		}
		if err == nil && crash != nil {
			crash.SetGit(r.Git(), r.Journal())
		}
		mygo.RunOnMain(func() {
			ws.git, ws.gitErr, ws.found = r, err, true
			if shellErr != nil && len(ws.list) > 0 {
				// Not fatal: git and the tools in the usual places are found
				// all the same; ones the shell adds to PATH may not be.
				ws.list[0].app.Notify(locales.AppShellNotRead(shellErr.Error()))
			}
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

// listenAsk starts listening for the questions git and ssh ask through
// the askpass helper, each shown in the window of the repository that
// asks, else the one last focused. None in the probe: it asks nothing.
func listenAsk(ws *windows) *askpass.Server {
	if ws.probe != "" {
		return nil
	}
	s, err := askpass.Listen(func(q *askpass.Ask) {
		mygo.RunOnMain(func() {
			w := ws.showing(q.Dir)
			if w == nil {
				w = ws.target()
			}
			w.app.Ask(q)
			if w.win != nil {
				w.win.Invalidate()
			}
		})
	})
	if err != nil {
		log.Print(err) // git and ssh then fail where they would ask
		return nil
	}
	mygo.App.OnQuit(func() { s.Close() })
	return s
}

// lookGit finds git on the user's PATH and makes the runner of every
// git process; env is the user's environment it runs with, and
// shellErr says why the login shell's could not be read.
func lookGit() (r *run.Runner, env []string, shellErr, err error) {
	env = os.Environ()
	if userenv.NeedsResolving() {
		res := userenv.Resolve(context.Background())
		env, shellErr = res.Env, res.Err
	}
	g, err := gitexe.Find(context.Background(), lookup(env, "PATH"))
	if err != nil {
		return nil, env, shellErr, err
	}
	return run.New(g, env, maxProcesses), env, shellErr, nil
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
		name := filepath.Base(req.Paths[1])
		if req.Name != "" {
			name = filepath.Base(req.Name)
		}
		title = locales.WindowCompare(name)
		cwd, _ := os.Getwd()
		app.CompareNamed(req.Paths[0], req.Paths[1], req.Name, toolLabels(req, cwd), func() { mygo.App.Exit(code) })
	case cli.Merge:
		title = locales.WindowMerge(filepath.Base(req.Paths[3]))
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
			r, _, _, err := lookGit()
			win.Update(func() { app.SetRunner(r, err) })
		}()
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
	os.Exit(code)
}

// toolLabels names the sides of a comparison git difftool asked for, in
// cwd, where it ran: as the request names them, else the work tree's
// file when a side is it, else the older and the newer.
func toolLabels(r cli.Request, cwd string) []string {
	if r.Labels != nil || r.Name == "" {
		return r.Labels
	}
	file := r.Name
	if !filepath.IsAbs(file) {
		file = filepath.Join(cwd, file)
	}
	labels := []string{locales.CompareSideOld(), locales.CompareSideNew()}
	for i, p := range r.Paths {
		if filepath.Clean(p) == filepath.Clean(file) {
			labels[i] = locales.CompareSideWorkTree()
		}
	}
	return labels
}

func lookup(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
			return v
		}
	}
	return ""
}

// repoMenu are the commands of the Repository menu, by id, "" a
// separator.
var repoMenu = []string{
	"commit", "commit-push", "",
	"stage-all", "unstage-all", "",
	"fetch", "pull", "push", "",
	"switch-branch", "new-branch", "new-tag", "stash", "",
	"refresh",
}

func repoMenuID(id string) string { return "repo:" + id }

// repoMenuEnabled enables the Repository menu's items while they act on
// the window focused: it shows a repository, with no dialog over it.
func (ws *windows) repoMenuEnabled() {
	m := mygo.App.Menu()
	if m == nil {
		return
	}
	on := ws.repoCommandsOn()
	for _, id := range repoMenu {
		if it := m.ItemByID(repoMenuID(id)); it != nil && it.IsEnabled() != on {
			it.SetEnabled(on)
		}
	}
}

func (ws *windows) repoCommandsOn() bool {
	return ws.focused != nil && ws.focused.app.RepoCommandsOn()
}

// menu is the app's menu; an item acts in the window it was chosen in,
// else the one last focused, and waits while a dialog is open there, as
// the window's own shortcuts do: the menu takes the keys first.
func menu(ws *windows) *mygo.Menu {
	key := func(id string) string { return ui.Accelerator(ws.deps.Settings.Keys, id) }
	act := func(fn func(*ui.App)) func(*mygo.MenuItem, *mygo.Window) {
		return func(_ *mygo.MenuItem, win *mygo.Window) {
			w := ws.of(win)
			if w == nil {
				w = ws.target()
			}
			if w.app.DialogOpen() {
				return
			}
			fn(w.app)
			if w.win != nil {
				w.win.Invalidate()
			}
		}
	}
	about := func(check bool) func(*mygo.MenuItem, *mygo.Window) {
		return func(*mygo.MenuItem, *mygo.Window) { ws.showAbout(check) }
	}
	mac := runtime.GOOS == "darwin"
	settingsItem := &mygo.MenuItem{Label: locales.MenuSettings(), Accelerator: key("settings"), Click: act((*ui.App).ShowSettings)}
	// macOS has About and Settings in the app's menu, the others About in
	// Help and Settings in File.
	appMenu := &mygo.MenuItem{Role: mygo.RoleAppMenu, Submenu: []*mygo.MenuItem{
		{Label: locales.MenuAbout(), Click: about(false)},
		{Label: locales.MenuCheckForUpdates(), Click: about(true)},
		mygo.Separator(),
		settingsItem,
		mygo.Separator(),
		{Role: mygo.RoleServices},
		mygo.Separator(),
		{Role: mygo.RoleHide},
		{Role: mygo.RoleHideOthers},
		{Role: mygo.RoleUnhide},
		mygo.Separator(),
		{Role: mygo.RoleQuit},
	}}
	help := &mygo.MenuItem{Label: locales.MenuHelp(), Hidden: mac, Submenu: []*mygo.MenuItem{
		{Label: locales.MenuCheckForUpdates(), Click: about(true)},
		mygo.Separator(),
		{Label: locales.MenuAbout(), Click: about(false)},
	}}
	file := []*mygo.MenuItem{
		{Label: locales.MenuNewWindow(), Accelerator: key("new-window"), Click: func(*mygo.MenuItem, *mygo.Window) { ws.open(nil) }},
		{Label: locales.MenuOpenRepository(), Accelerator: key("open"), Click: act((*ui.App).PickAndOpen)},
		{Label: locales.MenuCloneRepository(), Accelerator: key("clone"), Click: act((*ui.App).ShowClone)},
		{Label: locales.MenuRepositories(), Accelerator: key("repositories"), Click: act((*ui.App).ShowManager)},
		{Role: mygo.RoleOpenRecent},
		mygo.Separator(),
	}
	if !mac {
		file = append(file, settingsItem)
	}
	file = append(file,
		&mygo.MenuItem{Label: locales.MenuCommandPalette(), Accelerator: key("palette"), Click: act((*ui.App).ShowPalette)},
		mygo.Separator())
	// The commands on the repository shown, with the user's shortcuts,
	// enabled while the window shows one (repoMenuEnabled); one that
	// cannot run then, as a Push with nowhere to push, does nothing.
	repo := make([]*mygo.MenuItem, 0, len(repoMenu))
	for _, id := range repoMenu {
		if id == "" {
			repo = append(repo, mygo.Separator())
			continue
		}
		repo = append(repo, &mygo.MenuItem{ID: repoMenuID(id), Label: ui.CommandName(id), Accelerator: key(id),
			Disabled: !ws.repoCommandsOn(), Click: act(func(a *ui.App) { a.RunCommand(id) })})
	}
	return mygo.NewMenu([]*mygo.MenuItem{
		appMenu,
		{Label: locales.MenuFile(), Submenu: append(file, []*mygo.MenuItem{
			// Not RoleClose, whose Cmd+W would close the window and every
			// tab in it: the menu takes keys before the window does.
			{Label: locales.MenuCloseTab(), Accelerator: key("close-tab"), Click: func(_ *mygo.MenuItem, win *mygo.Window) {
				w := ws.of(win)
				switch {
				case w == nil && win != nil:
					// A window of no repositories, as About: it closes, and
					// no tab of a window behind it does.
					win.Close()
					return
				case w == nil:
					w = ws.focused
				}
				if w == nil || w.app.DialogOpen() {
					return
				}
				if !w.app.CloseTab() && w.win != nil {
					w.win.Close()
				}
				if w.win != nil {
					w.win.Invalidate()
				}
			}},
			{Label: locales.MenuCloseWindow(), Accelerator: "CmdOrCtrl+Shift+W", Click: func(_ *mygo.MenuItem, win *mygo.Window) {
				if w := ws.of(win); w != nil && w.win != nil {
					w.win.Close()
				} else if w == nil && win != nil {
					win.Close()
				}
			}},
		}...)},
		{Role: mygo.RoleEditMenu},
		{Label: locales.MenuRepository(), Submenu: repo},
		{Role: mygo.RoleViewMenu},
		{Role: mygo.RoleWindowMenu},
		help,
	})
}
