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

// GitLab's half of a review. A merge request's threads are its
// discussions on lines of the diff; a review is draft notes published
// together, with an approval apart from them.

func (c *CLI) gitlabOpenPulls(ctx context.Context, r Repo) ([]Listed, bool, error) {
	q := "query{project(fullPath:" + literal(r.Path) + "){mergeRequests(state:opened,first:" + strconv.Itoa(MaxListed) +
		",sort:UPDATED_DESC){count nodes{iid title webUrl draft state author{username} sourceBranch targetBranch updatedAt " +
		"approved detailedMergeStatus headPipeline{status} diffStatsSummary{additions deletions}}}}}"
	out, err := c.run(ctx, "", r.Host, "api", "graphql", "--hostname", r.Host, "-f", "query="+q)
	if err != nil {
		return nil, false, err
	}
	return parseGitLabOpen(out, r.Path)
}

type glUser struct {
	Username string `json:"username"`
}

// login is the user's name; a deleted account has none.
func (u *glUser) login() string {
	if u == nil {
		return "ghost"
	}
	return u.Username
}

type glListed struct {
	gitlabMR
	Author   *glUser   `json:"author"`
	Target   string    `json:"targetBranch"`
	Updated  time.Time `json:"updatedAt"`
	Approved bool      `json:"approved"`
	Status   string    `json:"detailedMergeStatus"`
}

// decision is what a merge request's approvals add up to.
func (m glListed) decision() ReviewState {
	switch {
	case m.Approved:
		return ReviewApproved
	case m.Status == "REQUESTED_CHANGES":
		return ReviewChangesRequested
	case m.Status == "NOT_APPROVED":
		return ReviewRequired
	}
	return ReviewNone
}

func (m glListed) pull() (Pull, bool) {
	n, err := strconv.Atoi(m.IID)
	if err != nil {
		return Pull{}, false
	}
	p := Pull{Number: n, Title: m.Title, URL: m.WebURL, Branch: m.Branch, Draft: m.Draft, State: gitlabState(m.State)}
	if m.Pipeline != nil {
		p.Checks = gitlabChecks(m.Pipeline.Status)
	}
	if m.Stats != nil {
		p.Added, p.Deleted = m.Stats.Additions, m.Stats.Deletions
	}
	return p, true
}

