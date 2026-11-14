package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/forge"
	"ramus/internal/git/redact"
	"ramus/internal/session"
	"ramus/locales"
)

// pullsMaxAge is how old the pull requests shown may be before the
// branches view, shown again, asks again. Nothing asks on a timer.
const pullsMaxAge = 5 * time.Minute

// pullsState is what the hosts said of a tab's branches' pull requests.
type pullsState struct {
	by map[string]session.PullInfo
	// key is the branches and upstreams asked about, at when.
	key     string
	at      time.Time
	loading bool
}

// forgeTools finds the user's gh and glab once each; find runs off the
// main thread.
type forgeTools struct {
	mu    sync.Mutex
	env   []string
	found map[forge.Kind]forgeFound
}

type forgeFound struct {
	c   *forge.CLI
	err error
}

func (f *forgeTools) find(k forge.Kind) (*forge.CLI, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if got, ok := f.found[k]; ok {
		return got.c, got.err
	}
	c, err := forge.Find(f.env, k)
	if f.found == nil {
		f.found = map[forge.Kind]forgeFound{}
	}
	f.found[k] = forgeFound{c, err}
	return c, err
}

// findForge is how the app finds a host's tool: through the user's PATH,
// or what a test gives.
func (a *App) findForge() func(forge.Kind) (*forge.CLI, error) {
	if a.forgeFind != nil {
		return a.forgeFind
	}
	if a.forges == nil {
		a.forges = &forgeTools{env: a.Runner.Environ()}
	}
	return a.forges.find
}

