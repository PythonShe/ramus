package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// What a pull request's review reads and writes, on GitHub and GitLab
// (gitlabreview.go). Another host's repository answers ErrNoReview.

// ErrNoReview is a host whose pull requests cannot be reviewed in the app.
var ErrNoReview = errors.New("forge: reviewing on this host is not supported")

// Bounds on what one read of a pull request holds; past them the rest is
// on the host's page (Review.More).
const (
	MaxListed   = 100
	maxComments = 100
	maxThreads  = 100
)

// ReviewState is a review's verdict, or the pull request's decision.
type ReviewState uint8

const (
	ReviewNone ReviewState = iota
	ReviewCommented
	ReviewApproved
	ReviewChangesRequested
	ReviewDismissed
	ReviewPending
	// ReviewRequired is a decision only: reviews are needed and none
	// decided yet.
	ReviewRequired
)

func reviewState(s string) ReviewState {
	switch s {
	case "COMMENTED":
		return ReviewCommented
	case "APPROVED":
		return ReviewApproved
	case "CHANGES_REQUESTED":
		return ReviewChangesRequested
	case "DISMISSED":
		return ReviewDismissed
	case "PENDING":
		return ReviewPending
	case "REVIEW_REQUIRED":
		return ReviewRequired
	}
	return ReviewNone
}

// Listed is an open pull request as a list of them shows it.
type Listed struct {
	Pull
	Author  string
	Base    string
	Updated time.Time
	// Decision is what the reviews add up to, ReviewNone where the
	// repository asks for none.
	Decision ReviewState
}

// OpenPulls lists r's open pull requests, latest updated first, up to
// MaxListed; more says there are others.
func (c *CLI) OpenPulls(ctx context.Context, r Repo) (list []Listed, more bool, err error) {
	if err := c.reviewable(r); err != nil {
		return nil, false, err
	}
	if r.Kind == GitLab {
		list, more, err = c.gitlabOpenPulls(ctx, r)
	} else {
		list, more, err = c.githubOpenPulls(ctx, r)
	}
	for i := range list {
		list[i].URL = r.PullURL(list[i].URL, list[i].Number)
	}
	return list, more, err
}

func (c *CLI) githubOpenPulls(ctx context.Context, r Repo) ([]Listed, bool, error) {
	owner, name, _ := strings.Cut(r.Path, "/")
	q := "query($owner:String!,$name:String!){repository(owner:$owner,name:$name){pullRequests(states:OPEN,first:" + strconv.Itoa(MaxListed) +
		",orderBy:{field:UPDATED_AT,direction:DESC}){totalCount nodes{number title url isDraft state author{login} headRefName baseRefName updatedAt reviewDecision additions deletions headRepositoryOwner{login} commits(last:1){nodes{commit{statusCheckRollup{state}}}}}}}}"
	out, err := c.run(ctx, "", r.Host, "api", "graphql", "--hostname", r.Host, "-f", "query="+q, "-f", "owner="+owner, "-f", "name="+name)
	if err != nil {
		return nil, false, err
	}
	return parseOpenPulls(out)
}

type ghAuthor struct {
	Login string `json:"login"`
}

// login is the author's name; a deleted account has none.
func (a *ghAuthor) login() string {
	if a == nil {
		return "ghost"
	}
	return a.Login
}

