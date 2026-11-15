#!/bin/sh
# The full gate: formatting, vet, the engine's import boundary, tests.
set -eu
cd "$(dirname "$0")/.."

unformatted=$(gofmt -l *.go internal spike scripts bench)
if [ -n "$unformatted" ]; then
	echo "gofmt needed:" >&2
	echo "$unformatted" >&2
	exit 1
fi

go vet ./...
go tool mygo vet .

# The engine must stay usable without the UI framework.
if go list -deps ./internal/git/... | grep -q '^github.com/egoist/mygo'; then
	echo "internal/git imports mygo" >&2
	exit 1
fi

# Network use is git's own, the user's gh or glab when the settings
# turn pull requests on, and the update check the user asks for in the
# About window (about.go, through mygo's updater): no package of ours
# opens a connection, and nothing else reaches mygo's updater, network,
# protocol or cookie modules. The askpass helper talks to the app over a
# Unix socket, so it may import net, for "unix" sockets only. The
# fixtures under internal/git/gittest serve tests alone, a Git LFS server
# on the loopback among them, and the app must not link them.
if go list -deps . | grep '^ramus/internal/git/gittest'; then
	echo "the app links test fixtures" >&2
	exit 1
fi
if go list -f '{{.ImportPath}} {{join .Imports " "}}' . ./internal/... |
	awk '$1 !~ /^ramus\/internal\/git\/gittest(\/|$)/ {for (i = 2; i <= NF; i++) if ($i ~ /^(net|net\/http|net\/rpc|net\/smtp|crypto\/tls)$/ && !($1 == "ramus/internal/askpass" && $i == "net")) print $1 " imports " $i}' | grep .; then
	echo "a package of ours uses the network" >&2
	exit 1
fi
if grep -n -E 'net\.(Dial|Listen)[A-Za-z]*\(' internal/askpass/*.go | grep -v '_test.go:' | grep -v '("unix"'; then
	echo "askpass opens a socket that is not a Unix socket" >&2
	exit 1
fi
if grep -rn -E 'mygo\.(Updater|Network|Protocol|Cookies)\b' --include='*.go' *.go internal | grep -v '_test.go:' | grep -v -E '^about\.go:[0-9]+:.*mygo\.Updater\.'; then
	echo "the app reaches mygo's network modules" >&2
	exit 1
fi

# The session package is where the main thread meets background work,
# so it runs under the race detector where that works: it needs cgo but
# on macOS, and is left out on Windows. Its time is git's, so the
# detector adds little, and it runs beside the rest, which the ui package
# outlasts.
race=
case $(go env GOOS) in
darwin) race=1 ;;
linux) [ "$(go env CGO_ENABLED)" = 1 ] && race=1 ;;
esac

# mygo is pure Go; building without cgo keeps cross-compiling working.
if [ -n "$race" ]; then
	go test -race ./internal/session/ &
	racing=$!
	trap 'kill "$racing" 2>/dev/null || true' EXIT
	CGO_ENABLED=0 go test $(go list ./... | grep -v '^ramus/internal/session$')
	wait "$racing"
else
	CGO_ENABLED=0 go test ./...
fi
