#!/bin/sh
# The full gate: formatting, vet, the engine's import boundary, tests.
# A step that fails says so last, on lines of their own starting
# "check.sh:", so a long log need not be searched for what stopped it:
# for the tests, a line for each test that failed, by package and name.
set -eu
cd "$(dirname "$0")/.."

fail() {
	echo "check.sh: $*" >&2
	exit 1
}

# The checks below search what a command printed. POSIX sh has no
# pipefail, and grep says "no match" and "could not read" by its status
# alone: a command that failed early in a pipe, or a grep that could not
# run, would leave a check nothing to find, and it would pass. So each
# command's output is kept whole before it is searched, and search and
# pick fail the gate when grep fails (status 2), not when it finds
# nothing (1). Called as x=$(pick ...), they fail in a subshell: follow
# them with || exit 1.

# search runs grep with the arguments given.
search() {
	s=0
	grep "$@" || s=$?
	[ "$s" -le 1 ] || fail "grep could not read what a check searches: its message is above"
}

# pick prints the lines of $1 that grep, given the rest, selects.
pick() {
	in=$1
	shift
	printf '%s\n' "$in" | search "$@"
}

# has reports whether $1 holds the line $2.
has() {
	case "
$1
" in
	*"
$2
"*) return 0 ;;
	esac
	return 1
}

unformatted=$(gofmt -l *.go internal spike scripts bench assets locales) || fail "gofmt could not read the files it names above"
if [ -n "$unformatted" ]; then
	echo "$unformatted" >&2
	fail "gofmt needed in the files above"
fi

go vet ./... || fail "go vet failed"
go tool mygo vet . || fail "go tool mygo vet failed"

# The engine must stay usable without the app and its UI framework: it,
# its tests included, on every system it is built for, imports only the
# standard library, its own packages, internal/statefile, and the modules
# below (git's text encodings, zlib, system calls, and macOS's file
# events through purego).
allowed='^(ramus/internal/git(/.*)?|ramus/internal/statefile|golang\.org/x/(sys|text)/.*|github\.com/klauspost/compress/.*|github\.com/ebitengine/purego(/.*)?)$'
for os in darwin linux windows; do
	engine=$(GOOS=$os CGO_ENABLED=0 go list -deps -test -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./internal/git/...) || fail "go list failed"
	has "$engine" ramus/internal/git || fail "go list named no engine package on $os"
	outside=$(pick "$engine" -v -E "$allowed") || exit 1
	if [ -n "$outside" ]; then
		echo "$outside" >&2
		fail "internal/git imports the packages above on $os"
	fi
done

# Network use is git's own, the user's gh or glab when the settings
# turn pull requests on, and the update check the user asks for in the
# About window (about.go, through mygo's updater): no package of ours
# opens a connection, and nothing else reaches mygo's updater, network,
# protocol or cookie modules. The askpass helper talks to the app over a
# Unix socket, so it may import net, for "unix" sockets only. The
# fixtures under internal/git/gittest serve tests alone, a Git LFS server
# on the loopback among them, and the app must not link them.
app=$(go list -deps .) || fail "go list failed"
has "$app" ramus || fail "go list named no main package"
fixtures=$(pick "$app" '^ramus/internal/git/gittest') || exit 1
if [ -n "$fixtures" ]; then
	echo "$fixtures" >&2
	fail "the app links test fixtures"
fi
# What a package links counts, not only what it names: net/http/httputil,
# golang.org/x/net, mygo's netimage or a client library connect as well.
# mygo's UI links net itself, so only the packages that import net by
# name are held to it; the main package links net/http through mygo's
# updater, so it is held to its own imports.
network='^(net|net/http|net/http/.*|net/rpc|net/smtp|crypto/tls|golang.org/x/net/.*|github.com/egoist/mygo/ui/netimage)$'
links=$(go list -f '{{.ImportPath}} {{join .Deps " "}}' ./internal/...) || fail "go list failed"
[ -n "$links" ] || fail "go list named no package under internal/"
linked=$(printf '%s\n' "$links" |
	awk -v net="$network" '$1 !~ /^ramus\/internal\/git\/gittest(\/|$)/ {for (i = 2; i <= NF; i++) if ($i ~ net && $i != "net") print $1 " links " $i}') ||
	fail "awk could not search what the packages link"
if [ -n "$linked" ]; then
	echo "$linked" >&2
	fail "a package of ours uses the network"
