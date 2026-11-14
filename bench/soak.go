package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"
)

// soak opens, changes, refreshes and closes repositories in the app for
// RAMUS_SOAK (2 minutes when unset), then checks that goroutines, open
// files, git processes and memory came back to what they were after the
// first round: everything a repository owns ends when it closes.
func soak(m *meter) error {
	length := 2 * time.Minute
	if v := os.Getenv("RAMUS_SOAK"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return err
		}
		length = d
	}
	var dirs []string
	for i := range 3 {
		dir, err := made(fmt.Sprintf("soak-%d", i), func(dir string) error { return fastImport(dir, 300, 400, 4) })
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
	shown := regexp.MustCompile(`^(No changes|Changes \(\d+\))$`)
	changed := regexp.MustCompile(`^Changes \(1\)$`)
	round := func(n int) error {
		for _, d := range dirs {
			x.a.Open(d)
		}
		if err := x.until("the repositories", time.Minute, func() bool { return x.shows(shown) }); err != nil {
			return err
		}
		// A change the watcher sees, then a reload, then the change undone.
		f := filepath.Join(dirs[len(dirs)-1], "soak.txt")
		if err := os.WriteFile(f, []byte(fmt.Sprintf("round %d\n", n)), 0o644); err != nil {
			return err
		}
		if err := x.until("the change", time.Minute, func() bool { return x.shows(changed) }); err != nil {
			return err
		}
		x.a.Reload()
		os.Remove(f)
		if err := x.until("the change gone", time.Minute, func() bool { return !x.shows(changed) }); err != nil {
			return err
		}
		x.a.CloseAll()
		// What the repositories ran stops once they close.
		return x.until("the processes", time.Minute, func() bool { return x.r.Running() == 0 })
	}
	settle := func() (goroutines, files int, heap uint64) {
		for range 20 {
			x.frame()
			time.Sleep(25 * time.Millisecond)
		}
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return runtime.NumGoroutine(), openFiles(), ms.HeapInuse
	}
	if err := round(0); err != nil {
		return err
	}
	g0, f0, h0 := settle()
	start, rounds := time.Now(), 1
	for time.Since(start) < length {
		if err := round(rounds); err != nil {
			return fmt.Errorf("round %d: %w", rounds, err)
		}
		rounds++
	}
	g1, f1, h1 := settle()
	m.time("soak", time.Since(start))
	m.fact("rounds", rounds)
	m.fact("goroutines", fmt.Sprintf("%d->%d", g0, g1))
	m.fact("open files", fmt.Sprintf("%d->%d", f0, f1))
	m.fact("heap in use", fmt.Sprintf("%s->%s", mib(int64(h0)), mib(int64(h1))))
	switch {
	case g1 > g0+2:
		return fmt.Errorf("goroutines grew from %d to %d over %d rounds", g0, g1, rounds)
	case f0 >= 0 && f1 > f0+2:
		return fmt.Errorf("open files grew from %d to %d over %d rounds", f0, f1, rounds)
	case h1 > 2*h0+(16<<20):
		return fmt.Errorf("heap in use grew from %s to %s over %d rounds", mib(int64(h0)), mib(int64(h1)), rounds)
	}
	return nil
}

// openFiles counts this process's open file descriptors, -1 where they
// cannot be listed.
func openFiles() int {
	for _, d := range []string{"/proc/self/fd", "/dev/fd"} {
		if es, err := os.ReadDir(d); err == nil {
			return len(es)
		}
	}
	return -1
}
