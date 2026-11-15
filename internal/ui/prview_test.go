package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui/editor"

	"ramus/internal/git/gittest"
)

// reviewGH is a gh that answers the list and the pull request from
// files, logs each write with its input, and answers {} to the rest.
func reviewGH(t *testing.T, list, review string) (env []string, log func() string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake gh is a shell script")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "list.json"), []byte(list), 0o644)
	os.WriteFile(filepath.Join(dir, "review.json"), []byte(review), 0o644)
	script := `#!/bin/sh
case "$*" in
*"pullRequests(states"*) cat '` + dir + `/list.json' ;;
*"pullRequest(number"*) cat '` + dir + `/review.json' ;;
*POST*|*ReviewThread*) printf '%s\n' "$*" >> '` + dir + `/log'; cat >> '` + dir + `/log'; echo >> '` + dir + `/log'; echo '{}' ;;
*) echo '{}' ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{"PATH=" + dir + ":/usr/bin:/bin"}, func() string {
		b, _ := os.ReadFile(filepath.Join(dir, "log"))
		return string(b)
	}
}

// A pull request is listed, opened, fetched and diffed locally, its
// threads shown under their lines; a reply, a resolve, a comment on a
// line and a review with a verdict go to the host.
func TestPullRequestReview(t *testing.T) {
	host := gittest.Init(t)
	host.Write("a.txt", "1\n2\n3\n")
	host.Commit("base")
	host.Git("checkout", "-q", "-b", "thing")
	host.Write("a.txt", "1\nTWO\n3\n")
	host.Write("b.go", "package b\n")
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
"commits":{"nodes":[]}}]}}}}`
	review := `{"data":{"viewer":{"login":"me"},"repository":{"pullRequest":{"number":7,"title":"Add a thing",
"url":"https://github.com/o/r/pull/7","body":"<!-- template -->Why it matters","state":"OPEN","isDraft":false,"author":{"login":"ann"},
"baseRefName":"main","headRefName":"thing","baseRefOid":"","headRefOid":"` + head + `","reviewDecision":"REVIEW_REQUIRED",
"createdAt":"2026-11-10T09:00:00Z","commits":{"nodes":[]},
"comments":{"totalCount":0,"nodes":[]},"reviews":{"totalCount":0,"nodes":[]},
"reviewThreads":{"totalCount":2,"nodes":[
 {"id":"T1","isResolved":false,"isOutdated":false,"path":"a.txt","line":2,"startLine":null,"originalLine":2,"diffSide":"RIGHT",
  "viewerCanResolve":true,"viewerCanUnresolve":false,"viewerCanReply":true,
  "comments":{"totalCount":1,"nodes":[{"id":"C1","databaseId":42,"author":{"login":"bob"},"body":"Why spell it out?","createdAt":"2026-11-11T10:00:00Z"}]}},
 {"id":"T2","isResolved":false,"isOutdated":true,"path":"b.go","line":null,"startLine":null,"originalLine":3,"diffSide":"RIGHT",
  "viewerCanResolve":true,"viewerCanUnresolve":false,"viewerCanReply":true,
  "comments":{"totalCount":1,"nodes":[{"id":"C2","databaseId":43,"author":{"login":"bob"},"body":"Old remark","createdAt":"2026-11-11T11:00:00Z"}]}}]}}}}}`
	env, log := reviewGH(t, list, review)
	tools := &forgeTools{env: env}
	h.app.forgeFind = tools.find
	h.app.Settings.PullRequests = true
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the pull requests in the sidebar", h.shows("Pull Requests"))
	h.click("Pull Requests")
	h.until("the list", h.shows("Add a thing"))
	h.click("Add a thing")
	h.until("the pull request", h.shows("ann wants to merge thing into main"))
	h.until("its files, fetched", func() bool { return h.tt.HasText("a.txt") && h.tt.HasText("b.go") })
	if got := strings.TrimSpace(r.Git("rev-parse", "refs/ramus/pulls/7/head")); got != head {
		t.Fatalf("fetched %s, want %s", got, head)
	}

	rv := h.app.current().pr.rv
	// The fake gh logs a write before it answers; the next click waits for
	// the answer.
	answered := func(what string) func() bool {
		return func() bool { return strings.Contains(log(), what) && !rv.sending }
	}
	h.click("a.txt")
	h.until("the thread under its line", h.shows("Why spell it out?"))
	h.click("Reply…")
	h.tt.Type("To be clear")
	h.tt.Frame()
	h.click("Reply")
	h.until("the reply sent", answered("repos/o/r/pulls/7/comments/42/replies"))
	if !strings.Contains(log(), `{"body":"To be clear"}`) {
		t.Fatalf("reply %q", log())
	}
	h.click("Resolve")
	h.until("the thread resolved", answered("resolveReviewThread"))

	// The gutter's + on the changed line opens a comment there.
	rv.pv.d.OnGutterButton(editor.DiffLineRange{Side: editor.DiffNew, Start: 1, End: 1})
	h.tt.Frame()
	h.until("the comment being written", h.shows("Comment on line 2"))
	h.tt.Type("Nice")
	h.tt.Frame()
	h.click("Add to Review")
	h.until("the pending comment", h.shows("Pending"))

	h.click("Submit Review (1)…")
	h.until("the dialog", h.shows("Submit your review of #7"))
	h.tt.Type("Good work")
	h.tt.Frame()
	h.click("Approve")
	h.click("Submit")
	h.until("the review sent", answered("repos/o/r/pulls/7/reviews"))
	l := log()
	i := strings.Index(l, "repos/o/r/pulls/7/reviews")
	var sent struct {
		CommitID string `json:"commit_id"`
		Event    string
		Body     string
		Comments []struct {
			Path string
			Line int
			Side string
			Body string
		}
	}
	line := strings.SplitN(l[i:], "\n", 3)[1]
	if err := json.Unmarshal([]byte(line), &sent); err != nil {
		t.Fatalf("%v: %q", err, line)
	}
	if sent.CommitID != head || sent.Event != "APPROVE" || sent.Body != "Good work" || len(sent.Comments) != 1 ||
		sent.Comments[0].Path != "a.txt" || sent.Comments[0].Line != 2 || sent.Comments[0].Side != "RIGHT" || sent.Comments[0].Body != "Nice" {
		t.Fatalf("sent %+v", sent)
	}
	h.until("the drafts gone", func() bool { return len(rv.drafts) == 0 && !rv.submit.open })

	h.click("Conversation (2)")
	h.until("the description, its template comment hidden", h.shows("Why it matters"))
	if !h.tt.HasText("Old remark") || h.tt.HasText("template") {
		t.Fatalf("texts %q", h.tt.Texts())
	}
}
