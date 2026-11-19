package ui

import (
	"encoding/json"
	"runtime"
	"strings"
	"testing"

	"ramus/internal/git/gittest"
)

// A long comment under a line costs a frame of the Files tab no more
// than a short one: its text is made ready to show and measured once,
// not in every frame, which a long review would make slow throughout.
func TestFilesTabNotesReadOnce(t *testing.T) {
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

	long := "Start of a long remark\n" + strings.Repeat("a word or two on the line\n", 80000)
	body, _ := json.Marshal(long)
	list := `{"data":{"repository":{"pullRequests":{"totalCount":1,"nodes":[{"id":"PR_7","number":7,"title":"Add a thing",
"url":"https://github.com/o/r/pull/7","isDraft":false,"state":"OPEN","author":{"login":"ann"},"headRefName":"thing",
"baseRefName":"main","updatedAt":"2026-11-13T16:04:48Z","headRepositoryOwner":{"login":"o"}}]}}}}`
	review := `{"data":{"viewer":{"login":"me"},"repository":{"pullRequest":{"number":7,"title":"Add a thing",
"url":"https://github.com/o/r/pull/7","body":"","state":"OPEN","isDraft":false,"author":{"login":"ann"},
"baseRefName":"main","headRefName":"thing","baseRefOid":"","headRefOid":"` + head + `","reviewDecision":"REVIEW_REQUIRED",
"createdAt":"2026-11-10T09:00:00Z","additions":1,"deletions":1,"commits":{"nodes":[]},
"comments":{"totalCount":0,"nodes":[]},"reviews":{"totalCount":0,"nodes":[]},
"reviewThreads":{"totalCount":1,"nodes":[
 {"id":"T1","isResolved":false,"isOutdated":false,"path":"a.txt","line":2,"startLine":null,"originalLine":2,"diffSide":"RIGHT",
  "viewerCanResolve":true,"viewerCanUnresolve":false,"viewerCanReply":true,
  "comments":{"totalCount":1,"nodes":[{"id":"C1","databaseId":42,"author":{"login":"bob"},"body":` + string(body) + `,"createdAt":"2026-11-11T10:00:00Z"}]}}]}}}}}`
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
	h.until("its files, fetched", h.shows("a.txt"))
	h.click("a.txt")
	h.until("the thread under its line", func() bool {
		return strings.Contains(strings.Join(h.tt.Texts(), "\n"), "Start of a long remark")
	})
	for range 3 {
		h.tt.Frame()
	}

	const frames = 5
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range frames {
		h.tt.Frame()
	}
	runtime.ReadMemStats(&after)
	per := (after.TotalAlloc - before.TotalAlloc) / frames
	t.Logf("a frame allocates %d bytes under a comment of %d", per, len(long))
	if per > uint64(len(long))/4 {
		t.Fatalf("a frame allocates %d bytes under a comment of %d", per, len(long))
	}
}
