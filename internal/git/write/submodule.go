package write

import (
	"strconv"
	"time"

	"ramus/internal/git/run"
)

// The submodules at paths, slash-separated from the top of the work
// tree, are taken literally; none means every one.

// SubmoduleInit records in the repository's config the URLs .gitmodules
// gives the submodules, which update then clones from.
func SubmoduleInit(w Where, paths []string) Op {
	return submoduleOp(w, "Initialize", []string{"init"}, paths, 0)
}

// SubmoduleUpdate checks out in each submodule the commit the repository
// records for it, cloning one not there yet; init registers those not
// initialized first, and recursive goes on into their own submodules.
func SubmoduleUpdate(w Where, paths []string, init, recursive bool) Op {
	args := []string{"update", "--progress"}
	if init {
		args = append(args, "--init")
	}
	if recursive {
		args = append(args, "--recursive")
	}
	return submoduleOp(w, "Update", args, paths, PushTimeout)
}

// SubmoduleSync copies the URLs .gitmodules gives into the config and the
// submodules' own remotes, after a URL there changed.
func SubmoduleSync(w Where, paths []string, recursive bool) Op {
	args := []string{"sync"}
	if recursive {
		args = append(args, "--recursive")
	}
	return submoduleOp(w, "Sync the URL of", args, paths, 0)
}

func submoduleOp(w Where, verb string, args, paths []string, timeout time.Duration) Op {
	var name string
	switch len(paths) {
	case 0:
		name = verb + " every submodule"
	case 1:
		name = verb + " submodule " + paths[0]
	default:
		name = verb + " " + strconv.Itoa(len(paths)) + " submodules"
	}
	args = append(append([]string{"submodule"}, args...), "--")
	return Op{Name: name, Cmd: run.Cmd{GitDir: w.GitDir, WorkTree: w.WorkTree, Args: append(args, paths...), Timeout: timeout,
		Env: []string{"GIT_LITERAL_PATHSPECS=1"}}}
}
