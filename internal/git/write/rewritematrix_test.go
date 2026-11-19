package write

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/gittest"
)

// matrixRepo is a history of six commits after base, whose subjects
// include the fixup!, squash! and amend! forms and whose messages hold
// lines starting with # and ;, with branch side at the fourth, branch
// other off base, a change staged and one not.
type matrixRepo struct {
	h    *harness
	base string
	ids  []string
	msgs []string // as stored
}

var matrixSubjects = []string{"one", "two", "fixup! one", "squash! two", "amend! two\n\ntwo amended", "six\n\n# not a comment\n; nor this"}

func newMatrixRepo(t *testing.T) *matrixRepo {
	h := newHarness(t)
	m := &matrixRepo{h: h}
	h.r.Write("f0", "0\n")
	m.base = h.r.Commit("base")
	for i, s := range matrixSubjects {
		h.r.Write(fmt.Sprintf("f%d", i+1), fmt.Sprint(i+1, "\n"))
		m.ids = append(m.ids, h.r.Commit(s))
		m.msgs = append(m.msgs, h.rawMessage(m.ids[i]))
	}
	h.r.Git("branch", "side", m.ids[3])
	h.r.Git("branch", "other", m.base)
	h.r.Write("f0", "local\n")
	h.r.Write("s", "staged\n")
	h.r.Git("add", "s")
	return m
}

// matrixCommit is a commit as the model has it.
type matrixCommit struct {
	msg   string
	files map[string]string
}

func (m *matrixRepo) history() []matrixCommit {
	var out []matrixCommit
	for _, id := range strings.Fields(m.h.r.Git("rev-list", "--reverse", "main", "^"+m.base)) {
		files := map[string]string{}
		for _, p := range strings.Fields(m.h.r.Git("ls-tree", "-r", "--name-only", id)) {
			files[p] = m.h.r.Git("show", id+":"+p)
		}
		out = append(out, matrixCommit{m.h.rawMessage(id), files})
	}
	return out
}

// model is the history made of steps, each a message and the files it
// adds to those before it.
func model(steps ...matrixCommit) []matrixCommit {
	files := map[string]string{"f0": "0\n"}
	var out []matrixCommit
	for _, s := range steps {
		nf := map[string]string{}
		for k, v := range files {
			nf[k] = v
		}
		for k, v := range s.files {
			nf[k] = v
		}
		files = nf
		out = append(out, matrixCommit{s.msg, nf})
	}
	return out
}

func adds(i int) map[string]string {
	return map[string]string{fmt.Sprintf("f%d", i+1): fmt.Sprint(i+1, "\n")}
}

func (c matrixCommit) String() string {
	var files []string
	for k, v := range c.files {
		files = append(files, k+"="+strings.TrimSpace(v))
	}
	sort.Strings(files)
	return fmt.Sprintf("%q %v", c.msg, files)
}

const matrixReword = "two reworded\n\n# hash line kept\n; semi line kept\n"

// matrixOp is a rewrite of the matrix repository, the history it should
// leave, and where side should then be (an index into it).
type matrixOp struct {
	name string
	make func(m *matrixRepo, v gitexe.Version) (Op, error)
	want func(m *matrixRepo) ([]matrixCommit, int)
}

func matrixOps() []matrixOp {
	rw := func(m *matrixRepo, i int, v gitexe.Version) Rewrite {
		parent := m.base
		if i > 0 {
			parent = m.ids[i-1]
		}
		return Rewrite{Commit: m.ids[i], Parent: parent, Git: v, Branches: []string{"refs/heads/main", "refs/heads/side", "refs/heads/other"}}
	}
	orig := func(m *matrixRepo, skip int) []matrixCommit {
		var s []matrixCommit
		for i := range m.ids {
			if i != skip {
				s = append(s, matrixCommit{m.msgs[i], adds(i)})
			}
		}
		return s
	}
	stage := func(m *matrixRepo) {
		m.h.r.Write("f2", "2 fixed\n")
		m.h.r.Git("add", "f2")
	}
	return []matrixOp{
		{"reword", func(m *matrixRepo, v gitexe.Version) (Op, error) {
			return Reword(m.h.w, rw(m, 1, v), matrixReword, m.h.seen())
		}, func(m *matrixRepo) ([]matrixCommit, int) {
			s := orig(m, -1)
			s[1].msg = cleanMessage(matrixReword)
			return model(s...), 3
		}},
		{"drop", func(m *matrixRepo, v gitexe.Version) (Op, error) {
			return DropCommit(m.h.w, rw(m, 1, v), m.h.seen())
		}, func(m *matrixRepo) ([]matrixCommit, int) { return model(orig(m, 1)...), 2 }},
		{"fixup", func(m *matrixRepo, v gitexe.Version) (Op, error) {
			stage(m)
			return FixupInto(m.h.w, rw(m, 1, v), "", m.h.seen())
		}, func(m *matrixRepo) ([]matrixCommit, int) {
			s := orig(m, -1)
			s[1].files = map[string]string{"f2": "2 fixed\n", "s": "staged\n"}
			return model(s...), 3
		}},
		{"squash", func(m *matrixRepo, v gitexe.Version) (Op, error) {
			stage(m)
			return FixupInto(m.h.w, rw(m, 1, v), matrixReword, m.h.seen())
		}, func(m *matrixRepo) ([]matrixCommit, int) {
			s := orig(m, -1)
			s[1] = matrixCommit{cleanMessage(matrixReword), map[string]string{"f2": "2 fixed\n", "s": "staged\n"}}
			return model(s...), 3
		}},
		{"edit", func(m *matrixRepo, v gitexe.Version) (Op, error) {
			// Squash joined, drop, reorder, reword, fix up, a branch moved.
			return EditHistory(m.h.w, rw(m, 0, v), []Step{
				{Action: DoPick, Commit: m.ids[0]},
				{Action: DoSquash, Commit: m.ids[1]},
				{Action: DoDrop, Commit: m.ids[2]},
				{Action: DoPick, Commit: m.ids[4]},
				{Action: DoReword, Commit: m.ids[3], Message: "four\n\n# kept\n"},
				{Action: DoFixup, Commit: m.ids[5], Refs: []string{"refs/heads/side"}},
			}, m.h.seen())
		}, func(m *matrixRepo) ([]matrixCommit, int) {
			return model(
				matrixCommit{cleanMessage(m.msgs[0] + "\n" + m.msgs[1]), map[string]string{"f1": "1\n", "f2": "2\n"}},
				matrixCommit{m.msgs[4], adds(4)},
				matrixCommit{"four\n\n# kept\n", map[string]string{"f4": "4\n", "f6": "6\n"}},
			), 2
		}},
	}
}

