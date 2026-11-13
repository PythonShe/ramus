package diff

import (
	"reflect"
	"testing"
)

func TestSides(t *testing.T) {
	text := "diff --git a/f b/f\n--- a/f\n+++ b/f\n" +
		"@@ -1,4 +1,4 @@\n a\n-b\r\n+B\n c\n-d\r\n+D\n" +
		"@@ -10,2 +10,3 @@\n x\n+y\n z\n\\ No newline at end of file\n"
	p, err := Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Runs(0); !reflect.DeepEqual(got, []Span{{1, 3}, {4, 6}, {7, 8}}) {
		t.Fatalf("runs %v", got)
	}
	old, new := p.SideLines(0, false)
	if !reflect.DeepEqual(old, []int32{0, 1, 3, 4, 6, 8}) || !reflect.DeepEqual(new, []int32{0, 2, 3, 5, 6, 7, 8}) {
		t.Fatalf("partial sides %v %v", old, new)
	}
	old, new = p.SideLines(0, true)
	if len(old) != 11 || old[9] != 6 || old[10] != 8 || old[4] != -1 || len(new) != 12 || new[10] != 7 {
		t.Fatalf("whole sides %v %v", old, new)
	}
	if got := p.LineEndings(0); got != "CRLF to LF" {
		t.Fatalf("endings %q", got)
	}
	mixed, _ := Parse([]byte("diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1,2 +1,2 @@\n-a\r\n-b\n+a\n+b\n"))
	if got := mixed.LineEndings(0); got != "" {
		t.Fatalf("mixed endings said %q", got)
	}
}
