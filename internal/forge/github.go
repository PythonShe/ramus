package forge

import (
	"context"
	"encoding/json"
	"errors"
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

// githubState reads a pull request's state, in GraphQL's capitals or
// REST's lower case; REST says closed of a merged one too, which its
// caller tells apart.
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

// restPull is a pull request as GitHub's REST API writes it.
type restPull struct {
	Number   int     `json:"number"`
	Title    string  `json:"title"`
	URL      string  `json:"html_url"`
	State    string  `json:"state"`
	Draft    bool    `json:"draft"`
	MergedAt *string `json:"merged_at"`
	Head     struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

func (p restPull) pull() Pull {
	st := githubState(p.State)
	if p.MergedAt != nil && *p.MergedAt != "" {
		st = PullMerged
	}
	return Pull{Number: p.Number, Title: p.Title, URL: p.URL, Branch: p.Head.Ref, Draft: p.Draft, State: st}
}

// CommitPull asks GitHub or GitLab for the pull or merge request that
// brought commit sha into r: the one numbered hint when the host names
// several, else a merged one, else an open one. When GitHub names none,
// hint, the number the subject ends with (see SquashHint), is asked for
// by itself; 0 asks nothing more, as GitLab's subjects end with none. ok is false when there is none. A host the tool does
// not know (Knows) is not asked: ErrUnknownHost. Its URL is on r's host
// (PullURL).
func (c *CLI) CommitPull(ctx context.Context, r Repo, sha string, hint int) (p Pull, ok bool, err error) {
	p, ok, err = c.commitPull(ctx, r, sha, hint)
	if ok {
		p.URL = r.PullURL(p.URL, p.Number)
	}
	return p, ok, err
}

func (c *CLI) commitPull(ctx context.Context, r Repo, sha string, hint int) (p Pull, ok bool, err error) {
	if r.Kind != c.Kind {
		return Pull{}, false, ErrNoCLI
	}
	if !c.Knows(r.Host) {
		return Pull{}, false, ErrUnknownHost
	}
	if !hexID(sha) {
		return Pull{}, false, fmt.Errorf("forge: %q is not a commit id", sha)
	}
	if r.Kind == GitLab {
		return c.gitlabCommitPull(ctx, r, sha)
	}
	out, err := c.run(ctx, "", r.Host, "api", "repos/"+r.Path+"/commits/"+sha+"/pulls", "--hostname", r.Host)
	switch {
	case errors.Is(err, ErrNotFound):
		// A commit GitHub has not seen, as one not pushed.
	case err != nil:
		return Pull{}, false, err
	default:
		var list []restPull
		if err := json.Unmarshal(out, &list); err != nil {
			return Pull{}, false, fmt.Errorf("gh: %w", err)
		}
		pulls := make([]Pull, len(list))
		for i, rp := range list {
			pulls[i] = rp.pull()
		}
		if p, ok := pickCommitPull(pulls, hint); ok {
			return p, true, nil
		}
	}
	if hint <= 0 {
		return Pull{}, false, nil
	}
	out, err = c.run(ctx, "", r.Host, "api", "repos/"+r.Path+"/pulls/"+strconv.Itoa(hint), "--hostname", r.Host)
	if errors.Is(err, ErrNotFound) {
		return Pull{}, false, nil
	}
	if err != nil {
		return Pull{}, false, err
	}
	var one restPull
	if err := json.Unmarshal(out, &one); err != nil {
		return Pull{}, false, fmt.Errorf("gh: %w", err)
	}
	if one.Number != hint {
		return Pull{}, false, nil
	}
	return one.pull(), true, nil
}

// hexID reports a full commit id, which goes into a URL path as it is.
func hexID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// pickCommitPull chooses among the pull requests a host names for a
// commit: the hinted one, else the first merged, else the first open,
// else the first.
func pickCommitPull(pulls []Pull, hint int) (Pull, bool) {
	if len(pulls) == 0 {
		return Pull{}, false
	}
	for _, p := range pulls {
		if hint > 0 && p.Number == hint {
			return p, true
		}
	}
	for _, want := range []PullState{PullMerged, PullOpen} {
		for _, p := range pulls {
			if p.State == want {
				return p, true
			}
		}
	}
	return pulls[0], true
}
