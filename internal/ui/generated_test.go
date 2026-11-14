package ui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// frameTime is the median of n frames.
func (h *harness) frameTime(n int) time.Duration {
	var ds []time.Duration
	for range n {
		s := time.Now()
		h.tt.Frame()
		ds = append(ds, time.Since(s))
	}
	slices.Sort(ds)
	return ds[n/2]
}

// Generated files stay quick to show: an SVG on one 2 MB line, and a
// .pbxproj conflict of 50,000 lines a side, which shows each side's first
// lines and keeps Edit for the user's editor.
func TestGeneratedFiles(t *testing.T) {
	h := newHarness(t)
	r := h.repoWith()
	svg := func(c string) string {
		return "<svg>" + strings.Repeat("<path d=\"M0 0L1 1\" fill=\""+c+"\"/>", 60000) + "</svg>\n"
	}
	pbx := func(tag string) string {
		var b strings.Builder
		b.WriteString("// !$*UTF8*$!\n{\n")
		for i := range 50000 {
			fmt.Fprintf(&b, "\t\t%024X /* %s */ = {isa = PBXBuildFile; fileRef = %024X; };\n", i, tag, i)
		}
		b.WriteString("}\n")
		return b.String()
	}
	r.Write("icon.svg", svg("red"))
	r.Write("project.pbxproj", pbx("base"))
	r.Commit("base")
	r.Git("switch", "-q", "-c", "other")
	r.Write("project.pbxproj", pbx("theirs"))
	r.Commit("theirs")
	r.Git("switch", "-q", "main")
	r.Write("project.pbxproj", pbx("ours"))
	r.Commit("ours")
	r.Run(nil, "merge", "-q", "other")
	r.Write("icon.svg", svg("blue"))
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the changes", func() bool { return h.tt.HasText("icon.svg") && h.tt.HasText("project.pbxproj") })

	h.click("icon.svg")
	d := &h.app.current().cv.diff
	h.until("the patch", func() bool { return !d.loading && d.pv.d != nil })
	svgFrame := h.frameTime(21)

	start := time.Now()
	h.click("project.pbxproj")
	h.until("the conflict", h.shows("⋯ 49800 more lines"))
	shown := time.Since(start)
	mergeFrame := h.frameTime(21)
	t.Logf("svg line of %d bytes: frame %v; conflict of 50,000 lines a side: shown after %v, frame %v",
		len(svg("blue")), svgFrame.Round(10*time.Microsecond), shown.Round(time.Millisecond), mergeFrame.Round(10*time.Microsecond))
	if svgFrame > 50*time.Millisecond || mergeFrame > 50*time.Millisecond {
		t.Fatalf("frames take %v and %v", svgFrame, mergeFrame)
	}
	ms := &h.app.current().merge
	h.click("Edit")
	if ms.choices[0] != 0 {
		t.Fatal("a 50,000-line conflict opened in place to edit")
	}
	h.click("Theirs")
	h.click("Save and Mark Resolved")
	h.idle(h.app.current(), "the resolution")
	if got := r.Git("show", ":project.pbxproj"); got != pbx("theirs") {
		t.Fatalf("staged %d bytes, want theirs", len(got))
	}
}
