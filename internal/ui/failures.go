package ui

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"ramus/internal/git/config"
	"ramus/internal/git/redact"
	"ramus/internal/git/repo"
	"ramus/internal/git/run"
	"ramus/internal/git/write"
	"ramus/internal/session"
	"ramus/locales"
)

// diagnosis is what the app makes of a failed write: the error bar's
// line, all of git's words for its tooltip, and what can be done.
type diagnosis struct {
	text, full string
	fixes      []remedy
	// covered is a failure another bar tells of already: an operation
	// stopped on conflicts, which the operation bar shows.
	covered bool
}

// remedy is a button that answers a failure.
type remedy struct {
	label string
	run   func()
}

// diagnose reads failed write e of tab t. The causes it knows it says in
// the app's words with a way out; the rest it says as git did. git's
// messages are matched where git keeps them untranslated (a rejected
// ref, a lock's path, ssh's words) or in English, as a terminal in an
// English locale shows them; a translated one is shown as it is.
func (a *App) diagnose(t *tab, e *session.LogEntry) diagnosis {
	why := failureOf(e)
	d := diagnosis{text: locales.RepoWriteFailed(e.Name, oneLine(why)), full: why}
	op, again := e.Op()
	stderr := ""
	var re *run.Error
	if errors.As(e.Err, &re) {
		stderr = re.Stderr
	}
	st := t.s.State
	unmerged := 0
	if st.Status != nil {
		unmerged = st.Status.Unmerged()
	}
	retry := func() remedy {
		return remedy{locales.RepoTryAgain(), func() { a.submit(t, op, nil) }}
	}
	var le *write.LockedError
	var kept *write.KeptError
	// A write stopped on conflicts says so; any other failure while a
	// merge has some, a lock or a refused login, is told as itself.
	// Only what git printed is read, not the error's paths.
	conflicted := unmerged > 0 && re != nil && strings.Contains(strings.ToLower(stderr+"\n"+strings.Join(e.Output, "\n")), "conflict")
	switch {
	case conflicted:
		switch st.Operation {
		case repo.Merge, repo.Rebase, repo.CherryPick, repo.Revert, repo.Am:
			d.covered = true
			return d
		}
		d.text = locales.RepoStoppedConflicts(e.Name, unmerged)
		d.fixes = append(d.fixes, remedy{locales.RepoShowConflicts(), func() { t.sel = "changes" }})
	case errors.As(e.Err, &le):
		name := lockName(t, le.Path)
		switch {
		case len(le.Holders) > 0:
			var who []string
			for _, h := range le.Holders {
				who = append(who, redact.Control(h.Command))
			}
			d.text = locales.RepoLockHeld(e.Name, name, strings.Join(who, ", "))
		case !le.Left():
			// Made a moment ago: in use, as by a commit running a hook,
			// whether or not who holds it can be told.
			d.text = locales.RepoLockInUse(e.Name, name)
			if again {
				d.fixes = append(d.fixes, retry())
			}
		case le.Unknown == nil:
			d.text = locales.RepoLockLeft(e.Name, name)
			label := locales.RepoRemoveLock()
			if again {
				label = locales.RepoRemoveLockTryAgain()
			}
			d.fixes = append(d.fixes, remedy{label, func() { a.removeLock(t, e, le.Path, op, again) }})
		}
	case errors.Is(e.Err, run.ErrTimeout):
		d.text = locales.RepoTimedOut(e.Name)
		if again {
			d.fixes = append(d.fixes, retry())
		}
	case errors.Is(e.Err, run.ErrStalled):
		d.text = locales.RepoStalled(e.Name)
		if again {
			d.fixes = append(d.fixes, retry())
		}
	case again && isBranchPush(op) && strings.Contains(stderr, "! [rejected]"):
		d.text = locales.RepoPushBehind()
		if st.Status != nil && st.Status.Branch.Upstream != "" && !st.Status.Branch.Gone {
			d.fixes = append(d.fixes, remedy{locales.RepoPullAndPush(), func() { a.pullThenPush(t, op) }})
		}
	case strings.Contains(stderr, "would be overwritten by"):
		d.text = locales.RepoWouldOverwrite(e.Name)
		if again {
			if stashing, ok := write.Stashing(op); ok {
				d.fixes = append(d.fixes, remedy{locales.RepoStashTryAgain(), func() { a.submit(t, stashing, nil) }})
			}
		}
	case strings.Contains(stderr, "Host key verification failed") || strings.Contains(stderr, "REMOTE HOST IDENTIFICATION HAS CHANGED"):
		d.text = locales.RepoHostKey(e.Name)
	case containsAny(stderr, "Author identity unknown", "Committer identity unknown", "unable to auto-detect email address", "empty ident name"):
		// The commonest failure of a first commit, which git ends with a
		// line that says nothing to someone new to it.
		d.text = locales.RepoIdentityUnknown(e.Name)
		d.fixes = append(d.fixes, remedy{locales.RepoSetIdentity(), a.openIdentity})
		if again {
			d.fixes = append(d.fixes, retry())
		}
	case containsAny(stderr, "Authentication failed", "Permission denied (", "Invalid username or password", "could not read Username"):
		d.text = locales.RepoLoginRefused(e.Name)
	case errors.As(e.Err, &kept):
		// It may have dropped work before it stopped: the user hears where
		// that is.
		cause := why
		if re == nil {
			cause = failure(kept.Err)
		}
		d.text = locales.RepoFailedKeptSnapshot(e.Name, oneLine(cause))
		if kept.Undo {
			d.text = locales.RepoFailedKeptUndo(e.Name, oneLine(cause))
		}
	case again && op.NoIndex:
		// The network, most often: the same again may well go through.
		d.fixes = append(d.fixes, retry())
	}
	return d
}

