package ui

import (
	"encoding/hex"
	"maps"
	"slices"

	"github.com/egoist/mygo/ui"

	"ramus/internal/settings"
	"ramus/locales"
)

// tableState is the state of a table whose columns the user moves and
// resizes, which the settings keep by the table's key.
type tableState struct {
	ui.ListState
	// key is the table whose layout Columns holds, and from that layout
	// as the settings had it, to follow a change another window saved.
	key  string
	from settings.TableLayout
}

// chosen reports whether row is chosen: mygo paints it in the accent
// colour with AccentText, which a cell's own colours must not fight.
func (s *tableState) chosen(row int) bool {
	return s.Selected != nil && *s.Selected == row
}

// ink is the colours a table cell takes for its text: the theme's, or on
// a chosen row AccentText, as the muted grey, the warning amber and the
// success green are all hard to read on the accent.
type ink struct {
	Muted, Warning, Success, Danger, Accent ui.Color
}

// plainInk is the theme's colours, for a cell outside a chosen row.
func plainInk(th *ui.Theme) ink {
	return ink{th.TextMuted, th.Warning, th.Success, th.Danger, th.Accent}
}

// ink returns the colours for the cells of row.
func (s *tableState) ink(c *ui.Context, row int) ink {
	th := c.Theme()
	if !s.chosen(row) {
		return plainInk(th)
	}
	// Secondary text keeps a little of its quiet, and stays well above
	// the contrast of the muted grey on the accent.
	return ink{th.AccentText.Alpha(0.85), th.AccentText, th.AccentText, th.AccentText, th.AccentText}
}

// savedTable is a ui.Table whose columns keep the order and the widths
// the user gives them, in the settings under key. Columns are known by
// their IDs, which must not change with the language. It returns the
// table, whose Changed and Submitted report as a ui.Table's do.
func (a *App) savedTable(c *ui.Context, key string, s *tableState, cols []ui.TableColumn, n int, cell func(row, col int)) ui.Element {
	saved := a.Settings.Tables[key]
	if s.key != key || !sameLayout(s.from, saved) {
		s.key, s.from = key, saved
		s.Columns = ui.TableLayout(saved)
	}
	// Rows as dense as the history's, not the theme's roomier ones.
	s.RowHeight = rowHeight
	// Headers name the columns quietly, below the rows' text.
	s.HeaderCell = func(cell ui.Element, _ int) { cell.FontSize(textSmall) }
	s.HeaderMenu = func(m *ui.Menu, _ int) {
		if m.Item(locales.ColumnsReset()).Disabled(len(s.Columns.Order) == 0 && len(s.Columns.Widths) == 0).Chosen() {
			s.Columns = ui.TableLayout{}
			a.keepTable(key, s)
		}
	}
	tbl := ui.Table(c, &s.ListState, cols, n, cell)
	// Changed reports a new choice of row too: only a new layout is kept.
	if tbl.Changed() && !sameLayout(settings.TableLayout(s.Columns), saved) {
		a.keepTable(key, s)
	}
	return tbl
}

// keepTable saves the layout of s's columns as table key's.
func (a *App) keepTable(key string, s *tableState) {
	// A new map: the settings saving off the main thread, and the other
	// windows, hold the old one.
	tables := maps.Clone(a.Settings.Tables)
	l := settings.TableLayout(s.Columns)
	if len(l.Order) == 0 && len(l.Widths) == 0 {
		delete(tables, key)
	} else {
		if tables == nil {
			tables = map[string]settings.TableLayout{}
		}
		tables[key] = l
	}
	if len(tables) == 0 {
		tables = nil
	}
	a.Settings.Tables = tables
	s.from = l
	a.keepSettings()
}

func sameLayout(x, y settings.TableLayout) bool {
	return slices.Equal(x.Order, y.Order) && maps.Equal(x.Widths, y.Widths)
}

// The columns lists of commits share: when, and which.
func dateColumn() ui.TableColumn {
	return ui.TableColumn{Title: locales.ColumnDate(), ID: "date", Width: 100, MinWidth: 60}
}

func commitColumn() ui.TableColumn {
	return ui.TableColumn{Title: locales.ColumnCommit(), ID: "commit", Width: 84, MinWidth: 50}
}

// commitColumns are a list of commits' columns: the message, taking the
// room the others leave, its author, its date and its hash.
func commitColumns() []ui.TableColumn {
	return []ui.TableColumn{
		{Title: locales.ColumnMessage(), ID: "message", MinWidth: 80},
		{Title: locales.ColumnAuthor(), ID: "author", Width: 140, MinWidth: 60},
		dateColumn(), commitColumn(),
	}
}

// mutedCell is the text of a column that says less than the row's
// first: smaller, muted, on one line.
func mutedCell(c *ui.Context, k ink, s string) ui.Element {
	return ui.Text(c, s).FontSize(textSmall).TextColor(k.Muted).SingleLine()
}

// hashCell is a commit's abbreviated hash.
func hashCell(c *ui.Context, k ink, oid []byte) ui.Element {
	return mutedCell(c, k, hex.EncodeToString(oid)[:min(8, 2*len(oid))]).Font("monospace")
}
