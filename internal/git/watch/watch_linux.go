//go:build linux

package watch

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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
	fd   int
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
	l := &linuxWatch{w: w, fd: fd, skip: opt.SkipDir, dirs: map[int32]string{}}
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

// addTree watches dir (relative to the root) and every directory under
// it that is not skipped.
func (l *linuxWatch) addTree(dir string) error {
	return filepath.WalkDir(filepath.Join(l.w.root, dir), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil // gone already
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
		wd, err := inotifyAddWatch(l.fd, p, mask|syscall.IN_ONLYDIR)
		if err == syscall.ENOSPC {
			return ErrLimit
		}
		if err != nil {
			if err == syscall.ENOENT || err == syscall.ENOTDIR {
				return nil
			}
			return err
		}
		l.mu.Lock()
		l.dirs[int32(wd)] = rel
		l.mu.Unlock()
		return nil
	})
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
			if ev.Mask&syscall.IN_ISDIR != 0 && ev.Mask&(syscall.IN_CREATE|syscall.IN_MOVED_TO) != 0 {
				// Watch the new directory, and report what was made in it
				// before the watch was in place.
				if err := l.addTree(rel); err != nil {
					l.w.fail(err)
				}
				l.w.changed(rel)
			}
		}
	}
}

func (l *linuxWatch) close() error {
	err := l.file.Close()
	l.done.Wait()
	return err
}
