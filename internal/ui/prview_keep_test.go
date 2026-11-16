package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui/editor"

	"ramus/internal/forge"
	"ramus/internal/git/gittest"
)

// keepTool is a gh that answers the list and the pull request from files
// in its folder, counts the reads of the pull request, logs each write
// with its input and, while the folder holds a file named slow, answers
// a write only after two seconds.
func keepTool(t *testing.T, list string) (dir string, env []string, log func() string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake tool is a shell script")
	}
	dir = t.TempDir()
	os.WriteFile(filepath.Join(dir, "list.json"), []byte(list), 0o644)
	script := `#!/bin/sh
d='` + dir + `'
case "$*" in
*"pullRequests(states"*) cat "$d/list.json" ;;
*"pullRequest(number"*) echo >> "$d/reads"; cat "$d/review.json" ;;
*POST*|*PUT*|*ReviewThread*)
  [ -f "$d/slow" ] && sleep 2
  printf '%s\n' "$*" >> "$d/log"; cat >> "$d/log"; echo >> "$d/log"; echo '{"id":1}' ;;
*) echo '{}' ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, []string{"PATH=" + dir + ":/usr/bin:/bin"}, func() string {
		b, _ := os.ReadFile(filepath.Join(dir, "log"))
		return string(b)
	}
}

// keepReview is pull request 7 at head, with one thread on line 2 of
// a.txt.
func keepReview(head string) string {
	return `{"data":{"viewer":{"login":"me"},"repository":{"pullRequest":{"number":7,"title":"Add a thing",
"url":"https://github.com/o/r/pull/7","body":"","state":"OPEN","isDraft":false,"author":{"login":"ann"},
"baseRefName":"main","headRefName":"thing","baseRefOid":"","headRefOid":"` + head + `","reviewDecision":"REVIEW_REQUIRED",
"createdAt":"2026-11-10T09:00:00Z","additions":2,"deletions":1,"commits":{"nodes":[]},
"comments":{"totalCount":0,"nodes":[]},"reviews":{"totalCount":0,"nodes":[]},
"reviewThreads":{"totalCount":1,"nodes":[
 {"id":"T1","isResolved":false,"isOutdated":false,"path":"a.txt","line":2,"startLine":null,"originalLine":2,"diffSide":"RIGHT",
  "viewerCanResolve":true,"viewerCanUnresolve":false,"viewerCanReply":true,
  "comments":{"totalCount":1,"nodes":[{"id":"C1","databaseId":42,"author":{"login":"bob"},"body":"Why spell it out?","createdAt":"2026-11-11T10:00:00Z"}]}}]}}}}}`
}

// A review keeps up with the pull request without losing what the user
// does in it: a push that adds files keeps the file chosen, and a
// comment half written stays, sent to the lines it was written on; a
// refresh asked for while a read runs is not lost; a write that timed
// out holds sending until the pull request is read again; and closing
// the tab asks before dropping comments not sent.
func TestPullRequestReviewKeepsUp(t *testing.T) {
	host := gittest.Init(t)
	host.Write("a.txt", "1\n2\n3\n")
	host.Commit("base")
	host.Git("checkout", "-q", "-b", "thing")
	host.Write("a.txt", "1\nTWO\n3\n")
	head := host.Commit("head")
	host.Git("update-ref", "refs/pull/7/head", head)
	host.Git("checkout", "-q", "main")

	h := newHarness(t)
	r := h.repoWith()
	r.Git("remote", "add", "origin", "https://github.com/o/r")
	r.Git("config", "url."+host.Dir+".insteadOf", "https://github.com/o/r")
	list := `{"data":{"repository":{"pullRequests":{"totalCount":1,"nodes":[{"number":7,"title":"Add a thing",
"url":"https://github.com/o/r/pull/7","isDraft":false,"state":"OPEN","author":{"login":"ann"},"headRefName":"thing",
"baseRefName":"main","updatedAt":"2026-11-13T16:04:48Z","reviewDecision":"REVIEW_REQUIRED","headRepositoryOwner":{"login":"o"},
"additions":2,"deletions":1,"commits":{"nodes":[]}}]}}}}`
	dir, env, log := keepTool(t, list)
	setReview := func(head string) {
		if err := os.WriteFile(filepath.Join(dir, "review.json"), []byte(keepReview(head)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reads := func() int {
		b, _ := os.ReadFile(filepath.Join(dir, "reads"))
		return len(b)
	}
	setReview(head)
	tools := &forgeTools{env: env}
	var hurried atomic.Bool
	h.app.forgeFind = func(k forge.Kind) (*forge.CLI, error) {
		c, err := tools.find(k)
		if err != nil || !hurried.Load() {
			return c, err
		}
		quick := *c
		quick.Timeout = 300 * time.Millisecond
		return &quick, nil
	}
	h.app.Settings.PullRequests = true
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the pull requests in the sidebar", h.shows("Pull Requests"))
	h.click("Pull Requests")
	h.until("the list", h.shows("Add a thing"))
	h.click("Add a thing")
	h.until("its files, fetched", h.shows("a.txt"))
	tb := h.app.current()
	rv := tb.pr.rv
	h.click("a.txt")
	h.until("the thread under its line", h.shows("Why spell it out?"))
	rv.pv.d.OnGutterButton(editor.DiffLineRange{Side: editor.DiffNew, Start: 1, End: 1})
	h.tt.Frame()
	h.until("the comment being written", h.shows("Comment on line 2"))
	h.tt.Type("Half written")
	h.tt.Frame()

	// Pushed again: a file that lists before a.txt, the head moved.
	host.Git("checkout", "-q", "thing")
	host.Write("0.txt", "zero\n")
	moved := host.Commit("more")
	host.Git("update-ref", "refs/pull/7/head", moved)
	host.Git("checkout", "-q", "main")
	setReview(moved)
	h.click("Refresh")
	h.until("the new head compared", func() bool {
		return rv.detail != nil && rv.detail.Files != nil && rv.detail.Files.Len() == 2 && rv.onHead()
	})
	h.tt.Frame()
	if rv.file < 0 || string(rv.detail.Files.Path(rv.file)) != "a.txt" {
		t.Fatalf("chosen %d, not a.txt", rv.file)
	}
	if rv.compose == nil || rv.compose.body != "Half written" || !h.tt.HasText("Written on older commits: it goes on the lines it was written on.") {
		t.Fatalf("compose %+v, texts %q", rv.compose, h.tt.Texts())
	}
	h.until("the thread on the new head", h.shows("Why spell it out?"))
	h.click("Comment Now")
	h.until("the comment sent", func() bool { return strings.Contains(log(), "repos/o/r/pulls/7/reviews") && !rv.sending })
	var sent struct {
		CommitID string `json:"commit_id"`
	}
	l := log()
	line := strings.SplitN(l[strings.Index(l, "repos/o/r/pulls/7/reviews"):], "\n", 3)[1]
	if err := json.Unmarshal([]byte(line), &sent); err != nil || sent.CommitID != head {
		t.Fatalf("sent on %q, want %s: %v", sent.CommitID, head, err)
	}
	h.until("the comment gone", func() bool { return rv.compose == nil && !rv.loading })

	// Asked to read again while a read runs: it reads again after it.
	before := reads()
	rv.reread()
	h.tt.Frame()
	if !rv.loading {
		t.Fatal("not reading")
	}
	rv.reread()
	h.until("read twice", func() bool { return rv.read && !rv.loading && reads() >= before+2 })

	// A reply the host takes too long to answer may have gone: nothing is
	// sent until the pull request is read again.
	hurried.Store(true)
	os.WriteFile(filepath.Join(dir, "slow"), nil, 0o644)
	h.click("Reply…")
	h.tt.Type("Slow reply")
	h.tt.Frame()
	h.click("Reply")
	h.until("told it may have gone", h.shows("GitHub did not answer in time, and may have taken it. The pull request is read again: look for it there before sending it again."))
	os.Remove(filepath.Join(dir, "slow"))
	hurried.Store(false)
	h.until("read again, sending again", func() bool { return !rv.verify && !rv.busy() && rv.read })
	if got := *rv.replies["T1"]; got != "Slow reply" {
		t.Fatalf("reply %q", got)
	}

	// The reply not sent: closing the tab asks first.
	h.app.closeTab(h.app.active)
	h.until("asked", h.shows("Discard and Close"))
	if len(h.app.tabs) != 1 {
		t.Fatalf("closed: %d tabs", len(h.app.tabs))
	}
	h.click("Cancel")
	// Quitting from another tab says what the first loses.
	other := h.repoWith()
	h.app.Open(other.Dir)
	h.until("the second tab", func() bool { return len(h.app.tabs) == 2 && h.app.active == 1 })
	if !h.app.AskToClose(true, func() {}) || h.app.active != 0 || !strings.Contains(h.app.confirm.message, "not sent yet") {
		t.Fatalf("quitting asked %+v, tab %d", h.app.confirm, h.app.active)
	}
	h.until("asked to quit", h.shows("Discard and Quit"))
	h.click("Discard and Quit")
}
