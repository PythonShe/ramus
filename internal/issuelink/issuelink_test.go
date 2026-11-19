package issuelink

import (
	"reflect"
	"strings"
	"testing"
)

// A long text, as a commit message shown frame after frame, is searched
// once: its references are kept, within a budget, for each text.
func TestLongTextsSearchedOnce(t *testing.T) {
	s := Compile(FromRemote("git@github.com:o/r.git"))
	text := strings.Repeat("lorem ipsum dolor sit amet, ", 1<<20/28) + "fixes #12"
	first := s.Find(text)
	again := s.Find(strings.Clone(text))
	if len(first) != 1 || len(again) != 1 || &first[0] != &again[0] {
		t.Fatalf("searched again: %v, %v", first, again)
	}
	// Another text of the same length has references of its own.
	other := strings.Replace(text, "#12", "#34", 1)
	if got := s.Find(other); len(got) != 1 || got[0].URL != "https://github.com/o/r/issues/34" {
		t.Fatalf("another text %v", got)
	}
	many := strings.Repeat("#1 ", maxMatches)
	for i := range 200 {
		s.Find(many + strings.Repeat(" ", remembered+i))
		if s.foundBytes > foundBudget {
			t.Fatalf("%d bytes kept", s.foundBytes)
		}
	}
}

func links(s *Set, text string) []string {
	var out []string
	for _, m := range s.Find(text) {
		out = append(out, text[m.Start:m.End]+" "+m.URL)
	}
	return out
}

func TestFromRemote(t *testing.T) {
	for _, remote := range []string{
		"git@github.com:octo/proj.git",
		"https://github.com/octo/proj",
		"https://user:secret@github.com/octo/proj.git",
		"ssh://git@github.com:22/octo/proj.git",
	} {
		s := Compile(FromRemote(remote))
		got := links(s, "Fix #12, see other/lib#3 and GH-7; not a#5 or #x.")
		want := []string{
			"#12 https://github.com/octo/proj/issues/12",
			"other/lib#3 https://github.com/other/lib/issues/3",
			"GH-7 https://github.com/octo/proj/issues/7",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %q", remote, got)
		}
	}
	gl := Compile(FromRemote("git@gitlab.com:group/sub/proj.git"))
	if got := links(gl, "#4 and !9"); !reflect.DeepEqual(got, []string{
		"#4 https://gitlab.com/group/sub/proj/-/issues/4",
		"!9 https://gitlab.com/group/sub/proj/-/merge_requests/9",
	}) {
		t.Errorf("gitlab %q", got)
	}
	for _, remote := range []string{"", "/srv/git/proj.git", "file:///srv/proj", "git@example.com:a/b.git", "https://github.com/a/b/c", "https://github.com/a/b%3Fx"} {
		if r := FromRemote(remote); r != nil {
			t.Errorf("%q gave %v", remote, r)
		}
	}
}

func TestRules(t *testing.T) {
	s := Compile([]Rule{
		{`\b([A-Z]+-\d+)\b`, "https://jira.example.com/browse/$1"},
		{`(?i)ticket (\S+)`, "https://t.example.com/$1"},
		{`x*`, "https://matches.empty/"},
		{`(\d+)`, "javascript:alert($1)"},
		{`[`, "https://bad.pattern/"},
	})
	if s.Len() != 2 {
		t.Fatalf("%d rules kept", s.Len())
	}
	got := links(s, "PROJ-42 and ticket a/../b?c")
	want := []string{
		"PROJ-42 https://jira.example.com/browse/PROJ-42",
		"ticket a/../b?c https://t.example.com/a%2F..%2Fb%3Fc",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%q", got)
	}
	for _, r := range []Rule{{"", "https://x/"}, {"a", "ftp://x/"}, {"a", ""}} {
		if Check(r) == nil {
			t.Errorf("%+v passed", r)
		}
	}
}
