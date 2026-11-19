package forge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
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
	// detailBatch is how many listed pull requests one query reads the
	// details of (PullDetails).
	detailBatch = 20
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
	// ReviewAwaited is a reviewer's only: asked, and not answered since.
	ReviewAwaited
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
	// ID is the host's node id, which GitHub's details are asked by.
	ID      string
	Author  string
	Base    string
	Updated time.Time
	// Decision is what the reviews add up to, ReviewNone where the
	// repository asks for none.
	Decision ReviewState
	// Detailed says Decision, the checks and the lines changed were
	// read: GitHub's list comes without them (PullDetails).
	Detailed bool
}

// OpenPulls lists r's open pull requests, latest updated first, up to
// MaxListed; more says there are others. GitHub's come without their
// details, which PullDetails reads.
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
		",orderBy:{field:UPDATED_AT,direction:DESC}){totalCount nodes{id number title url isDraft state author{login} headRefName baseRefName updatedAt headRepositoryOwner{login}}}}}"
	out, err := c.read(ctx, r.Host, "api", "graphql", "--hostname", r.Host, "-f", "query="+q, "-f", "owner="+owner, "-f", "name="+name)
	if err != nil {
		return nil, false, err
	}
	return parseOpenPulls(out)
}

// pullDetails is what GitHub is slow to say of a pull request: each of
// them takes it seconds over a hundred pull requests it was not asked
// about lately, and it cuts a query off at ten with 502.
const pullDetails = "reviewDecision additions deletions commits(last:1){nodes{commit{statusCheckRollup{state}}}}"

