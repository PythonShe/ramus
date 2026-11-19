//go:build unix

package write

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A lock another user made, which no process this user can see holds,
// is not taken for one left behind: their processes are hidden from this
// user, so it is reported as theirs and not removed.
func TestAnotherUsersLockIsNotLeft(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root sees every process")
	}
	gitDir := t.TempDir()
	lock := filepath.Join(gitDir, "index.lock")
	if err := os.WriteFile(lock, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-LeftAfter - time.Minute)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Made by this user: left behind, and removable.
	var le *LockedError
	if err := describeLock(ctx, lock); !errors.As(err, &le) || !le.Left() {
		t.Fatalf("this user's lock: %v", err)
	}
	// Seen as another user would see it: the lock's owner is not them.
	me := os.Getuid()
	currentUID = func() int { return me + 1 }
	t.Cleanup(func() { currentUID = os.Getuid })
	if err := describeLock(ctx, lock); !errors.As(err, &le) || le.Left() || !errors.Is(le.Unknown, errOtherUser) {
		t.Fatalf("another user's lock: %v", err)
	}
	if err := RemoveLock(ctx, gitDir, lock); !errors.As(err, &le) || !errors.Is(le.Unknown, errOtherUser) {
		t.Fatalf("RemoveLock: %v", err)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Fatalf("another user's lock was removed: %v", err)
	}
}
