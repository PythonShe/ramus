package ui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/editor"

	"ramus/internal/git/gittest"
)

// reviewTool is a gh or glab, named prog, that answers the list and
// the pull request from files, as the queries holding listQ and reviewQ,
// logs each write with its input, and answers {"id":1} to the rest.
func reviewTool(t *testing.T, prog, listQ, reviewQ, list, review string) (env []string, log func() string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the fake tool is a shell script")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "list.json"), []byte(list), 0o644)
	os.WriteFile(filepath.Join(dir, "review.json"), []byte(review), 0o644)
	script := `#!/bin/sh
case "$*" in
*"` + listQ + `"*) cat '` + dir + `/list.json' ;;
*"` + reviewQ + `"*) cat '` + dir + `/review.json' ;;
*POST*|*PUT*|*ReviewThread*) printf '%s\n' "$*" >> '` + dir + `/log'; cat >> '` + dir + `/log'; echo >> '` + dir + `/log'; echo '{"id":1}' ;;
*) echo '{}' ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, prog), []byte(script), 0o755); err != nil {
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
"additions":12,"deletions":3,"commits":{"nodes":[]}}]}}}}`
	review := `{"data":{"viewer":{"login":"me"},"repository":{"pullRequest":{"number":7,"title":"Add a thing",
"url":"https://github.com/o/r/pull/7","body":"<!-- template -->Why it matters","state":"OPEN","isDraft":false,"author":{"login":"ann"},
"baseRefName":"main","headRefName":"thing","baseRefOid":"","headRefOid":"` + head + `","reviewDecision":"REVIEW_REQUIRED",
"createdAt":"2026-11-10T09:00:00Z","additions":2,"deletions":1,"commits":{"nodes":[]},
"comments":{"totalCount":0,"nodes":[]},"reviews":{"totalCount":0,"nodes":[]},
"reviewThreads":{"totalCount":2,"nodes":[
 {"id":"T1","isResolved":false,"isOutdated":false,"path":"a.txt","line":2,"startLine":null,"originalLine":2,"diffSide":"RIGHT",
  "viewerCanResolve":true,"viewerCanUnresolve":false,"viewerCanReply":true,
  "comments":{"totalCount":1,"nodes":[{"id":"C1","databaseId":42,"author":{"login":"bob"},"body":"Why spell it out?","createdAt":"2026-11-11T10:00:00Z"}]}},
 {"id":"T2","isResolved":false,"isOutdated":true,"path":"b.go","line":null,"startLine":null,"originalLine":3,"diffSide":"RIGHT",
  "viewerCanResolve":true,"viewerCanUnresolve":false,"viewerCanReply":true,
  "comments":{"totalCount":1,"nodes":[{"id":"C2","databaseId":43,"author":{"login":"bob"},"body":"Old remark","createdAt":"2026-11-11T11:00:00Z"}]}}]}}}}}`
	env, log := reviewTool(t, "gh", "pullRequests(states", "pullRequest(number", list, review)
	tools := &forgeTools{env: env}
	h.app.forgeFind = tools.find
	h.app.Settings.PullRequests = true
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the pull requests in the sidebar", h.shows("Pull Requests"))
	h.click("Pull Requests")
	h.until("the list", h.shows("Add a thing"))
	// The lines it changes, as GitHub counts them.
	if !h.tt.HasText("+12") || !h.tt.HasText("−3") {
		t.Fatalf("list %q", h.tt.Texts())
	}
	h.click("Add a thing")
	h.until("the pull request", h.shows("ann wants to merge thing into main"))
	h.until("its files, fetched", func() bool { return h.tt.HasText("a.txt") && h.tt.HasText("b.go") })
	// Each file's lines, as git counts them in the commits fetched.
	h.until("its files counted", func() bool {
		_, ok := h.tt.Access("modified a.txt, 1 line added, 1 deleted")
		return ok
	})
	if _, ok := h.tt.Access("added b.go, 1 line added, 0 deleted"); !ok || !h.tt.HasText("+2") {
		t.Fatalf("texts %q", h.tt.Texts())
	}
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
	// A second, on line 1, deleted: the other stays, under its line.
	rv.pv.d.OnGutterButton(editor.DiffLineRange{Side: editor.DiffNew, Start: 0, End: 0})
	h.tt.Frame()
	h.until("the second comment being written", h.shows("Comment on line 1"))
	h.tt.Type("Extra")
	h.tt.Frame()
	h.click("Add to Review")
	h.until("both pending", h.shows("Submit Review (2)…"))
	h.click("Delete")
	h.until("one deleted", h.shows("Submit Review (1)…"))
	if len(rv.drafts) != 1 {
		t.Fatalf("drafts %+v", rv.drafts)
	}
	kept, gone := rv.drafts[0], "Nice"
	if kept.Body == gone {
		gone = "Extra"
	}
	if !h.tt.HasText(kept.Body) || h.tt.HasText(gone) {
		t.Fatalf("kept %+v, texts %q", kept, h.tt.Texts())
	}

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
		sent.Comments[0].Path != "a.txt" || sent.Comments[0].Line != kept.Line || sent.Comments[0].Side != "RIGHT" || sent.Comments[0].Body != kept.Body {
		t.Fatalf("sent %+v", sent)
	}
	h.until("the drafts gone", func() bool { return len(rv.drafts) == 0 && !rv.submit.open })

	h.click("Conversation (2)")
	h.until("the description, its template comment hidden", h.shows("Why it matters"))
	if !h.tt.HasText("Old remark") || h.tt.HasText("template") {
		t.Fatalf("texts %q", h.tt.Texts())
	}

	// Left as its commits are fetched, it fetches them again when shown
	// again, not stays fetching.
	tb := h.app.current()
	rv.detail, rv.fetchedFor = nil, ""
	h.app.fetchReview(tb, rv)
	h.app.leaveView(tb, sidePulls)
	if rv.fetching || rv.read {
		t.Fatalf("left fetching %v, read %v", rv.fetching, rv.read)
	}
	h.until("fetched again", func() bool { return rv.detail != nil && !rv.fetching })

	// Comments not sent are not dropped without asking.
	h.click("Files (2)")
	h.click("a.txt")
	rv.pv.d.OnGutterButton(editor.DiffLineRange{Side: editor.DiffNew, Start: 1, End: 1})
	h.tt.Frame()
	h.until("the comment being written", h.shows("Comment on line 2"))
	h.tt.Type("Unsent")
	h.tt.Frame()
	h.click("Add to Review")
	h.until("pending", h.shows("Submit Review (1)…"))
	h.tt.Key(0, ui.KeyEscape)
	h.until("asked", h.shows("Discard 1 unsent comment?"))
	h.click("Discard")
	h.until("the list", func() bool { return tb.pr.rv == nil })
}

