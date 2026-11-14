package spawn

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/run"
	"ramus/internal/git/trust"
	"ramus/internal/session"
	"ramus/spike/internal/measure"
)

func TestSpawn(t *testing.T) {
	if os.Getenv("RAMUS_MEASURE") == "" {
		t.Skip("set RAMUS_MEASURE=1 to measure")
	}
	r := gittest.Init(t)
	for i := range 200 {
		r.Write(fmt.Sprintf("d%d/f%d.txt", i%20, i), "x\n")
	}
	r.Commit("files")
	rn := testrun.Runner(t)
	ctx := context.Background()
	var one []time.Duration
	for range 50 {
		t0 := time.Now()
		if _, err := rn.Run(ctx, &run.Cmd{GitDir: r.GitDir(), Read: true, Args: []string{"rev-parse", "--git-dir"}}); err != nil {
			t.Fatal(err)
		}
		one = append(one, time.Since(t0))
	}
	fmt.Printf("%s/%s, git %s\n", runtime.GOOS, runtime.GOARCH, rn.Git().Version.Raw)
	measure.Summarize(one).Print(os.Stdout, "one git process")

	ts, err := trust.Load(filepath.Join(t.TempDir(), "trust.json"))
	if err != nil {
		t.Fatal(err)
	}
	ts.Set(r.GitDir(), true)
	var opens []time.Duration
	var spawned int
	for range 5 {
		before := len(rn.Journal().For(r.GitDir()))
		updates := make(chan func(), 256)
		t0 := time.Now()
		s := session.Open(rn, ts, r.Dir, func(fn func()) { updates <- fn })
		deadline := time.After(time.Minute)
		for s.State.Status == nil {
			select {
			case fn := <-updates:
				fn()
			case <-deadline:
				t.Fatal("no status")
			}
		}
		opens = append(opens, time.Since(t0))
		spawned = len(rn.Journal().For(r.GitDir())) - before
		s.Close()
		for done := false; !done; {
			select {
			case fn := <-updates:
				fn()
			case <-s.Done():
				done = true
			}
		}
	}
	measure.Summarize(opens).Print(os.Stdout, fmt.Sprintf("open to first status (%d git processes)", spawned))
}
