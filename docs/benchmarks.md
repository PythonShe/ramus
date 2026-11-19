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
| Date | 2026-11-19 |
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

### Linux kernel (1,507,296 commits, about 90,000 files)

| Case | p50 | Peak memory (app / git) |
|---|---|---|
| Open to branches and tags read | 765 ms | |
| History: first screen, after the load starts | 30 ms | 176 MiB / 33 MiB |
| History: open to first screen | 795 ms | |
| History: all 1,507,296 commits laid out | 1.59 s | |
| History with no commit-graph: first screen | 15.0 s | 159 MiB / 1.6 GiB |
| History with no commit-graph: all commits | 17.0 s | |
| Scrolling the whole graph: frame time p50 / max over 600 frames | 60 ms / 2.35 s | 292 MiB / 208 MiB |
| Jump to the first commit by hash | 496 ms | |
| Filter by message: first screen / all 7,058 found | 48 ms / 22.9 s | 97 MiB / 1.5 GiB |
| Filter by author: first screen / all 43,829 found | 33 ms / 23.8 s | |
| Filter by path (`kernel/sched/core.c`): first screen / all 1,994 found | 525 ms / 581 ms | |

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
in a made repository of 100,010 commits. These were taken on the day
above, on the same machine at GOMAXPROCS 18 and normal priority, 3 runs
each.

| Case | Kernel | 100,010 commits |
|---|---|---|
| Graph 10 behind: first screen | 22 ms | 11 ms |
| Graph 10 behind: whole history | 1.15 s | 23 ms |
| A commit on top: shown | 146 ms | 156 ms |
| A fetch of 100 commits: shown | 174 ms | 127 ms |
| Peak memory (app / git) | 180 MiB / 141 MiB | 80 MiB / 221 MiB |

The commits the graph lacks are read from their objects and history is
ordered natively, where git used to order it whole: before, the same
cases took 682 ms and 1.57 s for the first screen and the whole kernel
history, 1.67 s and 1.66 s for the commit and the fetch on top, at
285 MiB / 325 MiB. Commits on top of the history shown are laid out
alone above its rows, which stay as they were.

### A 390,000-file repository

| Case | p50 | Peak memory (app / git) |
|---|---|---|
| Open to a populated status panel, no fsmonitor | 3.36 s | 252 MiB / 134 MiB |

### Repositories the bench makes

| Case | p50 | Peak memory (app / git) |
|---|---|---|
| One file saved in a 20,000-file repository, to the status showing it | 154 ms | 30 MiB / 16 MiB |
| 5,000 files saved at once, to the status showing all of them | 1.53 s | 66 MiB / 16 MiB |
| ... frame time p50 / max meanwhile; git processes at once | 1.04 ms / 52 ms; 2 | |
| File list of a commit of 10,000 files | 42 ms | 30 MiB / 14 MiB |
| 50 MB text file's diff: git and reading / the viewer built | 690 ms / 283 ms | 512 MiB / 218 MiB |
| 100 MB binary's diff | 717 ms | |
| Ten repositories open, idle: CPU over 30 s | 0.09% | 55 MiB / 12 MiB |
| 50,000 branches: open to refs and status / branches view | 276 ms / 1.25 s | 65 MiB / 48 MiB |
| 2,001 packs and 5,000 loose objects: open to status / history | 230 ms / 502 ms | 27 MiB / 72 MiB |

Idle means no frame asked for and no update published over the 30 seconds,
with a heap of 6.5 MiB.

Repositories Ramus hands to git or reads another way, each of the same
3,000 commits:

| Repository | Open to status | Whole history |
|---|---|---|
| Plain (files refs, SHA-1) | 195 ms | 25 ms |
| Reftable refs | 161 ms | 36 ms |
| SHA-256 objects | 212 ms | 29 ms |
| Partial clone (`--filter=blob:none`) | 194 ms | 3.8 ms |
| Shallow clone (depth 100) | 202 ms | 15 ms |

### Soak

The `soak` case opens three repositories, changes a file in one, reloads
and closes them all, round after round, then checks that goroutines, open
files, git processes and memory came back. It fails if goroutines or open
files grow by more than two, or the heap to more than twice its size and
16 MiB. Two minutes on the machine above: 224 rounds, goroutines 6 to 6,
open files 6 to 6, heap 7.2 to 11.8 MiB. Ten minutes on GitHub's Linux
runner, on 14 November 2026 and older code: 2,278 rounds, goroutines 4 to
4, open files 9 to 9, heap 3.2 to 5.3 MiB. The `Soak` workflow runs it
for two hours when started.

### Cold start

`scripts/coldstart` starts the built app (`CGO_ENABLED=0 go build`) and
times it from exec to its first frame, ten times. On the machine above, at
normal priority, as a user starts it: four times ten starts, medians 151
to 160 ms, 155 ms over all, fastest 146 ms; the first start after a build
482 ms. At background priority, on the efficiency cores, the median is
300 ms.

CI runs it on GitHub's macOS runner, a virtual machine with a virtual GPU.
Over the 20 runs of 15 to 18 November 2026 the median of ten starts went
from 141 to 374 ms (220 ms over all) and the fastest from 135 to 263 ms,
for the same code, and the first start from 0.7 to 3.3 s. CI fails when
the fastest is over 300 ms and warns over 250 ms: a busy runner slows runs
at random, never speeds one up.

## Not measured yet

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
