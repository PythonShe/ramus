package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// githubQuery asks for the pull requests of n head branches, each a
// variable, in one query: for each, the open ones (bN), with their
// checks, and the merged and closed ones, latest first (cN). A branch of
// a fork shares its name with every other fork's, so each asks for more
// than one.
func githubQuery(n int) string {
	var b strings.Builder
	b.WriteString("query($owner:String!,$name:String!")
	for i := range n {
		fmt.Fprintf(&b, ",$b%d:String!", i)
	}
	b.WriteString("){repository(owner:$owner,name:$name){")
	for i := range n {
		fmt.Fprintf(&b, "b%d:pullRequests(headRefName:$b%d,states:[OPEN],first:10){nodes{...p}}", i, i)
		fmt.Fprintf(&b, "c%d:pullRequests(headRefName:$b%d,states:[MERGED,CLOSED],first:10,orderBy:{field:UPDATED_AT,direction:DESC}){nodes{...q}}", i, i)
	}
	b.WriteString("}}fragment q on PullRequest{number title url isDraft state headRefName headRepositoryOwner{login}}")
	b.WriteString("fragment p on PullRequest{...q commits(last:1){nodes{commit{statusCheckRollup{state}}}}}")
	return b.String()
}

// githubPulls asks base for the pull requests from branches of the
// repository owned by headOwner: base itself, or the fork they came from.
func (c *CLI) githubPulls(ctx context.Context, base Repo, headOwner string, branches []string, into map[string]Pull) error {
	owner, name, _ := strings.Cut(base.Path, "/")
	args := []string{"api", "graphql", "--hostname", base.Host, "-f", "query=" + githubQuery(len(branches)),
		"-f", "owner=" + owner, "-f", "name=" + name}
	for i, br := range branches {
		args = append(args, "-f", fmt.Sprintf("b%d=%s", i, br))
	}
	out, err := c.run(ctx, "", base.Host, args...)
	if err != nil {
		return err
	}
	return parseGitHub(out, headOwner, len(branches), into)
}

type githubPull struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	IsDraft bool   `json:"isDraft"`
	State   string `json:"state"`
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
// whose head is in owner's repository: another fork's branch of the same
// name is another branch. An open one wins over the latest closed.
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
		for _, alias := range []string{"b", "c"} {
			conn := ans.Data.Repository[alias+strconv.Itoa(i)]
			if conn == nil {
				continue
			}
			for _, p := range conn.Nodes {
				if p.Owner == nil || !strings.EqualFold(p.Owner.Login, owner) {
					continue
				}
				if _, seen := into[p.Head]; seen {
					continue
				}
				pull := Pull{Number: p.Number, Title: p.Title, URL: p.URL, Branch: p.Head, Draft: p.IsDraft, State: githubState(p.State)}
				if ns := p.Commits.Nodes; pull.State == PullOpen && len(ns) > 0 && ns[0].Commit.Rollup != nil {
					pull.Checks = githubChecks(ns[0].Commit.Rollup.State)
				}
				into[p.Head] = pull
			}
		}
	}
	return nil
}

// githubState reads a pull request's state.
func githubState(s string) PullState {
	switch strings.ToUpper(s) {
	case "MERGED":
		return PullMerged
	case "CLOSED":
		return PullClosed
	}
	return PullOpen
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
