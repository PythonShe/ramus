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
// set nowhere else, and it may be edited while the app runs, by hand or
// by another copy of the app. Once told the settings the app started
// with (Started), the keeper holds two copies of the settings apart, the
// app's and the file, and base, the last settings both held. A field in
// which the app differs from base is the app's change, one in which the
// file does is the file's. A write and a Take each merge the two, the
// app's change winning a field both changed: a write puts the result in
// the file and hands what the file holds that the app does not to OnFile;
// Take puts a reading of the file in force in the app.
//
// What keeps the two apart:
//
//  1. base moves only to settings the file is known to hold: those the
//     app had written, once the write succeeded, or a reading of the file
//     the app took. A change of the app's not yet written is never in it,
//     so a write that failed leaves its changes to the next, and a Take
//     never takes them for the file's.
//  2. A reading is put in force only when nothing happened since it was
//     made: no later reading or write of the file, and no Keep after the
//     last write that ended before it. Merged later, it would be merged
//     against a base it was not read from and undo what came after it.
//     Dropping it loses nothing: the write of a Keep reads the file and
//     hands on what it holds that the app does not; a write that failed
//     has the next Check read the file whatever it holds; and a later
//     reading is of the file as it is since.
//  3. A write replaces the file only while it is as the write read it,
//     looked at again before each try of the rename: else the write reads
//     it again and merges anew, so an edit made meanwhile is kept.
type Keeper struct {
	path string
	// OnFile, when set, hears settings read from the file that are not
	// the app's: edited by hand. It is called on a goroutine of its own,
	// which nothing waits for but Wait; Take them on the main thread.
	OnFile func(FromFile)

	mu      sync.Mutex
	next    Settings
	onError func(error)
	has     bool
	busy    bool
	// base is the last settings the app and the file both held (1 above).
	// Nil until Started: every field is then written.
	base *Settings
	// kept counts the Keeps; wrote is kept as it was when the last write
	// to end was asked for, whether it succeeded or not. seen counts the
	// readings of the file: each write, and each Check that found it
	// changed.
	kept, wrote, seen uint64
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
	// seen and wrote are the keeper's when it was read.
	seen, wrote uint64
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

// Keep has s, the app's settings, written soon; a later Keep before then
// replaces it. onError, which may be nil, hears that the write of s
// failed, on a goroutine of its own, as OnFile.
func (k *Keeper) Keep(s Settings, onError func(error)) {
	k.mu.Lock()
	k.kept++
	if !k.has {
		k.work.add()
	}
	k.next, k.onError, k.has = s, onError, true
	if k.busy {
		k.mu.Unlock()
		return
	}
	k.busy = true
	k.mu.Unlock()
	go k.run()
}

func (k *Keeper) run() {
	for {
		k.mu.Lock()
		if !k.has {
			k.busy = false
			k.mu.Unlock()
			return
		}
		s, gen, onError := k.next, k.kept, k.onError
		k.next, k.onError, k.has = Settings{}, nil, false
		k.mu.Unlock()
		news, err := k.write(s, gen)
		k.tell(err, onError, news)
		k.work.end()
	}
}

// write writes s, the settings of Keep number gen, and returns what the
// file holds then if the app does not.
func (k *Keeper) write(s Settings, gen uint64) (*FromFile, error) {
	k.file.Lock()
	defer k.file.Unlock()
	// No Take moves base meanwhile: Keep number gen is not written yet.
	k.mu.Lock()
	base := k.base
	k.mu.Unlock()
	got, data, err := save(k.path, s, base)
	// Whatever the file holds now, the next Check reads it whole.
	k.disk, k.diskKnown = data, err == nil
	k.mu.Lock()
	defer k.mu.Unlock()
	k.wrote = gen
	k.seen++
	if err != nil || base == nil {
		return nil, err
	}
	k.base = &s
	if Same(got, s) {
		return nil, nil
	}
	return &FromFile{got, k.seen, gen}, nil
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
		news, err := k.check()
		k.tell(err, onError, news)
		k.work.end()
	}()
}

func (k *Keeper) check() (*FromFile, error) {
	k.file.Lock()
	defer k.file.Unlock()
	return k.read()
}

// read is check with k.file held.
func (k *Keeper) read() (*FromFile, error) {
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
	k.mu.Lock()
	defer k.mu.Unlock()
	k.seen++
	return &FromFile{s, k.seen, k.wrote}, err
}

// Take merges f, read from the file, into cur, the app's settings, on the
// main thread: what the app changed since base stays, the rest is the
// file's. It returns the settings to put in force, and false when they
// are cur: nothing new, or f is not to be taken (2 on Keeper).
func (k *Keeper) Take(f FromFile, cur Settings) (Settings, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.base == nil || f.seen != k.seen || f.wrote != k.kept {
		return cur, false
	}
	s := Merge(*k.base, cur, f.s)
	k.base = &f.s
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
