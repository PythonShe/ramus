package settings

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"sync"
	"time"
)

// Keeper writes the settings to their file off the caller's thread, one
// write at a time and only the latest asked for: two writes in flight
// could land in either order and leave an older choice in the file.
// Every window of the app shares one.
//
// The file is the user's as much as the app's: Actions and Emails are
// set nowhere else, and it may be edited while the app runs. Once told
// the settings the app started with (Started), a write takes from the
// app only what it changed since, and leaves the rest as the file has
// it; what the file holds that the app does not goes to OnFile, for the
// app to put in force through Take.
type Keeper struct {
	path string
	// OnFile, when set, hears settings read from the file that are not
	// the app's: edited by hand. It is called on a goroutine of its own,
	// which nothing waits for but Wait; Take them on the main thread.
	OnFile func(FromFile)

	mu       sync.Mutex
	next     Settings
	nextBase *Settings
	onError  func(error)
	has      bool
	busy     bool
	// base is what the app's settings stem from: the settings last kept,
	// or taken from the file. Nil until Started: every field is then
	// written.
	base *Settings
	// reads counts the settings handed to OnFile, taken the count of the
	// last Take put in force: an older one is not.
	reads, taken uint64
	// file serializes reading and writing the file; disk is its content
	// as last read or written here, known once read.
	file      sync.Mutex
	disk      []byte
	diskKnown bool
	checkErr  string
	// work counts the writes and checks pending or running; told the
	// callbacks (OnFile, onError) running. A write or check counts as done
	// before its callbacks run, on a goroutine of their own: they may wait
	// for the main thread, which a quit holds waiting for the writes.
	work, told inflight
}

// FromFile is settings read from the file, to Take.
type FromFile struct {
	s Settings
	n uint64
}

// Settings are the settings read.
func (f FromFile) Settings() Settings { return f.s }

// NewKeeper keeps the settings at path.
func NewKeeper(path string) *Keeper { return &Keeper{path: path} }

// Path is the file the settings are kept in.
func (k *Keeper) Path() string { return k.path }

// Started tells the keeper the settings the app started with, read from
// its file, so that what the app changes is told apart from what the
// file does.
func (k *Keeper) Started(s Settings) {
	k.mu.Lock()
	k.base = &s
	k.mu.Unlock()
}

// Keep has s written soon; a later Keep before then replaces it.
// onError, which may be nil, hears that the write of s failed, on a
// goroutine of its own, as OnFile.
func (k *Keeper) Keep(s Settings, onError func(error)) {
	k.mu.Lock()
	if !k.has {
		// A Keep that replaces one pending holds its changes too: what they
		// changed is told from the base of the first.
		k.nextBase = k.base
	}
	k.next, k.onError, k.has = s, onError, true
	if k.base != nil {
		k.base = &s
	}
	if k.busy {
		k.mu.Unlock()
		return
	}
	k.busy = true
	k.work.add()
	k.mu.Unlock()
	go k.run()
}

func (k *Keeper) run() {
	defer k.work.end()
	for {
		k.mu.Lock()
		if !k.has {
			k.busy = false
			k.mu.Unlock()
			return
		}
		s, base, onError := k.next, k.nextBase, k.onError
		k.next, k.nextBase, k.onError, k.has = Settings{}, nil, nil, false
		k.mu.Unlock()
		k.file.Lock()
		got, data, err := save(k.path, s, base)
		var news *FromFile
		if err == nil {
			k.disk, k.diskKnown = data, true
			if base != nil && !Same(got, s) {
				news = k.fromFile(got)
			}
		}
		k.file.Unlock()
		k.tell(err, onError, news)
	}
}

// tell hands err to onError and news to OnFile, on a goroutine of its own.
func (k *Keeper) tell(err error, onError func(error), news *FromFile) {
	if err != nil && onError == nil {
		err = nil
	}
	if news != nil && k.OnFile == nil {
		news = nil
	}
	if err == nil && news == nil {
		return
	}
	k.told.add()
	go func() {
		defer k.told.end()
		if err != nil {
			onError(err)
		}
		if news != nil {
			k.OnFile(*news)
		}
	}()
}

