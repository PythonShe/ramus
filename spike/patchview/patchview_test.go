// Package patchview times a large patch through the session and mygo's
// diff viewer.
package patchview

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"path/filepath"
	"ramus/internal/git/diff"
	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
	"ramus/internal/git/trust"
	"ramus/internal/session"
)

// TestLargePatch reads the patch of a 200,000-line file with every tenth
// line changed, builds the viewer and scrolls it:
//
//	go test ./spike/patchview -run LargePatch -v
func TestLargePatch(t *testing.T) {
	r := gittest.Init(t)
	var old, nw strings.Builder
	for i := range 200_000 {
		line := "func f" + strconv.Itoa(i) + "() int { return " + strconv.Itoa(i) + " }\n"
		old.WriteString(line)
		if i%10 == 0 {
			line = "func f" + strconv.Itoa(i) + "() int { return " + strconv.Itoa(i*2) + " } // changed\n"
		}
		nw.WriteString(line)
	}
	r.Write("big.go", old.String())
	r.Commit("big")
	r.Write("big.go", nw.String())
	ts, _ := trust.Load(filepath.Join(t.TempDir(), "trust.json"))
	ts.Set(r.GitDir(), true)
	updates := make(chan func(), 64)
	s := session.Open(testrun.Runner(t), ts, r.Dir, func(fn func()) { updates <- fn })
	t.Cleanup(func() { s.Close(); <-s.Done() })
	for s.State.Status == nil {
		(<-updates)()
	}
	ids := s.State.Status.OIDs(0)
	start := time.Now()
	var res *session.PatchResult
	s.Patch(diff.PatchRequest{Paths: []string{"big.go"}, Context: -1, MaxBytes: 32 << 20},
		session.Sides{Old: ids[1], WorkTreePath: "big.go"}, func(pr session.PatchResult) { res = &pr })
	for res == nil {
		(<-updates)()
	}
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	t.Logf("patch read and parsed: %d bytes, %d lines, in %v", len(res.Parsed.Text), len(res.Parsed.Lines), time.Since(start))

	start = time.Now()
	d := &editor.DiffState{Indicators: editor.DiffClassic}
	fps, err := editor.ParsePatch(string(res.Parsed.Text))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.SetPatchFiles(fps[0], string(res.Old), string(res.New)); err != nil {
		t.Fatal(err)
	}
	t.Logf("viewer built off the main thread in %v", time.Since(start))
	for _, layout := range []editor.DiffLayout{editor.DiffUnified, editor.DiffSplit} {
		d.Layout = layout
		tt := ui.NewTester(func(c *ui.Context) { editor.DiffView(c, d).Fill() }, 1200, 800)
		start = time.Now()
		tt.Frame()
		first := time.Since(start)
		var worst time.Duration
		for i := range 50 {
			d.SetScrollY(float64(i) * 40_000)
			start = time.Now()
			tt.Frame()
			worst = max(worst, time.Since(start))
		}
		t.Logf("layout %d: first frame %v, worst of 50 scrolled frames %v", layout, first, worst)
	}
}
