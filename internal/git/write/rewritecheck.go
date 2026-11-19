package write

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"ramus/internal/git/gitexe"
	"ramus/internal/git/run"
)

type rewriteKind uint8

const (
	rewordKind rewriteKind = iota
	dropKind
	fixupKind
	editKind
)

// rewriteRun is a history rewrite as its op holds it. When its turn
// comes, it decides between git history and a rebase, makes what the
// command needs, and once the command is done checks that the commits are
// the ones asked for: a rewrite that drops, folds or changes a commit it
// does not name is put back and fails.
type rewriteRun struct {
	kind rewriteKind
	rw   Rewrite
	// msg is the new message of a reword or a squash, cleaned.
	msg   string
	steps []Step
	// history is set when Op.Cmd is git history's; fallback is the rebase
	// that runs in its place when the repository asks for what only a
	// rebase does, and always when history is not set.
	history  bool
	fallback run.Cmd
}

// rebaseHooks are the hooks a rebase runs and git history does not: with
// any of them, a rewrite takes the rebase, as the user's hooks then run
// as they do when the user rebases in a terminal.
var rebaseHooks = []string{"pre-rebase", "post-checkout", "pre-commit", "prepare-commit-msg", "commit-msg", "post-commit", "post-rewrite"}

// rewriteState is one run of a rewrite.
type rewriteState struct {
	rr  *rewriteRun
	cmd run.Cmd
	// head is HEAD before the rewrite.
	head string
	// headTree is HEAD's tree before the rewrite.
	headTree string
	// staged is the tree of the index before a fix up, or before a
	// rebase, whose autostash gives the changes back unstaged.
	staged string
	// rebase is set when the rebase runs, not git history.
	rebase bool
	// busy is a rebase in progress before the rewrite: not the rewrite's
	// to stop.
	busy bool
	// hooks are the rebaseHooks git would run.
	hooks map[string]bool
}

// ErrRewriteNotAsAsked is a rewrite that ran but did not come out as
// asked, as one that left out, folded or changed a commit it does not
// name. The branches are put back where they were.
var ErrRewriteNotAsAsked = errors.New("write: the rewrite did not come out as asked")

// errNothingStaged is a fix up with nothing staged to fold in.
var errNothingStaged = errors.New("write: nothing is staged to fold into the commit")

// checkTimeout bounds what a rewrite reads before and after it runs.
const checkTimeout = HookTimeout

func (s *rewriteState) read(ctx context.Context, q *Queue, args ...string) (string, error) {
	res, err := q.r.Run(ctx, &run.Cmd{GitDir: s.cmd.GitDir, WorkTree: s.cmd.WorkTree, Read: true, Args: args})
	return strings.TrimSpace(string(res.Stdout)), err
}

// start decides the command a rewrite runs: git history, or the rebase
// when the user's config asks for signed commits, notes copied to the
// rewritten commits, or has hooks a rebase runs; and for a fix up the
// commit of what is staged that the rebase folds in.
func (rr *rewriteRun) start(ctx context.Context, q *Queue, cmd run.Cmd) (*rewriteState, error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	s := &rewriteState{rr: rr, cmd: cmd, rebase: !rr.history}
	var err error
	if s.head, err = s.read(ctx, q, "rev-parse", "--verify", "HEAD^{commit}"); err != nil {
		return nil, err
	}
	s.busy = s.rebasing()
	w := Where{GitDir: cmd.GitDir, WorkTree: cmd.WorkTree}
	if s.hooks, err = hooksSet(ctx, q.r, w, rebaseHooks); err != nil {
		return nil, err
	}
	if rr.history {
		need, err := s.needsRebase(ctx, q)
		if err != nil {
			return nil, err
		}
		if need {
			if err := checkReplay(ctx, q.r, cmd.GitDir, &rr.rw); err != nil {
				return nil, err
			}
			s.rebase, s.cmd = true, rr.fallback
		}
	}
	if s.headTree, err = s.read(ctx, q, "rev-parse", "--verify", s.head+"^{tree}"); err != nil {
		return nil, err
	}
	if rr.kind == fixupKind || s.rebase {
		res, err := q.r.Run(ctx, &run.Cmd{GitDir: cmd.GitDir, WorkTree: cmd.WorkTree, Args: []string{"write-tree"}})
		if err != nil {
			return nil, err
		}
		s.staged = strings.TrimSpace(string(res.Stdout))
	}
	switch rr.kind {
	case fixupKind:
		return s, s.fixupCommit(ctx, q)
	case editKind:
		return s, s.checkSteps(ctx, q)
	}
	return s, nil
}

