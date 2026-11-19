package crashlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// ends is what readEnds keeps of content, which it checks is valid UTF-8
// and within the bounds.
func ends(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "out")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out, err := readEnds(f, int64(len(content)), fatalHead, fatalTail)
	if err != nil {
		t.Fatal(err)
	}
	if utf8.ValidString(content) && !utf8.ValidString(out) {
		t.Error("a character cut in two")
	}
	if len(out) > fatalError+fatalHead+fatalTail+200 {
		t.Errorf("kept %d bytes", len(out))
	}
	return out
}

// What a crash printed is cut so as to keep the stack of the goroutine
// that crashed whatever the length of its lines: after a panic value of
// any length, with lines longer than a cut, and with no line at all.
func TestFatalKeepsTheCrashingStack(t *testing.T) {
	if s := strings.Repeat("x", fatalHead+fatalTail-1) + "\n"; ends(t, s) != s {
		t.Error("a crash's output of the bound not kept whole")
	}
	idle := strings.Repeat("goroutine 9 [select]:\nmain.idle()\n\t/x/idle.go:3\n\n", 20000)
	// A panic value printing a long string, on one line.
	out := ends(t, "panic: "+strings.Repeat("v", 100<<10)+"\n\ngoroutine 1 [running]:\nmain.culprit()\n\t/x/main.go:10\n\n"+idle)
	if !strings.Contains(out, "panic: vvv") || !strings.Contains(out, "goroutine 1 [running]:\nmain.culprit()\n\t/x/main.go:10\n") {
		t.Errorf("the error or the crashing stack lost:\n%.300s", out)
	}
	// A frame's line longer than the head: cut inside it.
	out = ends(t, "fatal error: x\n\ngoroutine 1 [running]:\nmain.f("+strings.Repeat("é", fatalHead)+")\n"+idle)
	if !strings.Contains(out, "goroutine 1 [running]:\nmain.f(ééé") || !strings.Contains(out, "bytes left out") {
		t.Errorf("the crashing stack lost:\n%.300s", out)
	}
	// The last line longer than the tail: its end kept.
	out = ends(t, idle+"fatal: "+strings.Repeat("y", fatalTail+10)+"\n")
	if !strings.HasSuffix(out, "yyyy\n") {
		t.Error("the end lost to a long last line")
	}
	// No line at all.
	ends(t, strings.Repeat("é", fatalHead+fatalTail))
}
