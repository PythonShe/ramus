package ui

import (
	"errors"

	"ramus/internal/git/redact"
	"ramus/internal/statefile"
	"ramus/locales"
)

// LoadNotice is what the user is told of err, an error reading one of
// the app's own files (a *statefile.LoadError): which file, what is wrong
// in it, and where it was kept aside. Another error is shown as it is.
func LoadNotice(err error) string {
	var le *statefile.LoadError
	if !errors.As(err, &le) {
		return errText(err)
	}
	reason := redact.Shown(le.Err.Error())
	switch {
	case le.Partial && le.KeptAs != "":
		return locales.AppFilePartlyRead(le.Path, le.KeptAs, reason)
	case le.Partial && le.KeepErr == nil:
		// The settings file keeps what it could not read: nothing is lost.
		return locales.AppSettingsPartlyRead(le.Path, reason)
	case le.KeptAs != "":
		return locales.AppFileSetAside(le.Path, le.KeptAs, reason)
	}
	return locales.AppFileNotWritten(le.Path, reason)
}
