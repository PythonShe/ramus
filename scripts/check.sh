#!/bin/sh
# The full gate: formatting, vet, the engine's import boundary, tests.
set -eu
cd "$(dirname "$0")/.."

unformatted=$(gofmt -l *.go internal spike scripts)
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

# Network use is git's own: no package of ours opens a connection, and
# none reaches mygo's updater, network, protocol or cookie modules. A
# future update check is the one place to allow here. The askpass helper
# talks to the app over a Unix socket, so it may import net, for "unix"
# sockets only.
if go list -f '{{.ImportPath}} {{join .Imports " "}}' . ./internal/... |
	awk '{for (i = 2; i <= NF; i++) if ($i ~ /^(net|net\/http|net\/rpc|net\/smtp|crypto\/tls)$/ && !($1 == "ramus/internal/askpass" && $i == "net")) print $1 " imports " $i}' | grep .; then
	echo "a package of ours uses the network" >&2
	exit 1
fi
if grep -n -E 'net\.(Dial|Listen)[A-Za-z]*\(' internal/askpass/*.go | grep -v '_test.go:' | grep -v '("unix"'; then
	echo "askpass opens a socket that is not a Unix socket" >&2
	exit 1
fi
if grep -rn -E 'mygo\.(Updater|Network|Protocol|Cookies)\b' --include='*.go' *.go internal | grep -v '_test.go:'; then
	echo "the app reaches mygo's network modules" >&2
	exit 1
fi

# mygo is pure Go; building without cgo keeps cross-compiling working.
CGO_ENABLED=0 go test ./...