fi
set -- internal/askpass/*.go
[ -f "$1" ] || fail "no Go files in internal/askpass, which the checks below name"
imports=$(go list -f '{{.ImportPath}} {{join .Imports " "}}' . ./internal/...) || fail "go list failed"
[ -n "$imports" ] || fail "go list named no package"
imported=$(printf '%s\n' "$imports" |
	awk -v net="$network" '$1 !~ /^ramus\/internal\/git\/gittest(\/|$)/ {for (i = 2; i <= NF; i++) if ($i ~ net && !($1 == "ramus/internal/askpass" && $i == "net")) print $1 " imports " $i}') ||
	fail "awk could not search what the packages import"
if [ -n "$imported" ]; then
	echo "$imported" >&2
	fail "a package of ours uses the network"
fi
sockets=$(search -n -E '\.(Dial|Listen)[A-Za-z]*\(' "$@") || exit 1
sockets=$(pick "$sockets" -v '_test.go:') || exit 1
sockets=$(pick "$sockets" -v '"unix"') || exit 1
if [ -n "$sockets" ]; then
	echo "$sockets" >&2
	fail "askpass opens a socket that is not a Unix socket"
fi
# The check below knows mygo's modules by the package's own name.
named=$(search -rn -E '^[[:space:]]*(import[[:space:]]+)?[A-Za-z_.][A-Za-z0-9_]*[[:space:]]+"github\.com/egoist/mygo"' --include='*.go' *.go internal) || exit 1
named=$(pick "$named" -v -E ':[[:space:]]*(import[[:space:]]+)?mygo[[:space:]]+"') || exit 1
if [ -n "$named" ]; then
	echo "$named" >&2
	fail "mygo is imported under another name"
fi
reached=$(search -rn -E 'mygo\.(Updater|Network|Protocol|Cookies)\b' --include='*.go' *.go internal) || exit 1
reached=$(pick "$reached" -v '_test.go:') || exit 1
reached=$(pick "$reached" -v -E '^about\.go:[0-9]+:.*mygo\.Updater\.') || exit 1
if [ -n "$reached" ]; then
	echo "$reached" >&2
	fail "the app reaches mygo's network modules"
fi

# The packages under internal/ run under the race detector where that
# works: it needs cgo but on macOS, and is left out on Windows. Their
# time is mostly git's, so the detector adds little, and they run beside
# the rest, which the ui package outlasts. The ui package and its own
# measure frames against budgets the detector slows them past, and take
# two and a half times as long under it, so they run without.
race=
case $(go env GOOS) in
darwin) race=1 ;;
linux) [ "$(go env CGO_ENABLED)" = 1 ] && race=1 ;;
esac

# go test's events are kept, in RAMUS_TEST_JSON when it names a folder,
# for CI to list the tests skipped (scripts/testlog), else in a folder of
# our own; once the tests are done, the last lines name from them each
# test that failed.
events=${RAMUS_TEST_JSON:-}
if [ -z "$events" ]; then
	events=$(mktemp -d)
	ours=$events
else
	ours=
fi
racelog=$(mktemp)
racing=
trap 'if [ -n "$racing" ]; then kill "$racing" 2>/dev/null || true; fi; rm -f "$racelog"; if [ -n "$ours" ]; then rm -rf "$ours"; fi' EXIT

# gotest is go test of the arguments after the first, printing go test's
# usual output, its events kept in $events in a file named by the first
# argument. That go test passed is kept in a file, which a pipe in sh
# would lose.
gotest() {
	name=$1
	shift
	mkdir -p "$events"
	passed=$(mktemp)
	shown=0
	{ go test -json "$@" && echo yes >"$passed"; } |
		go run ./scripts/testlog -save "$events/$name.json" || shown=1
	result=$(cat "$passed")
	rm -f "$passed"
	[ "$result" = yes ] && [ "$shown" = 0 ]
}

# failures names, on the gate's last lines, each test that failed in the
# run named $2 and how, and each package that failed without a test to
# name; step $1 of check.sh.
failures() {
	said=$(go run ./scripts/testlog -failed "$1" "$events/$2.json") || said=
	if [ -n "$said" ]; then
		echo "$said" >&2
	else
		echo "check.sh: $1 failed, naming no test: its output is above" >&2
	fi
}

# mygo is pure Go; building without cgo keeps cross-compiling working.
# The write package takes 13 to 18 minutes on GitHub's Windows runner,
# past go test's default timeout of ten, and grows with every test: past
# this one, go test reports a hang.
timeout=30m
if [ -z "$race" ]; then
	if ! CGO_ENABLED=0 gotest all -timeout "$timeout" ./...; then
		failures "go test" all
		exit 1
	fi
	exit 0
fi
pkgs=$(go list ./...) || fail "go list failed"
ui='^ramus/internal/ui(/|$)'
internal=$(pick "$pkgs" '^ramus/internal/') || exit 1
raced=$(pick "$internal" -v -E "$ui") || exit 1
[ -n "$raced" ] || fail "go list named no package under internal/ to run under the race detector"
views=$(pick "$pkgs" -E "$ui") || exit 1
[ -n "$views" ] || fail "go list named no ui package"
rest=$(pick "$pkgs" -v '^ramus/internal/') || exit 1
has "$rest" ramus || fail "go list named no main package"

# The race run prints to a file of its own, shown once the rest is done:
# two go tests printing to one log interleave. Both are waited for, so a
# failure in one still shows all the other found.
# One package a word.
# shellcheck disable=SC2086
gotest race -race -timeout "$timeout" $raced >"$racelog" 2>&1 &
racing=$!
failed=
# shellcheck disable=SC2086
CGO_ENABLED=0 gotest rest -timeout "$timeout" $rest $views || failed=1
racefailed=
wait "$racing" || racefailed=1
racing=
echo "--- go test -race, the packages under internal/ but the ui's"
cat "$racelog"
if [ -n "$failed" ]; then
	failures "go test" rest
fi
if [ -n "$racefailed" ]; then
	failures "go test -race" race
fi
[ -z "$racefailed$failed" ] || exit 1