// needsRebase reports whether the user's config asks for what git history
// does not do: sign the commits, copy their notes, run hooks.
func (s *rewriteState) needsRebase(ctx context.Context, q *Queue) (bool, error) {
	for _, h := range rebaseHooks {
		if s.hooks[h] {
			return true, nil
		}
	}
	out, err := s.read(ctx, q, "config", "--type=bool", "--get-regexp", `^(commit\.gpgsign|notes\.rewrite\.rebase)$`)
	if err != nil && exitCode(err) != 1 {
		return false, err
	}
	sign, notes := false, true
	for line := range strings.SplitSeq(out, "\n") {
		k, v, _ := strings.Cut(line, " ")
		switch k {
		case "commit.gpgsign":
			sign = v == "true"
		case "notes.rewrite.rebase":
			notes = v == "true"
		}
	}
	if sign {
		return true, nil
	}
	if !notes {
		return false, nil
	}
	for _, e := range q.r.Environ() {
		if v, ok := strings.CutPrefix(e, "GIT_NOTES_REWRITE_REF="); ok && v != "" {
			return true, nil
		}
	}
	refs, err := s.read(ctx, q, "config", "--get-all", "notes.rewriteRef")
	if err != nil && exitCode(err) != 1 {
		return false, err
	}
	return refs != "", nil
}

// hooksSet reports which of names git would run in w: what git hook list
// lists, enabled; before git could list them, only the hooks folder holds
// hooks, and an executable file there is one.
func hooksSet(ctx context.Context, r *run.Runner, w Where, names []string) (map[string]bool, error) {
	set := map[string]bool{}
	if r.Git().Version.Has(gitexe.HookList) {
		for _, n := range names {
			hooks, err := Hooks(ctx, r, w.GitDir, w.WorkTree, n)
			if err != nil {
				return nil, err
			}
			for _, h := range hooks {
				set[n] = set[n] || !h.Disabled
			}
		}
		return set, nil
	}
	res, err := r.Run(ctx, &run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Read: true, Args: []string{"rev-parse", "--path-format=absolute", "--git-path", "hooks"}})
	if err != nil {
		return nil, err
	}
	dir := strings.TrimSpace(string(res.Stdout))
	for _, n := range names {
		st, err := os.Stat(filepath.Join(dir, n))
		set[n] = err == nil && st.Mode().IsRegular() && (runtime.GOOS == "windows" || st.Mode()&0o111 != 0)
	}
	return set, nil
}

// fixupCommit reads what is staged, and for the rebase makes the commit
// of it the rebase folds in: on HEAD, with no hooks run, signed or not as
// it does not stay, holding the squash's message for fixup -C to take.
func (s *rewriteState) fixupCommit(ctx context.Context, q *Queue) error {
	if s.staged == s.headTree {
		return errNothingStaged
	}
	if !s.rebase {
		return nil
	}
	msg := s.rr.msg
	if msg == "" {
		msg = "fixup! " + s.rr.rw.Commit + "\n"
	}
	res, err := q.r.Run(ctx, &run.Cmd{GitDir: s.cmd.GitDir, WorkTree: s.cmd.WorkTree, Stdin: strings.NewReader(msg),
		Args: []string{"commit-tree", "--no-gpg-sign", "-p", s.head, "-F", "-", s.staged}})
	if err != nil {
		return err
	}
	line := "fixup " + strings.TrimSpace(string(res.Stdout))
	if s.rr.msg != "" {
		line = "fixup -C " + strings.TrimSpace(string(res.Stdout))
	}
	s.cmd.Env = setEnv(s.cmd.Env, fixupEnv, line)
	return nil
}

