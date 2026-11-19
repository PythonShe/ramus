//go:build linux

package watch

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// inotify watches one directory per watch, so the tree is walked at start
// and new directories are added as they appear; directories SkipDir names
// (ignored build output) are left out, which keeps a large work tree
// under max_user_watches. Hitting the limit stops the watcher with
// ErrLimit, and the app refreshes on focus instead.

const mask = syscall.IN_MODIFY | syscall.IN_ATTRIB | syscall.IN_CLOSE_WRITE | syscall.IN_CREATE |
	syscall.IN_DELETE | syscall.IN_DELETE_SELF | syscall.IN_MOVED_FROM | syscall.IN_MOVED_TO |
	syscall.IN_MOVE_SELF | syscall.IN_DONT_FOLLOW | syscall.IN_EXCL_UNLINK

// inotifyAddWatch is the call, for tests to reach the limit.
var inotifyAddWatch = syscall.InotifyAddWatch

type linuxWatch struct {
	w    *Watcher
	file *os.File
	skip func(string) bool
	mu   sync.Mutex
	dirs map[int32]string // watch descriptor to path relative to the root
	done sync.WaitGroup
}

func start(w *Watcher, opt Options) (platform, error) {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		return nil, err
	}
	l := &linuxWatch{w: w, skip: opt.SkipDir, dirs: map[int32]string{}}
	// A non-blocking descriptor goes through the runtime's poller, so Close
	// wakes the reader.
	l.file = os.NewFile(uintptr(fd), "inotify")
	if err := l.addTree("."); err != nil {
		l.file.Close()
		return nil, err
	}
	l.done.Add(1)
	go l.read()
	return l, nil
}

// onFD runs fn with the inotify descriptor, which stays open while it
// runs; once the watcher is closed it does not run, and the error is
// os.ErrClosed's. The number alone could be another file's by then.
func (l *linuxWatch) onFD(fn func(fd int) error) error {
	rc, err := l.file.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := rc.Control(func(fd uintptr) { ferr = fn(int(fd)) }); err != nil {
		return os.ErrClosed // it fails only once the file is closing
	}
	return ferr
}

// addTree watches dir (relative to the root) and every directory under
// it that is not skipped. A directory below the root that cannot be read
// is left unwatched, as one gone already is: one such folder, lost+found
// or a volume of another user's, must not turn the watcher off.
func (l *linuxWatch) addTree(dir string) error {
	err := filepath.WalkDir(filepath.Join(l.w.root, dir), func(p string, d fs.DirEntry, err error) error {
		top := p == l.w.root
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || !top && denied(err) {
				return nil // gone already, or not ours to read
			}
			return err
		}
		if !d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(l.w.root, p)
		rel = filepath.ToSlash(rel)
		if rel != "." && l.skip != nil && l.skip(rel) {
			return filepath.SkipDir
		}
		var wd int
		err = l.onFD(func(fd int) error {
			var err error
			wd, err = inotifyAddWatch(fd, p, mask|syscall.IN_ONLYDIR)
			return err
		})
		switch {
		case err == syscall.ENOSPC:
			return ErrLimit
		case err == syscall.ENOENT || err == syscall.ENOTDIR:
			return nil
		case !top && denied(err):
			return filepath.SkipDir
		case err != nil:
			return err
		}
		l.mu.Lock()
		l.dirs[int32(wd)] = rel
		l.mu.Unlock()
		return nil
	})
	if errors.Is(err, os.ErrClosed) {
		return nil // the watcher closed while it walked
	}
	return err
}

func denied(err error) bool {
	return errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)
}

// forget removes the watches wds, of a directory moved out of the tree and
// those under it: inotify follows a directory where it goes, and would go
// on reporting what changes there, at its old path here, and count it
// against the limit of watches.
func (l *linuxWatch) forget(wds []int32) {
	l.mu.Lock()
	for _, wd := range wds {
		delete(l.dirs, wd)
	}
	l.mu.Unlock()
	l.onFD(func(fd int) error {
		for _, wd := range wds {
			syscall.InotifyRmWatch(fd, uint32(wd))
		}
		return nil
	})
}

// under returns the watches of the directory rel and of those under it.
func (l *linuxWatch) under(rel string) []int32 {
	l.mu.Lock()
	defer l.mu.Unlock()
	var wds []int32
	for wd, d := range l.dirs {
		if d == rel || strings.HasPrefix(d, rel+"/") {
			wds = append(wds, wd)
		}
	}
	return wds
}

func (l *linuxWatch) read() {
	defer l.done.Done()
	buf := make([]byte, 256<<10)
	for {
		n, err := l.file.Read(buf)
		if err != nil {
			if !errors.Is(err, os.ErrClosed) {
				l.w.fail(err)
			}
			return
		}
		// Directories moved away in this read, by the move's cookie: one
		// whose arrival in the tree is not in the same read left it. An
		// arrival read later only watches it anew.
		var moved map[uint32][]int32
		for off := 0; off+syscall.SizeofInotifyEvent <= n; {
			ev := (*syscall.InotifyEvent)(unsafe.Pointer(&buf[off]))
			nameEnd := off + syscall.SizeofInotifyEvent + int(ev.Len)
			if nameEnd > n {
				break
			}
			name := buf[off+syscall.SizeofInotifyEvent : nameEnd]
			for len(name) > 0 && name[len(name)-1] == 0 {
				name = name[:len(name)-1]
			}
			off = nameEnd
			if ev.Mask&syscall.IN_Q_OVERFLOW != 0 {
				l.w.dropped()
				continue
			}
			l.mu.Lock()
			dir, ok := l.dirs[ev.Wd]
			if ev.Mask&syscall.IN_IGNORED != 0 {
				delete(l.dirs, ev.Wd)
			}
			l.mu.Unlock()
			if !ok {
				continue
			}
			rel := filepath.Join(dir, string(name))
			l.w.changed(rel)
			if ev.Mask&syscall.IN_ISDIR != 0 && ev.Mask&syscall.IN_MOVED_FROM != 0 {
				if moved == nil {
					moved = map[uint32][]int32{}
				}
				moved[ev.Cookie] = l.under(filepath.ToSlash(rel))
			}
			if ev.Mask&syscall.IN_ISDIR != 0 && ev.Mask&(syscall.IN_CREATE|syscall.IN_MOVED_TO) != 0 {
				if ev.Mask&syscall.IN_MOVED_TO != 0 {
					delete(moved, ev.Cookie)
				}
				// Watch the new directory, and report what was made in it
				// before the watch was in place.
				if err := l.addTree(rel); err != nil {
					l.w.fail(err)
				}
				l.w.changed(rel)
			}
		}
		for _, wds := range moved {
			l.forget(wds)
		}
	}
}

func (l *linuxWatch) close() error {
	err := l.file.Close()
	l.done.Wait()
	return err
}
