<div align="center">

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/app-icon/monogram/png/icon-dark-1024.png">
  <img src="assets/app-icon/monogram/png/icon-light-1024.png" alt="Ramus app icon" width="128" height="128">
</picture>

# Ramus

A fast git client for macOS, Windows and Linux that stays light on large
repositories and runs your own git.

[Download](https://github.com/PythonShe/ramus/releases/latest) ·
[Benchmarks](docs/benchmarks.md) ·
[Contributing](CONTRIBUTING.md)

</div>

## What it does

- **History**: the commit graph of the whole repository, with filters,
  a file's or a line's history, and blame. Each commit's files show the
  lines they add and delete.
- **Changes**: the lines each change adds and deletes, staged and not;
  stage, unstage or discard a file, a change or single lines, and commit
  with co-authors.
- **Edit history**: reorder, reword, squash, fix up or drop commits by
  dragging them.
- **Conflicts**: a merge editor that takes a side or both, previews the
  result, and continues, skips or aborts the merge, rebase or cherry-pick.
- **Branches, tags and remotes**: fetch, pull and push, rebase a branch onto
  another, and clean up branches that are gone.
- **Pull requests**: each branch's pull or merge request and the one each
  commit came in by, from GitHub through `gh` or GitLab through `glab`,
  including GitHub stacks. Review a pull or merge request in the app: its
  files diffed locally, with the lines each changes and the threads under
  their lines; reply, resolve,
  comment on lines, and approve or request changes.
- **Worktrees**: every worktree's branch, changes, and how far ahead or
  behind it is.
- **Git LFS**: see who holds a lock, lock a file before editing it, and
  release or force off a lock, through your `git-lfs`; pictures stored in
  LFS diff as pictures.
- **Snapshots**: uncommitted work is kept before anything that would drop
  it, and can be restored or taken by hand at any time.
- **Statistics**: commits by person, a calendar, heat map and treemaps.
- **Diff and merge tool**: Ramus can be git's `difftool` and `mergetool`.

## Fast on large repositories

On Apple silicon (p50 of 3 runs, at background priority on a machine in
use):

| | |
|---|---|
| Linux kernel, open to the first screen of history | 1.08 s |
| Linux kernel, all 1,506,743 commits laid out in the graph | 2.54 s, 172 MiB |
| Scrolling the kernel's whole graph, slowest frame of 600 | 2.5 ms |
| 390,000-file repository, open to a populated status panel | 3.65 s |
| One file saved, to the status showing it (20,000 files) | 171 ms |
| 50 MB text diff, read and shown | 1.3 s |
| Ten repositories open and idle | 0.10% CPU, 48 MiB |
| Cold start to first frame | 255 ms |

Ramus reads git's files on disk itself and draws its interface natively on
the GPU, with no web view. Everything that writes runs your `git`, so hooks,
commit signing and credentials behave as they do in the terminal. How the
numbers were taken: [docs/benchmarks.md](docs/benchmarks.md).

## Install

Ramus needs git 2.36 or later. Pull requests need
[`gh`](https://cli.github.com) or [`glab`](https://gitlab.com/gitlab-org/cli),
logged in. Git LFS locks need
[`git-lfs`](https://git-lfs.com).

**macOS** (signed and notarized), 12 or later:

```sh
curl -fsSL https://github.com/PythonShe/ramus/releases/latest/download/install.sh | sh
```

or the `.dmg` from the [latest release](https://github.com/PythonShe/ramus/releases/latest).

**Windows**: the setup `.exe` from the
[latest release](https://github.com/PythonShe/ramus/releases/latest), or in
PowerShell:

```powershell
irm https://github.com/PythonShe/ramus/releases/latest/download/install.ps1 | iex
```

The Windows build is not signed yet, so SmartScreen asks before it runs.

**Linux**: the `.deb`, `.rpm` or AppImage from the
[latest release](https://github.com/PythonShe/ramus/releases/latest), or,
for your user alone, without root:

```sh
curl -fsSL https://github.com/PythonShe/ramus/releases/latest/download/install.sh | sh
```

Installs from the install scripts, the `.dmg` and the Windows setup update
themselves: **Check for Updates** is in the menu. A `.deb` or `.rpm` updates
with a newer package, and an AppImage by downloading the new one.

## From the command line

```sh
ramus .              # open the repository here
ramus log <file>     # a file's history
ramus blame <file>   # who changed each line
ramus diff <a> <b>   # compare two files
```

**Settings > Tools > Use Ramus for git difftool and mergetool** sets Ramus
up as both in your git config.

## License

[Apache 2.0](LICENSE). The file icons are from
[Symbols](https://github.com/miguelsolorio/vscode-symbols) by Miguel
Solorio, under the [MIT license](internal/ui/fileicon/LICENSE).
