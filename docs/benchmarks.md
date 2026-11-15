# Benchmarks

How fast Ramus is on large repositories, how each number was taken, and
how to take them again. The numbers are from one machine and one day; a
rerun on another machine will differ, which is why the method is here.

## The run

| | |
|---|---|
| Machine | Apple silicon, 18 cores |
| OS | macOS 27.2.1 |
| git | 2.56.1 |
| Go | 1.27.4 |
| Date | 2026-11-14 |
| Runs | 3 of each case, p50 shown (p95 in the JSON) |
| Load | GOMAXPROCS=6, at background priority (`taskpolicy -c background`), on a machine in use |

Background priority on a busy machine makes these numbers slower than a
quiet run would, not faster.

## What was measured

Each case runs in a process of its own, so its memory is its own and
nothing it loaded is warm for the next run. The operating system's file
cache is warm, as it is for a user opening a repository again. Peak memory
is the largest resident size of the app's process and, apart, of the
largest git process it ran. Git's is read on macOS only: on Linux the
kernel reports the app's peak for a process Go starts, so the bench shows
it as unknown there.

The app cases run the real `ui.App` without a window, through mygo's
headless tester: a frame there is building, layout and painting in
software, without the GPU. Cold start, which needs a window, is measured
apart, in CI.

### Linux kernel (1,506,743 commits, about 90,000 files)

| Case | p50 | Peak memory (app / git) |
|---|---|---|
| Open to branches and tags read | 1.05 s | |
| History: first screen, after the load starts | 35 ms | 172 MiB / 33 MiB |
| History: open to first screen | 1.08 s | |
| History: all 1,506,743 commits laid out | 2.54 s | |
| History with no commit-graph: first screen | 24.6 s | 156 MiB / 1.6 GiB |
| History with no commit-graph: all commits | 27.3 s | |
| Scrolling the whole graph: frame time p50 / max over 600 frames | 1.27 ms / 2.5 ms | 235 MiB / 121 MiB |
| Jump to the first commit by hash | 85 ms | |
| Filter by message: first screen / all 7,026 found | 108 ms / 29.7 s | 81 MiB / 1.5 GiB |
| Filter by author: first screen / all 43,776 found | 39 ms / 30.9 s | |
| Filter by path (`kernel/sched/core.c`): first screen / all 1,993 found | 585 ms / 672 ms | |

With a commit-graph Ramus walks history itself. Without one it asks
`git rev-list --topo-order`, which prints nothing until it has walked
every commit, and then writes the commit-graph in the background, a
layer at a time, as history would have it.

### History as commits land

The `reload-kernel` and `reload-100k` cases load history with the
commit-graph ten commits behind, then time the refs moving to the history
showing the new tip with every row laid out: one commit on top, then a
fetch of 100 on a remote branch. The kernel case works in a bare clone
sharing the fixture's objects, with its commit-graph linked in; the other
in a made repository of 100,010 commits. These were taken on 2026-11-15
on the machine above at GOMAXPROCS 18 and normal priority, 3 runs each.

| Case | Kernel | 100,010 commits |
|---|---|---|
| Graph 10 behind: first screen | 14 ms | 7.4 ms |
| Graph 10 behind: whole history | 879 ms | 14 ms |
| A commit on top: shown | 142 ms | 199 ms |
| A fetch of 100 commits: shown | 149 ms | 141 ms |
| Peak memory (app / git) | 178 MiB / 18 MiB | 32 MiB / 12 MiB |

The commits the graph lacks are read from their objects and history is
ordered natively, where git used to order it whole: before, the same
cases took 682 ms and 1.57 s for the first screen and the whole kernel
history, 1.67 s and 1.66 s for the commit and the fetch on top, at
285 MiB / 325 MiB. Commits on top of the history shown are laid out
alone above its rows, which stay as they were.

### A 390,000-file repository

| Case | p50 | Peak memory (app / git) |
|---|---|---|
| Open to a populated status panel, no fsmonitor | 3.65 s | 198 MiB / 134 MiB |

