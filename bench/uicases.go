package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	mygoui "github.com/egoist/mygo/ui"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/run"
	"ramus/internal/git/trust"
	"ramus/internal/library"
	"ramus/internal/settings"
	"ramus/internal/ui"
)

// app is the app without a window: mygo's headless tester builds, lays
// out and paints its frames in software, so a frame's time here is the
// app's work and mygo's, without the GPU's.
type app struct {
	a       *ui.App
	tt      *mygoui.Tester
	r       *run.Runner
	updates chan func()
}

func newApp() (*app, error) {
	g, err := gitexe.Find(context.Background(), os.Getenv("PATH"))
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "ramus-bench-app")
	if err != nil {
		return nil, err
	}
	ts, err := trust.Load(filepath.Join(dir, "trust.json"))
	if err != nil {
		return nil, err
	}
	lib, err := library.Load(filepath.Join(dir, "library.json"))
	if err != nil {
		return nil, err
	}
	x := &app{r: run.New(g, os.Environ(), maxProcesses), updates: make(chan func(), 4096)}
	x.a = ui.NewApp(ui.Deps{Runner: x.r, Trust: ts, Library: lib, Settings: settings.Defaults,
		Update: func(fn func()) { x.updates <- fn }})
	x.tt = mygoui.NewTester(x.a.View, 1400, 900)
	return x, nil
}

// frame builds a frame and runs the updates that came, and returns how
// long the frame took.
func (x *app) frame() time.Duration {
	t0 := time.Now()
	x.tt.Frame()
	d := time.Since(t0)
	for {
		select {
		case fn := <-x.updates:
			fn()
		default:
			return d
		}
	}
}

// until frames, at most 60 a second, until ok, within d.
func (x *app) until(what string, d time.Duration, ok func() bool) error {
	deadline := time.Now().Add(d)
	for !ok() {
		x.frame()
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %v waiting for %s", d, what)
		}
		select {
		case fn := <-x.updates:
			fn()
		case <-time.After(16 * time.Millisecond):
		}
	}
	return nil
}

func (x *app) close() {
	x.a.CloseAll()
	x.frame()
}

