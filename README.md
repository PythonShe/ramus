# Ramus

A git client built to stay light and fast on large repositories. Written
in Go, with a native interface drawn on the GPU by
[mygo](https://github.com/egoist/mygo): no webview, no cgo.

Not released yet; build it from source.

## How fast

On an Apple M5 Pro, git 2.56.1, at background priority on a machine in
use (p50 of 3 runs):

| | |
|---|---|
| Linux kernel, open to the first screen of history | 1.08 s |
| Linux kernel, all 1,506,743 commits laid out in the graph | 2.54 s, 172 MiB |
| Scrolling the kernel's whole graph, slowest frame of 600 | 2.5 ms |
| 390,000-file repository, open to a populated status panel | 3.65 s |
| One file saved, to the status showing it (20,000 files) | 171 ms |
| 5,000 files saved at once, to the status showing them | 876 ms, 1 git process |
| 50 MB text diff, read and shown | 1.3 s |
| Ten repositories open and idle | 0.10% CPU, 48 MiB |
| Cold start to first frame, CI macOS runner, median | 255 ms |

How each was taken, what is not measured yet, and how to rerun them:
[docs/benchmarks.md](docs/benchmarks.md).

## How

- Reads go through Ramus's own readers of git's files on disk: the index,
  packs, the commit-graph, refs in both the files and the reftable format.
  Everything that writes runs your `git`, so hooks, signing and
  credentials behave as they do in the terminal.
- What the readers do not support (SHA-256 objects, partial clones, sparse
  or split indexes) is handed to `git` rather than guessed at.
- Each reader is tested against `git`'s own output on real repositories.
- Nothing waits on disk or on git while drawing. Long lists are virtual,
  and large data sits in flat buffers the garbage collector does not walk.
- Every cache has a byte budget, and everything a repository holds ends
  when it closes.

## Building

You need [Go](https://go.dev) (the version in `go.mod`) and git 2.36 or
later.

```sh
go tool mygo dev      # run with live reload
go tool mygo build    # package the app into build/
scripts/check.sh      # formatting, vet and the tests
```

It builds for macOS, Windows and Linux; CI runs the tests on all three.

## License

[Apache 2.0](LICENSE)
