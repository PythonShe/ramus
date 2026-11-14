package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// gitlabQuery asks for the merge requests from branches into the project
// at path, in one query, as GitLab takes a list of source branches: the
// open ones (o), with their pipelines, and those in every state, latest
// first (d).
func gitlabQuery(path string, branches []string) string {
	var list strings.Builder
	for i, br := range branches {
		if i > 0 {
			list.WriteByte(',')
		}
		list.WriteString(literal(br))
	}
	var b strings.Builder
	b.WriteString("query{project(fullPath:")
	b.WriteString(literal(path))
	b.WriteString("){o:mergeRequests(state:opened,first:100,sourceBranches:[")
	b.WriteString(list.String())
	b.WriteString("]){nodes{...m headPipeline{status}}}d:mergeRequests(sort:UPDATED_DESC,first:100,sourceBranches:[")
	b.WriteString(list.String())
	b.WriteString("]){nodes{...m}}}}fragment m on MergeRequest{iid title webUrl draft state sourceBranch sourceProject{fullPath}}")
	return b.String()
}

// gitlabPulls asks the project base for the merge requests from branches
// of the project at head: base itself, or the fork they came from.
func (c *CLI) gitlabPulls(ctx context.Context, base Repo, head string, branches []string, into map[string]Pull) error {
	out, err := c.run(ctx, "", base.Host, "api", "graphql", "--hostname", base.Host, "-f", "query="+gitlabQuery(base.Path, branches))
	if err != nil {
		return err
	}
	return parseGitLab(out, base.Path, head, into)
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
	} `json:"headPipeline"`
}

// parseGitLab reads the answer to gitlabQuery on the project at path,
// keeping the merge requests from the project at head. An open one wins
// over the latest merged or closed.
func parseGitLab(out []byte, path, head string, into map[string]Pull) error {
	var ans struct {
		graphQLError
		Data struct {
			Project *struct {
				Open struct {
					Nodes []gitlabMR `json:"nodes"`
				} `json:"o"`
				All struct {
					Nodes []gitlabMR `json:"nodes"`
				} `json:"d"`
			} `json:"project"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &ans); err != nil {
		return fmt.Errorf("glab: %w", err)
	}
	if err := ans.err("glab"); err != nil {
		return err
	}
	if ans.Data.Project == nil {
		return fmt.Errorf("glab: no project %s, or no access to it", path)
	}
	for _, m := range append(ans.Data.Project.Open.Nodes, ans.Data.Project.All.Nodes...) {
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
	return nil
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
