#!/bin/sh
# The full gate: formatting, vet, the engine's import boundary, tests.
# A step that fails says so last, on a line of its own starting
# "check.sh:", so a long log need not be searched for what stopped it.
set -eu
cd "$(dirname "$0")/.."

fail() {
	echo "check.sh: $*" >&2
	exit 1
}

unformatted=$(gofmt -l *.go internal spike scripts bench assets locales) || fail "gofmt could not read the files it names above"
if [ -n "$unformatted" ]; then
	echo "$unformatted" >&2
	fail "gofmt needed in the files above"
fi

go vet ./... || fail "go vet failed"
go tool mygo vet . || fail "go tool mygo vet failed"

# What go list prints is kept whole before it is searched: piped into
# grep, a go list that failed would give the check nothing to find, and
# the check would pass.
#
# The engine must stay usable without the app and its UI framework: it,
# its tests included, on every system it is built for, imports only the
# standard library, its own packages, internal/statefile, and the modules
# below (git's text encodings, zlib, system calls, and macOS's file
# events through purego).
allowed='^(ramus/internal/git(/.*)?|ramus/internal/statefile|golang\.org/x/(sys|text)/.*|github\.com/klauspost/compress/.*|github\.com/ebitengine/purego(/.*)?)$'
for os in darwin linux windows; do
	engine=$(GOOS=$os CGO_ENABLED=0 go list -deps -test -f '{{if not .Standard}}{{.ImportPath}}{{end}}' ./internal/git/...) || fail "go list failed"
	if echo "$engine" | grep -v -E "$allowed" | grep . >&2; then
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
if echo "$app" | grep '^ramus/internal/git/gittest'; then
	fail "the app links test fixtures"
fi
# What a package links counts, not only what it names: net/http/httputil,
# golang.org/x/net, mygo's netimage or a client library connect as well.
# mygo's UI links net itself, so only the packages that import net by
# name are held to it; the main package links net/http through mygo's
# updater, so it is held to its own imports.
network='^(net|net/http|net/http/.*|net/rpc|net/smtp|crypto/tls|golang.org/x/net/.*|github.com/egoist/mygo/ui/netimage)$'
links=$(go list -f '{{.ImportPath}} {{join .Deps " "}}' ./internal/...) || fail "go list failed"
if echo "$links" |
	awk -v net="$network" '$1 !~ /^ramus\/internal\/git\/gittest(\/|$)/ {for (i = 2; i <= NF; i++) if ($i ~ net && $i != "net") print $1 " links " $i}' | grep .; then
	fail "a package of ours uses the network"
fi
imports=$(go list -f '{{.ImportPath}} {{join .Imports " "}}' . ./internal/...) || fail "go list failed"
if echo "$imports" |
	awk -v net="$network" '$1 !~ /^ramus\/internal\/git\/gittest(\/|$)/ {for (i = 2; i <= NF; i++) if ($i ~ net && !($1 == "ramus/internal/askpass" && $i == "net")) print $1 " imports " $i}' | grep .; then
	fail "a package of ours uses the network"
fi
if grep -n -E '\.(Dial|Listen)[A-Za-z]*\(' internal/askpass/*.go | grep -v '_test.go:' | grep -v '"unix"'; then
	fail "askpass opens a socket that is not a Unix socket"
fi
# The check below knows mygo's modules by the package's own name.
if grep -rn -E '^[[:space:]]*(import[[:space:]]+)?[A-Za-z_.][A-Za-z0-9_]*[[:space:]]+"github\.com/egoist/mygo"' --include='*.go' *.go internal | grep -v -E ':[[:space:]]*(import[[:space:]]+)?mygo[[:space:]]+"'; then
	fail "mygo is imported under another name"
fi
if grep -rn -E 'mygo\.(Updater|Network|Protocol|Cookies)\b' --include='*.go' *.go internal | grep -v '_test.go:' | grep -v -E '^about\.go:[0-9]+:.*mygo\.Updater\.'; then
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

# mygo is pure Go; building without cgo keeps cross-compiling working.
# The ui package takes five minutes on GitHub's Windows runner, half of
# go test's default timeout, and grows with every view: past it, go
# test reports a hang.
timeout=20m

# gotest is go test of the arguments after the first. With
# RAMUS_TEST_JSON set, a folder, go test's events are also kept there, in
# a file named by the first argument, for CI to list the tests skipped
# (scripts/testlog); what is printed stays go test's usual output. That
# go test passed is kept in a file, which a pipe in sh would lose.
gotest() {
	name=$1
	shift
	if [ -z "${RAMUS_TEST_JSON:-}" ]; then
		go test "$@"
		return
	fi
	mkdir -p "$RAMUS_TEST_JSON"
	passed=$(mktemp)
	shown=0
	{ go test -json "$@" && echo yes >"$passed"; } |
		go run ./scripts/testlog -save "$RAMUS_TEST_JSON/$name.json" || shown=1
	result=$(cat "$passed")
	rm -f "$passed"
	[ "$result" = yes ] && [ "$shown" = 0 ]
}

if [ -z "$race" ]; then
	CGO_ENABLED=0 gotest all -timeout "$timeout" ./... || fail "go test failed: the FAIL lines above name the package and the test"
	exit 0
fi
pkgs=$(go list ./...) || fail "go list failed"
ui='^ramus/internal/ui(/|$)'
raced=$(echo "$pkgs" | grep '^ramus/internal/' | grep -v -E "$ui")
rest=$(echo "$pkgs" | grep -v '^ramus/internal/' | grep -v -E "$ui" || true)
rest="$rest $(echo "$pkgs" | grep -E "$ui")"

# The race run writes to a file of its own, shown once the rest is done:
# two go tests writing to one log interleave. Both are waited for, so a
# failure in one still shows all the other found.
racelog=$(mktemp)
# One package a word.
# shellcheck disable=SC2086
gotest race -race -timeout "$timeout" $raced >"$racelog" 2>&1 &
racing=$!
trap 'kill "$racing" 2>/dev/null || true; rm -f "$racelog"' EXIT
failed=
# shellcheck disable=SC2086
CGO_ENABLED=0 gotest rest -timeout "$timeout" $rest || failed=1
racefailed=
wait "$racing" || racefailed=1
echo "--- go test -race, the packages under internal/ but the ui's"
cat "$racelog"
if [ -n "$racefailed" ]; then
	# A race seen outside any test ends the run with "Found N data
	# race(s)" and no "--- FAIL" line to look for.
	grep -E '^(--- FAIL|FAIL|panic:|Found [0-9]+ data race)' "$racelog" >&2 || true
	echo "check.sh: go test -race failed: its log is above, a race under WARNING: DATA RACE" >&2
fi
if [ -n "$failed" ]; then
	echo "check.sh: go test failed: the FAIL lines above name the package and the test" >&2
fi
[ -z "$racefailed$failed" ] || exit 1
