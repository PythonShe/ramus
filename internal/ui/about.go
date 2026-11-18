package ui

import (
	"context"
	"errors"
	"strings"

	"github.com/egoist/mygo/ui"

	"ramus/internal/crashlog"
	"ramus/internal/git/lifetime"
	"ramus/internal/licenses"
	"ramus/locales"
)

// ErrUpdatesOff is what About.Check returns for a build that does not
// update itself, as one run with mygo dev.
var ErrUpdatesOff error = updatesOff{}

type updatesOff struct{}

func (updatesOff) Error() string { return locales.AboutUpdatesOff() }

// Update is a newer version of the app that About.Check found.
type Update interface {
	Version() string
	// Install downloads the update and puts it in place of the app,
	// reporting how far it is; the app runs it once relaunched.
	Install(ctx context.Context, progress func(done, total int64)) error
}

// About is the About window: the app's name and version and a check for
// a newer one, and, a click away, the license of everything the app is
// built from.
type About struct {
	themes  themes
	Version string
	// Icon is the app's icon for a light appearance, IconDark for a dark
	// one.
	Icon, IconDark *ui.SVG
	// Check asks for a version newer than the running one, nil when there
	// is none; it runs off the main thread.
	Check func(ctx context.Context) (Update, error)
	// Update runs fn on the main thread and builds the view again.
	Update func(fn func())
	// Relaunch restarts the app, into an update installed.
	Relaunch func()
	// Crash keeps the crash reports; nil keeps none.
	Crash *crashlog.Log

	upd    updateState
	found  Update
	err    string
	done   float64
	cancel context.CancelFunc

	// licenses is whether the licenses show, in place of the app's name.
	licenses   bool
	notices    []licenses.Notice
	noticesErr string
	read       bool
	// chosen is the license shown in full, -1 for the list of them; text
	// is the one shown, reflowed, of notice shown.
	chosen, shown int
	text          string
	list          ui.ListState
}

type updateState int

const (
	updateIdle updateState = iota
	updateChecking
	updateNone
	updateFound
	updateInstalling
	updateOff
	updateFailed
)

// NewAbout is the About window of the app at version.
func NewAbout(version string) *About { return &About{Version: version, chosen: -1, shown: -1} }

// CheckNow checks for a newer version, as the button does, unless a check
// or an install is under way.
func (ab *About) CheckNow() {
	if ab.upd == updateChecking || ab.upd == updateInstalling || ab.Check == nil {
		return
	}
	ctx := ab.begin(updateChecking)
	check, update := ab.Check, ab.Update
	goRecover(ab.Crash, func() {
		u, err := check(ctx)
		update(func() {
			if ctx.Err() != nil {
				return
			}
			switch {
			case errors.Is(err, ErrUpdatesOff):
				ab.upd = updateOff
			case err != nil:
				ab.upd, ab.err = updateFailed, locales.AboutCheckFailed(errText(err))
			case u == nil:
				ab.upd = updateNone
			default:
				ab.upd, ab.found = updateFound, u
			}
		})
	}, ab.failed(update))
}

// failed shows a panic in a check or an install as its failure.
func (ab *About) failed(update func(func())) func(lifetime.Panic) {
	return func(p lifetime.Panic) {
		update(func() { ab.upd, ab.err = updateFailed, locales.AppPanic(p.Value) })
	}
}

// install installs the update found and relaunches into it.
func (ab *About) install() {
	u := ab.found
	ctx := ab.begin(updateInstalling)
	ab.done = -1 // of unknown length until the download says its size
	update, relaunch := ab.Update, ab.Relaunch
	goRecover(ab.Crash, func() {
		err := u.Install(ctx, func(done, total int64) {
			if total > 0 {
				update(func() { ab.done = float64(done) / float64(total) })
			}
		})
		update(func() {
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				ab.upd, ab.err = updateFailed, locales.AboutInstallFailed(errText(err))
				return
			}
			if relaunch != nil {
				relaunch()
			}
		})
	}, ab.failed(update))
}

