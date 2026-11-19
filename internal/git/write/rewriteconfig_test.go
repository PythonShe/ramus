package write

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"ramus/internal/git/gitexe"
)

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
	rewrites := []rewrite{
		{"reword", func(h *harness, ids []string, v gitexe.Version) (Op, error) {
			return Reword(h.w, rw(h, ids, 1, v), typed, h.seen())
		}, []int{1}, nil, nil},
		{"drop", func(h *harness, ids []string, v gitexe.Version) (Op, error) {
			return DropCommit(h.w, rw(h, ids, 1, v), h.seen())
		}, nil, []int{1}, nil},
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
