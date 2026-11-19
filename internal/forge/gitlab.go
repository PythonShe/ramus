package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// gitlabQuery asks for the merge requests from branches into the project
// at path, in one query, as GitLab takes a list of source branches: the
// open ones (o), with their pipelines, and those in every state, latest
// first (d), and where the next page of each starts.
func gitlabQuery(path string, branches []string) string {
	return gitlabPageQuery(path, branches, glCursor{}, glCursor{})
}

// glCursor is where a list of merge requests is read on from: after the
// cursor End when Next; with neither, the first page.
type glCursor struct {
	Next bool   `json:"hasNextPage"`
	End  string `json:"endCursor"`
}

// gitlabPageQuery is gitlabQuery for the pages after open and all: a
// list read to its end is not asked for again.
func gitlabPageQuery(path string, branches []string, open, all glCursor) string {
	var list strings.Builder
	for i, br := range branches {
		if i > 0 {
			list.WriteByte(',')
		}
		list.WriteString(literal(br))
	}
	after := func(p glCursor) string {
		if p.End == "" {
			return ""
		}
		return ",after:" + literal(p.End)
	}
	var b strings.Builder
	b.WriteString("query{project(fullPath:")
	b.WriteString(literal(path))
	b.WriteString("){")
	if open.Next || open.End == "" {
		// Newest first, as the d list is latest first: one of the head's
		// made or changed since a lookup is on the first page (searched).
		b.WriteString("o:mergeRequests(state:opened,sort:CREATED_DESC,first:100" + after(open) + ",sourceBranches:[")
		b.WriteString(list.String())
		b.WriteString("]){nodes{...m headPipeline{status}} pageInfo{hasNextPage endCursor}}")
	}
	if all.Next || all.End == "" {
		b.WriteString("d:mergeRequests(sort:UPDATED_DESC,first:100" + after(all) + ",sourceBranches:[")
		b.WriteString(list.String())
		b.WriteString("]){nodes{...m} pageInfo{hasNextPage endCursor}}")
	}
	b.WriteString("}}fragment m on MergeRequest{iid title webUrl draft state sourceBranch sourceProject{fullPath}}")
	return b.String()
}

// glMorePages bounds the pages read past the first of a batch's lists.
const glMorePages = 4

// gitlabPulls asks the project base for the merge requests from branches
// of the project at head: base itself, or the fork they came from. The
// branches of a batch share a page of a hundred, which other forks'
// merge requests from branches of the same names can fill: the pages
// after are read, a few at most, while one of the branches has none.
func (c *CLI) gitlabPulls(ctx context.Context, base Repo, head string, branches []string, into map[string]Pull) error {
	open, all := glCursor{}, glCursor{}
	opened, latest := map[string]Pull{}, map[string]Pull{}
	// done is a list read to its end, or no longer needed: not asked again.
	done := glCursor{End: "-"}
	// pagedOpen and pagedAll are set once a list is read past its first
	// page; a branch still lacking in it then is remembered (searched).
	var pagedOpen, pagedAll bool
	for page := 0; ; page++ {
		q := gitlabPageQuery(base.Path, branches, open, all)
		out, err := c.run(ctx, "", base.Host, "api", "graphql", "--hostname", base.Host, "-f", "query="+q)
		var o, d *glCursor
		if err == nil {
			o, d, err = parseGitLabPage(out, base.Path, head, opened, latest)
		}
		// A page after the first that fails leaves what the first found.
		if err != nil && (page == 0 || ctx.Err() != nil) {
			return err
		}
		if err != nil {
			break
		}
		// A branch whose list was read as far as it goes, at a lookup
		// before, is not paged for.
		lacks := func(found map[string]Pull, list string) bool {
			return slices.ContainsFunc(branches, func(br string) bool {
				_, ok := found[br]
				return !ok && !c.searched.has(searchKey(base, head, br, list))
			})
		}
		open, all = done, done
		if o != nil && o.Next && o.End != "" && lacks(opened, "open") {
			open, pagedOpen = *o, true
		}
		if d != nil && d.Next && d.End != "" && lacks(latest, "any") {
			all, pagedAll = *d, true
		}
		if !open.Next && !all.Next || page == glMorePages {
			for _, br := range branches {
				if _, ok := opened[br]; !ok && pagedOpen {
					c.searched.add(searchKey(base, head, br, "open"))
				}
				if _, ok := latest[br]; !ok && pagedAll {
					c.searched.add(searchKey(base, head, br, "any"))
				}
			}
			break
		}
	}
	for _, br := range branches {
		if _, seen := into[br]; seen {
			continue
		}
		if p, ok := opened[br]; ok {
			into[br] = p
		} else if p, ok := latest[br]; ok {
			into[br] = p
		}
	}
	return nil
}

