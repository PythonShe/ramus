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
// checks, and the merged and closed ones (cN), each list the latest
// updated first: one of a head's made, reopened or otherwise changed since
// a lookup is on the first page (searched). A branch of a fork shares
// its name with every other fork's, so each asks for more than one, and
// for where the next page starts.
func githubQuery(n int) string {
	var b strings.Builder
	b.WriteString("query($owner:String!,$name:String!")
	for i := range n {
		fmt.Fprintf(&b, ",$b%d:String!", i)
	}
	b.WriteString("){repository(owner:$owner,name:$name){")
	for i := range n {
		fmt.Fprintf(&b, "b%d:pullRequests(headRefName:$b%d,states:[OPEN],first:10,orderBy:{field:UPDATED_AT,direction:DESC}){nodes{...p} %s}", i, i, ghPageInfo)
		fmt.Fprintf(&b, "c%d:pullRequests(headRefName:$b%d,states:[MERGED,CLOSED],first:10,orderBy:{field:UPDATED_AT,direction:DESC}){nodes{...q} %s}", i, i, ghPageInfo)
	}
	b.WriteString("}}" + ghPullFragments)
	return b.String()
}

const (
	ghPageInfo      = "pageInfo{hasNextPage endCursor}"
	ghPullFragments = "fragment q on PullRequest{number title url isDraft state headRefName headRepositoryOwner{login}}" +
		"fragment p on PullRequest{...q commits(last:1){nodes{commit{statusCheckRollup{state}}}}}"
	// ghMorePages bounds the pages read past the first of a branch's
	// lists, a hundred pull requests each.
	ghMorePages = 4
)

// ghList is a list of a branch's pull requests read on from after: its
// open ones, or its merged and closed ones.
type ghList struct {
	branch string
	open   bool
	after  string
}

// githubMoreQuery asks for the next page of each of lists (pN), from
// where it ended (aN).
func githubMoreQuery(lists []ghList) string {
	var b strings.Builder
	b.WriteString("query($owner:String!,$name:String!")
	for i := range lists {
		fmt.Fprintf(&b, ",$h%d:String!,$a%d:String!", i, i)
	}
	b.WriteString("){repository(owner:$owner,name:$name){")
	for i, l := range lists {
		if l.open {
			fmt.Fprintf(&b, "p%d:pullRequests(headRefName:$h%d,states:[OPEN],first:100,after:$a%d,orderBy:{field:UPDATED_AT,direction:DESC}){nodes{...p} %s}", i, i, i, ghPageInfo)
		} else {
			fmt.Fprintf(&b, "p%d:pullRequests(headRefName:$h%d,states:[MERGED,CLOSED],first:100,after:$a%d,orderBy:{field:UPDATED_AT,direction:DESC}){nodes{...q} %s}", i, i, i, ghPageInfo)
		}
	}
	b.WriteString("}}" + ghPullFragments)
	return b.String()
}

// githubPulls asks base for the pull requests from branches of the
// repository owned by headOwner: base itself, or the fork they came from.
// A name many forks' branches share may have more pull requests than a
// page holds, the owner's not among the first: the pages after are read,
// a few at most, until it is found.
func (c *CLI) githubPulls(ctx context.Context, base Repo, headOwner string, branches []string, into map[string]Pull) error {
	owner, name, _ := strings.Cut(base.Path, "/")
	args := []string{"api", "graphql", "--hostname", base.Host, "-f", "query=" + githubQuery(len(branches)),
		"-f", "owner=" + owner, "-f", "name=" + name}
	for i, br := range branches {
		args = append(args, "-f", fmt.Sprintf("b%d=%s", i, br))
	}
	out, err := c.read(ctx, base.Host, args...)
	if err != nil {
		return err
	}
	conns, err := parseGitHubLists(out)
	if err != nil {
		return err
	}
	open, closed := map[string]Pull{}, map[string]Pull{}
	var lists []ghList
	for i, br := range branches {
		lists = append(lists, ghList{branch: br, open: true}, ghList{branch: br})
		conns["l"+strconv.Itoa(2*i)], conns["l"+strconv.Itoa(2*i+1)] = conns["b"+strconv.Itoa(i)], conns["c"+strconv.Itoa(i)]
	}
	key := func(l ghList) string {
		list := "closed"
		if l.open {
			list = "open"
		}
		return searchKey(base, headOwner, l.branch, list)
	}
	// deep are the lists read past their first page and not found in
	// yet; those read as far as they go are remembered (searched).
	deep := map[string]bool{}
	for page := 0; ; page++ {
		var next []ghList
		for i, l := range lists {
			conn := conns["l"+strconv.Itoa(i)]
			if conn == nil {
				delete(deep, key(l))
				continue
			}
			found := closed
			if l.open {
				found = open
			}
			if p, ok := ownPull(conn.Nodes, headOwner); ok {
				if _, seen := found[l.branch]; !seen {
					found[l.branch] = p
				}
				delete(deep, key(l))
				continue
			}
			// An open one found, the closed ones are not looked through.
			_, opened := open[l.branch]
			switch {
			case !l.open && opened:
				delete(deep, key(l))
			case conn.Page.Next && conn.Page.End != "" && !c.searched.has(key(l)):
				next = append(next, ghList{branch: l.branch, open: l.open, after: conn.Page.End})
				deep[key(l)] = true
			}
		}
		if len(next) == 0 || page == ghMorePages {
			for k := range deep {
				c.searched.add(k)
			}
			break
		}
		lists = next
		args := []string{"api", "graphql", "--hostname", base.Host, "-f", "query=" + githubMoreQuery(lists),
			"-f", "owner=" + owner, "-f", "name=" + name}
		for i, l := range lists {
			args = append(args, "-f", fmt.Sprintf("h%d=%s", i, l.branch), "-f", fmt.Sprintf("a%d=%s", i, l.after))
		}
		// A page after the first that fails leaves what the first found.
		out, err := c.read(ctx, base.Host, args...)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		more, perr := parseGitHubLists(out)
		if err != nil || perr != nil {
			break
		}
		conns = map[string]*ghConn{}
		for i := range lists {
			conns["l"+strconv.Itoa(i)] = more["p"+strconv.Itoa(i)]
		}
	}
	for _, br := range branches {
		if p, ok := open[br]; ok {
			into[br] = p
		} else if p, ok := closed[br]; ok {
			into[br] = p
		}
	}
	return nil
}

