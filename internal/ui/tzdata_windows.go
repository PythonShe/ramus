package ui

// Windows has no IANA zone database for time.LoadLocation to read, so
// the commit view's places come from the copy Go embeds.
import _ "time/tzdata"