// checkSteps refuses an edit of history whose steps are not the commits
// after rw.Parent, each once: the rebase would drop a commit the steps
// leave out without a word.
func (s *rewriteState) checkSteps(ctx context.Context, q *Queue) error {
	rw := &s.rr.rw
	if err := checkReplay(ctx, q.r, s.cmd.GitDir, rw); err != nil {
		return err
	}
	ids, err := s.commits(ctx, q, s.head)
	if err != nil {
		return err
	}
	named := map[string]bool{}
	for _, st := range s.rr.steps {
		named[st.Commit] = true
	}
	n := 0
	for _, id := range ids {
		if named[id] {
			n++
		}
	}
	if n != len(ids) || len(named) != len(ids) {
		return fmt.Errorf("write: the edit names %d commits, and %d come after %s; look again before going on", len(named), len(ids), short(rw.Parent))
	}
	return nil
}

// commits are the commits after rw.Parent up to tip, oldest first.
func (s *rewriteState) commits(ctx context.Context, q *Queue, tip string) ([]string, error) {
	args := []string{"rev-list", "--reverse", tip}
	if s.rr.rw.Parent != "" {
		args = append(args, "^"+s.rr.rw.Parent)
	}
	out, err := s.read(ctx, q, append(args, "--")...)
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

// rebasing reports whether a rebase is in progress.
func (s *rewriteState) rebasing() bool {
	for _, d := range []string{"rebase-merge", "rebase-apply"} {
		if _, err := os.Lstat(filepath.Join(s.cmd.GitDir, d)); err == nil {
			return true
		}
	}
	return false
}

// stopped is err, from a rebase of the rewrite that stopped part way for
// another reason than a conflict for the user to resolve: a commit that
// could not be signed, a hook that refused. The rebase is stopped, which
// puts the branch and the local changes back as they were.
func (s *rewriteState) stopped(ctx context.Context, q *Queue, err error) error {
	if !s.rebase || s.busy {
		return err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkTimeout)
	defer cancel()
	if s.rebasing() {
		if out, lerr := s.read(ctx, q, "ls-files", "--unmerged"); lerr != nil || out != "" {
			return err // the conflicts are the user's to resolve, or to abort
		}
		_, aerr := q.r.Run(ctx, &run.Cmd{GitDir: s.cmd.GitDir, WorkTree: s.cmd.WorkTree, Timeout: HookTimeout,
			Env: []string{"GIT_EDITOR=true"}, Args: []string{"rebase", "--abort"}})
		if aerr != nil {
			return fmt.Errorf("%w (stopping the rebase it left failed too: %v)", err, aerr)
		}
	}
	// A rebase refused before it began has given the autostash back too,
	// unstaged.
	if rerr := s.restage(ctx, q); rerr != nil {
		return fmt.Errorf("%w; the rewrite was stopped, and the branch is as it was, but what was staged is now in the files only: %v", err, rerr)
	}
	return fmt.Errorf("%w; the rewrite was stopped, and the branch is as it was", err)
}

// finish checks a rewrite that ran: when its commits are not the ones
// asked for, the write j recorded is undone and dropped, and the rewrite
// fails. A rebase that stopped for the user at an edit step is theirs to
// go on with.
func (s *rewriteState) finish(ctx context.Context, q *Queue, j *Job) error {
	if s.rebase && s.rebasing() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), checkTimeout)
	defer cancel()
	why := s.verify(ctx, q)
	if why != nil {
		return s.takeBack(ctx, q, j, why)
	}
	if s.rebase {
		if err := s.restage(ctx, q); err != nil {
			return fmt.Errorf("write: the rewrite is done, but what was staged is now in the files only: %w", err)
		}
	}
	return nil
}

// restage stages again what was staged before a rebase, whose autostash
// gives the local changes back unstaged: the index the rewrite started
// from, or, where the rewrite changed HEAD's files, that change put on it.
// When the two do not merge, or git cannot merge trees, the changes stay
// as the autostash left them, all in the files.
func (s *rewriteState) restage(ctx context.Context, q *Queue) error {
	if s.staged == "" || s.staged == s.headTree {
		return nil
	}
	want := s.staged
	tree, err := s.read(ctx, q, "rev-parse", "--verify", "HEAD^{tree}")
	if err != nil {
		return err
	}
	if tree != s.headTree {
		if !q.r.Git().Version.AtLeast(2, 45, 0) {
			return nil
		}
		out, err := s.read(ctx, q, "merge-tree", "--write-tree", "--no-messages", "--merge-base="+s.headTree, tree, s.staged)
		if err != nil {
			return nil // they conflict
		}
		want, _, _ = strings.Cut(out, "\n")
	}
	res, err := q.r.Run(ctx, &run.Cmd{GitDir: s.cmd.GitDir, WorkTree: s.cmd.WorkTree, Args: []string{"write-tree"}})
	if err != nil || strings.TrimSpace(string(res.Stdout)) == want {
		return err
	}
	_, err = q.r.Run(ctx, &run.Cmd{GitDir: s.cmd.GitDir, WorkTree: s.cmd.WorkTree, Args: []string{"read-tree", want}})
	return err
}