func parseOpenPulls(out []byte) ([]Listed, bool, error) {
	var ans struct {
		graphQLError
		Data struct {
			Repository *struct {
				PullRequests struct {
					TotalCount int `json:"totalCount"`
					Nodes      []struct {
						githubPull
						Author   *ghAuthor `json:"author"`
						Base     string    `json:"baseRefName"`
						Updated  time.Time `json:"updatedAt"`
						Decision string    `json:"reviewDecision"`
					} `json:"nodes"`
				} `json:"pullRequests"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &ans); err != nil {
		return nil, false, fmt.Errorf("gh: %w", err)
	}
	if err := ans.err("gh"); err != nil {
		return nil, false, err
	}
	if ans.Data.Repository == nil {
		return nil, false, ErrNotFound
	}
	prs := ans.Data.Repository.PullRequests
	list := make([]Listed, 0, len(prs.Nodes))
	for _, n := range prs.Nodes {
		p := Pull{Number: n.Number, Title: n.Title, URL: n.URL, Branch: n.Head, Draft: n.IsDraft, State: githubState(n.State), Added: n.Additions, Deleted: n.Deletions}
		if ns := n.Commits.Nodes; len(ns) > 0 && ns[0].Commit.Rollup != nil {
			p.Checks = githubChecks(ns[0].Commit.Rollup.State)
		}
		list = append(list, Listed{Pull: p, Author: n.Author.login(), Base: n.Base, Updated: n.Updated, Decision: reviewState(n.Decision)})
	}
	return list, prs.TotalCount > len(list), nil
}

// Comment is one comment, of a pull request's conversation or of a
// review thread.
type Comment struct {
	// ID is the host's node id; DatabaseID the number its REST API takes.
	ID         string
	DatabaseID int64
	Author     string
	Body       string
	At         time.Time
}

// Submitted is a review someone submitted: its verdict and what it says.
type Submitted struct {
	Author string
	State  ReviewState
	Body   string
	At     time.Time
}

// Side is the side of a diff a line is on.
type Side uint8

const (
	Right Side = iota // the new side, the pull request's head
	Left              // the old side, the merge base
)

func (s Side) api() string {
	if s == Left {
		return "LEFT"
	}
	return "RIGHT"
}

func side(s string) Side {
	if s == "LEFT" {
		return Left
	}
	return Right
}

// Thread is a review thread: comments on lines of a file.
type Thread struct {
	ID       string
	Path     string
	Resolved bool
	// Outdated is a thread on lines the head has changed since: Line is
	// 0 and OriginalLine where it was.
	Outdated bool
	// Line is the last line commented on, from 1, on Side; StartLine the
	// first of several, 0 for one line.
	Line, StartLine int
	OriginalLine    int
	Side            Side
	CanResolve      bool
	CanUnresolve    bool
	CanReply        bool
	Comments        []Comment
}

// Review is a pull request as its review shows it.
type Review struct {
	Pull
	Author string
	Body   string
	Base   string
	// BaseOID, StartOID and HeadOID are the commits the host diffs: the
	// merge base, the base branch's tip (GitLab only) and the head.
	BaseOID, StartOID, HeadOID string
	// Decision is what the reviews add up to.
	Decision ReviewState
	Created  time.Time
	Comments []Comment
	Reviews  []Submitted
	Threads  []Thread
	// Viewer is who the tool is logged in as.
	Viewer string
	// More says the host holds comments, reviews or threads past what was
	// read.
	More bool
}

// PullReview reads r's pull request n with its conversation, reviews and
// review threads.
func (c *CLI) PullReview(ctx context.Context, r Repo, n int) (Review, error) {
	if err := c.reviewable(r); err != nil {
		return Review{}, err
	}
	var rv Review
	var err error
	if r.Kind == GitLab {
		rv, err = c.gitlabPullReview(ctx, r, n)
	} else {
		rv, err = c.githubPullReview(ctx, r, n)
	}
	if err != nil {
		return Review{}, err
	}
	if rv.Number != n {
		return Review{}, ErrNotFound
	}
	rv.URL = r.PullURL(rv.URL, rv.Number)
	return rv, nil
}

func (c *CLI) githubPullReview(ctx context.Context, r Repo, n int) (Review, error) {
	owner, name, _ := strings.Cut(r.Path, "/")
	page := func(n int) string { return "(first:" + strconv.Itoa(n) + ")" }
	q := "query($owner:String!,$name:String!,$n:Int!){viewer{login} repository(owner:$owner,name:$name){pullRequest(number:$n){" +
		"number title url body state isDraft author{login} baseRefName headRefName baseRefOid headRefOid reviewDecision createdAt additions deletions " +
		"commits(last:1){nodes{commit{statusCheckRollup{state}}}} " +
		"comments" + page(maxComments) + "{totalCount nodes{id databaseId author{login} body createdAt}} " +
		"reviews" + page(maxComments) + "{totalCount nodes{author{login} state body submittedAt}} " +
		"reviewThreads" + page(maxThreads) + "{totalCount nodes{id isResolved isOutdated path line startLine originalLine diffSide " +
		"viewerCanResolve viewerCanUnresolve viewerCanReply comments" + page(maxComments) + "{totalCount nodes{id databaseId author{login} body createdAt}}}}}}}"
	out, err := c.run(ctx, "", r.Host, "api", "graphql", "--hostname", r.Host, "-f", "query="+q,
		"-f", "owner="+owner, "-f", "name="+name, "-F", "n="+strconv.Itoa(n))
	if err != nil {
		return Review{}, err
	}
	return parseReview(out)
}

type ghComment struct {
	ID         string    `json:"id"`
	DatabaseID int64     `json:"databaseId"`
	Author     *ghAuthor `json:"author"`
	Body       string    `json:"body"`
	CreatedAt  time.Time `json:"createdAt"`
}

func (g ghComment) comment() Comment {
	return Comment{ID: g.ID, DatabaseID: g.DatabaseID, Author: g.Author.login(), Body: g.Body, At: g.CreatedAt}
}

type ghComments struct {
	TotalCount int         `json:"totalCount"`
	Nodes      []ghComment `json:"nodes"`
}

func parseReview(out []byte) (Review, error) {
	var ans struct {
		graphQLError
		Data struct {
			Viewer     ghAuthor `json:"viewer"`
			Repository *struct {
				PullRequest *struct {
					githubPull
					Body     string     `json:"body"`
					Author   *ghAuthor  `json:"author"`
					Base     string     `json:"baseRefName"`
					BaseOID  string     `json:"baseRefOid"`
					HeadOID  string     `json:"headRefOid"`
					Decision string     `json:"reviewDecision"`
					Created  time.Time  `json:"createdAt"`
					Comments ghComments `json:"comments"`
					Reviews  struct {
						TotalCount int `json:"totalCount"`
						Nodes      []struct {
							Author      *ghAuthor `json:"author"`
							State       string    `json:"state"`
							Body        string    `json:"body"`
							SubmittedAt time.Time `json:"submittedAt"`
						} `json:"nodes"`
					} `json:"reviews"`
					Threads struct {
						TotalCount int `json:"totalCount"`
						Nodes      []struct {
							ID           string     `json:"id"`
							Resolved     bool       `json:"isResolved"`
							Outdated     bool       `json:"isOutdated"`
							Path         string     `json:"path"`
							Line         *int       `json:"line"`
							StartLine    *int       `json:"startLine"`
							OriginalLine *int       `json:"originalLine"`
							Side         string     `json:"diffSide"`
							CanResolve   bool       `json:"viewerCanResolve"`
							CanUnresolve bool       `json:"viewerCanUnresolve"`
							CanReply     bool       `json:"viewerCanReply"`
							Comments     ghComments `json:"comments"`
						} `json:"nodes"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &ans); err != nil {
		return Review{}, fmt.Errorf("gh: %w", err)
	}
	if err := ans.err("gh"); err != nil {
		return Review{}, err
	}
	if ans.Data.Repository == nil || ans.Data.Repository.PullRequest == nil {
		return Review{}, ErrNotFound
	}
	p := ans.Data.Repository.PullRequest
	rv := Review{
		Pull: Pull{Number: p.Number, Title: p.Title, URL: p.URL, Branch: p.Head, Draft: p.IsDraft, State: githubState(p.State),
			Added: p.Additions, Deleted: p.Deletions},
		Author: p.Author.login(), Body: p.Body, Base: p.Base, BaseOID: p.BaseOID, HeadOID: p.HeadOID,
		Decision: reviewState(p.Decision), Created: p.Created, Viewer: ans.Data.Viewer.Login,
	}
	if ns := p.Commits.Nodes; len(ns) > 0 && ns[0].Commit.Rollup != nil {
		rv.Checks = githubChecks(ns[0].Commit.Rollup.State)
	}
	for _, c := range p.Comments.Nodes {
		rv.Comments = append(rv.Comments, c.comment())
	}
	for _, s := range p.Reviews.Nodes {
		rv.Reviews = append(rv.Reviews, Submitted{Author: s.Author.login(), State: reviewState(s.State), Body: s.Body, At: s.SubmittedAt})
	}
	rv.More = p.Comments.TotalCount > len(p.Comments.Nodes) || p.Reviews.TotalCount > len(p.Reviews.Nodes) ||
		p.Threads.TotalCount > len(p.Threads.Nodes)
	num := func(p *int) int {
		if p == nil {
			return 0
		}
		return *p
	}
	for _, t := range p.Threads.Nodes {
		th := Thread{ID: t.ID, Path: t.Path, Resolved: t.Resolved, Outdated: t.Outdated, Line: num(t.Line), StartLine: num(t.StartLine),
			OriginalLine: num(t.OriginalLine), Side: side(t.Side), CanResolve: t.CanResolve, CanUnresolve: t.CanUnresolve, CanReply: t.CanReply}
		for _, c := range t.Comments.Nodes {
			th.Comments = append(th.Comments, c.comment())
		}
		if t.Comments.TotalCount > len(t.Comments.Nodes) {
			rv.More = true
		}
		rv.Threads = append(rv.Threads, th)
	}
	return rv, nil
}

// Verdict is what a review submitted says of the pull request.
type Verdict uint8

const (
	VerdictComment Verdict = iota
	VerdictApprove
	VerdictRequestChanges
)

// Draft is a comment on lines of a file, sent with a review.
type Draft struct {
	Path string
	// OldPath is the file's path on the old side, when it was renamed.
	OldPath string
	Side    Side
	// Line is the last line, from 1; StartLine the first of several, 0
	// for one line.
	Line, StartLine int
	// Across is the line on the other side that Line faces, for an
	// unchanged line; 0 for one added or removed.
	Across int
	Body   string
}

// Revs are the commits a review's comments are placed on: the head
// reviewed, and on GitLab the merge base and base branch tip it was
// diffed against (Review.BaseOID and StartOID).
type Revs struct {
	Base, Start, Head string
}

// SubmitReview submits a review of r's pull request n, of the commits
// at (those the drafts' lines are of), with body and drafts.
func (c *CLI) SubmitReview(ctx context.Context, r Repo, n int, at Revs, v Verdict, body string, drafts []Draft) error {
	if err := c.reviewable(r); err != nil {
		return err
	}
	head := at.Head
	if !hexID(head) {
		return fmt.Errorf("forge: %q is not a commit id", head)
	}
	if r.Kind == GitLab {
		return c.gitlabSubmit(ctx, r, n, at, v, body, drafts)
	}
	type comment struct {
		Path      string `json:"path"`
		Line      int    `json:"line"`
		Side      string `json:"side"`
		StartLine int    `json:"start_line,omitempty"`
		StartSide string `json:"start_side,omitempty"`
		Body      string `json:"body"`
	}
	req := struct {
		CommitID string    `json:"commit_id"`
		Event    string    `json:"event"`
		Body     string    `json:"body,omitempty"`
		Comments []comment `json:"comments,omitempty"`
	}{CommitID: head, Event: [...]string{"COMMENT", "APPROVE", "REQUEST_CHANGES"}[v], Body: body}
	for _, d := range drafts {
		cm := comment{Path: d.Path, Line: d.Line, Side: d.Side.api(), Body: d.Body}
		if d.StartLine > 0 && d.StartLine < d.Line {
			cm.StartLine, cm.StartSide = d.StartLine, d.Side.api()
		}
		req.Comments = append(req.Comments, cm)
	}
	return c.post(ctx, r, "repos/"+r.Path+"/pulls/"+strconv.Itoa(n)+"/reviews", req)
}

// Reply adds body to the review thread th of r's pull request n.
func (c *CLI) Reply(ctx context.Context, r Repo, n int, th Thread, body string) error {
	if err := c.reviewable(r); err != nil {
		return err
	}
	if r.Kind == GitLab {
		id, err := gitlabDiscussion(th.ID)
		if err != nil {
			return err
		}
		return c.gitlabSend(ctx, r, "POST", mrPath(r, n)+"/discussions/"+id+"/notes", map[string]string{"body": body}, nil)
	}
	if len(th.Comments) == 0 {
		return ErrNotFound
	}
	return c.post(ctx, r, "repos/"+r.Path+"/pulls/"+strconv.Itoa(n)+"/comments/"+strconv.FormatInt(th.Comments[0].DatabaseID, 10)+"/replies",
		map[string]string{"body": body})
}

// AddComment adds body to the conversation of r's pull request n.
func (c *CLI) AddComment(ctx context.Context, r Repo, n int, body string) error {
	if err := c.reviewable(r); err != nil {
		return err
	}
	if r.Kind == GitLab {
		return c.gitlabSend(ctx, r, "POST", mrPath(r, n)+"/notes", map[string]string{"body": body}, nil)
	}
	return c.post(ctx, r, "repos/"+r.Path+"/issues/"+strconv.Itoa(n)+"/comments", map[string]string{"body": body})
}

// Resolve marks the review thread th of r's pull request n resolved, or
// not.
func (c *CLI) Resolve(ctx context.Context, r Repo, n int, th Thread, resolved bool) error {
	if err := c.reviewable(r); err != nil {
		return err
	}
	if r.Kind == GitLab {
		id, err := gitlabDiscussion(th.ID)
		if err != nil {
			return err
		}
		return c.gitlabSend(ctx, r, "PUT", mrPath(r, n)+"/discussions/"+id, map[string]bool{"resolved": resolved}, nil)
	}
	id := th.ID
	m := "unresolveReviewThread"
	if resolved {
		m = "resolveReviewThread"
	}
	out, err := c.run(ctx, "", r.Host, "api", "graphql", "--hostname", r.Host,
		"-f", "query=mutation($id:ID!){"+m+"(input:{threadId:$id}){thread{isResolved}}}", "-f", "id="+id)
	if err != nil {
		return err
	}
	var ans graphQLError
	if err := json.Unmarshal(out, &ans); err != nil {
		return fmt.Errorf("gh: %w", err)
	}
	return ans.err("gh")
}

// post sends v as JSON to the REST path of r's host.
func (c *CLI) post(ctx context.Context, r Repo, path string, v any) error {
	body, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = c.runInput(ctx, r.Host, body, "api", "--hostname", r.Host, "-X", "POST", path, "--input", "-")
	return err
}

// reviewable says whether r's pull requests can be reviewed through c.
func (c *CLI) reviewable(r Repo) error {
	if r.Kind != c.Kind {
		return ErrNoCLI
	}
	if r.Kind != GitHub && r.Kind != GitLab {
		return ErrNoReview
	}
	if !c.Knows(r.Host) {
		return ErrUnknownHost
	}
	return nil
}
