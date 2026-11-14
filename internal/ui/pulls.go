package ui

import (
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"

	"ramus/internal/forge"
	"ramus/internal/git/redact"
	"ramus/internal/session"
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

// pullsKey names the branches that track a remote branch, and what.
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
	return b.String()
}

// loadPulls asks the hosts about the branches' pull requests, when the
// settings turn it on and what is shown is for other branches or old.
func (a *App) loadPulls(t *tab) {
	ps, info := &t.pulls, t.refs.info
	if !a.Settings.PullRequests || info == nil || info.Err != nil || ps.loading || a.Runner == nil && a.forgeFind == nil {
		return
	}
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
		text = "Pull requests need GitHub's gh or GitLab's glab, logged in; neither was found on the PATH"
	default:
		text = "Pull requests not read: " + text
	}
	if a.forgeWarned == nil {
		a.forgeWarned = map[string]bool{}
	}
	if !a.forgeWarned[text] {
		a.forgeWarned[text] = true
		a.notice = text
	}
}

// pullBadge shows a branch's open pull request in its row: its number,
// which opens it, and its checks.
func pullBadge(c *ui.Context, p session.PullInfo) {
	th := c.Theme()
	ref := p.Ref(p.Repo.Kind)
	ui.Link(c, ref, p.URL).FontSize(12).Tooltip(redact.Control(p.Title))
	if p.Draft {
		ui.Text(c, "draft").FontSize(12).TextColor(th.TextMuted)
	}
	mark, color := "", th.TextMuted
	switch p.Checks {
	case forge.ChecksPassing:
		mark, color = "✓", th.Success
	case forge.ChecksFailing:
		mark, color = "✗", th.Danger
	case forge.ChecksPending:
		mark = "●"
	}
	if mark != "" {
		ui.Text(c, mark).FontSize(12).TextColor(color).Label(p.Checks.String()).Tooltip(p.Checks.String())
	}
}

// pullItems are a branch's menu items for its pull requests on the web:
// the one open, the branch's list, and a new one. They need no tool.
func (a *App) pullItems(m *ui.Menu, t *tab, b session.BranchInfo) {
	info := t.refs.info
	if info == nil {
		return
	}
	repo, name, ok := session.BranchRepo(b, info.Remotes)
	if !ok {
		return
	}
	m.Separator()
	what := repo.Name()
	if p, ok := t.pulls.by[b.Name]; ok && m.Item("Open "+what+" "+p.Ref(repo.Kind)).Chosen() {
		a.openURL = p.URL
	}
	pushed := b.Upstream != "" && !b.Gone
	if m.Item("Show " + what + "s in Browser").Disabled(!pushed).Chosen() {
		a.openURL = repo.BranchPulls(name)
	}
	if m.Item("Create " + what + " in Browser").Disabled(!pushed).Chosen() {
		a.openURL = repo.NewPull(name)
	}
}
