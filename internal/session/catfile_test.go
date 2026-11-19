package session

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ramus/internal/git/catfile"
	"ramus/internal/git/gittest"
)

// A read through the repository's cat-file waits its turn behind
// another's batch; cancelled, as when its view is left or the repository
// closes, it stops waiting rather than reading on for nobody.
func TestCatFileReadsStopWithTheirContext(t *testing.T) {
	r := gittest.Init(t)
	r.Write("a", "1")
	id := mustHex(t, strings.TrimSpace(r.Commit("one")))
	s, _ := openFor(t, r)
	cat, err := s.Repo.CatFile()
	if err != nil {
		t.Fatal(err)
	}
	holding, release := make(chan struct{}), make(chan struct{})
	held := make(chan error, 1)
	go func() {
		held <- cat.ReadContext(context.Background(), [][]byte{id}, func(int, catfile.Object) error {
			close(holding)
			<-release
			return nil
		})
	}()
	<-holding
	defer func() { close(release); <-held }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := make(chan error, 1)
	go func() {
		_, err := commitsOf(ctx, cat, nil, [][]byte{id})
		got <- err
	}()
	select {
	case err := <-got:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("a cancelled read said %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("a cancelled read waited for the batch before it")
	}
}
