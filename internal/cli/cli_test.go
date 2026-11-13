package cli

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	cwd := "/work/repo"
	for _, c := range []struct {
		args []string
		want Request
	}{
		{nil, Request{}},
		{[]string{"."}, Request{Kind: Open, Paths: []string{"/work/repo"}}},
		{[]string{"../other"}, Request{Kind: Open, Paths: []string{"/work/other"}}},
		{[]string{"diff", "a.txt", "/tmp/b.txt"}, Request{Kind: Diff, Paths: []string{"/work/repo/a.txt", "/tmp/b.txt"}}},
		{[]string{"diff", "--wait", "--", "-a", "b"}, Request{Kind: Diff, Wait: true, Paths: []string{"/work/repo/-a", "/work/repo/b"}}},
		{[]string{"log", "src/x.go"}, Request{Kind: Log, Paths: []string{"/work/repo/src/x.go"}}},
		{[]string{"blame", "x.go"}, Request{Kind: Blame, Paths: []string{"/work/repo/x.go"}}},
		{[]string{"merge", "--wait", "B", "L", "R", "M"}, Request{Kind: Merge, Wait: true, Paths: []string{"/work/repo/B", "/work/repo/L", "/work/repo/R", "/work/repo/M"}}},
		{[]string{"merge", "L", "R", "M"}, Request{Kind: Merge, Paths: []string{"", "/work/repo/L", "/work/repo/R", "/work/repo/M"}}},
		// git passes an empty $BASE when there is no common ancestor.
		{[]string{"merge", "", "L", "R", "M"}, Request{Kind: Merge, Paths: []string{"", "/work/repo/L", "/work/repo/R", "/work/repo/M"}}},
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

func TestToolConfig(t *testing.T) {
	c := ToolConfig("/Applications/Ramus.app/Contents/MacOS/ramus")
	if !strings.Contains(c, `cmd = /Applications/Ramus.app/Contents/MacOS/ramus diff --wait "$LOCAL" "$REMOTE"`) || !strings.Contains(c, "trustExitCode = true") {
		t.Fatal(c)
	}
	if c := ToolConfig("/opt/My Apps/ramus"); !strings.Contains(c, `cmd = '/opt/My Apps/ramus' merge`) {
		t.Fatal(c)
	}
}