type githubPull struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	IsDraft bool   `json:"isDraft"`
	State   string `json:"state"`
	Head    string `json:"headRefName"`
	// Additions and Deletions are read where a query asks for them.
	Additions int `json:"additions"`
	Deletions int `json:"deletions"`
	Owner     *struct {
		Login string `json:"login"`
	} `json:"headRepositoryOwner"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				Rollup *struct {
					State string `json:"state"`
					// Contexts are read where a query asks for them.
					Contexts struct {
						TotalCount int       `json:"totalCount"`
						Nodes      []ghCheck `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

// ghConn is a list of pull requests read, and whether more follow it.
type ghConn struct {
	Nodes []githubPull `json:"nodes"`
	Page  struct {
		Next bool   `json:"hasNextPage"`
		End  string `json:"endCursor"`
	} `json:"pageInfo"`
}

// parseGitHubLists reads the lists of pull requests an answer to
// githubQuery or githubMoreQuery holds, by their aliases.
func parseGitHubLists(out []byte) (map[string]*ghConn, error) {
	var ans struct {
		graphQLError
		Data struct {
			Repository map[string]*ghConn `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &ans); err != nil {
		return nil, fmt.Errorf("gh: %w", err)
	}
	if err := ans.err("gh"); err != nil {
		return nil, err
	}
	if ans.Data.Repository == nil {
		return map[string]*ghConn{}, nil
	}
	return ans.Data.Repository, nil
}

// ownPull is the first of nodes whose head is in owner's repository:
// another fork's branch of the same name is another branch.
func ownPull(nodes []githubPull, owner string) (Pull, bool) {
	for _, p := range nodes {
		if p.Owner == nil || !strings.EqualFold(p.Owner.Login, owner) {
			continue
		}
		pull := Pull{Number: p.Number, Title: p.Title, URL: p.URL, Branch: p.Head, Draft: p.IsDraft, State: githubState(p.State)}
		if ns := p.Commits.Nodes; pull.State == PullOpen && len(ns) > 0 && ns[0].Commit.Rollup != nil {
			pull.Checks = githubChecks(ns[0].Commit.Rollup.State)
		}
		return pull, true
	}
	return Pull{}, false
}

// parseGitHub reads the answer to githubQuery for n branches, keeping
// the pull requests whose head is in owner's repository. An open one
// wins over the latest closed.
func parseGitHub(out []byte, owner string, n int, into map[string]Pull) error {
	conns, err := parseGitHubLists(out)
	if err != nil {
		return err
	}
	for i := range n {
		for _, alias := range []string{"b", "c"} {
			conn := conns[alias+strconv.Itoa(i)]
			if conn == nil {
				continue
			}
			if p, ok := ownPull(conn.Nodes, owner); ok {
				if _, seen := into[p.Branch]; !seen {
					into[p.Branch] = p
				}
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
	var rej *RejectedError
	switch {
	case errors.Is(err, ErrNotFound) || errors.As(err, &rej):
		// A commit GitHub has not seen, as one not pushed: 422.
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
