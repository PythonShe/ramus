package write

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/gittest"
	"ramus/internal/git/lifetime"
	"ramus/internal/git/refs"
	"ramus/internal/git/run"
)

// newHarnessEnv is newHarness with env added to what every command the
// queue runs gets, as the user's environment reaches git in the app.
func newHarnessEnv(t *testing.T, env ...string) *harness {
	t.Helper()
	g, err := gitexe.Find(context.Background(), os.Getenv("PATH"))
	if err != nil {
		t.Skip(err)
	}
	r := gittest.Init(t)
	life := lifetime.New(context.Background())
	t.Cleanup(func() { life.Close(); <-life.Done() })
	q, err := NewQueue(life, run.New(g, append(gittest.Env(), env...), 8), r.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	q.Refs = refs.New(r.GitDir(), r.GitDir(), 20)
	return &harness{t: t, r: r, q: q, w: Where{GitDir: r.GitDir(), WorkTree: r.Dir}}
}

// commitRaw commits what is staged with exactly msg as its message.
func (h *harness) commitRaw(msg string) string {
	h.t.Helper()
	date := fmt.Sprintf("GIT_AUTHOR_DATE=@%d +0000", h.r.Tick())
	old := h.r.Env
	h.r.Env = append(append([]string{}, old...), date, "GIT_COMMITTER_DATE"+strings.TrimPrefix(date, "GIT_AUTHOR_DATE"))
	defer func() { h.r.Env = old }()
	if _, err := h.r.Run([]byte(msg), "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "--cleanup=verbatim", "-F", "-"); err != nil {
		h.t.Fatal(err)
	}
	return h.rev("HEAD")
}

// rawMessage is commit id's message as stored.
func (h *harness) rawMessage(id string) string {
	_, msg, _ := strings.Cut(h.r.Git("cat-file", "commit", id), "\n\n")
	return msg
}

// recorder is a script that notes in a file that it ran, and fails: an
// editor no rewrite may open.
func recorder(t *testing.T) (script, log string) {
	dir := filepath.Join(t.TempDir(), "dir with space")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	log = filepath.Join(dir, "ran")
	script = filepath.Join(dir, "editor")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$0 $*\" >>'"+log+"'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, log
}

func ran(log string) string {
	b, _ := os.ReadFile(log)
	return string(b)
}

// git's own whitespace cleanup, which the commit box asks for, is what
// cleanMessage does.
func TestCleanMessageIsGitWhitespaceCleanup(t *testing.T) {
	h := newHarness(t)
	for _, msg := range []string{
		"", "\n", "a", "a\n", "a\n\n\n", "\n\na\n", "a  \t\nb\r\n\r\nc\r\n", "a\v\nb\f\n", "  lead\n",
		"# hash\n\n#123 ref\n", "subject\n\n# ------------------------ >8 ------------------------\nafter\n",
		"a\n \n\t\n\nb", "x\r", "\r\n\r\n", "é\n\n\n\nü  \n",
	} {
		want, err := h.r.Run([]byte(msg), "stripspace")
		if err != nil {
			t.Fatal(err)
		}
		if got := cleanMessage(msg); got != want {
			t.Errorf("%q: %q, git says %q", msg, got, want)
		}
	}
}

// Fix up on a git without git history fixup folds what is staged into the
// commit chosen and nothing else: the user's own fixup!, squash! and
// amend! commits stay as they are, a target whose subject starts with
// fixup! gets the change, and no editor is opened, whatever
// rebase.autoSquash, core.editor, sequence.editor and GIT_EDITOR say.
func TestFixupFallbackFoldsOnlyTheChange(t *testing.T) {
	for _, auto := range []string{"true", "false"} {
		t.Run("autoSquash="+auto, func(t *testing.T) {
			editor, log := recorder(t)
			h := newHarnessEnv(t, "GIT_EDITOR="+editor, "GIT_SEQUENCE_EDITOR="+editor, "EDITOR="+editor, "VISUAL="+editor)
			h.r.Git("config", "rebase.autoSquash", auto)
			h.r.Git("config", "core.editor", "'"+editor+"'")
			h.r.Git("config", "sequence.editor", "'"+editor+"'")
			h.r.Write("f0", "0\n")
			base := h.r.Commit("base")
			h.r.Write("f1", "1\n")
			target := h.r.Commit("fixup! one")
			h.r.Write("f2", "2\n")
			h.r.Commit("two")
			h.r.Write("f3", "3\n")
			h.r.Commit("fixup! two")
			h.r.Write("f4", "4\n")
			h.r.Commit("squash! fixup! one")
			h.r.Write("f5", "5\n")
			h.r.Commit("amend! two\n\ntwo amended")
			before := strings.TrimSpace(h.r.Git("log", "--format=%s", "main"))
			h.r.Write("f1", "1\nfixed\n")
			h.r.Git("add", "f1")
			h.r.Write("f5", "5 not staged\n")
			op, err := FixupInto(h.w, Rewrite{Commit: target, Parent: base, Git: oldGit}, "", h.seen())
			h.do(op, err, true)
			if got := strings.TrimSpace(h.r.Git("log", "--format=%s", "main")); got != before {
				t.Fatalf("log:\n%s\nwant\n%s", got, before)
			}
			if got := h.r.Git("show", "main~4:f1"); got != "1\nfixed\n" {
				t.Fatalf("target's f1 %q", got)
			}
			for i := 0; i < 4; i++ {
				if got := h.r.Git("diff-tree", "-r", "--name-only", "--no-commit-id", fmt.Sprintf("main~%d", i)); strings.Contains(got, "f1") {
					t.Fatalf("main~%d changed f1 too", i)
				}
			}
			if h.status() != "M f5" {
				t.Fatalf("status %q", h.status())
			}
			if s := ran(log); s != "" {
				t.Fatalf("an editor ran:\n%s", s)
			}
		})
	}
}

// Undo of a fix up or a squash takes back the rewrite, not the edit: what
// was staged is staged again, a file staged in part included, and the
// files are as they were; redo folds it in again.
func TestFixupUndoStagesTheChangeAgain(t *testing.T) {
	for _, v := range []string{"history", "rebase"} {
		for _, msg := range []string{"", "one, squashed\n"} {
			t.Run(fmt.Sprintf("%s/%q", v, msg), func(t *testing.T) {
				h := newHarness(t)
				gv := localGit(t)
				if v == "rebase" {
					gv = oldGit
				} else if !gv.Has(gitexe.HistoryDrop) {
					t.Skip("git history fixup without --empty")
				}
				h.r.Write("f0", "0\n")
				base := h.r.Commit("base")
				h.r.Write("f1", "a\nb\nc\nd\ne\nf\ng\n")
				one := h.r.Commit("one")
				h.r.Write("f2", "2\n")
				h.r.Commit("two")
				h.r.Write("f1", "A\nb\nc\nd\ne\nf\ng\n")
				h.r.Git("add", "f1")
				h.r.Write("f1", "A\nb\nc\nd\ne\nf\nG\n") // not staged
				h.r.Write("new", "new\n")
				h.r.Git("add", "new")
				index, files := h.r.Git("ls-files", "--stage"), h.read("f1")
				head := h.rev("main")
				op, err := FixupInto(h.w, Rewrite{Commit: one, Parent: base, Git: gv}, msg, h.seen())
				h.do(op, err, true)
				if h.r.Git("show", "main~1:f1") != "A\nb\nc\nd\ne\nf\ng\n" || h.r.Git("show", "main~1:new") != "new\n" {
					t.Fatal("the staged change is not in the commit")
				}
				h.undo()
				if h.rev("main") != head {
					t.Fatal("main not put back")
				}
				if got := h.r.Git("ls-files", "--stage"); got != index {
					t.Fatalf("index after undo:\n%s\nwant\n%s", got, index)
				}
				if h.read("f1") != files || h.read("new") != "new\n" {
					t.Fatalf("f1 after undo %q", h.read("f1"))
				}
				if got := h.r.Git("diff", "--cached", "--name-only"); got != "f1\nnew\n" {
					t.Fatalf("staged after undo %q", got)
				}
				if _, err := wait(t, h.q.Redo()); err != nil {
					t.Fatal(err)
				}
				if h.r.Git("show", "main~1:f1") != "A\nb\nc\nd\ne\nf\ng\n" || h.status() != "M f1" {
					t.Fatalf("redo: status %q", h.status())
				}
			})
		}
	}
}

// Fix up keeps an unstaged edit on the staged line, or on the line next to
// it, in the file and out of the stash list, on the rebase as with git
// history: what is staged is folded, the edit stays unstaged; undo stages
// the change again under the edit, as it was.
func TestFixupKeepsAnUnstagedEditOnTheStagedLine(t *testing.T) {
	for _, v := range []string{"history", "rebase"} {
		for _, shape := range []string{"same line", "next line", "far line"} {
			t.Run(v+"/"+shape, func(t *testing.T) {
				h := newHarness(t)
				gv := localGit(t)
				if v == "rebase" {
					gv = oldGit
				} else if !gv.Has(gitexe.HistoryDrop) {
					t.Skip("git history fixup without --empty")
				}
				h.r.Write("f0", "0\n")
				base := h.r.Commit("base")
				h.r.Write("f1", "a\nb\nc\nd\ne\nf\ng\n")
				one := h.r.Commit("one")
				h.r.Write("f2", "2\n")
				h.r.Commit("two")
				h.r.Write("f1", "A\nb\nc\nd\ne\nf\ng\n")
				h.r.Git("add", "f1")
				working := map[string]string{
					"same line": "A and more\nb\nc\nd\ne\nf\ng\n",
					"next line": "A\nB\nc\nd\ne\nf\ng\n",
					"far line":  "A\nb\nc\nd\ne\nf\nG\n",
				}[shape]
				h.r.Write("f1", working)
				index, head := h.r.Git("ls-files", "--stage"), h.rev("main")
				op, err := FixupInto(h.w, Rewrite{Commit: one, Parent: base, Git: gv}, "", h.seen())
				h.do(op, err, true)
				if got := h.r.Git("show", "main~1:f1"); got != "A\nb\nc\nd\ne\nf\ng\n" {
					t.Fatalf("f1 in the commit %q", got)
				}
				if got := h.read("f1"); got != working {
					t.Fatalf("f1 on disk %q, want %q", got, working)
				}
				if got := h.r.Git("diff", "--cached", "--name-only"); got != "" {
					t.Fatalf("staged after the fix up %q", got)
				}
				if got := h.r.Git("stash", "list"); got != "" {
					t.Fatalf("stash list %q", got)
				}
				h.undo()
				if h.rev("main") != head {
					t.Fatal("main not put back")
				}
				if got := h.r.Git("ls-files", "--stage"); got != index {
					t.Fatalf("index after undo:\n%s\nwant\n%s", got, index)
				}
				if got := h.read("f1"); got != working {
					t.Fatalf("f1 after undo %q", got)
				}
				if got := h.r.Git("stash", "list"); got != "" {
					t.Fatalf("stash list after undo %q", got)
				}
			})
		}
	}
}

// Every way of rewriting keeps the messages of the commits it does not
// change byte for byte, whatever commit.cleanup says, and stores a message
// it sets as the commit box stores one.
func TestRewriteKeepsMessagesByteForByte(t *testing.T) {
	raw := []string{
		"one\n\n#123 an issue\n# a heading\n",
		"\nleading blank line\n",
		"trailing blank lines\n\n\n",
		"crlf\r\n\r\nbody\r\n",
		"scissors\n\n# ------------------------ >8 ------------------------\nbelow\n",
		"no final newline  ",
	}
	const typed = "  reworded  \n\n\n#7 kept\n# ------------------------ >8 ------------------------\nkept too\r\n\n"
	type rewrite struct {
		name string
		make func(h *harness, ids []string, v gitexe.Version) (Op, error)
		// touched are the commits whose message the rewrite sets; gone
		// those it takes out; folded those it folds a change into, which
		// keep their message but for the blank lines at its start.
		touched, gone, folded []int
	}
	rw := func(h *harness, ids []string, i int, v gitexe.Version) Rewrite {
		return Rewrite{Commit: ids[i], Parent: h.rev(ids[i] + "~1"), Git: v}
	}
	stage := func(h *harness) {
		h.r.Write("c1", "fixed\n")
		h.r.Git("add", "c1")
	}
	rewrites := []rewrite{
		{"reword", func(h *harness, ids []string, v gitexe.Version) (Op, error) {
			return Reword(h.w, rw(h, ids, 1, v), typed, h.seen())
		}, []int{1}, nil, nil},
		{"drop", func(h *harness, ids []string, v gitexe.Version) (Op, error) {
			return DropCommit(h.w, rw(h, ids, 1, v), h.seen())
		}, nil, []int{1}, nil},
		{"fixup", func(h *harness, ids []string, v gitexe.Version) (Op, error) {
			stage(h)
			return FixupInto(h.w, rw(h, ids, 1, v), "", h.seen())
		}, nil, nil, []int{1}},
		{"squash", func(h *harness, ids []string, v gitexe.Version) (Op, error) {
			stage(h)
			return FixupInto(h.w, rw(h, ids, 1, v), typed, h.seen())
		}, []int{1}, nil, nil},
		{"edit", func(h *harness, ids []string, v gitexe.Version) (Op, error) {
			// Reorder, reword one, fold one keeping the message before it,
			// leave the rest.
			return EditHistory(h.w, rw(h, ids, 0, v), []Step{
				{Action: DoPick, Commit: ids[0]}, {Action: DoReword, Commit: ids[2], Message: typed},
				{Action: DoPick, Commit: ids[1]}, {Action: DoFixup, Commit: ids[3]},
				{Action: DoPick, Commit: ids[4]}, {Action: DoPick, Commit: ids[5]},
			}, h.seen())
		}, []int{2}, []int{3}, []int{1}},
	}
	for _, cleanup := range []string{"default", "strip", "whitespace", "verbatim", "scissors"} {
		for _, path := range []string{"history", "rebase"} {
			for _, x := range rewrites {
				t.Run(cleanup+"/"+path+"/"+x.name, func(t *testing.T) {
					// Each case has a repository of its own.
					t.Parallel()
					v := localGit(t)
					if path == "rebase" {
						v = oldGit
					} else if !v.Has(gitexe.HistoryDrop) {
						t.Skip("no git history drop")
					}
					h := newHarness(t)
					h.r.Write("f0", "0\n")
					h.r.Commit("base")
					var ids []string
					for i, m := range raw {
						h.r.Write(fmt.Sprintf("c%d", i), fmt.Sprint(i, "\n"))
						h.r.Git("add", "-A")
						ids = append(ids, h.commitRaw(m))
					}
					if cleanup != "default" {
						h.r.Git("config", "commit.cleanup", cleanup)
					}
					h.r.Git("config", "core.commentChar", "#")
					op, err := x.make(h, ids, v)
					h.do(op, err, true)
					got := strings.Fields(h.r.Git("rev-list", "--reverse", "main", "^"+ids[0]+"~1"))
					// The messages in the order the rewrite leaves them.
					var want []string
					order := []int{0, 1, 2, 3, 4, 5}
					if x.name == "edit" {
						order = []int{0, 2, 1, 4, 5}
					}
					clean := cleanMessage(typed)
					for _, i := range order {
						if slices.Contains(x.gone, i) {
							continue
						}
						m := raw[i]
						if slices.Contains(x.touched, i) {
							m = clean
						}
						if slices.Contains(x.folded, i) {
							m = strings.TrimLeft(m, "\n")
						}
						want = append(want, m)
					}
					if len(got) != len(want) {
						t.Fatalf("%d commits, want %d", len(got), len(want))
					}
					for i, id := range got {
						if m := h.rawMessage(id); m != want[i] {
							t.Errorf("commit %d: %q, want %q", i, m, want[i])
						}
					}
				})
			}
		}
	}
	// The commit box stores the typed message as the rewrites do.
	h := newHarness(t)
	h.r.Git("config", "commit.cleanup", "verbatim")
	h.r.Stage("a", "1")
	h.do(mustOp(t)(Commit(h.w, CommitOptions{Message: typed})), nil, true)
	if got := h.rawMessage("HEAD"); got != cleanMessage(typed) {
		t.Fatalf("the commit box stores %q, a rewrite %q", got, cleanMessage(typed))
	}
}

// An edit of history whose todo git would read with the user's comment
// character, a letter that starts pick, fixup or exec, or auto, replays
// every step as asked.
func TestEditHistoryWhateverTheCommentChar(t *testing.T) {
	for _, cc := range [][2]string{{"core.commentChar", "p"}, {"core.commentChar", "e"}, {"core.commentChar", "f"},
		{"core.commentChar", "u"}, {"core.commentChar", "auto"}, {"core.commentString", "pick"}} {
		t.Run(cc[0]+"="+cc[1], func(t *testing.T) {
			h := newHarness(t)
			h.r.Write("f0", "0\n")
			base := h.r.Commit("base")
			h.r.Write("f1", "1\n")
			one := h.r.Commit("one")
			h.r.Write("f2", "2\n")
			two := h.r.Commit("two")
			h.r.Write("f3", "3\n")
			three := h.r.Commit("three")
			h.r.Git("config", cc[0], cc[1])
			op, err := EditHistory(h.w, Rewrite{Commit: one, Parent: base, Git: localGit(t), Branches: []string{"refs/heads/main"}}, []Step{
				{Action: DoReword, Commit: one, Message: "ONE"},
				{Action: DoSquash, Commit: two},
				{Action: DoPick, Commit: three},
			}, h.seen())
			h.do(op, err, true)
			if got := h.r.Git("log", "--format=%B|", "main"); got != "three\n|\nONE\n\ntwo\n|\nbase\n|\n" {
				t.Fatalf("log %q", got)
			}
		})
	}
}

// A rewrite whose result is not the commits asked for, here a rebase whose
// todo was changed under it to drop one more, is put back and fails, and
// redo cannot do it again; an edit of history whose steps leave out a
// commit is refused before it starts.
func TestRewriteNotAsAskedIsPutBack(t *testing.T) {
	h := newHarness(t)
	h.r.Write("f0", "0\n")
	base := h.r.Commit("base")
	var ids []string
	for i := 1; i <= 3; i++ {
		h.r.Write(fmt.Sprint("f", i), "x\n")
		ids = append(ids, h.r.Commit(fmt.Sprint("c", i)))
	}
	h.r.Write("f0", "local\n")
	head := h.rev("main")
	op, err := DropCommit(h.w, Rewrite{Commit: ids[0], Parent: base, Git: oldGit}, h.seen())
	if err != nil {
		t.Fatal(err)
	}
	// Drops the commit asked for, and the last one too.
	op.Cmd.Env = setEnv(op.Cmd.Env, "GIT_SEQUENCE_EDITOR", "f() { sed -i.x -e 's/^pick "+ids[0]+"/drop "+ids[0]+"/' -e 's/^pick "+ids[2]+"/drop "+ids[2]+"/' \"$1\"; }; f")
	_, err = wait(t, h.q.Submit(op))
	if !errors.Is(err, ErrRewriteNotAsAsked) {
		t.Fatalf("err %v", err)
	}
	if h.rev("main") != head || h.read("f0") != "local\n" {
		t.Fatalf("main at %s, f0 %q", h.rev("main"), h.read("f0"))
	}
	if undo, redo := h.q.History.Next(); undo != "" || redo != "" {
		t.Fatalf("history: undo %q redo %q", undo, redo)
	}

	op, err = EditHistory(h.w, Rewrite{Commit: ids[0], Parent: base, Git: localGit(t)}, []Step{
		{Action: DoPick, Commit: ids[0]}, {Action: DoPick, Commit: ids[2]},
	}, h.seen())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = wait(t, h.q.Submit(op)); err == nil || h.rev("main") != head {
		t.Fatalf("an edit leaving out %s ran: %v", short(ids[1]), err)
	}
}

// A rebase a rewrite runs that stops on something other than a conflict,
// a hook refusing or a commit that cannot be signed, is stopped: the
// branch and the local changes are as they were, and the user is told.
func TestRewriteStoppedIsPutBack(t *testing.T) {
	for _, what := range []string{"pre-commit", "commit-msg", "gpg"} {
		t.Run(what, func(t *testing.T) {
			h := newHarness(t)
			h.r.Write("f0", "0\n")
			base := h.r.Commit("base")
			h.r.Write("f1", "1\n")
			one := h.r.Commit("one")
			h.r.Write("f2", "2\n")
			h.r.Commit("two")
			h.r.Write("f0", "local\n")
			switch what {
			case "gpg":
				h.r.Git("config", "commit.gpgSign", "true")
				h.r.Git("config", "gpg.format", "ssh")
				h.r.Git("config", "user.signingKey", filepath.ToSlash(filepath.Join(t.TempDir(), "no such key")))
			default:
				hook(t, h.r, what, `echo "`+what+` says no" >&2; exit 1`)
			}
			head := h.rev("main")
			op, err := Reword(h.w, Rewrite{Commit: one, Parent: base, Git: localGit(t), Branches: []string{"refs/heads/main"}}, "one reworded", h.seen())
			if err != nil {
				t.Fatal(err)
			}
			_, err = wait(t, h.q.Submit(op))
			if err == nil {
				t.Fatal("the reword went through")
			}
			var re *run.Error
			if !errors.As(err, &re) {
				t.Fatalf("not git's error: %v", err)
			}
			if h.rev("main") != head || h.read("f0") != "local\n" || h.status() != "M f0" {
				t.Fatalf("main at %s, status %q", h.rev("main"), h.status())
			}
			if _, err := os.Stat(filepath.Join(h.r.GitDir(), "rebase-merge")); err == nil {
				t.Fatal("the rebase was left in progress")
			}
			if s := strings.TrimSpace(h.r.Git("stash", "list")); s != "" {
				t.Fatalf("stash left: %s", s)
			}
		})
	}
}

// Where the user's config asks for signed commits, every commit a rewrite
// makes is signed, through a rebase where git history cannot sign; so are
// the notes of the commits copied where it asks for that, and the hooks a
// rebase runs run.
func TestRewriteDoesWhatTheConfigAsks(t *testing.T) {
	type rewrite struct {
		name string
		make func(h *harness, rw Rewrite) (Op, error)
	}
	rewrites := []rewrite{
		{"reword", func(h *harness, rw Rewrite) (Op, error) { return Reword(h.w, rw, "one reworded", h.seen()) }},
		{"drop", func(h *harness, rw Rewrite) (Op, error) { return DropCommit(h.w, rw, h.seen()) }},
		{"fixup", func(h *harness, rw Rewrite) (Op, error) {
			h.r.Write("f1", "fixed\n")
			h.r.Git("add", "f1")
			return FixupInto(h.w, rw, "", h.seen())
		}},
		{"squash", func(h *harness, rw Rewrite) (Op, error) {
			h.r.Write("f1", "fixed\n")
			h.r.Git("add", "f1")
			return FixupInto(h.w, rw, "one squashed", h.seen())
		}},
	}
	for _, asks := range []string{"sign", "notes", "post-rewrite"} {
		for _, x := range rewrites {
			t.Run(asks+"/"+x.name, func(t *testing.T) {
				h := newHarness(t)
				h.r.Write("f0", "0\n")
				base := h.r.Commit("base")
				h.r.Write("f1", "1\n")
				one := h.r.Commit("one")
				h.r.Write("f2", "2\n")
				h.r.Commit("two")
				h.r.Write("f3", "3\n")
				h.r.Commit("three")
				var allowed string
				switch asks {
				case "sign":
					allowed = gittest.SSHSigning(t, h.r)
					h.r.Git("config", "commit.gpgSign", "true")
				case "notes":
					for _, rev := range []string{"main~2", "main~1", "main"} {
						h.r.Git("notes", "add", "-m", "note of "+rev, rev)
					}
					h.r.Git("config", "notes.rewriteRef", "refs/notes/commits")
				case "post-rewrite":
					hook(t, h.r, "post-rewrite", `cat >>"$(git rev-parse --git-path rewritten)"`)
				}
				head := h.rev("main")
				op, err := x.make(h, Rewrite{Commit: one, Parent: base, Git: localGit(t), Branches: []string{"refs/heads/main"}})
				h.do(op, err, true)
				ids := strings.Fields(h.r.Git("rev-list", "main", "^"+base))
				switch asks {
				case "sign":
					for _, id := range ids {
						if out, err := h.r.Run(nil, "-c", "gpg.ssh.allowedSignersFile="+allowed, "verify-commit", id); err != nil {
							t.Fatalf("%s not signed: %v\n%s", short(id), err, out)
						}
					}
				case "notes":
					for _, id := range ids {
						if out, err := h.r.Run(nil, "notes", "show", id); err != nil || !strings.HasPrefix(out, "note of ") {
							t.Fatalf("%s has no note: %v %q", short(id), err, out)
						}
					}
				case "post-rewrite":
					b, _ := os.ReadFile(filepath.Join(h.r.GitDir(), "rewritten"))
					if !strings.Contains(string(b), head) {
						t.Fatalf("post-rewrite did not hear of %s: %q", short(head), b)
					}
				}
			})
		}
	}
}
