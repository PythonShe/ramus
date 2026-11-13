// Package trust decides how much a repository may run: one Ramus did not
// clone and the user has not trusted opens restricted, reading through the
// native readers and through git only with every way its own config could
// start a program switched off.
package trust

import (
	"os"
	"strings"

	"ramus/internal/git/config"
)

// Hardening returns the -c overrides for git in a repository that is not
// trusted, given its config. They come after anything else on the command
// line, so they win over every config file:
//
//   - core.fsmonitor and hooks (core.hooksPath at a path that holds none)
//     run programs on reads;
//   - log.showSignature and gpg programs run on log;
//   - diff.external, and the drivers its own config defines for filters,
//     diffs and merges, run programs on status, diff and checkout.
//
// Drivers from the user's global config stay: the user chose them.
func Hardening(cfg *config.Config) []string {
	out := []string{
		"core.fsmonitor=false",
		"core.hooksPath=" + os.DevNull,
		"core.pager=cat",
		"safe.bareRepository=explicit",
		"diff.external=",
		"log.showSignature=false",
	}
	seen := map[string]bool{}
	add := func(kv string) {
		if !seen[kv] {
			seen[kv] = true
			out = append(out, kv)
		}
	}
	for _, e := range cfg.Entries() {
		if e.Scope != config.Local && e.Scope != config.Worktree {
			continue
		}
		section, rest, ok := strings.Cut(e.Key, ".")
		if !ok {
			continue
		}
		i := strings.LastIndexByte(rest, '.')
		if i < 0 {
			if section == "gpg" && rest == "program" {
				add("gpg.program=")
			}
			continue
		}
		sub := rest[:i]
		switch section {
		case "filter":
			for _, k := range []string{"clean=", "smudge=", "process=", "required=false"} {
				add("filter." + sub + "." + k)
			}
		case "diff":
			add("diff." + sub + ".textconv=")
			add("diff." + sub + ".command=")
		case "merge":
			add("merge." + sub + ".driver=")
		case "gpg":
			add("gpg." + sub + ".program=")
		}
	}
	return out
}
