package write

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/gittest"
)

// hookRepo is a history of six commits after base, the same commits in
// every repository it makes (the clock is the repository's), and a
// change staged.
func hookRepo(t *testing.T, r *gittest.Repo) []string {
	r.Write("f0", "0\n")
	r.Commit("base")
	var ids []string
	for _, s := range []string{"one", "two", "three", "four", "five", "six"} {
		r.Write("f"+s, s+"\n")
		ids = append(ids, r.Commit(s))
	}
	r.Write("ftwo", "two fixed\n")
	r.Git("add", "ftwo")
	return ids
}

// countingHooks installs, in a folder core.hooksPath names, the hooks a
// commit runs, each noting its runs in a log; prepare-commit-msg also
// puts a prefix on the message. It returns the log's path.
func countingHooks(t *testing.T, r *gittest.Repo) string {
	dir := filepath.Join(t.TempDir(), "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "runs")
	for _, name := range []string{"prepare-commit-msg", "commit-msg", "pre-commit", "post-commit"} {
		body := "#!/bin/sh\necho " + name + " >>'" + filepath.ToSlash(log) + "'\n"
		if name == "prepare-commit-msg" {
			body += "printf 'PREP: %s\\n' \"$(cat \"$1\")\" >\"$1.tmp\" && mv \"$1.tmp\" \"$1\"\n"
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r.Git("config", "core.hooksPath", filepath.ToSlash(dir))
	return log
}

// hookRuns counts the runs of each hook in log.
func hookRuns(t *testing.T, log string) map[string]int {
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	n := map[string]int{}
	for _, name := range strings.Fields(string(b)) {
		n[name]++
	}
	return n
}

// A rewrite runs prepare-commit-msg, and the other hooks of a commit, as
// often as git rebase -i does for the same commits in a terminal, and no
// more: the reword, fix up,
// squash and edit of history of the app, on the path through git history
// (which a hook routes to the rebase) and on the rebase, against git's own
// rebase of a twin repository with the todo a user would write.
func TestRewriteRunsTheMessageHookAsGitDoes(t *testing.T) {
	type rewrite struct {
		name  string
		ramus func(h *harness, ids []string, v gitexe.Version) (Op, error)
		// todo is git's own todo for the same rewrite, the commits named
		// by index and S the commit of what is staged; messages are what
		// git's editor is given, one per time it opens.
		todo     string
		messages []string
	}
	rwOf := func(h *harness, ids []string, v gitexe.Version) Rewrite {
		return Rewrite{Commit: ids[1], Parent: ids[0], Git: v, Branches: []string{"refs/heads/main"}}
	}
	rewrites := []rewrite{
		{"reword", func(h *harness, ids []string, v gitexe.Version) (Op, error) {
			return Reword(h.w, rwOf(h, ids, v), "two reworded\n", h.seen())
		}, "reword 1\npick 2\npick 3\npick 4\npick 5\n", []string{"two reworded\n"}},
		{"fix up", func(h *harness, ids []string, v gitexe.Version) (Op, error) {
			return FixupInto(h.w, rwOf(h, ids, v), "", h.seen())
		}, "pick 1\nfixup S\npick 2\npick 3\npick 4\npick 5\n", nil},
		{"squash", func(h *harness, ids []string, v gitexe.Version) (Op, error) {
			return FixupInto(h.w, rwOf(h, ids, v), "two squashed\n", h.seen())
		}, "pick 1\nsquash S\npick 2\npick 3\npick 4\npick 5\n", []string{"two squashed\n"}},
		{"edit history, first commit reworded", func(h *harness, ids []string, v gitexe.Version) (Op, error) {
			return EditHistory(h.w, rwOf(h, ids, v), []Step{
				{Action: DoReword, Commit: ids[1], Message: "two reworded\n"},
				{Action: DoPick, Commit: ids[2]},
				{Action: DoPick, Commit: ids[3]},
				{Action: DoPick, Commit: ids[4]},
				{Action: DoPick, Commit: ids[5]},
			}, h.seen())
		}, "reword 1\npick 2\npick 3\npick 4\npick 5\n", []string{"two reworded\n"}},
		{"edit history", func(h *harness, ids []string, v gitexe.Version) (Op, error) {
			// Moved, reworded, squashed with a message, fixed up, squashed
			// with the messages joined.
			return EditHistory(h.w, rwOf(h, ids, v), []Step{
				{Action: DoPick, Commit: ids[2]},
				{Action: DoReword, Commit: ids[1], Message: "two reworded\n"},
				{Action: DoSquash, Commit: ids[3], Message: "four squashed\n"},
				{Action: DoFixup, Commit: ids[4]},
				{Action: DoSquash, Commit: ids[5]},
			}, h.seen())
		}, "pick 2\nreword 1\nsquash 3\nexec true\nfixup 4\nsquash 5\n", []string{"two reworded\n", "four squashed\n", ""}},
	}
	for _, rr := range rewrites {
		t.Run(rr.name, func(t *testing.T) {
			// Each case has repositories of its own.
			t.Parallel()
			// git's own count, on a twin of the repository.
			twin := gittest.Init(t)
			tids := hookRepo(t, twin)
			if strings.Contains(rr.todo, " S\n") {
				tree := strings.TrimSpace(twin.Git("write-tree"))
				s := strings.TrimSpace(twin.Git("commit-tree", "-p", "HEAD", "-m", "staged", tree))
				twin.Git("reset", "-q", "--hard")
				rr.todo = strings.ReplaceAll(rr.todo, " S\n", " "+s+"\n")
			} else {
				twin.Git("reset", "-q", "--hard")
			}
			todo := rr.todo
			for i := len(tids) - 1; i >= 0; i-- {
				todo = strings.ReplaceAll(todo, " "+string(rune('0'+i))+"\n", " "+tids[i]+"\n")
			}
			scripts := t.TempDir()
			todoFile := filepath.Join(scripts, "todo")
			if err := os.WriteFile(todoFile, []byte(todo), 0o644); err != nil {
				t.Fatal(err)
			}
			// The editor gives each time it opens the next message, or
			// leaves git's when there is none.
			for i, m := range rr.messages {
				if err := os.WriteFile(filepath.Join(scripts, string(rune('a'+i))), []byte(m), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			editor := filepath.Join(scripts, "editor")
			if err := os.WriteFile(editor, []byte("#!/bin/sh\ncd '"+filepath.ToSlash(scripts)+"' || exit 1\n"+
				"for m in a b c d; do if test -f $m; then if test -s $m; then cat $m >\"$1\"; fi; rm -f $m; exit 0; fi; done\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			tlog := countingHooks(t, twin)
			twin.Env = append(twin.Env, "GIT_SEQUENCE_EDITOR=cp '"+filepath.ToSlash(todoFile)+"'", "GIT_EDITOR='"+filepath.ToSlash(editor)+"'")
			twin.Git("-c", "commit.cleanup=verbatim", "rebase", "-q", "-i", "--no-autosquash", tids[0])
			want := hookRuns(t, tlog)

			for _, path := range []string{"history", "rebase"} {
				v := localGit(t)
				if path == "rebase" {
					v = oldGit
				}
				h := newHarness(t)
				ids := hookRepo(t, h.r)
				log := countingHooks(t, h.r)
				op, err := rr.ramus(h, ids, v)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := wait(t, h.q.Submit(op)); err != nil {
					t.Fatalf("%s: %v", path, err)
				}
				if got := hookRuns(t, log); fmt.Sprint(got) != fmt.Sprint(want) {
					t.Errorf("%s: the hooks ran %v times, git rebase runs them %v times", path, got, want)
				}
				if out := h.r.Git("log", "--format=%B", "main"); strings.Contains(out, "PREP: PREP:") {
					t.Errorf("%s: a message took the hook's change twice:\n%s", path, out)
				}
			}
		})
	}
}