// A merge request is reviewed as a pull request is: its threads under
// their lines, and a comment on an unchanged line placed by both its
// numbers on the commits GitLab diffs.
func TestMergeRequestReview(t *testing.T) {
	host := gittest.Init(t)
	host.Write("a.txt", "1\n2\n3\n")
	base := host.Commit("base")
	host.Git("checkout", "-q", "-b", "thing")
	host.Write("a.txt", "1\nTWO\n3\n")
	head := host.Commit("head")
	host.Git("update-ref", "refs/merge-requests/12/head", head)
	host.Git("checkout", "-q", "main")

	h := newHarness(t)
	r := h.repoWith()
	r.Git("remote", "add", "origin", "https://gitlab.com/g/p")
	r.Git("config", "url."+host.Dir+".insteadOf", "https://gitlab.com/g/p")

	list := `{"data":{"project":{"mergeRequests":{"count":1,"nodes":[{"iid":"12","title":"Add a thing",
"webUrl":"https://gitlab.com/g/p/-/merge_requests/12","draft":false,"state":"opened","author":{"username":"ann"},
"sourceBranch":"thing","targetBranch":"main","updatedAt":"2026-11-13T16:04:48Z","approved":false,"detailedMergeStatus":"NOT_APPROVED",
"diffStatsSummary":{"additions":40,"deletions":2}}]}}}}`
	review := `{"data":{"currentUser":{"username":"me"},"project":{"mergeRequest":{"iid":"12","title":"Add a thing",
"webUrl":"https://gitlab.com/g/p/-/merge_requests/12","description":"Why it matters","state":"opened","draft":false,"author":{"username":"ann"},
"sourceBranch":"thing","targetBranch":"main","createdAt":"2026-11-10T09:00:00Z","approved":false,"detailedMergeStatus":"NOT_APPROVED",
"diffRefs":{"baseSha":"` + base + `","startSha":"` + base + `","headSha":"` + head + `"},"userPermissions":{"createNote":true},
"approvedBy":{"nodes":[]},"notes":{"pageInfo":{"hasNextPage":false},"nodes":[
 {"id":"gid://gitlab/Note/2","author":{"username":"bob"},"body":"Why spell it out?","createdAt":"2026-11-11T10:00:00Z","system":false,
  "discussion":{"id":"gid://gitlab/Discussion/bb","resolvable":true,"resolved":false,"userPermissions":{"resolveNote":true}},
  "position":{"positionType":"text","filePath":"a.txt","oldPath":"a.txt","newPath":"a.txt","oldLine":null,"newLine":2,"diffRefs":{"headSha":"` + head + `"}}}]}}}}}`
	env, log := reviewTool(t, "glab", "mergeRequests(state", "mergeRequest(iid", list, review)
	tools := &forgeTools{env: env}
	h.app.forgeFind = tools.find
	h.app.Settings.PullRequests = true
	h.trust.Set(r.GitDir(), true)
	h.app.Open(r.Dir)
	h.until("the merge requests in the sidebar", h.shows("Merge Requests"))
	h.click("Merge Requests")
	h.until("the list", h.shows("!12"))
	if !h.tt.HasText("+40") || !h.tt.HasText("−2") {
		t.Fatalf("list %q", h.tt.Texts())
	}
	h.click("Add a thing")
	h.until("the merge request", h.shows("!12 Add a thing"))
	h.until("its files, fetched", h.shows("a.txt"))
	if got := strings.TrimSpace(r.Git("rev-parse", "refs/ramus/pulls/12/head")); got != head {
		t.Fatalf("fetched %s, want %s", got, head)
	}
	rv := h.app.current().pr.rv
	answered := func(what string) func() bool {
		return func() bool { return strings.Contains(log(), what) && !rv.sending }
	}
	h.click("a.txt")
	h.until("the thread under its line", h.shows("Why spell it out?"))
	h.click("Resolve")
	h.until("the thread resolved", answered("--method PUT projects/g%2Fp/merge_requests/12/discussions/bb"))

	// Line 1 is unchanged: GitLab takes it by both its numbers.
	rv.pv.d.OnGutterButton(editor.DiffLineRange{Side: editor.DiffNew, Start: 0, End: 0})
	h.tt.Frame()
	h.until("the comment being written", h.shows("Comment on line 1"))
	h.tt.Type("Nice")
	h.tt.Frame()
	h.click("Add to Review")
	h.click("Submit Review (1)…")
	h.until("the dialog", h.shows("Submit your review of !12"))
	h.click("Request Changes")
	h.click("Submit")
	h.until("the review published", answered("draft_notes/bulk_publish"))
	l := log()
	i := strings.Index(l, "merge_requests/12/draft_notes --input -")
	if i < 0 {
		t.Fatalf("log %q", l)
	}
	var dn struct {
		Note     string
		Position map[string]any
	}
	if err := json.Unmarshal([]byte(strings.SplitN(l[i:], "\n", 3)[1]), &dn); err != nil {
		t.Fatal(err)
	}
	if p := dn.Position; dn.Note != "Nice" || p["new_line"] != 1.0 || p["old_line"] != 1.0 || p["new_path"] != "a.txt" ||
		p["base_sha"] != base || p["start_sha"] != base || p["head_sha"] != head {
		t.Fatalf("draft %+v", dn)
	}
	if !strings.Contains(l, "bulk_publish --input -\n{\"reviewer_state\":\"requested_changes\"}") {
		t.Fatalf("publish %q", l)
	}
}
