//go:build !darwin && !linux && !windows

package watch

func start(*Watcher, Options) (platform, error) { return nil, ErrUnsupported }