### Repositories the bench makes

| Case | p50 | Peak memory (app / git) |
|---|---|---|
| One file saved in a 20,000-file repository, to the status showing it | 171 ms | 26 MiB / 16 MiB |
| 5,000 files saved at once, to the status showing all of them | 876 ms | 48 MiB / 12 MiB |
| ... frame time p50 / max meanwhile; git processes at once | 0.84 ms / 12 ms; 1 | |
| File list of a commit of 10,000 files | 74 ms | 28 MiB / 14 MiB |
| 50 MB text file's diff: git and reading / the viewer built | 965 ms / 334 ms | 383 MiB / 218 MiB |
| 100 MB binary's diff | 861 ms | |
| Ten repositories open, idle: CPU over 30 s | 0.10% | 48 MiB / 9 MiB |
| 50,000 branches: open to refs and status / branches view | 366 ms / 1.79 s | 64 MiB / 21 MiB |
| 2,001 packs and 5,000 loose objects: open to status / history | 266 ms / 474 ms | 23 MiB / 73 MiB |

Idle means no frame asked for and no update published over the 30 seconds,
with a heap of 4.8 MiB.

Repositories Ramus hands to git or reads another way, each of the same
3,000 commits:

| Repository | Open to status | Whole history |
|---|---|---|
| Plain (files refs, SHA-1) | 274 ms | 57 ms |
| Reftable refs | 232 ms | 56 ms |
| SHA-256 objects | 274 ms | 58 ms |
| Partial clone (`--filter=blob:none`) | 274 ms | 6 ms |
| Shallow clone (depth 100) | 272 ms | 23 ms |

### Soak

The `soak` case opens three repositories, changes a file in one, reloads
and closes them all, round after round, then checks that goroutines, open
files, git processes and memory came back. It fails if goroutines or open
files grow by more than two or the heap more than doubles. Two minutes on
the machine above: 240 rounds, goroutines 6 to 6, open files 6 to 6, heap
3.5 to 7.4 MiB. Ten minutes on GitHub's Linux runner: 2,278 rounds,
goroutines 4 to 4, open files 9 to 9, heap 3.2 to 5.3 MiB. The `Soak`
workflow runs it for two hours when started.

### Cold start

`scripts/coldstart` starts the built app and times it to its first frame;
CI runs it ten times on GitHub's macOS runner and fails when the fastest
run is over 300 ms: a busy runner slows runs at random, never speeds one
up. The last run: first start 655 ms, median 171 ms, fastest 153 ms.

## Not measured yet

- Cold start on real hardware, rather than a CI virtual machine.
- Status with fsmonitor, and status on Windows and Linux.
- Status refresh after touching one file in the kernel and the
  390,000-file repository, and anything on a repository of a million files.
- The same cases in other git clients.

## Running them

You need git, Go (the version in `go.mod`) and, for the cases on fixtures,
a folder holding them:

```sh
mkdir fixtures && cd fixtures
git clone https://github.com/torvalds/linux.git linux
git -C linux -c commitGraph.changedPathsVersion=2 commit-graph write --reachable
go run <ramus>/bench/monorepo -out monorepo.noindex   # about 2.7 GB, deterministic
```

Then, from the Ramus checkout:

```sh
RAMUS_FIXTURES=/path/to/fixtures go run ./bench -runs 5 -out bench.json
go run ./bench -list                  # the cases
go run ./bench -only 'kernel' -runs 3 # some of them
```

Cases whose fixture is missing are skipped. Nothing is written into a
fixture: the no-commit-graph case turns the graph off through a
configuration file of its own, and cases that write make their
repositories in a temporary folder, removed at the end (kept in
`RAMUS_BENCH_CACHE` when that is set). `RAMUS_SOAK=2h` sets how long the
soak runs.

The JSON holds the machine, every run's times, p50, p95, minimum and
maximum, and peak memory.
