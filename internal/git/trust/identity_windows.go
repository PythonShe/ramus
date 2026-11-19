//go:build windows

package trust

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// identity names the folder at path apart from any other the path may
// name later: its volume's serial number and its file index, which NTFS
// does not give again while the folder exists and changes the sequence
// part of when it reuses one.
func identity(path string) (string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	// Backup semantics open a directory; no access is asked but what
	// reading its attributes needs.
	h, err := windows.CreateFile(p, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	var fi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &fi); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x:%x:%x", fi.VolumeSerialNumber, fi.FileIndexHigh, fi.FileIndexLow), nil
}
