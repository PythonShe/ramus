# Contributing to Ramus

Ramus is written in Go, with its interface drawn on the GPU by
[mygo](https://github.com/egoist/mygo): no web view, no cgo.

## Setup

You need [Go](https://go.dev) (the version in `go.mod`) and git 2.36 or
later. Nothing else: mygo comes in as a Go tool.

```sh
go tool mygo dev      # run with live reload
go tool mygo build    # package the app into build/
scripts/check.sh      # the full gate: gofmt, vet, the engine's import boundary, no network of ours, the tests
```

It builds for macOS, Windows and Linux. CI runs the tests on all three
when a maintainer starts it and before every release; it does not run on
pull requests by itself.
On Windows, set `git config --global core.autocrlf false` before the tests:
they compare files byte for byte.

Run `scripts/check.sh` before sending a change; it must pass.

## Layout

| Path | What it is |
|------|------------|
| `main.go`, `windows.go` | Starts the app and keeps its windows, one `ui.App` each |
| `about.go` | The About window and the update check |
| `internal/git/` | The engine: readers of git's files on disk, and the writes, which run `git` |
| `internal/session/` | A repository open in the app: its state, loads and write queue |
| `internal/ui/` | App state and views |
| `internal/forge/` | GitHub and GitLab, through `gh` and `glab` |
| `internal/cli/` | The `ramus` command line |
| `locales/` | Every text a user reads, in `en.json` |
| `bench/` | The benchmarks; see [docs/benchmarks.md](docs/benchmarks.md) |
| `assets/app-icon/` | The app icon; see its README before touching an output |

## How Ramus is built

- **Reads are native, writes run `git`.** Reads go through Ramus's own
  readers of git's on-disk formats: the index, packs, the commit-graph,
  refs in both the files and the reftable format. Everything that writes
  runs the `git` executable, so hooks, signing and credentials behave as in
  the terminal.
- **Hand off what is not supported.** When the engine meets something it
  does not read (SHA-256 objects, partial clones, sparse or split indexes),
  it hands that repository or operation to `git` instead of guessing.
- **git is the oracle.** A reader is tested by comparing its output with
  git's on real repositories.
- **The main thread never waits** on I/O, a subprocess or a lock held by
  background work. Work runs in goroutines and publishes through
  `Window.Update`; stale work is cancelled.
- **Subprocesses** are bounded in number, cancellable and time-limited.
- **Lists are virtual.** Any collection that can grow goes in `List`,
  `Table` or `Tree`, never a `Scroll` that builds every row: a frame costs
  what the view builds.
- **Large data stays out of the garbage collector's way:** memory-mapped
  files, and flat pointer-free slices with one shared byte buffer for text.
- **Everything has a budget and a lifetime.** Every cache has a byte budget.
  Everything a repository owns (maps, goroutines, subprocesses, watchers)
  ends when it closes. The app must never need restarting.
- **No network of its own.** The only connections are git's, the user's `gh`
  or `glab` when pull requests are turned on, and the update check;
  `scripts/check.sh` fails on anything else.
- **The engine stands alone.** `internal/git` imports nothing from mygo;
  `scripts/check.sh` fails if it does.

## Views and text

- Views are tested without a window through `ui.NewTester`; see the tests
  in `internal/ui/`.
- Every text a user reads is a message in `locales/en.json`, in ICU
  MessageFormat: a whole sentence with placeholders and plurals, never
  pieces joined in Go. `go tool mygo dev`, `go tool mygo build` and
  `go tool mygo generate` write `locales/catalog_gen.go` from it; never edit
  that file. `TestTextComesFromTheCatalog` fails on literal text in a view.
- Keep the interface quiet: let layout and controls carry the meaning, and
  add explanatory text only where Ramus does something a user would not
  expect.
- Read `.agents/skills/mygo-maintenance` before changing a view.

## Dependencies

- mygo is pre-1.0 and pinned in `go.mod`. A bug in it is fixed by a pull
  request [upstream](https://github.com/egoist/mygo), not by a fork or a
  workaround here.
- When a dependency changes, regenerate the licenses with
  `go run internal/licenses/generate.go`; `TestCoversTheBuild` reports a stale
  list.
- Never edit generated files: `locales/catalog_gen.go`,
  `internal/licenses/licenses.json.gz`, the file icons in
  `internal/ui/fileicon/` and the app icon's outputs each have a generator.

## Commits

[Conventional Commits](https://www.conventionalcommits.org):
`type(scope): description`, lower case, imperative, no trailing period, one
concern per commit. The scopes are `git` (the engine), `ui`, `docs` and
`workspace` (root files, scripts, mygo config); combine them as `git,ui`.

When a change makes a doc wrong, fix the doc in the same change.

## Releases

Maintainers release by setting the version in `mygo.json` and pushing a
`vX.Y.Z` tag that matches it; `.github/workflows/release.yml` builds, signs,
checks and publishes it. A `## X.Y.Z` section in `CHANGELOG.md` replaces the
generated release notes; once that file exists, every release needs its
section, or mygo stops the build. A version with a pre-release suffix, as
`1.1.0-rc.1`, is published as a prerelease, which installed apps do not
update to. `go run ./scripts/verifyrelease vX.Y.Z` checks a published
release whole: its files, checksums, update manifests and signatures.
