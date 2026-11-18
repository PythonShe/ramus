# Ramus product cards

Pictures of Ramus for the places that list it, each 1600x1000 and under
300 KB:

| Card | What it shows |
|------|---------------|
| `bento.jpg` | Three benchmark figures beside the dark window. The [mygo showcase](https://github.com/egoist/mygo-showcase) uses this one. |
| `midnight.jpg` | A headline over the dark window. |
| `ivory.jpg` | A headline beside the window in the light theme. |

A card is a page of HTML (`bento.html` and the others, sharing `base.css`)
laid over a screenshot and the app icon of `assets/app-icon`. Change the
page or the screenshot, never a `.jpg`, then render:

```bash
assets/cards/render.sh
```

It needs Chrome and ImageMagick, and the network for the Inter font, which
the headings use: Apple's license for San Francisco does not cover a
picture like this.

## The screenshots

`history-dark.png` and `history-light.png` are the History view on the
Linux kernel, with a commit chosen and one of its files open, drawn by the
app's own views at 1440x900 and twice the scale. `TestScreenshot` in
`internal/ui` draws them without a window:

```bash
RAMUS_SCREENSHOT=$PWD/assets/cards/history-dark.png \
RAMUS_SCREENSHOT_REPO=~/Projects/ramus-fixtures/linux \
RAMUS_SCREENSHOT_CLICK='arm64: suspend: Only track cpuidle context for idle tasks|arch/arm64/kernel/suspend.c' \
go test ./internal/ui -run '^TestScreenshot$' -count=1
```

Add `RAMUS_SCREENSHOT_LIGHT=1` for the light one. The commit named here
leaves the first screen as the kernel moves on; name one that is on it.

The title bar with its three buttons is drawn by the page, after the one
macOS gives the window: the views draw everything below it.

## The figures

The numbers on `bento.jpg` and in the headline of `midnight.jpg` are those
of `docs/benchmarks.md`. When one changes there, change it in the page and
render again.