// takeBack undoes the write j recorded, which did not come out as asked,
// and drops it, so redo cannot do it again.
func (s *rewriteState) takeBack(ctx context.Context, q *Queue, j *Job, why error) error {
	j.mu.Lock()
	id, backup := j.entry, j.backup
	j.mu.Unlock()
	if id == 0 {
		return fmt.Errorf("%w: %v; it could not be undone, and the branch's backup is %s", ErrRewriteNotAsAsked, why, backup)
	}
	_, err := q.travel(ctx, true, id)
	q.History.forget(id)
	j.mu.Lock()
	j.entry = 0
	j.mu.Unlock()
	if err == nil {
		// HEAD is back: what was staged is staged again, as it was.
		err = s.restage(ctx, q)
	}
	if err != nil {
		return fmt.Errorf("%w: %v; putting the branches back failed (%v), and the branch's backup is %s", ErrRewriteNotAsAsked, why, err, backup)
	}
	return fmt.Errorf("%w: %v; the branches were put back where they were", ErrRewriteNotAsAsked, why)
}

// forget drops entry id from what Redo can do.
func (h *History) forget(id uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.undone = slices.DeleteFunc(h.undone, func(e Entry) bool { return e.id == id })
}

// commitFacts are what a check compares of a commit.
type commitFacts struct {
	id, tree, author string
	// encoding is the commit's encoding header, "" for none.
	encoding string
	// msg is the message as stored; text is it as UTF-8 text, the same
	// unless the commit names another encoding.
	msg, text string
	// change sums the commit's patch without context, line numbers or
	// blob ids, as git patch-id does: the same change on other lines of
	// the file sums the same. "" for none.
	change string
}

// facts reads the commits after rw.Parent up to tip, oldest first; diffs
// adds what each changes.
func (s *rewriteState) facts(ctx context.Context, q *Queue, tip string, diffs bool) ([]commitFacts, error) {
	ids, err := s.commits(ctx, q, tip)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	in := strings.Join(ids, "\n") + "\n"
	res, err := q.r.Run(ctx, &run.Cmd{GitDir: s.cmd.GitDir, Read: true, Stdin: strings.NewReader(in), Args: []string{"cat-file", "--batch"}})
	if err != nil {
		return nil, err
	}
	out := make([]commitFacts, 0, len(ids))
	rest := res.Stdout
	for _, id := range ids {
		head, body, ok := bytes.Cut(rest, []byte("\n"))
		var size int
		if _, err := fmt.Sscanf(string(head), id+" commit %d", &size); !ok || err != nil || size > len(body) {
			return nil, fmt.Errorf("write: reading commit %s: %q", short(id), head)
		}
		f := parseCommit(id, body[:size])
		if f.encoding != "" && !strings.EqualFold(f.encoding, "utf-8") && !strings.EqualFold(f.encoding, "utf8") {
			// git stores a message it replays in i18n.commitEncoding: the
			// same text in other bytes.
			res, err := q.r.Run(ctx, &run.Cmd{GitDir: s.cmd.GitDir, Read: true,
				Args: []string{"log", "-1", "--no-show-signature", "--encoding=UTF-8", "--format=%B", id, "--"}})
			if err != nil {
				return nil, err
			}
			f.text = strings.TrimSuffix(string(res.Stdout), "\n")
		}
		out = append(out, f)
		rest = bytes.TrimPrefix(body[size:], []byte("\n"))
	}
	if !diffs {
		return out, nil
	}
	sums := &changeSums{sums: map[string]string{}}
	_, err = q.r.Run(ctx, &run.Cmd{GitDir: s.cmd.GitDir, Read: true, Stdin: strings.NewReader(in), Stdout: sums,
		Args: []string{"diff-tree", "--stdin", "--root", "--always", "-r", "-p", "-U0", "--no-renames", "--no-ext-diff", "--no-textconv", "--full-index"}})
	if err != nil {
		return nil, err
	}
	sums.end()
	for i := range out {
		out[i].change = sums.sums[out[i].id]
	}
	return out, nil
}

