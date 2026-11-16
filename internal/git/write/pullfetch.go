package write

import (
	"errors"
	"strconv"
	"strings"

	"ramus/internal/git/run"
)

// PullPrefix is where the commits of pull requests under review are
// kept: PullHead and PullBase of each, which history does not show.
const PullPrefix = "refs/ramus/pulls/"

// PullHead is the ref that keeps pull request n's head once fetched.
func PullHead(n int) string { return PullPrefix + strconv.Itoa(n) + "/head" }

// PullBase is the ref that keeps the tip of pull request n's base branch.
func PullBase(n int) string { return PullPrefix + strconv.Itoa(n) + "/base" }

// FetchPull fetches pull request n's head, as the host publishes it at
// hostRef (refs/pull/N/head on GitHub), and its base branch from from, a
// remote's name or a URL, into PullHead and PullBase. Nothing else
// moves: no remote branch, no tag, not FETCH_HEAD. deepen, above 0,
// fetches a shallow clone's history that many commits deeper.
func FetchPull(w Where, from string, n int, hostRef, base string, deepen int) (Op, error) {
	if err := checkName("remote", from); err != nil {
		return Op{}, err
	}
	if n <= 0 || !strings.HasPrefix(hostRef, "refs/") || strings.ContainsAny(hostRef, ": ") {
		return Op{}, errors.New("write: no pull request chosen")
	}
	if err := checkName("branch", base); err != nil || strings.ContainsAny(base, ": ") {
		return Op{}, errors.New("write: the pull request has no base branch")
	}
	args := []string{"fetch", "--progress", "--no-tags", "--no-write-fetch-head", "--refmap="}
	if deepen > 0 {
		args = append(args, "--deepen="+strconv.Itoa(deepen))
	}
	args = append(args, "--end-of-options", from,
		"+"+hostRef+":"+PullHead(n), "+refs/heads/"+base+":"+PullBase(n))
	return Op{Name: "Fetch pull request #" + strconv.Itoa(n), RefsOnly: true, NoIndex: true,
		Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: args, Timeout: PushTimeout, Stall: NetworkStall}}, nil
}
