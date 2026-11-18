package ui

import (
	"slices"
	"testing"
	"time"

	"ramus/internal/forge"
	"ramus/internal/git/gittest"
)

// A pull request's page says whether it can be merged, names its labels
// and reviewers, tells what happened to it between what was said, shows
// what was written as its Markdown reads, and lists the checks of its
// latest commit, the failed first, each opening its page.
func TestPullRequestDetails(t *testing.T) {
	host := gittest.Init(t)
	host.Write("a.txt", "1\n")
	host.Commit("base")
	host.Git("checkout", "-q", "-b", "thing")
	host.Write("a.txt", "2\n")
	head := host.Commit("head")
	host.Git("update-ref", "refs/pull/7/head", head)
	host.Git("checkout", "-q", "main")

	h := newHarness(t)
	h.app.Now = func() time.Time { return time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC) }
	r := h.repoWith()
	r.Git("remote", "add", "origin", "https://github.com/o/r")
	r.Git("config", "url."+host.Dir+".insteadOf", "https://github.com/o/r")

	list := `{"data":{"repository":{"pullRequests":{"totalCount":1,"nodes":[{"id":"PR_7","number":7,"title":"Add a thing",
"url":"https://github.com/o/r/pull/7","isDraft":false,"state":"OPEN","author":{"login":"ann"},"headRefName":"thing",
"baseRefName":"main","updatedAt":"2026-11-13T16:04:48Z","headRepositoryOwner":{"login":"o"}}]}}}}`
	review := `{"data":{"viewer":{"login":"me"},"repository":{"pullRequest":{"number":7,"title":"Add a thing",
"url":"https://github.com/o/r/pull/7","body":"Why it **matters** to [us](https://example.com/why), see #5","state":"OPEN","isDraft":false,"author":{"login":"ann"},
"baseRefName":"main","headRefName":"thing","baseRefOid":"","headRefOid":"` + head + `","reviewDecision":"REVIEW_REQUIRED",
"createdAt":"2026-11-10T09:00:00Z","additions":1,"deletions":1,
"mergeable":"CONFLICTING","mergeStateStatus":"DIRTY",
"labels":{"nodes":[{"name":"bug","color":"d73a4a"}]},"assignees":{"nodes":[{"login":"dan"}]},
"reviewRequests":{"nodes":[{"requestedReviewer":{"__typename":"User","login":"bob"}}]},
"latestReviews":{"nodes":[{"author":{"login":"cat"},"state":"APPROVED"}]},
"commits":{"nodes":[{"commit":{"statusCheckRollup":{"state":"FAILURE","contexts":{"totalCount":3,"nodes":[
 {"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"SUCCESS","detailsUrl":"https://github.com/o/r/actions/runs/1/job/1",
  "startedAt":"2026-11-11T10:00:00Z","completedAt":"2026-11-11T10:02:30Z","isRequired":true,"checkSuite":{"workflowRun":{"workflow":{"name":"CI"}}}},
 {"__typename":"CheckRun","name":"test","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://github.com/o/r/actions/runs/1/job/2",
  "startedAt":"2026-11-11T10:00:00Z","completedAt":"2026-11-11T10:01:00Z","isRequired":false,"checkSuite":{"workflowRun":{"workflow":{"name":"CI"}}}},
 {"__typename":"StatusContext","context":"ci/external","state":"PENDING","targetUrl":"javascript:alert(1)","isRequired":false}]}}}}]},
"timelineItems":{"pageInfo":{"hasPreviousPage":false},"nodes":[
 {"__typename":"PullRequestCommit","commit":{"abbreviatedOid":"aaaaaaa","messageHeadline":"First try","committedDate":"2026-11-10T08:00:00Z","author":{"name":"Ann","user":{"login":"ann"}}}},
 {"__typename":"LabeledEvent","actor":{"login":"bot"},"createdAt":"2026-11-10T09:00:01Z","label":{"name":"bug"}},
 {"__typename":"LabeledEvent","actor":{"login":"bot"},"createdAt":"2026-11-10T09:00:02Z","label":{"name":"triage"}},
 {"__typename":"HeadRefForcePushedEvent","actor":{"login":"ann"},"createdAt":"2026-11-10T13:00:00Z","beforeCommit":{"abbreviatedOid":"aaaaaaa"},"afterCommit":{"abbreviatedOid":"bbbbbbb"}},
 {"__typename":"ReviewRequestedEvent","actor":{"login":"ann"},"createdAt":"2026-11-10T13:10:00Z","requestedReviewer":{"__typename":"User","login":"bob"}}]},
"comments":{"totalCount":1,"nodes":[{"id":"IC_1","databaseId":11,"author":{"login":"cat"},"body":"Looks *fine*","createdAt":"2026-11-10T12:00:00Z"}]},
"reviews":{"totalCount":0,"nodes":[]},"reviewThreads":{"totalCount":0,"nodes":[]}}}}}`
	env, _ := reviewTool(t, "gh", "pullRequests(states", "pullRequest(number", list, review)
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

	// Under its title: whether it can be merged, who reviews it and with
	// what verdict, who it is assigned to, and its labels.
	for _, text := range []string{"Conflicts with main", "Reviewers", "bob", "cat", "Assignees", "dan", "bug"} {
		if !h.tt.HasText(text) {
			t.Fatalf("no %q in the header: %q", text, h.tt.Texts())
		}
	}
	for _, says := range []string{"bob, review requested", "cat, approved"} {
		if _, ok := h.tt.Access(says); !ok {
			t.Fatalf("no reviewer read as %q", says)
		}
	}

	// The conversation tells what happened between what was said, in the
	// order it happened, and shows what was written as Markdown reads.
	h.click("Conversation (6)")
	h.until("the description as its Markdown reads", h.shows("Why it matters to us, see #5"))
	rv := h.app.current().pr.rv
	var whats []string
	for _, e := range rv.timeline() {
		whats = append(whats, e.author+" "+e.what)
	}
	want := []string{"ann opened this", "ann added 1 commit", "bot added the labels bug, triage", "cat commented",
		"ann force-pushed, from aaaaaaa to bbbbbbb", "ann requested a review from bob"}
	if !slices.Equal(whats, want) {
		t.Fatalf("conversation %q", whats)
	}
	for _, text := range []string{"added 1 commit", "aaaaaaa", "First try", "added the labels bug, triage", "Looks fine", "force-pushed, from aaaaaaa to bbbbbbb"} {
		if !h.tt.HasText(text) {
			t.Fatalf("no %q in the conversation: %q", text, h.tt.Texts())
		}
	}

	// The checks, the failed first; one opens its page, and one whose
	// address is no page opens nothing.
	h.click("Checks (3)")
	h.until("the checks", func() bool { _, ok := h.tt.Access("test, Failed"); return ok })
	var names []string
	for _, c := range rv.checks {
		names = append(names, c.Name)
	}
	if !slices.Equal(names, []string{"test", "ci/external", "build"}) || rv.checks[1].URL != "" || rv.checks[1].State != forge.CheckRunning {
		t.Fatalf("checks %+v", rv.checks)
	}
	for _, text := range []string{"CI", "Required", "2m30s", "1m0s"} {
		if !h.tt.HasText(text) {
			t.Fatalf("no %q among the checks: %q", text, h.tt.Texts())
		}
	}
	h.click("ci/external, Running")
	h.tt.Frame()
	if got := h.tt.OpenedURLs(); len(got) != 0 {
		t.Fatalf("opened %q", got)
	}
	h.click("test, Failed")
	h.until("its page opened", func() bool {
		return slices.Contains(h.tt.OpenedURLs(), "https://github.com/o/r/actions/runs/1/job/2")
	})
}