// matrixConfig is a user's config a rewrite must do what was asked under.
type matrixConfig struct {
	name   string
	config [][2]string
	hooks  map[string]string // in a core.hooksPath folder
	// fails are the rewrites git refuses under the config: they fail, and
	// leave everything as it was.
	fails []string
	// messages is false where a hook rewrites messages, as git lets it.
	messages bool
	signed   bool
}

// Reword, drop, fix up, squash and an edit of history (reorder, reword,
// join, fold, drop) do exactly what was asked under each config the user
// may have, through git history and through the rebase, and undo puts it
// all back: the branches, the index and the files.
func TestRewriteUnderTheUsersConfig(t *testing.T) {
	scripts := t.TempDir()
	editor := filepath.Join(scripts, "an editor", "edit")
	if err := os.MkdirAll(filepath.Dir(editor), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(editor, []byte("#!/bin/sh\necho 'EDITED BY THE USER' >\"$1\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	all := []string{"reword", "drop", "fixup", "squash", "edit"}
	configs := []matrixConfig{
		{name: "none", messages: true},
		{name: "autoSquash", config: [][2]string{{"rebase.autoSquash", "true"}}, messages: true},
		{name: "abbreviateCommands", config: [][2]string{{"rebase.abbreviateCommands", "true"}}, messages: true},
		{name: "autoStash off", config: [][2]string{{"rebase.autoStash", "false"}}, messages: true},
		{name: "updateRefs", config: [][2]string{{"rebase.updateRefs", "true"}}, messages: true},
		{name: "missingCommitsCheck", config: [][2]string{{"rebase.missingCommitsCheck", "error"}}, messages: true},
		{name: "instructionFormat", config: [][2]string{{"rebase.instructionFormat", "%an%n%H %s"}}, messages: true},
		{name: "rebaseMerges", config: [][2]string{{"rebase.rebaseMerges", "rebase-cousins"}}, messages: true},
		{name: "editors", config: [][2]string{{"core.editor", "'" + editor + "'"}, {"sequence.editor", "'" + editor + "'"}}, messages: true},
		{name: "editors that fail", config: [][2]string{{"core.editor", "false"}, {"sequence.editor", "false"}}, messages: true},
		{name: "commentChar letter", config: [][2]string{{"core.commentChar", "p"}}, messages: true},
		{name: "commentChar auto", config: [][2]string{{"core.commentChar", "auto"}}, messages: true},
		{name: "commentString", config: [][2]string{{"core.commentString", "e"}, {"commit.cleanup", "strip"}}, messages: true},
		{name: "cleanup strip", config: [][2]string{{"commit.cleanup", "strip"}}, messages: true},
		{name: "cleanup verbatim", config: [][2]string{{"commit.cleanup", "verbatim"}}, messages: true},
		{name: "commitEncoding", config: [][2]string{{"i18n.commitEncoding", "ISO-8859-1"}}, messages: true},
		{name: "pull and merge", config: [][2]string{{"pull.rebase", "interactive"}, {"merge.conflictStyle", "zdiff3"}, {"rebase.rescheduleFailedExec", "true"}}, messages: true},
		{name: "hooks that rewrite messages", hooks: map[string]string{
			"commit-msg":         `printf 'HOOKED: %s' "$(cat "$1")" >"$1.tmp" && mv "$1.tmp" "$1"`,
			"prepare-commit-msg": `if test "$2" != message; then printf 'PREP: %s' "$(cat "$1")" >"$1.tmp" && mv "$1.tmp" "$1"; fi`,
		}},
		{name: "commit-msg fails", hooks: map[string]string{"commit-msg": `echo "commit-msg says no" >&2; exit 1`},
			fails: []string{"reword", "edit"}, messages: true},
		{name: "pre-commit fails", hooks: map[string]string{"pre-commit": `echo "pre-commit says no" >&2; exit 1`},
			fails: []string{"reword", "edit"}, messages: true},
		{name: "pre-rebase refuses", hooks: map[string]string{"pre-rebase": `echo "pre-rebase says no" >&2; exit 1`},
			fails: all, messages: true},
		{name: "gpgSign", messages: true, signed: true},
		{name: "gpgSign without a key", config: [][2]string{{"commit.gpgSign", "true"}, {"gpg.format", "ssh"}, {"user.signingKey", "/no/such/key"}},
			fails: all, messages: true},
	}
	for _, c := range configs {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			for _, path := range []string{"history", "rebase"} {
				v := localGit(t)
				if path == "rebase" {
					v = oldGit
				} else if !v.Has(gitexe.HistoryDrop) {
					continue
				}
				for _, o := range matrixOps() {
					testUnderConfig(t, c, path, v, o)
				}
			}
		})
	}
}

func testUnderConfig(t *testing.T, c matrixConfig, path string, v gitexe.Version, o matrixOp) {
	t.Helper()
	label := path + "/" + o.name
	m := newMatrixRepo(t)
	h := m.h
	for _, kv := range c.config {
		h.r.Git("config", kv[0], kv[1])
	}
	var allowed string
	if c.signed {
		allowed = gittest.SSHSigning(t, h.r)
		h.r.Git("config", "commit.gpgSign", "true")
	}
	if len(c.hooks) > 0 {
		dir := filepath.Join(t.TempDir(), "hooks")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range c.hooks {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		h.r.Git("config", "core.hooksPath", filepath.ToSlash(dir))
	}
	refs := func() string { return h.r.Git("for-each-ref", "--format=%(refname) %(objectname)", "refs/heads") }
	state := func() string {
		return h.r.Git("status", "--porcelain") + h.r.Git("ls-files", "--stage") + h.read("f0") + h.read("f2")
	}
	before, local := refs(), state()
	op, err := o.make(m, v)
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	staged := state() // a fix up stages its change first
	_, err = wait(t, h.q.Submit(op))
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if _, serr := os.Stat(filepath.Join(h.r.GitDir(), d)); serr == nil {
			t.Fatalf("%s: a rebase is left in progress (%v)", label, err)
		}
	}
	if s := strings.TrimSpace(h.r.Git("stash", "list")); s != "" {
		t.Fatalf("%s: a stash is left: %s", label, s)
	}
	if slices.Contains(c.fails, o.name) {
		if err == nil {
			t.Fatalf("%s: went through", label)
		}
		if refs() != before || state() != staged {
			t.Fatalf("%s: failed (%v) and left the branches or files changed:\n%s", label, err, h.r.Git("status", "--porcelain"))
		}
		return
	}
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	want, side := o.want(m)
	got := m.history()
	if len(got) != len(want) {
		t.Fatalf("%s: %d commits, want %d:\n%v", label, len(got), len(want), got)
	}
	for i := range want {
		if fmt.Sprint(got[i].files) != fmt.Sprint(want[i].files) || c.messages && got[i].msg != want[i].msg {
			t.Fatalf("%s: commit %d is %v, want %v", label, i, got[i], want[i])
		}
	}
	ids := strings.Fields(h.r.Git("rev-list", "--reverse", "main", "^"+m.base))
	if h.rev("side") != ids[side] || h.rev("other") != m.base {
		t.Fatalf("%s: side at %s, want %s; other moved: %v", label, short(h.rev("side")), short(ids[side]), h.rev("other") != m.base)
	}
	wantStatus := " M f0\nA  s\n"
	if o.name == "fixup" || o.name == "squash" {
		wantStatus = " M f0\n"
	}
	if st := h.r.Git("status", "--porcelain"); st != wantStatus {
		t.Fatalf("%s: status %q, want %q", label, st, wantStatus)
	}
	if c.signed {
		for _, id := range ids[1:] {
			if out, err := h.r.Run(nil, "-c", "gpg.ssh.allowedSignersFile="+allowed, "verify-commit", id); err != nil {
				t.Fatalf("%s: %s not signed: %v\n%s", label, short(id), err, out)
			}
		}
	}
	if _, err := wait(t, h.q.Undo()); err != nil {
		t.Fatalf("%s: undo: %v", label, err)
	}
	if refs() != before {
		t.Fatalf("%s: undo left the branches moved", label)
	}
	if st := state(); st != staged && st != local {
		t.Fatalf("%s: undo left\n%s", label, h.r.Git("status", "--porcelain"))
	}
}