// failureOf is why write e failed, as git said it, without the command
// line the log already shows: its error output, else the last line it
// printed (a merge tells of conflicts on its standard output), else how
// it ended.
func failureOf(e *session.LogEntry) string {
	var re *run.Error
	if errors.As(e.Err, &re) && strings.TrimSpace(re.Stderr) == "" {
		for i := len(e.Output) - 1; i >= 0; i-- {
			if l := strings.TrimSpace(e.Output[i]); l != "" {
				return l // the log keeps its lines redacted
			}
		}
	}
	return failure(e.Err)
}

// lockName is a lock as the bar names it: its path in the repository's
// git directory, else its own name.
func lockName(t *tab, path string) string {
	if r := t.s.Repo; r != nil {
		if rel, err := filepath.Rel(r.GitDir, path); err == nil && !strings.HasPrefix(rel, "..") {
			return filepath.ToSlash(rel)
		}
	}
	return filepath.Base(path)
}

// removeLock removes the lock at path that failed write e, off the main
// thread, as the user asked, and submits op again when again is set.
// The engine refuses if a process holds the lock by then.
func (a *App) removeLock(t *tab, e *session.LogEntry, path string, op write.Op, again bool) {
	gitDir := t.s.Where().GitDir
	update := a.Update
	a.goWork(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := write.RemoveLock(ctx, gitDir, path)
		update(func() {
			if err != nil {
				a.tell(t, errText(err))
				return
			}
			t.failSeen = e
			if again {
				a.submit(t, op, nil)
			}
		})
	})
}

// pullThenPush pulls the branch, as git's config says a pull takes in
// the upstream's commits, then pushes again with push, once the pull
// went through.
func (a *App) pullThenPush(t *tab, push write.Op) {
	seen := t.s.Seen()
	pull, err := write.Pull(t.s.Where(), tabPullMode(t, seen.Branch), t.s.LocalBranches(), seen)
	if err != nil {
		a.tell(t, errText(err))
		return
	}
	j, err := t.s.Submit(pull)
	if err != nil {
		a.tell(t, errText(err))
		return
	}
	t.s.Then(j, func(err error) {
		if err == nil {
			a.submit(t, push, nil)
		}
	})
}

// tabPullMode is pullMode with the config of tab t's repository.
func tabPullMode(t *tab, branch string) write.PullMode {
	var cfg *config.Config
	if r := t.s.Repo; r != nil {
		cfg = r.Config()
	}
	return pullMode(cfg, branch)
}

// pullMode is how git pull would take in the upstream of branch (a full
// ref name) as cfg says: rebasing for branch.<name>.rebase, else
// pull.rebase; fast-forward only for pull.ff=only; else merging.
func pullMode(cfg *config.Config, branch string) write.PullMode {
	if cfg == nil {
		return write.PullMerge
	}
	v, ok := cfg.Get("branch." + strings.TrimPrefix(branch, "refs/heads/") + ".rebase")
	if !ok {
		v, ok = cfg.Get("pull.rebase")
	}
	if ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "merges", "m":
			return write.PullRebaseMerges
		case "true", "yes", "on", "1", "interactive", "i":
			// Interactive would open an editor no one is at.
			return write.PullRebase
		}
	}
	if ff, ok := cfg.Get("pull.ff"); ok && strings.ToLower(strings.TrimSpace(ff)) == "only" {
		return write.PullFFOnly
	}
	return write.PullMerge
}

// isBranchPush reports a push of the branch HEAD is on, as write.Push
// makes it without a force: not a tag's, a stack's or a forced one,
// which a pull does not answer.
func isBranchPush(op write.Op) bool {
	if len(op.Cmd.Args) == 0 || op.Cmd.Args[0] != "push" {
		return false
	}
	for _, a := range op.Cmd.Args[1:] {
		if a == "--force-with-lease" || strings.Contains(a, ":") || strings.HasPrefix(a, "refs/tags/") {
			return false
		}
	}
	return true
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// pushOptions is how the branch of tab t is pushed: to its upstream, or,
// with none, to the one remote there is, or origin among several, made
// its upstream, as Commit and Push does. One whose upstream is gone is
// published again to the remote it tracked. False when there is nowhere
// to push it.
func pushOptions(t *tab) (write.PushOptions, bool) {
	st := t.s.State.Status
	if st == nil {
		return write.PushOptions{}, false
	}
	if st.Branch.Upstream != "" && !st.Branch.Gone {
		return write.PushOptions{}, true
	}
	r := t.s.Repo
	if r == nil {
		return write.PushOptions{}, false
	}
	remotes := r.Config().Subsections("remote", "url")
	if st.Branch.Gone && st.Branch.Head != "" {
		if v, ok := r.Config().Get("branch." + st.Branch.Head + ".remote"); ok && slices.Contains(remotes, v) {
			return write.PushOptions{Remote: v}, true
		}
	}
	switch {
	case len(remotes) == 1:
		return write.PushOptions{Remote: remotes[0]}, true
	case slices.Contains(remotes, "origin"):
		return write.PushOptions{Remote: "origin"}, true
	}
	return write.PushOptions{}, false
}
