// Package libstats times library statistics on the fixtures.
package libstats

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"ramus/internal/git/activity"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/libstats"
)

// TestLibraryKernel times reading the kernel's activity records cold, and
// again from the cache directory, and counting them:
//
//	RAMUS_FIXTURES=~/Projects/ramus-fixtures go test ./spike/libstats -run LibraryKernel -v
func TestLibraryKernel(t *testing.T) {
	dir := filepath.Join(gittest.FixturesDir(t), "linux")
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Skip(err)
	}
	cache := t.TempDir()
	updates := make(chan func(), 1024)
	load := func() (libstats.Repo, time.Duration) {
		l := libstats.New(testrun.Runner(t), nil, cache, func(fn func()) { updates <- fn })
		defer l.Close()
		start := time.Now()
		var got libstats.Repo
		l.Load([]string{dir}, []string{"linux"}, func(_ int, r libstats.Repo) { got = r })
		deadline := time.After(5 * time.Minute)
		for !got.Done {
			select {
			case fn := <-updates:
				fn()
			case <-deadline:
				t.Fatal("timed out")
			}
		}
		if got.Err != nil {
			t.Fatal(got.Err)
		}
		return got, time.Since(start)
	}
	cold, took := load()
	t.Logf("cold: %d commits, %d identities, %d MB held, in %v", cold.Records.Len(), cold.Records.Identities(), cold.Records.Bytes()>>20, took)
	files, _ := filepath.Glob(filepath.Join(cache, "*.activity"))
	for _, f := range files {
		fi, _ := os.Stat(f)
		t.Logf("cache file %d MB", fi.Size()>>20)
	}
	warm, took := load()
	t.Logf("from the cache directory: %d commits in %v", warm.Records.Len(), took)
	today := activity.DayOf(time.Now())
	for _, f := range []libstats.Filter{{}, {Since: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Unix()}, {Mine: true, Emails: []string{"torvalds@linux-foundation.org"}}} {
		start := time.Now()
		s := libstats.Summarize([]libstats.Repo{warm}, f, today)
		t.Logf("summary %+v: %d commits, %d people, %d active days, in %v", f, s.Commits, s.Contributors, s.ActiveDays, time.Since(start))
	}
}
