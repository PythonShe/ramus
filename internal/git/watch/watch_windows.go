//go:build windows

package watch

import (
	"os"
	"path/filepath"
	"sync"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// One recursive ReadDirectoryChangesW covers the tree. Its buffer is
// raised well past the 64 KB default so a checkout's burst fits; when it
// still overflows, the system returns no records and the batch reports
// Overflow.

const (
	bufferSize = 1 << 20

	fileListDirectory = 0x0001
	notifyFilter      = windows.FILE_NOTIFY_CHANGE_FILE_NAME | windows.FILE_NOTIFY_CHANGE_DIR_NAME |
		windows.FILE_NOTIFY_CHANGE_ATTRIBUTES | windows.FILE_NOTIFY_CHANGE_SIZE |
		windows.FILE_NOTIFY_CHANGE_LAST_WRITE | windows.FILE_NOTIFY_CHANGE_CREATION
)

// The read is overlapped so that Close can cancel it from another
// thread; a synchronous one could not be.
type windowsWatch struct {
	w      *Watcher
	h      windows.Handle
	ov     windows.Overlapped
	mu     sync.Mutex
	closed bool
	done   sync.WaitGroup
}

func start(w *Watcher, _ Options) (platform, error) {
	p, err := windows.UTF16PtrFromString(w.root)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, fileListDirectory,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, err
	}
	ww := &windowsWatch{w: w, h: h}
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	ww.ov.HEvent = ev
	ww.done.Add(1)
	go ww.read()
	return ww, nil
}

func (ww *windowsWatch) read() {
	defer ww.done.Done()
	buf := make([]byte, bufferSize)
	for {
		var n uint32
		// A read is issued under the lock close takes to cancel, so that
		// none starts after the cancel and waits for ever.
		ww.mu.Lock()
		if ww.closed {
			ww.mu.Unlock()
			return
		}
		err := windows.ReadDirectoryChanges(ww.h, &buf[0], uint32(len(buf)), true, notifyFilter, nil, &ww.ov, 0)
		ww.mu.Unlock()
		if err == nil {
			err = windows.GetOverlappedResult(ww.h, &ww.ov, &n, true)
		}
		ww.mu.Lock()
		closed := ww.closed
		ww.mu.Unlock()
		if closed {
			return
		}
		if err != nil {
			ww.w.fail(err)
			return
		}
		if n == 0 {
			ww.w.dropped() // the buffer overflowed
			continue
		}
		for off := uint32(0); off+12 <= n; {
			rec := (*windows.FileNotifyInformation)(unsafe.Pointer(&buf[off]))
			nameLen := rec.FileNameLength / 2
			if off+12+rec.FileNameLength > n {
				break
			}
			name := string(utf16.Decode(unsafe.Slice(&rec.FileName, nameLen)))
			if !ww.folderModified(rec.Action, name) {
				ww.w.changed(name)
			}
			if rec.NextEntryOffset == 0 {
				break
			}
			off += rec.NextEntryOffset
		}
	}
}

// folderModified reports a record of a folder modified, which Windows
// makes for every folder whose entries change, besides the record of the
// entry itself: one for .git each time git renames a file into it. It
// says nothing the entry's own record does not, and a folder reported
// reads as one replaced, everything under it to be read again. A folder
// added, removed or renamed is still reported.
func (ww *windowsWatch) folderModified(action uint32, name string) bool {
	if action != windows.FILE_ACTION_MODIFIED {
		return false
	}
	fi, err := os.Lstat(filepath.Join(ww.w.root, name))
	return err == nil && fi.IsDir()
}

func (ww *windowsWatch) close() error {
	// Wake the blocked read, then wait for it before closing the handle.
	ww.mu.Lock()
	ww.closed = true
	windows.CancelIoEx(ww.h, &ww.ov)
	ww.mu.Unlock()
	ww.done.Wait()
	windows.CloseHandle(ww.ov.HEvent)
	return windows.CloseHandle(ww.h)
}
