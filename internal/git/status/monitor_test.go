package status

import (
	"strings"
	"testing"
	"time"

	"ramus/internal/git/config"
	"ramus/internal/git/gitexe"
)

func TestAdviseMonitor(t *testing.T) {
	cfg := func(s string) *config.Config {
		c, err := config.Parse([]byte(strings.ReplaceAll(s, "|", "\x00")))
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	v := func(s string) gitexe.Version {
		v, err := gitexe.ParseVersion("git version " + s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	none := cfg("")
	slow := 2 * time.Second
	for _, c := range []struct {
		name    string
		goos    string
		git     string
		cfg     *config.Config
		trusted bool
		took    time.Duration
		want    []string // the keys set
	}{
		{"macOS", "darwin", "2.40.0", none, true, slow, []string{"core.fsmonitor", "core.untrackedCache"}},
		{"Windows", "windows", "2.40.0", none, true, slow, []string{"core.fsmonitor", "core.untrackedCache"}},
		{"Linux before the daemon", "linux", "2.54.0", none, true, slow, []string{"core.untrackedCache"}},
		{"Linux with the daemon", "linux", "2.55.0", none, true, slow, []string{"core.fsmonitor", "core.untrackedCache"}},
		{"not trusted", "darwin", "2.40.0", none, false, slow, nil},
		{"fast", "darwin", "2.40.0", none, true, 100 * time.Millisecond, nil},
		{"a hook of the user's", "darwin", "2.40.0", cfg("local|f|core.fsmonitor\n.git/hooks/watchman|"), true, slow, []string{"core.untrackedCache"}},
		{"turned off", "darwin", "2.40.0", cfg("local|f|core.fsmonitor\nfalse|local|f|core.untrackedcache\nfalse|"), true, slow, nil},
	} {
		a, ok := AdviseMonitor(c.goos, v(c.git), c.cfg, "/r/.git", "/r", c.trusted, c.took)
		var got []string
		for _, cmd := range a.Cmds {
			got = append(got, cmd.Args[1])
		}
		if ok != (c.want != nil) || strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("%s: offered %v (%v), want %v", c.name, got, ok, c.want)
		}
		if ok && (a.Daemon != "") != (c.want[0] == "core.fsmonitor") {
			t.Errorf("%s: daemon note %q", c.name, a.Daemon)
		}
		if ok {
			t.Logf("%s: %s", c.name, a.Reason)
		}
	}
}