// fromFile numbers settings read from the file; k.file is held, so the
// numbers follow the file's changes.
func (k *Keeper) fromFile(s Settings) *FromFile {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.reads++
	return &FromFile{s, k.reads}
}

// Check reads the file again off the caller's thread, when the app
// regains the user, who may have edited it: settings it holds that are
// not the app's go to OnFile, and onError hears, once, that it does not
// read; saves are refused until it does.
func (k *Keeper) Check(onError func(error)) {
	k.mu.Lock()
	started := k.base != nil
	k.mu.Unlock()
	if !started || k.OnFile == nil {
		return
	}
	k.work.add()
	go func() {
		defer k.work.end()
		news, err := k.check()
		k.tell(err, onError, news)
	}()
}

func (k *Keeper) check() (*FromFile, error) {
	k.file.Lock()
	defer k.file.Unlock()
	data, err := os.ReadFile(k.path)
	if errors.Is(err, fs.ErrNotExist) {
		data, err = nil, nil // gone: the defaults
	}
	if err != nil {
		if err.Error() == k.checkErr {
			return nil, nil
		}
		k.checkErr = err.Error()
		return nil, &LoadError{Path: k.path, Err: err}
	}
	k.checkErr = ""
	if k.diskKnown && bytes.Equal(data, k.disk) {
		return nil, nil
	}
	k.disk, k.diskKnown = data, true
	s := Defaults
	if data != nil {
		var partial bool
		s, partial, err = decode(data)
		if err != nil && !partial {
			return nil, &LoadError{Path: k.path, Err: err}
		}
		if err != nil {
			err = &LoadError{Path: k.path, Err: err, Partial: true}
		}
	}
	return k.fromFile(s), err
}

// Take merges f, read from the file, into cur, the app's settings, on the
// main thread: what the app changed since it last had them stays, the
// rest is the file's. It returns the settings to put in force, and false
// when they are cur: nothing new, or f is older than settings taken.
func (k *Keeper) Take(f FromFile, cur Settings) (Settings, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.base == nil || f.n <= k.taken {
		return cur, false
	}
	k.taken = f.n
	s := Merge(*k.base, cur, f.s)
	k.base = &s
	return s, !Same(s, cur)
}

// Wait returns once what was asked for is written and told: it waits for
// the callbacks too, so never call it on a thread they wait for. A quit,
// on the main thread, waits with WaitWrites.
func (k *Keeper) Wait() {
	k.work.wait(-1)
	k.told.wait(-1)
}

// WaitWrites waits up to d for the writes and checks asked for to end,
// and reports whether they did. It does not wait for their callbacks,
// and so may be called on the main thread.
func (k *Keeper) WaitWrites(d time.Duration) bool { return k.work.wait(d) }

// inflight counts work running, and lets a wait for none be bounded,
// which a sync.WaitGroup does not.
type inflight struct {
	mu sync.Mutex
	n  int
	// none is closed when n falls to 0.
	none chan struct{}
}

func (f *inflight) add() {
	f.mu.Lock()
	if f.n == 0 {
		f.none = make(chan struct{})
	}
	f.n++
	f.mu.Unlock()
}

func (f *inflight) end() {
	f.mu.Lock()
	f.n--
	if f.n == 0 {
		close(f.none)
	}
	f.mu.Unlock()
}

// wait returns true once nothing runs, false after d; d < 0 waits for as
// long as it takes.
func (f *inflight) wait(d time.Duration) bool {
	f.mu.Lock()
	n, none := f.n, f.none
	f.mu.Unlock()
	if n == 0 {
		return true
	}
	if d < 0 {
		<-none
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-none:
		return true
	case <-t.C:
		return false
	}
}
