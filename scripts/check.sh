#!/bin/sh
# The full gate: formatting, vet, the engine's import boundary, tests.
set -eu
cd "$(dirname "$0")/.."

unformatted=$(gofmt -l main.go internal)
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

# mygo is pure Go; building without cgo keeps cross-compiling working.
CGO_ENABLED=0 go test ./...