type gitlabMR struct {
	IID     string `json:"iid"`
	Title   string `json:"title"`
	WebURL  string `json:"webUrl"`
	Draft   bool   `json:"draft"`
	State   string `json:"state"`
	Branch  string `json:"sourceBranch"`
	Project *struct {
		FullPath string `json:"fullPath"`
	} `json:"sourceProject"`
	Pipeline *struct {
		Status string `json:"status"`
		// Jobs are read where a query asks for them.
		Jobs *struct {
			Count int     `json:"count"`
			Nodes []glJob `json:"nodes"`
		} `json:"jobs"`
	} `json:"headPipeline"`
	// Stats are read where a query asks for them.
	Stats *struct {
		Additions int `json:"additions"`
		Deletions int `json:"deletions"`
	} `json:"diffStatsSummary"`
}

// parseGitLab reads the answer to gitlabQuery on the project at path,
// keeping the merge requests from the project at head. An open one wins
// over the latest merged or closed.
func parseGitLab(out []byte, path, head string, into map[string]Pull) error {
	opened, latest := map[string]Pull{}, map[string]Pull{}
	if _, _, err := parseGitLabPage(out, path, head, opened, latest); err != nil {
		return err
	}
	for _, m := range []map[string]Pull{opened, latest} {
		for br, p := range m {
			if _, seen := into[br]; !seen {
				into[br] = p
			}
		}
	}
	return nil
}

// parseGitLabPage reads a page of the answer to gitlabPageQuery, adding
// to opened the first open merge request of each branch from the project
// at head, and to latest the first in any state, and returns where the
// lists asked for go on.
func parseGitLabPage(out []byte, path, head string, opened, latest map[string]Pull) (o, d *glCursor, err error) {
	type list struct {
		Nodes []gitlabMR `json:"nodes"`
		Page  glCursor   `json:"pageInfo"`
	}
	var ans struct {
		graphQLError
		Data struct {
			Project *struct {
				Open *list `json:"o"`
				All  *list `json:"d"`
			} `json:"project"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &ans); err != nil {
		return nil, nil, fmt.Errorf("glab: %w", err)
	}
	if err := ans.err("glab"); err != nil {
		return nil, nil, err
	}
	pr := ans.Data.Project
	if pr == nil {
		return nil, nil, fmt.Errorf("glab: no project %s, or no access to it", path)
	}
	add := func(l *list, into map[string]Pull) *glCursor {
		if l == nil {
			return nil
		}
		for _, m := range l.Nodes {
			if m.Project == nil || !strings.EqualFold(m.Project.FullPath, head) {
				continue
			}
			if _, seen := into[m.Branch]; seen {
				continue
			}
			n, err := strconv.Atoi(m.IID)
			if err != nil {
				continue
			}
			p := Pull{Number: n, Title: m.Title, URL: m.WebURL, Branch: m.Branch, Draft: m.Draft, State: gitlabState(m.State)}
			if m.Pipeline != nil && p.State == PullOpen {
				p.Checks = gitlabChecks(m.Pipeline.Status)
			}
			into[m.Branch] = p
		}
		return &l.Page
	}
	return add(pr.Open, opened), add(pr.All, latest), nil
}

// gitlabState reads a merge request's state; a locked one is being
// merged, still open.
func gitlabState(s string) PullState {
	switch s {
	case "merged":
		return PullMerged
	case "closed":
		return PullClosed
	}
	return PullOpen
}

// gitlabChecks reads a pipeline's status.
func gitlabChecks(status string) Checks {
	switch status {
	case "SUCCESS":
		return ChecksPassing
	case "FAILED", "CANCELED":
		return ChecksFailing
	case "SKIPPED", "":
		return NoChecks
	}
	return ChecksPending
}

type gitlabRESTMR struct {
	IID    int    `json:"iid"`
	Title  string `json:"title"`
	WebURL string `json:"web_url"`
	Draft  bool   `json:"draft"`
	State  string `json:"state"`
	Branch string `json:"source_branch"`
}

// gitlabCommitPull asks the project r for the merge requests commit sha
// is in, merged ones and those still open.
func (c *CLI) gitlabCommitPull(ctx context.Context, r Repo, sha string) (Pull, bool, error) {
	out, err := c.run(ctx, "", r.Host, "api", "projects/"+projectID(r.Path)+"/repository/commits/"+sha+"/merge_requests", "--hostname", r.Host)
	if errors.Is(err, ErrNotFound) {
		// A commit GitLab has not seen, as one not pushed.
		return Pull{}, false, nil
	}
	if err != nil {
		return Pull{}, false, err
	}
	var list []gitlabRESTMR
	if err := json.Unmarshal(out, &list); err != nil {
		return Pull{}, false, fmt.Errorf("glab: %w", err)
	}
	pulls := make([]Pull, len(list))
	for i, m := range list {
		pulls[i] = Pull{Number: m.IID, Title: m.Title, URL: m.WebURL, Branch: m.Branch, Draft: m.Draft, State: gitlabState(m.State)}
	}
	p, ok := pickCommitPull(pulls, 0)
	return p, ok, nil
}

// projectID is the project at path as GitLab's REST paths take it, its
// slashes escaped.
func projectID(path string) string { return strings.ReplaceAll(path, "/", "%2F") }
