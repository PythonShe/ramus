package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	vol := ""
	if runtime.GOOS == "windows" {
		vol = "C:"
	}
	// at is the path p, absolute, as the platform writes it.
	at := func(p string) string { return filepath.FromSlash(vol + p) }
	cwd := at("/work/repo")
	for _, c := range []struct {
		args []string
		want Request
	}{
		{nil, Request{}},
		{[]string{"."}, Request{Kind: Open, Paths: []string{at("/work/repo")}}},
		{[]string{"../other"}, Request{Kind: Open, Paths: []string{at("/work/other")}}},
		{[]string{"diff", "a.txt", "/tmp/b.txt"}, Request{Kind: Diff, Paths: []string{at("/work/repo/a.txt"), at("/tmp/b.txt")}}},
		{[]string{"diff", "--wait", "--", "-a", "b"}, Request{Kind: Diff, Wait: true, Paths: []string{at("/work/repo/-a"), at("/work/repo/b")}}},
		{[]string{"log", "src/x.go"}, Request{Kind: Log, Paths: []string{at("/work/repo/src/x.go")}}},
		{[]string{"blame", "x.go"}, Request{Kind: Blame, Paths: []string{at("/work/repo/x.go")}}},
		{[]string{"merge", "--wait", "B", "L", "R", "M"}, Request{Kind: Merge, Wait: true, Paths: []string{at("/work/repo/B"), at("/work/repo/L"), at("/work/repo/R"), at("/work/repo/M")}}},
		{[]string{"merge", "L", "R", "M"}, Request{Kind: Merge, Paths: []string{"", at("/work/repo/L"), at("/work/repo/R"), at("/work/repo/M")}}},
		// git passes an empty $BASE when there is no common ancestor.
		{[]string{"merge", "", "L", "R", "M"}, Request{Kind: Merge, Paths: []string{"", at("/work/repo/L"), at("/work/repo/R"), at("/work/repo/M")}}},
	} {
		got, err := Parse(c.args, cwd)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("Parse(%q) = %+v, %v; want %+v", c.args, got, err, c.want)
			continue
		}
		back, err := Parse(got.Args(), "/elsewhere")
		if err != nil || !reflect.DeepEqual(back, got) {
			t.Errorf("round trip of %q: %+v, %v", c.args, back, err)
		}
	}
	for _, bad := range [][]string{{"diff", "a"}, {"log"}, {"merge", "a", "b"}, {"diff", "--nope", "a", "b"}, {"log", "--wait", "x"}, {"a", "b"}, {"--help"}} {
		if _, err := Parse(bad, cwd); !errors.Is(err, ErrUsage) {
			t.Errorf("Parse(%q) accepted: %v", bad, err)
		}
	}
}

func TestToolCommands(t *testing.T) {
	d, m := ToolCommands("/Applications/Ramus.app/Contents/MacOS/ramus")
	if d != `/Applications/Ramus.app/Contents/MacOS/ramus diff --wait "$LOCAL" "$REMOTE"` || !strings.HasSuffix(m, `merge --wait "$BASE" "$LOCAL" "$REMOTE" "$MERGED"`) {
		t.Fatal(d, m)
	}
	if d, _ := ToolCommands("/opt/My Apps/ramus"); !strings.HasPrefix(d, `'/opt/My Apps/ramus' diff`) {
		t.Fatal(d)
	}
}

// A path with any character a shell gives a meaning to is quoted, and the
// shell git runs the command in then sees the path as it is: a
// downloaded folder named a;b runs nothing.
func TestQuoteShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no sh")
	}
	for _, p := range []string{"/usr/local/bin/ramus", "/a/b-c_d.e+f,g:h@i=j/ramus", "C:/Ramus/ramus.exe"} {
		if q := quote(p); q != p {
			t.Errorf("quote(%q) = %q, want it bare", p, q)
		}
	}
	marks := []string{";", "&", "|", "(", ")", "<", ">", "*", "?", "#", "$", "`", "!", "~", "[", "]", "{", "}",
		"'", `"`, `\`, " ", "\t", "\n", "%", "^", "$(touch x)", "a=b"}
	dir := t.TempDir()
	for _, m := range marks {
		p := "/opt/r" + m + "x/ramus"
		if m == "a=b" {
			p = m
		}
		q := quote(p)
		if q == p {
			t.Errorf("quote(%q) left it bare", p)
			continue
		}
		cmd := exec.Command("sh", "-c", "printf %s "+q)
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil || string(out) != p {
			t.Errorf("sh read quote(%q) = %s as %q, %v", p, q, out, err)
		}
	}
	if q := quote(""); q != "''" {
		t.Errorf("quote of nothing %q", q)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("the shell ran something: %v", entries)
	}
}

// An app running from an AppImage is started by the AppImage, not by its
// executable in the mount that goes when it quits; any other by its own.
func TestProgram(t *testing.T) {
	env := func(image, dir string) func(string) string {
		return func(k string) string {
			return map[string]string{"APPIMAGE": image, "APPDIR": dir}[k]
		}
	}
	for _, tc := range []struct {
		exe, image, dir, want string
	}{
		{"/tmp/.mount_ab/usr/bin/ramus", "/home/me/ramus.AppImage", "/tmp/.mount_ab", "/home/me/ramus.AppImage"},
		{"/opt/ramus/ramus", "", "", "/opt/ramus/ramus"},
		// Started by an AppImage, from elsewhere.
		{"/opt/ramus/ramus", "/home/me/other.AppImage", "/tmp/.mount_cd", "/opt/ramus/ramus"},
		{"/tmp/.mount_ab2/ramus", "/home/me/ramus.AppImage", "/tmp/.mount_ab", "/tmp/.mount_ab2/ramus"},
		{"/tmp/.mount_ab/ramus", "ramus.AppImage", "/tmp/.mount_ab", "/tmp/.mount_ab/ramus"},
	} {
		if runtime.GOOS == "windows" {
			t.Skip("AppImages are Linux's")
		}
		if got := Program(tc.exe, env(tc.image, tc.dir)); got != tc.want {
			t.Errorf("Program(%s, %s, %s) = %s, want %s", tc.exe, tc.image, tc.dir, got, tc.want)
		}
	}
}
