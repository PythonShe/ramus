package pathsafe

import (
	"slices"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
)

// git's quoted listing, unquoted, is its -z listing.
func TestUnquote(t *testing.T) {
	r := gittest.Init(t)
	for _, n := range []string{"plain.txt", "tab\there", "new\nline", `quote"d`, `back\slash`, "日本語.txt", "sp ace"} {
		r.Write(n, "x")
	}
	r.Git("add", ".")
	quoted := strings.Split(strings.TrimSuffix(r.Git("-c", "core.quotePath=true", "ls-files"), "\n"), "\n")
	var got []string
	for _, q := range quoted {
		got = append(got, Unquote(q))
	}
	want := strings.Split(strings.TrimSuffix(r.Git("ls-files", "-z"), "\x00"), "\x00")
	if !slices.Equal(got, want) {
		t.Fatalf("unquoted %q\nwant %q", got, want)
	}
	if Unquote(`"bad\q"`) != `"bad\q"` || Unquote(`"`) != `"` {
		t.Fatal("a quoted path that does not parse changed")
	}
}
