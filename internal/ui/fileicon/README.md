# File icons

The icons of files in Ramus's file lists come from
[Symbols](https://github.com/miguelsolorio/vscode-symbols) by Miguel
Solorio, version 0.0.26, under the MIT license in `LICENSE`.

`icons.tar.gz` (the file icons' SVGs) and `tables_gen.go` (which file
takes which icon) are generated; never edit them. To update, from the
repository's root, with a checkout of a Symbols release:

```bash
go run internal/ui/fileicon/generate.go <symbols checkout> <version>
```

then copy the checkout's `LICENSE` here and update the version above.
