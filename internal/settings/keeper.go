package settings

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"sync"
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
	// the app's: edited by hand. It is called on a goroutine of the
	// keeper's; Take them on the main thread.
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
	// idle is done whenever no write or check is pending or running.
	idle sync.WaitGroup
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
// onError, which may be nil, hears that the write of s failed, on the
// writing goroutine. Call it on the main thread, as Take.
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
	k.idle.Add(1)
	k.mu.Unlock()
	go k.run()
}

func (k *Keeper) run() {
	defer k.idle.Done()
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
		if err != nil && onError != nil {
			onError(err)
		}
		if news != nil && k.OnFile != nil {
			k.OnFile(*news)
		}
	}
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
	k.idle.Add(1)
	go func() {
		defer k.idle.Done()
		news, err := k.check()
		if err != nil && onError != nil {
			onError(err)
		}
		if news != nil {
			k.OnFile(*news)
		}
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

// Wait returns once what was asked for is written.
func (k *Keeper) Wait() { k.idle.Wait() }
