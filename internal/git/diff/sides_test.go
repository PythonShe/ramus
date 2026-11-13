package diff

import (
	"fmt"
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

func TestSubmodule(t *testing.T) {
	a, b := "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	p, _ := Parse([]byte("diff --git a/s b/s\nindex 1111111..2222222 160000\n--- a/s\n+++ b/s\n@@ -1 +1 @@\n" +
		"-Subproject commit " + a + "\n+Subproject commit " + b + "-dirty\n"))
	old, new, dirty, ok := p.Submodule(0)
	if !ok || !dirty || fmt.Sprintf("%x", old) != a || fmt.Sprintf("%x", new) != b {
		t.Fatalf("%x %x %v %v", old, new, dirty, ok)
	}
	added, _ := Parse([]byte("diff --git a/s b/s\nnew file mode 160000\n--- /dev/null\n+++ b/s\n@@ -0,0 +1 @@\n+Subproject commit " + b + "\n"))
	if old, new, _, ok := added.Submodule(0); !ok || old != nil || new == nil {
		t.Fatalf("added %x %x %v", old, new, ok)
	}
	text, _ := Parse([]byte("diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1 +1 @@\n-Subproject commit " + a + "\n+x\n"))
	if _, _, _, ok := text.Submodule(0); ok {
		t.Fatal("a file that only mentions a commit")
	}
}
