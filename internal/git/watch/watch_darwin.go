//go:build darwin

package watch

import (
	"errors"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// One FSEvents stream watches the whole tree with one kernel registration,
// reporting each file that changed (kFSEventStreamCreateFlagFileEvents).
// Streams deliver on one serial dispatch queue, off the main thread.

const (
	fseNoDefer    = 0x02
	fseWatchRoot  = 0x04
	fseFileEvents = 0x10

	fseMustScanSubDirs = 0x01
	fseUserDropped     = 0x02
	fseKernelDropped   = 0x04
	fseRootChanged     = 0x20

	fseSinceNow = ^uint64(0)
	cfUTF8      = 0x08000100
)

var (
	fseCreate     func(alloc, callback uintptr, ctx *fseContext, paths uintptr, since uint64, latency float64, flags uint32) uintptr
	fseSetQueue   func(stream, queue uintptr)
	fseStart      func(stream uintptr) uint8
	fseStop       func(stream uintptr)
	fseInvalidate func(stream uintptr)
	fseRelease    func(stream uintptr)
	cfString      func(alloc uintptr, s *byte, enc uint32) uintptr
	cfArray       func(alloc uintptr, values *uintptr, n int, callbacks uintptr) uintptr
	cfRelease     func(obj uintptr)
	queueCreate   func(label *byte, attr uintptr) uintptr
	dispatchSync  func(queue, ctx, work uintptr)
	arrayCalls    uintptr

	// The process has one callback and one queue, made once: purego
	// callbacks are never freed.
	callback, flush, queue uintptr
	loadOnce               sync.Once
	loadErr                error

	streamsMu sync.Mutex
	streams   = map[uintptr]*Watcher{}
	nextID    uintptr
)

type fseContext struct {
	version, info, retain, release, describe uintptr
}

func load() {
	loadOnce.Do(func() {
		cs, err := purego.Dlopen("/System/Library/Frameworks/CoreServices.framework/CoreServices", purego.RTLD_GLOBAL|purego.RTLD_NOW)
		if err != nil {
			loadErr = err
			return
		}
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_GLOBAL|purego.RTLD_NOW)
		if err != nil {
			loadErr = err
			return
		}
		purego.RegisterLibFunc(&fseCreate, cs, "FSEventStreamCreate")
		purego.RegisterLibFunc(&fseSetQueue, cs, "FSEventStreamSetDispatchQueue")
		purego.RegisterLibFunc(&fseStart, cs, "FSEventStreamStart")
		purego.RegisterLibFunc(&fseStop, cs, "FSEventStreamStop")
		purego.RegisterLibFunc(&fseInvalidate, cs, "FSEventStreamInvalidate")
		purego.RegisterLibFunc(&fseRelease, cs, "FSEventStreamRelease")
		purego.RegisterLibFunc(&cfString, cf, "CFStringCreateWithCString")
		purego.RegisterLibFunc(&cfArray, cf, "CFArrayCreate")
		purego.RegisterLibFunc(&cfRelease, cf, "CFRelease")
		if arrayCalls, err = purego.Dlsym(cf, "kCFTypeArrayCallBacks"); err != nil {
			loadErr = err
			return
		}
		purego.RegisterLibFunc(&queueCreate, purego.RTLD_DEFAULT, "dispatch_queue_create")
		purego.RegisterLibFunc(&dispatchSync, purego.RTLD_DEFAULT, "dispatch_sync_f")
		callback = purego.NewCallback(events)
		flush = purego.NewCallback(func(uintptr) {})
		label := []byte("ramus.watch\x00")
		queue = queueCreate(&label[0], 0)
	})
}

// events is the FSEventStreamCallback: (stream, info, count, paths,
// flags, ids). It runs on the watch queue and never blocks.
func events(_ uintptr, info uintptr, n uintptr, paths **byte, flags *uint32, _ uintptr) {
	streamsMu.Lock()
	w := streams[info]
	streamsMu.Unlock()
	if w == nil {
		return
	}
	ps := unsafe.Slice(paths, int(n))
	fs := unsafe.Slice(flags, int(n))
	for i := range ps {
		f := fs[i]
		switch {
		case f&(fseUserDropped|fseKernelDropped|fseMustScanSubDirs|fseRootChanged) != 0:
			w.dropped()
		default:
			w.changed(goString(ps[i]))
		}
	}
}

func goString(p *byte) string {
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 {
		n++
	}
	return string(unsafe.Slice(p, n))
}

type darwinWatch struct {
	stream uintptr
	id     uintptr
}

func start(w *Watcher, _ Options) (platform, error) {
	load()
	if loadErr != nil {
		return nil, loadErr
	}
	path := append([]byte(w.root), 0)
	s := cfString(0, &path[0], cfUTF8)
	if s == 0 {
		return nil, errors.New("watch: cannot make a CFString of the root")
	}
	defer cfRelease(s)
	arr := cfArray(0, &s, 1, arrayCalls)
	if arr == 0 {
		return nil, errors.New("watch: cannot make a CFArray")
	}
	defer cfRelease(arr)
	streamsMu.Lock()
	nextID++
	id := nextID
	streams[id] = w
	streamsMu.Unlock()
	ctx := fseContext{info: id}
	latency := w.latency.Seconds() / 5 // FSEvents batches a little; deliver does the rest
	stream := fseCreate(0, callback, &ctx, arr, fseSinceNow, latency, fseFileEvents|fseNoDefer|fseWatchRoot)
	if stream == 0 {
		forget(id)
		return nil, errors.New("watch: FSEventStreamCreate failed")
	}
	fseSetQueue(stream, queue)
	if fseStart(stream) == 0 {
		fseInvalidate(stream)
		fseRelease(stream)
		forget(id)
		return nil, errors.New("watch: FSEventStreamStart failed")
	}
	return &darwinWatch{stream: stream, id: id}, nil
}

func forget(id uintptr) {
	streamsMu.Lock()
	delete(streams, id)
	streamsMu.Unlock()
}

func (d *darwinWatch) close() error {
	fseStop(d.stream)
	fseInvalidate(d.stream)
	// Wait for a callback already running on the queue to finish.
	dispatchSync(queue, 0, flush)
	fseRelease(d.stream)
	forget(d.id)
	return nil
}