func parseGitLabOpen(out []byte, path string) ([]Listed, bool, error) {
	var ans struct {
		graphQLError
		Data struct {
			Project *struct {
				MRs struct {
					Count int        `json:"count"`
					Nodes []glListed `json:"nodes"`
				} `json:"mergeRequests"`
			} `json:"project"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &ans); err != nil {
		return nil, false, fmt.Errorf("glab: %w", err)
	}
	if err := ans.err("glab"); err != nil {
		return nil, false, err
	}
	if ans.Data.Project == nil {
		return nil, false, fmt.Errorf("%w: glab: no project %s, or no access to it", ErrNotFound, path)
	}
	mrs := ans.Data.Project.MRs
	list := make([]Listed, 0, len(mrs.Nodes))
	for _, m := range mrs.Nodes {
		if p, ok := m.pull(); ok {
			list = append(list, Listed{Pull: p, Author: m.Author.login(), Base: m.Target, Updated: m.Updated, Decision: m.decision()})
		}
	}
	return list, mrs.Count > len(mrs.Nodes), nil
}

func (c *CLI) gitlabPullReview(ctx context.Context, r Repo, n int) (Review, error) {
	// Its discussions, read whole, would hold the host's own notes of
	// pushes and labels among those written; its notes can leave them out.
	page := "(first:" + strconv.Itoa(maxComments) + ")"
	q := "query{currentUser{username} project(fullPath:" + literal(r.Path) + "){mergeRequest(iid:" + literal(strconv.Itoa(n)) + "){" +
		"iid title webUrl description state draft author{username} sourceBranch targetBranch createdAt approved detailedMergeStatus " +
		"headPipeline{status} diffStatsSummary{additions deletions} diffRefs{baseSha startSha headSha} userPermissions{createNote} approvedBy" + page + "{nodes{username}} " +
		"notes(filter:ONLY_COMMENTS,first:" + strconv.Itoa(glMaxNotes) + "){pageInfo{hasNextPage} nodes{id author{username} body createdAt system " +
		"discussion{id resolvable resolved userPermissions{resolveNote}} " +
		"position{positionType filePath oldPath newPath oldLine newLine diffRefs{headSha}}}}}}}"
	out, err := c.run(ctx, "", r.Host, "api", "graphql", "--hostname", r.Host, "-f", "query="+q)
	if err != nil {
		return Review{}, err
	}
	return parseGitLabReview(out)
}

// glMaxNotes bounds the comments read of a merge request, its threads'
// among them: GitLab answers no more in one page.
const glMaxNotes = 100

type glNote struct {
	ID     string    `json:"id"`
	Author *glUser   `json:"author"`
	Body   string    `json:"body"`
	At     time.Time `json:"createdAt"`
	System bool      `json:"system"`
	Thread *struct {
		ID         string `json:"id"`
		Resolvable bool   `json:"resolvable"`
		Resolved   bool   `json:"resolved"`
		Can        struct {
			Resolve bool `json:"resolveNote"`
		} `json:"userPermissions"`
	} `json:"discussion"`
	Position *struct {
		Type     string `json:"positionType"`
		FilePath string `json:"filePath"`
		NewPath  string `json:"newPath"`
		OldLine  *int   `json:"oldLine"`
		NewLine  *int   `json:"newLine"`
		DiffRefs struct {
			Head string `json:"headSha"`
		} `json:"diffRefs"`
	} `json:"position"`
}

func (g glNote) comment() Comment {
	return Comment{ID: g.ID, Author: g.Author.login(), Body: g.Body, At: g.At}
}

type glPage struct {
	Next bool `json:"hasNextPage"`
}

func parseGitLabReview(out []byte) (Review, error) {
	var ans struct {
		graphQLError
		Data struct {
			Viewer  *glUser `json:"currentUser"`
			Project *struct {
				MR *struct {
					glListed
					Body     string    `json:"description"`
					Created  time.Time `json:"createdAt"`
					DiffRefs *struct {
						Base  string `json:"baseSha"`
						Start string `json:"startSha"`
						Head  string `json:"headSha"`
					} `json:"diffRefs"`
					Can struct {
						Note bool `json:"createNote"`
					} `json:"userPermissions"`
					ApprovedBy *struct {
						Nodes []glUser `json:"nodes"`
					} `json:"approvedBy"`
					Notes struct {
						Page  glPage   `json:"pageInfo"`
						Nodes []glNote `json:"nodes"`
					} `json:"notes"`
				} `json:"mergeRequest"`
			} `json:"project"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &ans); err != nil {
		return Review{}, fmt.Errorf("glab: %w", err)
	}
	if err := ans.err("glab"); err != nil {
		return Review{}, err
	}
	if ans.Data.Project == nil || ans.Data.Project.MR == nil {
		return Review{}, ErrNotFound
	}
	m := ans.Data.Project.MR
	p, ok := m.pull()
	if !ok {
		return Review{}, ErrNotFound
	}
	rv := Review{Pull: p, Author: m.Author.login(), Body: m.Body, Base: m.Target, Decision: m.decision(), Created: m.Created}
	if ans.Data.Viewer != nil {
		rv.Viewer = ans.Data.Viewer.Username
	}
	if d := m.DiffRefs; d != nil {
		rv.BaseOID, rv.StartOID, rv.HeadOID = d.Base, d.Start, d.Head
	}
	if m.ApprovedBy != nil {
		for _, u := range m.ApprovedBy.Nodes {
			rv.Reviews = append(rv.Reviews, Submitted{Author: u.Username, State: ReviewApproved})
		}
	}
	rv.More = m.Notes.Page.Next
	// A thread's notes come in the order written, among the others.
	at := map[string]int{}
	for _, n := range m.Notes.Nodes {
		if n.System {
			continue
		}
		d, pos := n.Thread, n.Position
		if d == nil || pos == nil || pos.Type != "text" {
			rv.Comments = append(rv.Comments, n.comment())
			continue
		}
		if i, ok := at[d.ID]; ok {
			rv.Threads[i].Comments = append(rv.Threads[i].Comments, n.comment())
			continue
		}
		th := Thread{ID: d.ID, Path: pos.NewPath, Resolved: d.Resolved, CanResolve: d.Resolvable && d.Can.Resolve && !d.Resolved,
			CanUnresolve: d.Resolvable && d.Can.Resolve && d.Resolved, CanReply: m.Can.Note, Comments: []Comment{n.comment()}}
		if th.Path == "" {
			th.Path = pos.FilePath
		}
		line := 0
		if pos.NewLine != nil {
			line = *pos.NewLine
		} else if pos.OldLine != nil {
			line, th.Side = *pos.OldLine, Left
		}
		// GitLab moves a discussion to each new head its line is still in;
		// one left on an older head is on lines changed since.
		if pos.DiffRefs.Head != rv.HeadOID {
			th.Outdated, th.OriginalLine = true, line
		} else {
			th.Line, th.OriginalLine = line, line
		}
		at[d.ID] = len(rv.Threads)
		rv.Threads = append(rv.Threads, th)
	}
	return rv, nil
}

// mrPath is the REST path of the merge request n of r.
func mrPath(r Repo, n int) string {
	return "projects/" + projectID(r.Path) + "/merge_requests/" + strconv.Itoa(n)
}

// gitlabDiscussion is the REST id of a discussion GraphQL names
// gid://gitlab/Discussion/<id>.
func gitlabDiscussion(id string) (string, error) {
	s := id[strings.LastIndexByte(id, '/')+1:]
	if len(s) == 0 || len(s) > 64 {
		return "", fmt.Errorf("forge: %q is not a discussion", id)
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", fmt.Errorf("forge: %q is not a discussion", id)
		}
	}
	return s, nil
}

