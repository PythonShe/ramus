package status

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/git/gittest"
	"ramus/internal/ui/pathtree"
	"ramus/spike/internal/measure"
)

func git(t *testing.T, dir string, args ...string) ([]byte, time.Duration) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "--no-optional-locks"}, args...)...)
	cmd.Env = append(gittest.Env(), "GIT_NO_LAZY_FETCH=1")
	start := time.Now()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out, time.Since(start)
}

type view struct {
	tree  *pathtree.Tree
	state ui.OutlineState[pathtree.Node]
	row   int
}

func (v *view) build(c *ui.Context) {
	v.state.List.Selected = &v.row
	ui.Outline(c, &v.state, v.tree.Children(pathtree.Root), func(n pathtree.Node) []pathtree.Node {
		if !v.tree.IsDir(n) {
			return nil
		}
		return v.tree.Children(n)
	}, func(n pathtree.Node) {
		ui.Text(c, v.tree.Name(n)).SingleLine()
	}).Grow(1)
}

func frames(tt *ui.Tester, v *view, step, n int) measure.Summary {
	var ds []time.Duration
	target := 0
	for i := 0; i < n; i++ {
		target += step
		v.state.List.ScrollTo(target, ui.Start)
		start := time.Now()
		tt.Frame()
		ds = append(ds, time.Since(start))
	}
	return measure.Summarize(ds)
}

func TestMonorepo(t *testing.T) {
	dir := filepath.Join(gittest.FixturesDir(t), "monorepo.noindex")
	if _, err := os.Stat(dir); err != nil {
		t.Skip(err)
	}
	t.Logf("GOMAXPROCS=%d", runtime.GOMAXPROCS(0))
	statusArgs := []string{"status", "--porcelain=v2", "-z", "--branch", "--show-stash", "--no-ahead-behind", "--ignore-submodules=dirty"}
	for i := 0; i < 3; i++ {
		out, d := git(t, dir, statusArgs...)
		t.Logf("git status (clean work tree), run %d: %v, %d bytes", i+1, d.Round(time.Millisecond), len(out))
	}
	paths, d := git(t, dir, "ls-files", "-z")
	t.Logf("git ls-files: %v, %d bytes", d.Round(time.Millisecond), len(paths))

	// Every tracked file as if changed: the worst case for the view.
	start := time.Now()
	tree := pathtree.Build(paths, 0)
	t.Logf("tree of %d files, %d nodes: built in %v, %.1f MB", tree.Files(), tree.Len(),
		time.Since(start).Round(time.Millisecond), float64(tree.Bytes())/(1<<20))

	v := &view{tree: tree}
	tt := ui.NewTester(v.build, 1000, 900)
	start = time.Now()
	tt.Frame()
	t.Logf("first frame, folders closed: %v", time.Since(start).Round(10*time.Microsecond))
	s := frames(tt, v, 1, 100)
	t.Logf("folders closed, scrolling: p50=%v p95=%v max=%v", s.P50, s.P95, s.Max)

	start = time.Now()
	dirs := 0
	for n := pathtree.Node(1); int(n) < tree.Len(); n++ {
		if tree.IsDir(n) {
			v.state.Open.Add(n)
			dirs++
		}
	}
	t.Logf("opened %d folders in %v", dirs, time.Since(start).Round(time.Millisecond))
	start = time.Now()
	tt.Frame()
	t.Logf("first frame, every folder open: %v", time.Since(start).Round(10*time.Microsecond))
	for _, step := range []int{3, 40} {
		s := frames(tt, v, step, 100)
		t.Logf("every folder open, %d rows a frame: p50=%v p95=%v max=%v", step, s.P50, s.P95, s.Max)
	}
	// A refresh: the same status arrives again as a new tree.
	start = time.Now()
	v.tree = pathtree.Build(paths, 0)
	tt.Frame()
	t.Logf("refresh (rebuild tree + frame), every folder open: %v", time.Since(start).Round(time.Millisecond))
	// 30,000 changed files, every folder open: the size of change a coding
	// agent or a dependency bump makes.
	var sub []byte
	k := 0
	for rest := paths; len(rest) > 0; k++ {
		i := bytes.IndexByte(rest, 0)
		if k%13 == 0 {
			sub = append(sub, rest[:i+1]...)
		}
		rest = rest[i+1:]
	}
	v = &view{tree: pathtree.Build(sub, 0)}
	for n := pathtree.Node(1); int(n) < v.tree.Len(); n++ {
		if v.tree.IsDir(n) {
			v.state.Open.Add(n)
		}
	}
	tt = ui.NewTester(v.build, 1000, 900)
	start = time.Now()
	tt.Frame()
	t.Logf("%d changed files, %d rows open: first frame %v", v.tree.Files(), v.tree.Len()-1, time.Since(start).Round(10*time.Microsecond))
	s = frames(tt, v, 3, 100)
	t.Logf("%d changed files, every folder open, scrolling: p50=%v p95=%v max=%v", v.tree.Files(), s.P50, s.P95, s.Max)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	t.Logf("heap in use %.0f MB", float64(ms.HeapInuse)/(1<<20))
}