func parseCommit(id string, raw []byte) commitFacts {
	f := commitFacts{id: id}
	header, msg, _ := bytes.Cut(raw, []byte("\n\n"))
	f.msg = string(msg)
	f.text = f.msg
	for line := range strings.SplitSeq(string(header), "\n") {
		k, v, _ := strings.Cut(line, " ")
		switch k {
		case "tree":
			f.tree = v
		case "author":
			f.author = v
		case "encoding":
			f.encoding = v
		}
	}
	return f
}

// changeSums reads git diff-tree --stdin -p into a sum of each commit's
// change.
type changeSums struct {
	sums map[string]string
	cur  string
	h    hash.Hash
	any  bool
	// index is the index line of the file being read, summed only when
	// the file shows to be binary: its blob ids are then all its patch
	// says.
	index []byte
	part  []byte
}

func (c *changeSums) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			c.part = append(c.part, p...)
			break
		}
		line := p[:i]
		if len(c.part) > 0 {
			c.part = append(c.part, line...)
			line = c.part
		}
		c.line(line)
		c.part = c.part[:0]
		p = p[i+1:]
	}
	return n, nil
}

func (c *changeSums) line(l []byte) {
	if (len(l) == 40 || len(l) == 64) && isHex(string(l)) {
		c.end()
		c.cur, c.h, c.any = string(l), sha256.New(), false
		return
	}
	if c.cur == "" {
		return
	}
	switch {
	case bytes.HasPrefix(l, []byte("diff --git ")):
		c.index = nil
	case bytes.HasPrefix(l, []byte("index ")):
		c.index = append(c.index[:0], l...)
		return
	case bytes.HasPrefix(l, []byte("@@")):
		return
	case bytes.HasPrefix(l, []byte("Binary files ")) && c.index != nil:
		c.write(c.index)
	}
	c.write(l)
}

func (c *changeSums) write(l []byte) {
	c.h.Write(l)
	c.h.Write([]byte{'\n'})
	c.any = true
}

func (c *changeSums) end() {
	if c.cur == "" {
		return
	}
	c.sums[c.cur] = ""
	if c.any {
		c.sums[c.cur] = hex.EncodeToString(c.h.Sum(nil))
	}
	c.cur = ""
}

// verify compares the commits after rw.Parent before the rewrite and now:
// every commit the rewrite does not name is there, in order, with its
// author, its change and, unless a hook may have rewritten it as git
// commits it, its message; the commit named changed only as asked.
func (s *rewriteState) verify(ctx context.Context, q *Queue) error {
	now, err := resolve(ctx, q.r, s.cmd.GitDir, "HEAD")
	if err != nil {
		return err
	}
	diffs := s.rr.kind != rewordKind
	old, err := s.facts(ctx, q, s.head, diffs)
	if err != nil {
		return err
	}
	cur, err := s.facts(ctx, q, now, diffs)
	if err != nil {
		return err
	}
	// git history runs no hooks; a rebase may run some that change a
	// message as git commits it.
	msgs := !s.rebase || !s.hooks["prepare-commit-msg"] && !s.hooks["commit-msg"]
	target := s.rr.rw.Commit
	switch s.rr.kind {
	case rewordKind:
		if len(cur) != len(old) {
			return countError(len(old), len(cur))
		}
		for i, o := range old {
			c := cur[i]
			want := wantMsg{o.msg, o.text}
			if o.id == target {
				want = wantMsg{s.rr.msg, s.rr.msg}
			}
			if c.tree != o.tree || c.author != o.author || msgs && !c.says(want) {
				return changedError(o)
			}
		}
	case dropKind:
		if len(cur) != len(old)-1 {
			return countError(len(old)-1, len(cur))
		}
		i := 0
		for _, o := range old {
			if o.id == target {
				continue
			}
			if !kept(o, cur[i], msgs) {
				return changedError(o)
			}
			i++
		}
	case fixupKind:
		if len(cur) != len(old) {
			return countError(len(old), len(cur))
		}
		for i, o := range old {
			c := cur[i]
			if o.id != target {
				if !kept(o, c, msgs) {
					return changedError(o)
				}
				continue
			}
			// A fold keeps the message, but for blank lines at its start.
			want := wantMsg{o.msg, o.text}.skipBlank()
			if s.rr.msg != "" {
				want = wantMsg{s.rr.msg, s.rr.msg}
			}
			if c.author != o.author || msgs && !c.says(want) {
				return changedError(o)
			}
		}
		if cur[len(cur)-1].tree != s.staged {
			return errors.New("the files at the end are not those staged")
		}
	case editKind:
		return s.verifyEdit(old, cur, msgs)
	}
	return nil
}

