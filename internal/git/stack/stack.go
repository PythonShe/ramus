// Package stack finds branches stacked on each other: a branch whose
// first-parent history, before it reaches the trunk, passes through the
// tip of another local branch is stacked on it.
package stack

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"

	"ramus/internal/git/run"
)

// Branch is a local branch: its full ref name and its tip, hex.
type Branch struct {
	Name, Tip string
}

// MaxCommits bounds the commits off the trunk read to find stacks;
// branches deeper than that are not placed in one.
const MaxCommits = 50_000

// Find returns the stacks among branches, each from the bottom branch to
// the top one, sorted by the top's name. trunk are the tips whose history
// is shared by all (the default branch and its upstream); with none,
// nothing is a stack. A branch below two others is in both stacks.
func Find(ctx context.Context, r *run.Runner, gitDir string, branches []Branch, trunk []string) ([][]string, error) {
	if len(trunk) == 0 || len(branches) < 2 {
		return nil, nil
	}
	var in bytes.Buffer
	tipOf := map[string][]string{}
	for _, b := range branches {
		if len(b.Tip) < 40 {
			continue
		}
		tipOf[b.Tip] = append(tipOf[b.Tip], b.Name)
		fmt.Fprintf(&in, "%s\n", b.Tip)
	}
	for _, t := range trunk {
		fmt.Fprintf(&in, "^%s\n", t)
	}
	res, err := r.Run(ctx, &run.Cmd{GitDir: gitDir, Read: true, Stdin: &in,
		Args: []string{"rev-list", "--first-parent", "--parents", fmt.Sprintf("--max-count=%d", MaxCommits), "--stdin"}})
	if err != nil {
		return nil, err
	}
	parent := map[string]string{}
	for _, line := range bytes.Split(res.Stdout, []byte{'\n'}) {
		f := bytes.Fields(line)
		switch len(f) {
		case 0:
		case 1:
			parent[string(f[0])] = ""
		default:
			parent[string(f[0])] = string(f[1])
		}
	}
	if len(parent) >= MaxCommits {
		return nil, errors.New("stack: too many commits off the trunk")
	}
	// below[b] are the branches b's history passes through, nearest first.
	below := map[string][]string{}
	under := map[string]bool{}
	for _, b := range branches {
		if _, ok := parent[b.Tip]; !ok {
			continue // on the trunk
		}
		for c := parent[b.Tip]; ; c = parent[c] {
			if _, off := parent[c]; !off {
				break // reached the trunk
			}
			for _, n := range tipOf[c] {
				below[b.Name] = append(below[b.Name], n)
				under[n] = true
			}
		}
	}
	var stacks [][]string
	for top, bs := range below {
		if under[top] {
			continue
		}
		s := make([]string, 0, len(bs)+1)
		for i := len(bs) - 1; i >= 0; i-- {
			s = append(s, bs[i])
		}
		stacks = append(stacks, append(s, top))
	}
	sort.Slice(stacks, func(i, j int) bool { return stacks[i][len(stacks[i])-1] < stacks[j][len(stacks[j])-1] })
	return stacks, nil
}
