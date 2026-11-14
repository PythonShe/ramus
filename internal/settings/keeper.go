package settings

import "sync"

// Keeper writes the settings to their file off the caller's thread, one
// write at a time and only the latest asked for: two writes in flight
// could land in either order and leave an older choice in the file.
// Every window of the app shares one.
type Keeper struct {
	path string

	mu      sync.Mutex
	next    Settings
	onError func(error)
	has     bool
	busy    bool
	// idle is done whenever no write is pending or running.
	idle sync.WaitGroup
}

// NewKeeper keeps the settings at path.
func NewKeeper(path string) *Keeper { return &Keeper{path: path} }

// Path is the file the settings are kept in.
func (k *Keeper) Path() string { return k.path }

// Keep has s written soon; a later Keep before then replaces it.
// onError, which may be nil, hears that the write of s failed, on the
// writing goroutine.
func (k *Keeper) Keep(s Settings, onError func(error)) {
	k.mu.Lock()
	k.next, k.onError, k.has = s, onError, true
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
		s, onError := k.next, k.onError
		k.next, k.onError, k.has = Settings{}, nil, false
		k.mu.Unlock()
		if err := Save(k.path, s); err != nil && onError != nil {
			onError(err)
		}
	}
}

// Wait returns once what was asked for is written.
func (k *Keeper) Wait() { k.idle.Wait() }