// verifyEdit compares an edit of history with its steps: one commit for
// each that is not folded or dropped, in their order, with the author of
// its first commit, and the message the steps make; a commit nothing is
// folded into has its own change. With nothing dropped, the files at the
// end are those at the start.
func (s *rewriteState) verifyEdit(old, cur []commitFacts, msgs bool) error {
	by := map[string]commitFacts{}
	for _, o := range old {
		by[o.id] = o
	}
	type group struct {
		lead  Step
		folds []Step
	}
	var groups []group
	dropped := false
	for _, st := range s.rr.steps {
		switch st.Action {
		case DoDrop:
			dropped = true
		case DoSquash, DoFixup:
			groups[len(groups)-1].folds = append(groups[len(groups)-1].folds, st)
		default:
			groups = append(groups, group{lead: st})
		}
	}
	if len(cur) != len(groups) {
		return countError(len(groups), len(cur))
	}
	for i, g := range groups {
		o, c := by[g.lead.Commit], cur[i]
		msg := wantMsg{o.msg, o.text}
		if g.lead.Action == DoReword {
			m := cleanMessage(g.lead.Message)
			msg = wantMsg{m, m}
		}
		for _, f := range g.folds {
			switch {
			case f.Action == DoFixup:
				msg = msg.skipBlank()
			case f.Message != "":
				m := cleanMessage(f.Message)
				msg = wantMsg{m, m}
			default:
				j := by[f.Commit]
				msg = wantMsg{cleanMessage(msg.raw + "\n\n" + j.msg), cleanMessage(msg.text + "\n\n" + j.text)}
			}
		}
		if c.author != o.author || msgs && !c.says(msg) || len(g.folds) == 0 && c.change != o.change && c.change != "" {
			return changedError(o)
		}
	}
	if !dropped && len(old) > 0 && cur[len(cur)-1].tree != old[len(old)-1].tree {
		return errors.New("the files at the end are not those at the start")
	}
	return nil
}

// wantMsg is a message a commit should have, as stored and as text: a
// commit in another encoding than the one asked for has it in one or the
// other.
type wantMsg struct{ raw, text string }

func (c commitFacts) says(m wantMsg) bool { return c.msg == m.raw || c.text == m.text }

// skipBlank is m without the blank lines at its start, which git drops
// from the message of a commit it folds another into.
func (m wantMsg) skipBlank() wantMsg { return wantMsg{skipBlank(m.raw), skipBlank(m.text)} }

func skipBlank(msg string) string {
	for {
		line, rest, ok := strings.Cut(msg, "\n")
		if !ok || strings.Trim(line, " \t\r") != "" {
			return msg
		}
		msg = rest
	}
}

// kept reports whether c is o replayed: the same author, change and,
// with msgs, message. A change that was already there leaves the commit
// with none, which it keeps, as asked.
func kept(o, c commitFacts, msgs bool) bool {
	return c.author == o.author && (c.change == o.change || c.change == "") && (!msgs || c.says(wantMsg{o.msg, o.text}))
}

func countError(want, got int) error {
	return fmt.Errorf("it left %d commits where %d were asked for", got, want)
}

func changedError(o commitFacts) error {
	subject, _, _ := strings.Cut(o.text, "\n")
	return fmt.Errorf("commit %s (%q) did not come out as asked", short(o.id), subject)
}