// PullDetails reads what OpenPulls left out of list's pull requests,
// into list: a few of them a query, so that none takes the host long
// enough to be cut off, and two queries at once. each, when not nil, is
// called after each query answered, never twice at once nor while list
// is written. The first failure ends it, the rest left as they were.
func (c *CLI) PullDetails(ctx context.Context, r Repo, list []Listed, each func()) error {
	if err := c.reviewable(r); err != nil {
		return err
	}
	type batch struct {
		at  []int
		ids []string
	}
	var batches []batch
	for i := range list {
		if list[i].Detailed || list[i].ID == "" {
			continue
		}
		if n := len(batches); n == 0 || len(batches[n-1].at) == detailBatch {
			batches = append(batches, batch{})
		}
		b := &batches[len(batches)-1]
		b.at, b.ids = append(b.at, i), append(b.ids, list[i].ID)
	}
	var (
		mu    sync.Mutex
		next  int
		first error
		wg    sync.WaitGroup
	)
	for range min(maxRunning, len(batches)) {
		wg.Go(func() {
			for {
				mu.Lock()
				if first != nil || next == len(batches) {
					mu.Unlock()
					return
				}
				b := batches[next]
				next++
				mu.Unlock()
				got, err := c.githubDetails(ctx, r, b.ids)
				mu.Lock()
				if err != nil {
					if first == nil {
						first = err
					}
					mu.Unlock()
					return
				}
				for _, i := range b.at {
					if d, ok := got[list[i].ID]; ok {
						l := &list[i]
						l.Decision, l.Checks, l.Added, l.Deleted, l.Detailed = d.decision, d.checks, d.added, d.deleted, true
					}
				}
				if each != nil {
					each()
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	return first
}

type pullDetail struct {
	decision       ReviewState
	checks         Checks
	added, deleted int
}

// githubDetails reads the details of the pull requests whose node ids
// are ids, by id.
func (c *CLI) githubDetails(ctx context.Context, r Repo, ids []string) (map[string]pullDetail, error) {
	var q strings.Builder
	q.WriteString("query{nodes(ids:[")
	for i, id := range ids {
		if i > 0 {
			q.WriteByte(',')
		}
		q.WriteString(literal(id))
	}
	q.WriteString("]){... on PullRequest{id " + pullDetails + "}}}")
	out, err := c.read(ctx, r.Host, "api", "graphql", "--hostname", r.Host, "-f", "query="+q.String())
	if err != nil {
		// A pull request gone since it was listed is an error to gh,
		// which still prints what GitHub said of the others.
		if got, perr := parseDetails(out); perr == nil && len(got) > 0 {
			return got, nil
		}
		return nil, err
	}
	return parseDetails(out)
}

func parseDetails(out []byte) (map[string]pullDetail, error) {
	var ans struct {
		graphQLError
		Data struct {
			// A node gone since it was listed is null.
			Nodes []*struct {
				githubPull
				ID       string `json:"id"`
				Decision string `json:"reviewDecision"`
			} `json:"nodes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &ans); err != nil {
		return nil, fmt.Errorf("gh: %w", err)
	}
	got := make(map[string]pullDetail, len(ans.Data.Nodes))
	for _, n := range ans.Data.Nodes {
		if n == nil || n.ID == "" {
			continue
		}
		d := pullDetail{decision: reviewState(n.Decision), added: n.Additions, deleted: n.Deletions}
		if ns := n.Commits.Nodes; len(ns) > 0 && ns[0].Commit.Rollup != nil {
			d.checks = githubChecks(ns[0].Commit.Rollup.State)
		}
		got[n.ID] = d
	}
	// GitHub names each node gone as an error, beside the others' answers.
	if len(got) == 0 {
		if err := ans.err("gh"); err != nil {
			return nil, err
		}
	}
	return got, nil
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
						ID      string    `json:"id"`
						Author  *ghAuthor `json:"author"`
						Base    string    `json:"baseRefName"`
						Updated time.Time `json:"updatedAt"`
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
		p := Pull{Number: n.Number, Title: n.Title, URL: n.URL, Branch: n.Head, Draft: n.IsDraft, State: githubState(n.State)}
		list = append(list, Listed{Pull: p, ID: n.ID, Author: n.Author.login(), Base: n.Base, Updated: n.Updated})
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
	// Mergeable is whether the host would merge it now.
	Mergeable Mergeable
	// CheckList are the checks of its head commit, as the host lists
	// them; ChecksMore says it has others.
	CheckList  []Check
	ChecksMore bool
	Labels     []Label
	// Reviewers are those asked to review and those who did, each once.
	Reviewers []Reviewer
	Assignees []string
	// Events are what happened to it beside what was said, oldest first;
	// GitHub's only.
	Events []Event
	// Viewer is who the tool is logged in as.
	Viewer string
	// More says the host holds comments, reviews, threads or events past
	// what was read.
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
		"mergeable mergeStateStatus labels" + page(maxPeople) + "{nodes{name color}} assignees" + page(maxPeople) + "{nodes{login}} " +
		"reviewRequests" + page(maxPeople) + "{nodes{requestedReviewer{" + ghActorNames + "}}} latestReviews" + page(maxPeople) + "{nodes{author{login} state}} " +
		"commits(last:1){nodes{commit{statusCheckRollup{state contexts" + page(maxChecks) + "{totalCount nodes{__typename " +
		"... on CheckRun{name status conclusion detailsUrl startedAt completedAt isRequired(pullRequestNumber:$n) checkSuite{workflowRun{workflow{name}}}} " +
		"... on StatusContext{context state targetUrl isRequired(pullRequestNumber:$n)}}}}}}} " +
		"timelineItems(last:" + strconv.Itoa(maxEvents) + ",itemTypes:[" + ghEventTypes + "]){pageInfo{hasPreviousPage} nodes{__typename " + ghEventFields + "}} " +
		"comments" + page(maxComments) + "{totalCount nodes{id databaseId author{login} body createdAt}} " +
		"reviews" + page(maxComments) + "{totalCount nodes{author{login} state body submittedAt}} " +
		"reviewThreads" + page(maxThreads) + "{totalCount nodes{id isResolved isOutdated path line startLine originalLine diffSide " +
		"viewerCanResolve viewerCanUnresolve viewerCanReply comments" + page(maxComments) + "{totalCount nodes{id databaseId author{login} body createdAt}}}}}}}"
	out, err := c.read(ctx, r.Host, "api", "graphql", "--hostname", r.Host, "-f", "query="+q,
		"-f", "owner="+owner, "-f", "name="+name, "-F", "n="+strconv.Itoa(n))
	if err != nil {
		return Review{}, err
	}
	return parseReview(out)
}

// ghActorNames names who a review is asked of: a person, or a team.
const ghActorNames = "__typename ... on User{login} ... on Bot{login} ... on Mannequin{login} ... on Team{name}"

// ghEventTypes are the timeline items read, and ghEventFields what of
// each.
const (
	ghEventTypes = "PULL_REQUEST_COMMIT,HEAD_REF_FORCE_PUSHED_EVENT,MERGED_EVENT,CLOSED_EVENT,REOPENED_EVENT,READY_FOR_REVIEW_EVENT," +
		"CONVERT_TO_DRAFT_EVENT,REVIEW_REQUESTED_EVENT,LABELED_EVENT,UNLABELED_EVENT,RENAMED_TITLE_EVENT"
	ghEventFields = "... on PullRequestCommit{commit{abbreviatedOid messageHeadline committedDate author{name user{login}}}} " +
		"... on HeadRefForcePushedEvent{actor{login} createdAt beforeCommit{abbreviatedOid} afterCommit{abbreviatedOid}} " +
		"... on MergedEvent{actor{login} createdAt} ... on ClosedEvent{actor{login} createdAt} ... on ReopenedEvent{actor{login} createdAt} " +
		"... on ReadyForReviewEvent{actor{login} createdAt} ... on ConvertToDraftEvent{actor{login} createdAt} " +
		"... on ReviewRequestedEvent{actor{login} createdAt requestedReviewer{" + ghActorNames + "}} " +
		"... on LabeledEvent{actor{login} createdAt label{name}} ... on UnlabeledEvent{actor{login} createdAt label{name}} " +
		"... on RenamedTitleEvent{actor{login} createdAt previousTitle currentTitle}"
)

// ghNamed is a person or a team, as ghActorNames reads one.
type ghNamed struct {
	Type  string `json:"__typename"`
	Login string `json:"login"`
	Name  string `json:"name"`
}

// name is the login, or the team's name; "" for one the login in use
// may not see.
func (g *ghNamed) name() (name string, team bool) {
	if g == nil {
		return "", false
	}
	if g.Type == "Team" {
		return g.Name, true
	}
	return g.Login, false
}

type ghCheck struct {
	Type string `json:"__typename"`
	// A check run's.
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	Conclusion string     `json:"conclusion"`
	DetailsURL string     `json:"detailsUrl"`
	Started    *time.Time `json:"startedAt"`
	Completed  *time.Time `json:"completedAt"`
	Suite      *struct {
		Run *struct {
			Workflow *struct {
				Name string `json:"name"`
			} `json:"workflow"`
		} `json:"workflowRun"`
	} `json:"checkSuite"`
	// A status's.
	Context   string `json:"context"`
	State     string `json:"state"`
	TargetURL string `json:"targetUrl"`

	Required bool `json:"isRequired"`
}

func (g ghCheck) check() Check {
	if g.Type == "StatusContext" {
		c := Check{Name: g.Context, URL: WebURL(g.TargetURL), Required: g.Required, State: CheckWaiting}
		switch g.State {
		case "SUCCESS":
			c.State = CheckPassed
		case "FAILURE", "ERROR":
			c.State = CheckFailed
		case "PENDING":
			c.State = CheckRunning
		}
		return c
	}
	c := Check{Name: g.Name, URL: WebURL(g.DetailsURL), Required: g.Required, State: CheckWaiting}
	if s := g.Suite; s != nil && s.Run != nil && s.Run.Workflow != nil {
		c.Group = s.Run.Workflow.Name
	}
	if g.Started != nil {
		c.Started = *g.Started
	}
	switch g.Status {
	case "IN_PROGRESS":
		c.State = CheckRunning
	case "COMPLETED":
		if g.Completed != nil {
			c.Ended = *g.Completed
		}
		switch g.Conclusion {
		case "SUCCESS":
			c.State = CheckPassed
		case "CANCELLED":
			c.State = CheckCancelled
		case "SKIPPED", "NEUTRAL", "STALE":
			c.State = CheckSkipped
		default:
			// A failure, a time out, an action required, a start failed.
			c.State = CheckFailed
		}
	}
	return c
}

type ghEvent struct {
	Type   string     `json:"__typename"`
	Actor  *ghAuthor  `json:"actor"`
	At     *time.Time `json:"createdAt"`
	Commit *struct {
		ID      string    `json:"abbreviatedOid"`
		Subject string    `json:"messageHeadline"`
		At      time.Time `json:"committedDate"`
		Author  *struct {
			Name string    `json:"name"`
			User *ghAuthor `json:"user"`
		} `json:"author"`
	} `json:"commit"`
	Before *struct {
		ID string `json:"abbreviatedOid"`
	} `json:"beforeCommit"`
	After *struct {
		ID string `json:"abbreviatedOid"`
	} `json:"afterCommit"`
	Reviewer *ghNamed `json:"requestedReviewer"`
	Label    *struct {
		Name string `json:"name"`
	} `json:"label"`
	Previous string `json:"previousTitle"`
	Current  string `json:"currentTitle"`
}

// labelsTogether is how close in time labels one actor adds or takes
// away are one event, as a bot's that sets several at once.
const labelsTogether = time.Minute

// githubEvents reads timeline items into events: the commits that
// follow one another, by one author, are one event, and so are the
// labels one actor adds, or takes away, together.
func githubEvents(items []ghEvent) []Event {
	var out []Event
	for _, it := range items {
		if it.Type == "PullRequestCommit" {
			cm := it.Commit
			if cm == nil {
				continue
			}
			who := ""
			if a := cm.Author; a != nil {
				if who = a.Name; a.User != nil && a.User.Login != "" {
					// An app is dependabot where it writes, and
					// dependabot[bot] where it commits: one name.
					who = strings.TrimSuffix(a.User.Login, "[bot]")
				}
			}
			ec := EventCommit{ID: cm.ID, Subject: cm.Subject}
			if n := len(out); n > 0 && out[n-1].Kind == EventCommits && out[n-1].Actor == who {
				out[n-1].Commits = append(out[n-1].Commits, ec)
				continue
			}
			out = append(out, Event{Kind: EventCommits, Actor: who, At: cm.At, Commits: []EventCommit{ec}})
			continue
		}
		e := Event{Actor: it.Actor.login()}
		if it.At != nil {
			e.At = *it.At
		}
		switch it.Type {
		case "HeadRefForcePushedEvent":
			e.Kind = EventForcePushed
			if it.Before != nil {
				e.From = it.Before.ID
			}
			if it.After != nil {
				e.Subject = it.After.ID
			}
		case "MergedEvent":
			e.Kind = EventMerged
		case "ClosedEvent":
			e.Kind = EventClosed
		case "ReopenedEvent":
			e.Kind = EventReopened
		case "ReadyForReviewEvent":
			e.Kind = EventReady
		case "ConvertToDraftEvent":
			e.Kind = EventDrafted
		case "ReviewRequestedEvent":
			e.Kind = EventReviewRequested
			if e.Subject, _ = it.Reviewer.name(); e.Subject == "" {
				continue
			}
		case "LabeledEvent", "UnlabeledEvent":
			e.Kind = EventLabeled
			if it.Type == "UnlabeledEvent" {
				e.Kind = EventUnlabeled
			}
			if it.Label == nil {
				continue
			}
			if n := len(out); n > 0 && out[n-1].Kind == e.Kind && out[n-1].Actor == e.Actor && e.At.Sub(out[n-1].At) < labelsTogether {
				out[n-1].Labels = append(out[n-1].Labels, it.Label.Name)
				continue
			}
			e.Labels = []string{it.Label.Name}
		case "RenamedTitleEvent":
			e.Kind, e.From, e.Subject = EventRenamed, it.Previous, it.Current
		default:
			continue
		}
		out = append(out, e)
	}
	return out
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
					Body     string    `json:"body"`
					Author   *ghAuthor `json:"author"`
					Base     string    `json:"baseRefName"`
					BaseOID  string    `json:"baseRefOid"`
					HeadOID  string    `json:"headRefOid"`
					Decision string    `json:"reviewDecision"`
					Created  time.Time `json:"createdAt"`
					Merge    string    `json:"mergeable"`
					Status   string    `json:"mergeStateStatus"`
					Labels   struct {
						Nodes []struct {
							Name  string `json:"name"`
							Color string `json:"color"`
						} `json:"nodes"`
					} `json:"labels"`
					Assignees struct {
						Nodes []ghAuthor `json:"nodes"`
					} `json:"assignees"`
					Requests struct {
						Nodes []struct {
							Reviewer *ghNamed `json:"requestedReviewer"`
						} `json:"nodes"`
					} `json:"reviewRequests"`
					Latest struct {
						Nodes []struct {
							Author *ghAuthor `json:"author"`
							State  string    `json:"state"`
						} `json:"nodes"`
					} `json:"latestReviews"`
					Timeline struct {
						// The latest are asked for: more are before them.
						Page struct {
							Before bool `json:"hasPreviousPage"`
						} `json:"pageInfo"`
						Nodes []ghEvent `json:"nodes"`
					} `json:"timelineItems"`
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
		p.Threads.TotalCount > len(p.Threads.Nodes) || p.Timeline.Page.Before
	if rv.State == PullOpen {
		rv.Mergeable = githubMergeable(p.Merge, p.Status)
	}
	if ns := p.Commits.Nodes; len(ns) > 0 && ns[0].Commit.Rollup != nil {
		cs := ns[0].Commit.Rollup.Contexts
		for _, c := range cs.Nodes {
			rv.CheckList = append(rv.CheckList, c.check())
		}
		rv.ChecksMore = cs.TotalCount > len(cs.Nodes)
	}
	for _, l := range p.Labels.Nodes {
		rv.Labels = append(rv.Labels, Label{Name: l.Name, Color: hexColor(l.Color)})
	}
	for _, a := range p.Assignees.Nodes {
		rv.Assignees = append(rv.Assignees, a.Login)
	}
	// One asked again after a verdict is awaited again. A team is not
	// the user of its name.
	awaited := map[string]bool{}
	for _, q := range p.Requests.Nodes {
		if name, team := q.Reviewer.name(); name != "" {
			awaited[name] = awaited[name] || !team
			rv.Reviewers = append(rv.Reviewers, Reviewer{Name: name, Team: team, State: ReviewAwaited})
		}
	}
	for _, l := range p.Latest.Nodes {
		if name := l.Author.login(); !awaited[name] && l.State != "PENDING" {
			rv.Reviewers = append(rv.Reviewers, Reviewer{Name: name, State: reviewState(l.State)})
		}
	}
	rv.Events = githubEvents(p.Timeline.Nodes)
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

// CommentNow posts d, a comment on lines of the commits at, on r's pull
// request n by itself, as a review of that one comment would be, but
// for nothing else: on GitLab a review published takes with it every
// draft the user left pending in the browser, so the comment goes as a
// discussion of its own.
func (c *CLI) CommentNow(ctx context.Context, r Repo, n int, at Revs, d Draft) error {
	if r.Kind != GitLab {
		return c.SubmitReview(ctx, r, n, at, VerdictComment, "", []Draft{d})
	}
	if err := c.reviewable(r); err != nil {
		return err
	}
	if !hexID(at.Head) || !hexID(at.Base) || !hexID(at.Start) {
		return fmt.Errorf("forge: merge request !%d has no diff to comment on", n)
	}
	p := d.position(at)
	return c.gitlabSend(ctx, r, "POST", mrPath(r, n)+"/discussions", struct {
		Body     string      `json:"body"`
		Position *glPosition `json:"position"`
	}{d.Body, &p}, nil)
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