// begin starts work of state s, cancelling any before it.
func (ab *About) begin(s updateState) context.Context {
	if ab.cancel != nil {
		ab.cancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	ab.cancel, ab.upd, ab.err = cancel, s, ""
	return ctx
}

// Close stops a check or an install under way, as the window closes.
func (ab *About) Close() {
	if ab.cancel != nil {
		ab.cancel()
		ab.cancel = nil
	}
	ab.upd = updateIdle
}

// aboutIconSize is the app's icon in the About window, and aboutStatusW
// the widest a line about an update runs before it wraps.
const (
	aboutIconSize = 104
	aboutStatusW  = 280
)

// View builds the About window.
func (ab *About) View(c *ui.Context) {
	ab.themes.apply(c, "")
	th := c.Theme()
	ui.Column(c).Fill().Background(th.Background).Children(func() {
		if ab.licenses {
			ab.licensesView(c)
			return
		}
		// The name sits in the middle of the room over the last lines,
		// which stay at the foot of the window whatever its height.
		ui.Column(c).Fill().AlignItems(ui.Center).Padding(space3XL, space3XL, spaceXL, space3XL).Children(func() {
			ui.Spacer(c)
			if icon := ab.Icon; icon != nil {
				if th.Dark && ab.IconDark != nil {
					icon = ab.IconDark
				}
				ui.Image(c, icon).Width(aboutIconSize).Height(aboutIconSize).Role(ui.RoleNone).Margin(0, 0, spaceL, 0)
			}
			ui.Text(c, locales.AboutName()).FontSize(textDisplay).Bold()
			ui.Text(c, locales.AboutVersion(ab.Version)).FontSize(textSmall).TextColor(th.TextMuted).Selectable().Margin(space2XS, 0, 0, 0)
			ab.updates(c)
			ui.Spacer(c)
			b := ui.ButtonBase(c).Radius(th.Radius).Padding(spaceXS, spaceM)
			if b.Hovered() {
				b.Background(th.SurfaceHover)
			}
			b.Children(func() {
				ui.Row(c).Gap(spaceXS).AlignItems(ui.Center).Children(func() {
					ui.Text(c, locales.AboutLicenses()).FontSize(textSmall).TextColor(th.TextMuted)
					ui.Icon(c, glyphs().chevronRight).FontSize(iconSmall).TextColor(th.TextMuted)
				})
			})
			if b.Clicked() {
				ab.licenses, ab.chosen = true, -1
			}
			ui.Text(c, locales.AboutCopyright()).FontSize(textCaption).TextColor(th.TextMuted).Margin(spaceXS, 0, 0, 0)
		})
	})
}

// updates is the check for a newer version: a button, then what it found.
// The line under the button has its room before it has anything to say,
// so that what is above does not move as it comes.
func (ab *About) updates(c *ui.Context) {
	th := c.Theme()
	ui.Column(c).AlignItems(ui.Center).Gap(spaceM).Margin(space2XL, 0, 0, 0).Children(func() {
		status := func(s string, col ui.Color) {
			ui.Text(c, s).FontSize(textSmall).TextColor(col).TextAlign(ui.Center).MaxWidth(aboutStatusW).MinHeight(spaceXL)
		}
		switch ab.upd {
		case updateFound:
			if ui.PrimaryButton(c, locales.AboutInstallUpdate()).Clicked() {
				ab.install()
			}
			status(locales.AboutUpdateFound(ab.found.Version()), th.Text)
			return
		case updateInstalling:
			ui.Progress(c, ab.done).Width(200).Label(locales.AboutInstalling(ab.found.Version())).Margin(spaceM, 0)
			status(locales.AboutInstalling(ab.found.Version()), th.TextMuted)
			return
		}
		b := ui.Button(c, locales.AboutCheckForUpdates()).Disabled(ab.upd == updateChecking || ab.Check == nil)
		if b.Clicked() {
			ab.CheckNow()
		}
		switch ab.upd {
		case updateChecking:
			status(locales.AboutChecking(), th.TextMuted)
		case updateNone:
			status(locales.AboutUpToDate(), th.TextMuted)
		case updateOff:
			status(locales.AboutUpdatesOff(), th.TextMuted)
		case updateFailed:
			status(ab.err, th.Danger)
		default:
			status("", th.TextMuted)
		}
	})
}

// licensesView lists the licenses, or shows the one chosen in full, under
// a bar that goes back: from a license to the list, from the list to the
// app's name.
func (ab *About) licensesView(c *ui.Context) {
	th := c.Theme()
	if !ab.read {
		ab.read = true
		n, err := licenses.All()
		ab.notices = n
		if err != nil {
			ab.noticesErr = locales.AboutLicensesFailed(errText(err))
		}
	}
	one := ab.noticesErr == "" && ab.chosen >= 0 && ab.chosen < len(ab.notices)
	title := locales.AboutLicenses()
	if one {
		title = ab.notices[ab.chosen].Name
	}
	ui.Row(c).Padding(spaceM, insetX).Gap(spaceM).AlignItems(ui.Center).MinHeight(barHeight).Children(func() {
		back := ui.ButtonBase(c).Label(locales.AboutBack()).Tooltip(locales.AboutBack()).Size(controlHeight, controlHeight).Radius(th.Radius).Center().Shrink(0)
		if back.Hovered() {
			back.Background(th.SurfaceHover)
		}
		back.Children(func() { ui.Icon(c, glyphs().chevronLeft).FontSize(iconSmall).TextColor(th.TextMuted) })
		// After the build, which still shows the license it leaves.
		back.OnClick(func() {
			if one {
				ab.chosen = -1
			} else {
				ab.licenses = false
			}
		})
		ui.Text(c, title).Bold().SingleLine().TruncateMiddle().TextAlign(ui.Center).Grow(1).Shrink(1)
		// As wide as the button, for the title to sit in the middle.
		ui.Box(c).Width(controlHeight).Shrink(0)
	})
	ui.Box(c).Height(1).FillWidth().Background(hairline(th))
	switch {
	case ab.noticesErr != "":
		ui.Text(c, ab.noticesErr).TextColor(th.Danger).Padding(spaceM, insetX)
	case one:
		if ab.shown != ab.chosen {
			ab.shown, ab.text = ab.chosen, reflow(ab.notices[ab.chosen].Text)
		}
		ui.Scroll(c).Grow(1).FillWidth().Children(func() {
			ui.Text(c, ab.text).FontSize(textSmall).TextColor(th.TextMuted).Selectable().Padding(spaceL, spaceXL, spaceXL, spaceXL)
		})
	default:
		ab.list.Label = func(i int) string { return ab.notices[i].Name }
		ui.List(c, &ab.list, len(ab.notices), func(i int) {
			n := ab.notices[i]
			row := ui.Row(c).Padding(spaceS, insetX).Gap(spaceM).AlignItems(ui.Center).FillWidth().Radius(th.Radius).
				Role(ui.RoleButton).Focusable().Cursor(ui.CursorPointer)
			if row.Hovered() {
				row.Background(th.SurfaceHover)
			}
			row.Children(func() {
				// Not grown: what grows starts from nothing, and the
				// license's name would take its room before the name.
				ui.Row(c).Gap(spaceS).AlignItems(ui.Baseline).Shrink(1).Children(func() {
					ui.Text(c, n.Name).SingleLine().TruncateMiddle().Shrink(1)
					if n.Version != "" {
						ui.Text(c, n.Version).FontSize(textSmall).TextColor(th.TextMuted).SingleLine().Shrink(0)
					}
				})
				ui.Spacer(c)
				if n.Kind != "" {
					// The name is read whole before the license's is.
					ui.Text(c, n.Kind).FontSize(textCaption).TextColor(th.TextMuted).SingleLine().TextAlign(ui.End).Shrink(100).MinWidth(space3XL)
				}
				ui.Icon(c, glyphs().chevronRight).FontSize(iconSmall).TextColor(th.TextMuted.Alpha(0.6))
			})
			if row.Clicked() {
				ab.chosen = i
			}
		}).Label(locales.AboutLicenses()).Grow(1).Padding(spaceS, spaceS)
	}
}

// reflow joins the lines of each paragraph of a license, which its file
// breaks at 80 columns, for the window to wrap them at its width: a
// line keeps its own only as it starts an item or a copyright.
func reflow(text string) string {
	var b strings.Builder
	blank := true
	for _, l := range strings.Split(text, "\n") {
		t := strings.TrimSpace(l)
		switch {
		case t == "":
			if !blank {
				b.WriteString("\n\n")
			}
			blank = true
			continue
		case blank:
		case startsLine(t):
			b.WriteByte('\n')
		default:
			b.WriteByte(' ')
		}
		b.WriteString(t)
		blank = false
	}
	return strings.TrimSpace(b.String())
}

// startsLine is whether a license's line, trimmed, keeps its own: one
// starting an item, as "1.", "(a)", "-" or "*" do, or a copyright.
func startsLine(t string) bool {
	f, _, _ := strings.Cut(t, " ")
	switch {
	case f == "-" || f == "*" || strings.EqualFold(f, "copyright"):
		return true
	case len(f) >= 2 && f[len(f)-1] == '.' && strings.Trim(f[:len(f)-1], "0123456789") == "":
		return true
	case len(f) >= 3 && f[0] == '(' && f[len(f)-1] == ')':
		return true
	}
	return false
}