// gitlabSend sends v as JSON to the REST path of r's host with method,
// and reads the answer into into, when not nil.
func (c *CLI) gitlabSend(ctx context.Context, r Repo, method, path string, v, into any) error {
	var body []byte
	args := []string{"api", "--hostname", r.Host, "--method", method, path}
	if v != nil {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		body = b
		args = append(args, "--input", "-")
	}
	out, err := c.runInput(ctx, r.Host, body, args...)
	if err != nil || into == nil {
		return err
	}
	if err := json.Unmarshal(out, into); err != nil {
		return fmt.Errorf("glab: %w", err)
	}
	return nil
}

type glPosition struct {
	Base    string `json:"base_sha"`
	Start   string `json:"start_sha"`
	Head    string `json:"head_sha"`
	Type    string `json:"position_type"`
	OldPath string `json:"old_path"`
	NewPath string `json:"new_path"`
	OldLine int    `json:"old_line,omitempty"`
	NewLine int    `json:"new_line,omitempty"`
}

// position places d on the diff of at. GitLab takes a changed line by
// the number on its own side and an unchanged one by both; a comment on
// several lines goes on its last.
func (d Draft) position(at Revs) glPosition {
	p := glPosition{Base: at.Base, Start: at.Start, Head: at.Head, Type: "text", OldPath: d.OldPath, NewPath: d.Path}
	if p.OldPath == "" {
		p.OldPath = d.Path
	}
	if d.Side == Left {
		p.OldLine, p.NewLine = d.Line, d.Across
	} else {
		p.NewLine, p.OldLine = d.Line, d.Across
	}
	return p
}

// gitlabSubmit publishes a review of merge request n: the drafts and the
// summary as draft notes published together, then the approval. GitLab
// publishes every draft note the user has pending on it, those begun in
// the browser too, as its page does.
func (c *CLI) gitlabSubmit(ctx context.Context, r Repo, n int, at Revs, v Verdict, body string, drafts []Draft) error {
	mr := mrPath(r, n)
	if v == VerdictApprove && len(drafts) == 0 {
		if body != "" {
			if err := c.gitlabSend(ctx, r, "POST", mr+"/notes", map[string]string{"body": body}, nil); err != nil {
				return err
			}
			if err := c.gitlabApprove(ctx, r, n, at.Head); err != nil {
				return &SentError{Err: err}
			}
			return nil
		}
		return c.gitlabApprove(ctx, r, n, at.Head)
	}
	if !hexID(at.Base) || !hexID(at.Start) {
		return fmt.Errorf("forge: merge request !%d has no diff to comment on", n)
	}
	type draftNote struct {
		Note     string      `json:"note"`
		Position *glPosition `json:"position,omitempty"`
	}
	notes := make([]draftNote, 0, len(drafts)+1)
	summary := body
	if len(drafts) == 0 {
		// Publishing takes a draft to publish: the summary is the one.
		notes, summary = append(notes, draftNote{Note: body}), ""
	}
	for _, d := range drafts {
		p := d.position(at)
		notes = append(notes, draftNote{Note: d.Body, Position: &p})
	}
	var made []int64
	// Left pending, the drafts made would go out with the next review
	// published, this one's sent again among them.
	drop := func(ids []int64) {
		for _, id := range ids {
			c.gitlabSend(context.WithoutCancel(ctx), r, "DELETE", mr+"/draft_notes/"+strconv.FormatInt(id, 10), nil, nil)
		}
	}
	for _, dn := range notes {
		var ans struct {
			ID int64 `json:"id"`
		}
		if err := c.gitlabSend(ctx, r, "POST", mr+"/draft_notes", dn, &ans); err != nil {
			drop(made)
			return err
		}
		made = append(made, ans.ID)
	}
	pub := struct {
		Note  string `json:"note,omitempty"`
		State string `json:"reviewer_state,omitempty"`
	}{Note: summary}
	if v == VerdictRequestChanges {
		pub.State = "requested_changes"
	}
	if err := c.gitlabSend(ctx, r, "POST", mr+"/draft_notes/bulk_publish", pub, nil); err != nil {
		drop(made)
		return err
	}
	if v == VerdictApprove {
		if err := c.gitlabApprove(ctx, r, n, at.Head); err != nil {
			return &SentError{Err: err}
		}
	}
	return nil
}

// SentError is a review whose comments went out but whose approval did
// not: sending it again would post them twice.
type SentError struct{ Err error }

func (e *SentError) Error() string {
	return "forge: the comments were sent, the approval was not: " + e.Err.Error()
}
func (e *SentError) Unwrap() error { return e.Err }

// ErrHeadMoved is an approval of a head the merge request has moved on
// from.
var ErrHeadMoved = errors.New("forge: the merge request has new commits")

// gitlabApprove approves merge request n as of head: GitLab answers 409
// when the head has moved on since.
func (c *CLI) gitlabApprove(ctx context.Context, r Repo, n int, head string) error {
	err := c.gitlabSend(ctx, r, "POST", mrPath(r, n)+"/approve", map[string]string{"sha": head}, nil)
	if err != nil && strings.Contains(err.Error(), "409") {
		return fmt.Errorf("%w: %v", ErrHeadMoved, err)
	}
	return err
}
