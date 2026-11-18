package forge

import (
	"net/url"
	"strings"
	"time"
)

// What a pull request's page shows beside its conversation: the checks
// of its head, whether it can be merged, who reviews it, its labels and
// what happened to it.

// CheckState is where one check of a commit stands.
type CheckState uint8

const (
	// CheckWaiting is a check not started: queued, or waiting on a
	// person or another job.
	CheckWaiting CheckState = iota
	CheckRunning
	CheckPassed
	CheckFailed
	CheckCancelled
	// CheckSkipped is a check that did not run, or says neither.
	CheckSkipped
)

// Check is one check of a pull request's head commit: a job of a
// workflow, a pipeline's job, or a status another service set.
type Check struct {
	Name string
	// Group is the workflow the job is of, or the pipeline's stage; ""
	// for a status.
	Group string
	State CheckState
	// URL is its page, "" when it has none that can be opened (WebURL).
	URL string
	// Started and Ended are zero for a check that has not, and for a
	// status, which says neither.
	Started, Ended time.Time
	// Required is a check the base branch asks for before merging; on
	// GitLab, a job that may not fail.
	Required bool
}

// WebURL is given when it is a page a browser can be sent to from what
// a host answered, else "": https, with no user name. Unlike a pull
// request's own page (Repo.PullURL), a check's is wherever the service
// that ran it keeps it.
func WebURL(given string) string {
	u, err := url.Parse(given)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" || u.Hostname() == "" {
		return ""
	}
	return given
}

// Mergeable is whether a host would merge an open pull request now.
type Mergeable uint8

const (
	// MergeUnknown is a host that has not worked it out, or a pull
	// request not open.
	MergeUnknown Mergeable = iota
	MergeReady
	// MergeConflicts is changes that conflict with the base branch.
	MergeConflicts
	// MergeBehind is a head the base branch asks to be brought up to it.
	MergeBehind
	// MergeBlocked is a rule of the base branch not met: reviews, or
	// checks it requires.
	MergeBlocked
	// MergeUnstable is one that can be merged while checks fail or run.
	MergeUnstable
	MergeDraft
)

func githubMergeable(mergeable, status string) Mergeable {
	switch status {
	case "CLEAN", "HAS_HOOKS":
		return MergeReady
	case "DIRTY":
		return MergeConflicts
	case "BEHIND":
		return MergeBehind
	case "BLOCKED":
		return MergeBlocked
	case "UNSTABLE":
		return MergeUnstable
	case "DRAFT":
		return MergeDraft
	}
	if mergeable == "CONFLICTING" {
		return MergeConflicts
	}
	return MergeUnknown
}

func gitlabMergeable(status string, conflicts bool) Mergeable {
	switch status {
	case "MERGEABLE":
		return MergeReady
	case "CONFLICT":
		return MergeConflicts
	case "NEED_REBASE":
		return MergeBehind
	case "DRAFT_STATUS":
		return MergeDraft
	case "CI_MUST_PASS", "CI_STILL_RUNNING":
		return MergeUnstable
	case "", "CHECKING", "UNCHECKED", "PREPARING", "APPROVALS_SYNCING", "NOT_OPEN":
		if conflicts {
			return MergeConflicts
		}
		return MergeUnknown
	}
	// What else it names is a rule not met.
	return MergeBlocked
}

// Label is a label of a pull request.
type Label struct {
	Name string
	// Color is its color as six hex digits, "" when the host gave none
	// that reads so.
	Color string
}

// hexColor is s as six hex digits, without the # GitLab begins it with,
// else "".
func hexColor(s string) string {
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return ""
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return ""
		}
	}
	return strings.ToLower(s)
}

// Reviewer is someone asked to review a pull request, or who did.
type Reviewer struct {
	// Name is a login, or a team's name.
	Name string
	Team bool
	// State is the latest verdict, or ReviewAwaited for one asked who has
	// not answered since.
	State ReviewState
}

// EventKind is what happened to a pull request.
type EventKind uint8

const (
	// EventCommits is commits added to the head.
	EventCommits EventKind = iota
	EventForcePushed
	EventMerged
	EventClosed
	EventReopened
	EventReady
	EventDrafted
	EventReviewRequested
	EventLabeled
	EventUnlabeled
	EventRenamed
)

// EventCommit is a commit added to a pull request.
type EventCommit struct {
	// ID is its abbreviated id.
	ID, Subject string
}

// Event is something that happened to a pull request, beside what was
// said of it.
type Event struct {
	Kind  EventKind
	Actor string
	At    time.Time
	// Subject is who was asked to review, the new title, or the commit a
	// force push left the head at; From the title before, or the commit
	// before a force push.
	Subject, From string
	// Commits are those added, for EventCommits, oldest first.
	Commits []EventCommit
	// Labels are those added or taken away.
	Labels []string
}

// maxEvents and maxChecks bound what one read of a pull request holds
// of each; maxPeople its labels and reviewers.
const (
	maxEvents = 100
	maxChecks = 100
	maxPeople = 30
)