func (x *app) shows(re *regexp.Regexp) bool {
	for _, s := range x.tt.Texts() {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// frames is a frame-time histogram.
type frames []time.Duration

func (f frames) record(m *meter, prefix string) {
	if len(f) == 0 {
		return
	}
	s := append(frames(nil), f...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	at := func(q float64) time.Duration { return s[min(len(s)-1, int(q*float64(len(s))))] }
	m.time(prefix+"frame p50", at(0.5))
	m.time(prefix+"frame p95", at(0.95))
	m.time(prefix+"frame max", s[len(s)-1])
	buckets := []time.Duration{4 * time.Millisecond, 8 * time.Millisecond, 16700 * time.Microsecond, 33 * time.Millisecond}
	counts := make([]int, len(buckets)+1)
	for _, d := range s {
		i := sort.Search(len(buckets), func(i int) bool { return d <= buckets[i] })
		counts[i]++
	}
	m.fact(prefix+"frames", len(s))
	m.fact(prefix+"histogram", fmt.Sprintf("<=4ms:%d <=8ms:%d <=16.7ms:%d <=33ms:%d more:%d", counts[0], counts[1], counts[2], counts[3], counts[4]))
}

// save5000 saves 5,000 files at once in an open repository and measures
// the frames while the app catches up, and the git processes at once.
func save5000(m *meter) error {
	dir, err := made("save-5000", func(dir string) error { return fastImport(dir, 1, 5000, 5000) })
	if err != nil {
		return err
	}
	work := repoDir(dir)
	if _, err := git(work, nil, "reset", "-q", "--hard"); err != nil {
		return err
	}
	x, err := newApp()
	if err != nil {
		return err
	}
	defer x.close()
	x.a.Open(work)
	clean := regexp.MustCompile(`^No changes$`)
	if err := x.until("the repository", time.Minute, func() bool { return x.shows(clean) }); err != nil {
		return err
	}
	var paths []string
	filepath.WalkDir(work, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".txt") && !strings.Contains(p, ".git"+string(filepath.Separator)) {
			paths = append(paths, p)
		}
		return nil
	})
	stamp := randomHex(4)
	var wg sync.WaitGroup
	t0 := time.Now()
	wg.Add(1)
	go func() {
		defer wg.Done()
		for _, p := range paths {
			os.WriteFile(p, []byte("saved "+stamp+"\n"), 0o644)
		}
	}()
	all := regexp.MustCompile(fmt.Sprintf(`^Changes \(%d\)$`, len(paths)))
	var fs frames
	peak := 0
	var settled time.Duration
	deadline := time.Now().Add(2 * time.Minute)
	for settled == 0 || time.Since(t0) < settled+time.Second {
		next := time.Now().Add(16700 * time.Microsecond)
		fs = append(fs, x.frame())
		peak = max(peak, x.r.Running())
		if settled == 0 && x.shows(all) {
			settled = time.Since(t0)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the status never showed the %d files", len(paths))
		}
		for time.Now().Before(next) {
			peak = max(peak, x.r.Running())
			select {
			case fn := <-x.updates:
				fn()
			case <-time.After(time.Millisecond):
			}
		}
	}
	wg.Wait()
	m.time("saved to status shown", settled)
	fs.record(m, "")
	m.fact("files", len(paths))
	m.fact("peak git processes", peak)
	return nil
}

// idle10 opens ten repositories and measures what the app uses while
// nothing happens.
func idle10(m *meter) error {
	var dirs []string
	for i := range 10 {
		dir, err := made(fmt.Sprintf("idle-%d", i), func(dir string) error { return fastImport(dir, 500, 2000, 5) })
		if err != nil {
			return err
		}
		dirs = append(dirs, repoDir(dir))
	}
	x, err := newApp()
	if err != nil {
		return err
	}
	defer x.close()
	for _, d := range dirs {
		x.a.Open(d)
	}
	clean := regexp.MustCompile(`^No changes$`)
	if err := x.until("the repositories", 2*time.Minute, func() bool { return x.shows(clean) }); err != nil {
		return err
	}
	// Settle: the first reads and the watchers' start.
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); {
		x.frame()
		time.Sleep(16 * time.Millisecond)
	}
	const window = 30 * time.Second
	runtime.GC()
	cpu0 := cpuTime()
	updates, built := 0, 0
	end := time.Now().Add(window)
	for time.Now().Before(end) {
		// A window shows a frame only when asked for one.
		if x.tt.FramePending() {
			x.tt.Frame()
			built++
		}
		select {
		case fn := <-x.updates:
			updates++
			fn()
		case <-time.After(50 * time.Millisecond):
		}
	}
	used := cpuTime() - cpu0
	m.time("CPU used in 30 s idle", used)
	m.fact("CPU percent", fmt.Sprintf("%.2f", 100*used.Seconds()/window.Seconds()))
	m.fact("frames asked for", built)
	m.fact("updates", updates)
	m.fact("goroutines", runtime.NumGoroutine())
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	m.fact("heap in use", mib(int64(ms.HeapInuse)))
	m.fact("repositories", len(dirs))
	return nil
}

// scrollKernel scrolls the kernel's whole graph and jumps to its first
// commit.
func scrollKernel(m *meter) error {
	dir, err := m.fixture("linux")
	if err != nil {
		return err
	}
	x, err := newApp()
	if err != nil {
		return err
	}
	defer x.close()
	x.a.Open(dir)
	if err := x.until("the repository", time.Minute, func() bool { return x.tt.HasText("History") }); err != nil {
		return err
	}
	if err := x.tt.Click("History"); err != nil {
		return err
	}
	done := regexp.MustCompile(`^\d+ commits$`)
	t0 := time.Now()
	if err := x.until("the whole history", 5*time.Minute, func() bool { return x.shows(done) }); err != nil {
		return err
	}
	m.time("history to its end, shown", time.Since(t0))
	for _, s := range x.tt.Texts() {
		if done.MatchString(s) {
			m.fact("commits", strings.TrimSuffix(s, " commits"))
		}
	}
	at, ok := x.tt.Find("Commits")
	if !ok {
		return fmt.Errorf("no list of commits")
	}
	// Fling: 600 frames of a large wheel step, as a fast trackpad swipe.
	var fs frames
	for range 600 {
		x.tt.Wheel(at.X+at.W/2, at.Y+200, 0, -2400)
		fs = append(fs, x.frame())
	}
	fs.record(m, "scroll: ")
	// The kernel's first commit, 2.6.12-rc2.
	const first = "1da177e4c3f41524e886b7f1b8a0c1fc7321cac2"
	if err := x.tt.Click("Go to hash or ref"); err != nil {
		return err
	}
	x.frame()
	x.tt.Type(first)
	t1 := time.Now()
	x.tt.Key(0, mygoui.KeyEnter)
	shown := regexp.MustCompile(`^Linux-2\.6\.12-rc2`)
	if err := x.until("the first commit", time.Minute, func() bool { return x.shows(shown) }); err != nil {
		return err
	}
	m.time("jump to the first commit", time.Since(t1))
	return nil
}