// pullsKey names the branches that track a remote branch, and what, and
// the remotes: one named upstream added changes where they are asked.
func pullsKey(info *session.RefInfo) string {
	var b strings.Builder
	for _, br := range info.Branches {
		if br.Upstream != "" && !br.Gone {
			b.WriteString(br.Name)
			b.WriteByte(0)
			b.WriteString(br.Upstream)
			b.WriteByte(0)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	for _, rm := range info.Remotes {
		b.WriteString(rm.Name)
		b.WriteByte(0)
		b.WriteString(rm.URL)
		b.WriteByte(0)
		b.WriteString(rm.PushURL)
		b.WriteByte(0)
	}
	return b.String()
}

// loadPulls asks the hosts about the branches' pull requests, when the
// settings turn it on and what is shown is for other branches or old.
func (a *App) loadPulls(t *tab) {
	ps, info := &t.pulls, t.refs.info
	if !a.Settings.PullRequests || info == nil || info.Err != nil || ps.loading || a.Runner == nil && a.forgeFind == nil {
		return
	}
	a.findStackTool()
	key, now := pullsKey(info), a.Now()
	if key == ps.key && now.Sub(ps.at) < pullsMaxAge {
		return
	}
	if key == "" {
		ps.key, ps.at, ps.by = key, now, nil
		return
	}
	ps.loading = true
	t.s.LoadPulls(a.findForge(), info, func(res session.PullsResult) {
		ps.loading = false
		ps.key, ps.at, ps.by = key, a.Now(), res.Pulls
		if res.Err != nil {
			a.forgeNotice(res.Err)
		}
	})
}

// forgeNotice says once in the app's run why pull requests are not
// shown: a tool missing, a login missing or expired, or what else went
// wrong. The views ask again later, and say nothing.
func (a *App) forgeNotice(err error) {
	text := errText(err)
	var le *forge.LoginError
	switch {
	case errors.As(err, &le):
	case errors.Is(err, forge.ErrNoCLI):
		text = locales.PullsNoCLI()
	default:
		text = locales.PullsNotRead(text)
	}
	if a.forgeWarned == nil {
		a.forgeWarned = map[string]bool{}
	}
	if !a.forgeWarned[text] {
		a.forgeWarned[text] = true
		a.notice = text
	}
}

// pullBadge shows a branch's pull request in its row: its number, which
// opens it, its state, and an open one's checks.
func pullBadge(c *ui.Context, p session.PullInfo) {
	th := c.Theme()
	ref := p.Ref(p.Repo.Kind)
	ui.Link(c, ref, p.URL).FontSize(textSmall).Tooltip(redact.Control(p.Title))
	pullState(c, p.Pull)
	if p.State != forge.PullOpen {
		return
	}
	mark, color, says := "", th.TextMuted, ""
	switch p.Checks {
	case forge.ChecksPassing:
		mark, color, says = "✓", th.Success, locales.PullsChecksPassed()
	case forge.ChecksFailing:
		mark, color, says = "✗", th.Danger, locales.PullsChecksFailed()
	case forge.ChecksPending:
		mark, says = "●", locales.PullsChecksRunning()
	}
	if mark != "" {
		ui.Text(c, mark).FontSize(textSmall).TextColor(color).Label(says).Tooltip(says)
	}
}

// pullState names a pull request's state in the colors the hosts give
// it: open in green, merged in the accent, closed muted, and an open
// draft as a draft.
func pullState(c *ui.Context, p forge.Pull) {
	th := c.Theme()
	text, color := locales.PullsStateOpen(), th.Success
	switch {
	case p.State == forge.PullMerged:
		text, color = locales.PullsStateMerged(), th.Accent
	case p.State == forge.PullClosed:
		text, color = locales.PullsStateClosed(), th.TextMuted
	case p.Draft:
		text, color = locales.PullsDraft(), th.TextMuted
	}
	ui.Text(c, text).FontSize(textSmall).TextColor(color).SingleLine()
}

// pullItems are a branch's menu items for its pull requests on the web:
// the one open, the branch's list, and a new one. They need no tool.
func (a *App) pullItems(m *ui.Menu, t *tab, b session.BranchInfo, act *func()) {
	info := t.refs.info
	if info == nil {
		return
	}
	repo, name, ok := session.BranchRepo(b, info.Remotes)
	if !ok {
		return
	}
	m.Separator()
	kind := "github"
	if repo.Kind == forge.GitLab {
		kind = "gitlab"
	}
	if p, ok := t.pulls.by[b.Name]; ok && m.Item(locales.PullsOpen(kind, p.Ref(repo.Kind))).Chosen() {
		a.openURL = p.URL
	}
	pushed := b.Upstream != "" && !b.Gone
	if m.Item(locales.PullsShowInBrowser(kind)).Disabled(!pushed).Chosen() {
		// A fork's are in the repository it was forked from.
		a.openURL = session.PullsRepo(repo, info.Remotes).BranchPulls(name)
	}
	if m.Item(locales.PullsCreateInBrowser(kind)).Disabled(!pushed).Chosen() {
		a.openURL = repo.NewPull(name)
	}
	a.stackItems(m, t, b, repo, act)
}

// stackTool is the user's gh with the stack extension, looked for once
// pull requests are turned on.
type stackTool struct {
	asked, ok bool
	c         *forge.CLI
}

// findStackTool looks, off the main thread, for gh stack.
func (a *App) findStackTool() {
	if a.ghStack.asked {
		return
	}
	a.ghStack.asked = true
	find, update := a.findForge(), a.Update
	a.goWork(nil, func() {
		c, err := find(forge.GitHub)
		ok := false
		if err == nil {
			_, ok = c.HasStack(context.Background())
		}
		update(func() { a.ghStack.c, a.ghStack.ok = c, ok })
	})
}

// stackBadge shows where a branch is in the GitHub stack gh stack keeps,
// and its pull request there when the host was not asked.
func (a *App) stackBadge(c *ui.Context, t *tab, info *session.RefInfo, b session.BranchInfo) {
	th := c.Theme()
	short := strings.TrimPrefix(b.Name, "refs/heads/")
	st, i, ok := forge.StackOf(info.GitHubStacks, short)
	if !ok {
		return
	}
	what := locales.PullsStackBranch(i+1, len(st.Branches))
	if st.Number > 0 {
		what = locales.PullsStackBranchNumber(i+1, len(st.Branches), st.Number)
	}
	ui.Text(c, locales.PullsStackPosition(i+1, len(st.Branches))).FontSize(textSmall).TextColor(th.TextMuted).SingleLine().Label(what).Tooltip(what)
	if _, asked := t.pulls.by[b.Name]; !asked || !a.Settings.PullRequests {
		// The file is in the git directory, which a repository handed over
		// as a folder brings along: its link goes to the remote's host only.
		repo, _, isOn := session.BranchRepo(b, info.Remotes)
		if br := st.Branches[i]; br.PR > 0 && br.URL != "" && !br.Merged && isOn && repo.Kind == forge.GitHub {
			ui.Link(c, fmt.Sprintf("#%d", br.PR), session.PullsRepo(repo, info.Remotes).PullURL(br.URL, br.PR)).FontSize(textSmall)
		}
	}
}

// stackItems are a branch's menu items for GitHub stacks, when gh has
// the stack extension: make a stack of the branches stacked here, or
// push the stack it is in and make or update its pull requests. gh stack
// works on the stack of the branch checked out.
func (a *App) stackItems(m *ui.Menu, t *tab, b session.BranchInfo, repo forge.Repo, act *func()) {
	info := t.refs.info
	if !a.ghStack.ok || repo.Kind != forge.GitHub || info == nil {
		return
	}
	ro := t.s.Queue == nil
	c := a.ghStack.c
	short := strings.TrimPrefix(b.Name, "refs/heads/")
	if st, _, ok := forge.StackOf(info.GitHubStacks, short); ok {
		head := t.s.State.Head
		cur, _, onIt := forge.StackOf(info.GitHubStacks, strings.TrimPrefix(head.Target, "refs/heads/"))
		onIt = onIt && !head.Detached && cur.Branches[0].Name == st.Branches[0].Name
		n := len(st.Branches)
		if m.Item(locales.PullsSubmitStackItem()).Disabled(ro || !onIt).Chosen() {
			*act = func() {
				t.refs.ask = confirmState{open: true, title: locales.PullsSubmitStackTitle(),
					message: locales.PullsSubmitStackMessage(n),
					button:  locales.PullsSubmit(),
					run:     func() { a.runStack(t, c, locales.PullsSubmitStackJob(), "submit", "--auto") }}
			}
		}
		if m.Item(locales.PullsPushStackItem()).Disabled(ro || !onIt).Chosen() {
			*act = func() { a.runStack(t, c, locales.PullsPushStackJob(), "push") }
		}
		return
	}
	local := info.StackOf(b.Name)
	if len(local) < 2 {
		return
	}
	names := make([]string, len(local))
	for i, n := range local {
		names[i] = strings.TrimPrefix(n, "refs/heads/")
		if _, _, in := forge.StackOf(info.GitHubStacks, names[i]); in {
			return
		}
	}
	if m.Item(locales.PullsCreateStackItem(len(names))).Disabled(ro).Chosen() {
		*act = func() {
			top := names[len(names)-1]
			t.refs.ask = confirmState{open: true, title: locales.PullsCreateStackTitle(),
				message: locales.PullsCreateStackMessage(strings.Join(names, ", "), top),
				button:  locales.CommonCreate(),
				run:     func() { a.runStack(t, c, locales.PullsCreateStackJob(), append([]string{"init", "--"}, names...)...) }}
		}
	}
}

func (a *App) runStack(t *tab, c *forge.CLI, name string, args ...string) {
	if _, err := t.s.GitHubStack(c, name, args...); err != nil {
		a.notice = errText(err)
	}
}
