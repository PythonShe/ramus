package message

import (
	"context"
	"slices"
	"testing"

	"ramus/internal/git/gittest"
	"ramus/internal/git/gittest/testrun"
)

func TestTemplateAndRecent(t *testing.T) {
	r := gittest.Init(t)
	rn := testrun.Runner(t)
	w := Where{GitDir: r.GitDir(), WorkTree: r.Dir}
	ctx := context.Background()
	if tpl, err := Template(ctx, rn, w); err != nil || tpl != "" {
		t.Fatalf("no template: %q %v", tpl, err)
	}
	if msgs, err := Recent(ctx, rn, w, 10); err != nil || len(msgs) != 0 {
		t.Fatalf("unborn: %q %v", msgs, err)
	}
	r.Write(".tpl", "Subject\n\n# Why is this needed?\n#\n")
	r.Git("config", "commit.template", ".tpl")
	tpl, err := Template(ctx, rn, w)
	if err != nil || tpl != "Subject\n\n# Why is this needed?\n#\n" {
		t.Fatalf("template %q %v", tpl, err)
	}
	msg := StripTemplate("Fix it\n\n# Why is this needed?\n#123 is the issue\n#\n", tpl, CommentChar(ctx, rn, w))
	if msg != "Fix it\n\n#123 is the issue\n" {
		t.Fatalf("stripped %q", msg)
	}
	r.Commit("one")
	r.Commit("two\n\nbody")
	r.Commit("one")
	msgs, err := Recent(ctx, rn, w, 10)
	if err != nil || !slices.Equal(msgs, []string{"one", "two\n\nbody"}) {
		t.Fatalf("recent %q %v", msgs, err)
	}
	if last, err := Last(ctx, rn, w); err != nil || last != "one" {
		t.Fatalf("last %q %v", last, err)
	}
}

func TestJoinSplitCheck(t *testing.T) {
	if m := Join(" Subject ", "\n\nbody\n"); m != "Subject\n\nbody" {
		t.Fatalf("join %q", m)
	}
	if m := Join("Subject", "  \n"); m != "Subject" {
		t.Fatalf("join %q", m)
	}
	if s, b := Split("Subject\n\nbody\nmore"); s != "Subject" || b != "body\nmore" {
		t.Fatalf("split %q %q", s, b)
	}
	if Check(" \n") == nil || Check("a\x00b") == nil || Check("ok") != nil {
		t.Fatal("check")
	}
	if CheckPerson("Ada Lovelace <ada@example.com>") != nil || CheckPerson("ada@example.com") == nil || CheckPerson("A <b> <c>") == nil {
		t.Fatal("check person")
	}
}

// The comment string is the one git uses: core.commentChar and
// core.commentString are one setting under two names, the last set wins,
// and it may be longer than a character. git stripspace is the oracle.
func TestCommentCharIsGits(t *testing.T) {
	rn := testrun.Runner(t)
	ctx := context.Background()
	for _, c := range []struct {
		config [][2]string
		want   string
	}{
		{nil, "#"},
		{[][2]string{{"core.commentChar", ";"}}, ";"},
		{[][2]string{{"core.commentString", "//"}}, "//"},
		{[][2]string{{"core.commentChar", ";"}, {"core.commentString", "//"}}, "//"},
		{[][2]string{{"core.commentString", "//"}, {"core.commentChar", ";"}}, ";"},
		{[][2]string{{"core.commentChar", "auto"}}, "#"},
	} {
		r := gittest.Init(t)
		w := Where{GitDir: r.GitDir(), WorkTree: r.Dir}
		for _, kv := range c.config {
			r.Git("config", "--add", kv[0], kv[1])
		}
		got := CommentChar(ctx, rn, w)
		if got != c.want {
			t.Fatalf("%v: comment %q, want %q", c.config, got, c.want)
		}
		if got == "#" && len(c.config) > 0 {
			continue // auto: git picks a character per message
		}
		out, err := r.Run([]byte(got+" a comment\nkept\n"), "stripspace", "--strip-comments")
		if err != nil || out != "kept\n" {
			t.Fatalf("%v: git strips %q to %q %v", c.config, got+" a comment", out, err)
		}
	}
}
