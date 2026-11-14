package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// githubQuery asks for the open pull requests of n head branches, each
// a variable, in one query.
func githubQuery(n int) string {
	var b strings.Builder
	b.WriteString("query($owner:String!,$name:String!")
	for i := range n {
		fmt.Fprintf(&b, ",$b%d:String!", i)
	}
	b.WriteString("){repository(owner:$owner,name:$name){")
	for i := range n {
		fmt.Fprintf(&b, "b%d:pullRequests(headRefName:$b%d,states:[OPEN],first:5){nodes{...p}}", i, i)
	}
	b.WriteString("}}fragment p on PullRequest{number title url isDraft headRefName headRepositoryOwner{login}")
	b.WriteString("commits(last:1){nodes{commit{statusCheckRollup{state}}}}}")
	return b.String()
}

func (c *CLI) githubPulls(ctx context.Context, r Repo, branches []string, into map[string]Pull) error {
	owner, name, _ := strings.Cut(r.Path, "/")
	args := []string{"api", "graphql", "--hostname", r.Host, "-f", "query=" + githubQuery(len(branches)),
		"-f", "owner=" + owner, "-f", "name=" + name}
	for i, br := range branches {
		args = append(args, "-f", fmt.Sprintf("b%d=%s", i, br))
	}
	out, err := c.run(ctx, "", r.Host, args...)
	if err != nil {
		return err
	}
	return parseGitHub(out, owner, len(branches), into)
}

type githubPull struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	IsDraft bool   `json:"isDraft"`
	Head    string `json:"headRefName"`
	Owner   *struct {
		Login string `json:"login"`
	} `json:"headRepositoryOwner"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				Rollup *struct {
					State string `json:"state"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

// parseGitHub reads the answer to githubQuery, keeping the pull requests
// whose head is in owner's repository: a fork's branch of the same name
// is another branch.
func parseGitHub(out []byte, owner string, n int, into map[string]Pull) error {
	var ans struct {
		graphQLError
		Data struct {
			Repository map[string]*struct {
				Nodes []githubPull `json:"nodes"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &ans); err != nil {
		return fmt.Errorf("gh: %w", err)
	}
	if err := ans.err("gh"); err != nil {
		return err
	}
	for i := range n {
		conn := ans.Data.Repository[fmt.Sprintf("b%d", i)]
		if conn == nil {
			continue
		}
		for _, p := range conn.Nodes {
			if p.Owner == nil || !strings.EqualFold(p.Owner.Login, owner) {
				continue
			}
			pull := Pull{Number: p.Number, Title: p.Title, URL: p.URL, Branch: p.Head, Draft: p.IsDraft}
			if ns := p.Commits.Nodes; len(ns) > 0 && ns[0].Commit.Rollup != nil {
				pull.Checks = githubChecks(ns[0].Commit.Rollup.State)
			}
			if _, seen := into[p.Head]; !seen {
				into[p.Head] = pull
			}
		}
	}
	return nil
}

// githubChecks reads a status check rollup's state.
func githubChecks(state string) Checks {
	switch state {
	case "SUCCESS":
		return ChecksPassing
	case "FAILURE", "ERROR":
		return ChecksFailing
	case "PENDING", "EXPECTED":
		return ChecksPending
	}
	return NoChecks
}
