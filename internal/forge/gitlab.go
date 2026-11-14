package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// gitlabQuery asks for the open merge requests from branches of the
// project at path, in one query: GitLab takes a list of source branches.
func gitlabQuery(path string, branches []string) string {
	var b strings.Builder
	b.WriteString("query{project(fullPath:")
	b.WriteString(literal(path))
	b.WriteString("){mergeRequests(state:opened,first:100,sourceBranches:[")
	for i, br := range branches {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(literal(br))
	}
	b.WriteString("]){nodes{iid title webUrl draft sourceBranch sourceProject{fullPath} headPipeline{status}}}}}")
	return b.String()
}

func (c *CLI) gitlabPulls(ctx context.Context, r Repo, branches []string, into map[string]Pull) error {
	out, err := c.run(ctx, "", r.Host, "api", "graphql", "--hostname", r.Host, "-f", "query="+gitlabQuery(r.Path, branches))
	if err != nil {
		return err
	}
	return parseGitLab(out, r.Path, into)
}

// parseGitLab reads the answer to gitlabQuery, keeping the merge
// requests from the project itself.
func parseGitLab(out []byte, path string, into map[string]Pull) error {
	var ans struct {
		graphQLError
		Data struct {
			Project *struct {
				MergeRequests struct {
					Nodes []struct {
						IID     string `json:"iid"`
						Title   string `json:"title"`
						WebURL  string `json:"webUrl"`
						Draft   bool   `json:"draft"`
						Branch  string `json:"sourceBranch"`
						Project *struct {
							FullPath string `json:"fullPath"`
						} `json:"sourceProject"`
						Pipeline *struct {
							Status string `json:"status"`
						} `json:"headPipeline"`
					} `json:"nodes"`
				} `json:"mergeRequests"`
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
	for _, m := range ans.Data.Project.MergeRequests.Nodes {
		if m.Project == nil || !strings.EqualFold(m.Project.FullPath, path) {
			continue
		}
		n, err := strconv.Atoi(m.IID)
		if err != nil {
			continue
		}
		p := Pull{Number: n, Title: m.Title, URL: m.WebURL, Branch: m.Branch, Draft: m.Draft}
		if m.Pipeline != nil {
			p.Checks = gitlabChecks(m.Pipeline.Status)
		}
		if _, seen := into[m.Branch]; !seen {
			into[m.Branch] = p
		}
	}
	return nil
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
