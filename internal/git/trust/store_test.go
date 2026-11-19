package trust

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStore(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings", "trust.json")
	s, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(dir, "repo", ".git")
	os.MkdirAll(repo, 0o755)
	link := filepath.Join(dir, "link")
	os.Symlink(filepath.Join(dir, "repo"), link)
	if s.Trusted(repo) {
		t.Fatal("trusted before asked")
	}
	if err := s.Set(repo, true); err != nil {
		t.Fatal(err)
	}
	if !s.Trusted(filepath.Join(link, ".git")) {
		t.Error("the same repository through a symlink is not trusted")
	}
	again, err := Load(path)
	if err != nil || !again.Trusted(repo) {
		t.Fatalf("not saved: %v", err)
	}
	if err := again.Set(repo, false); err != nil {
		t.Fatal(err)
	}
	if third, _ := Load(path); third.Trusted(repo) {
		t.Error("still trusted after revoking")
	}
}

// A folder put where a trusted one was, an archive extracted again, is
// not trusted: the answer was given of the folder, not of its path. A
// list saved before folders were told apart still holds.
func TestTrustHoldsForTheFolderTrusted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trust.json")
	repo := filepath.Join(dir, "project", ".git")
	os.MkdirAll(repo, 0o755)
	s, _ := Load(path)
	if err := s.Set(repo, true); err != nil {
		t.Fatal(err)
	}
	if again, _ := Load(path); !again.Trusted(repo) {
		t.Fatal("not trusted after loading again")
	}
	// Made while the first still exists, so it cannot have its identity.
	other := filepath.Join(dir, "other")
	os.MkdirAll(filepath.Join(other, ".git"), 0o755)
	os.RemoveAll(filepath.Join(dir, "project"))
	if err := os.Rename(other, filepath.Join(dir, "project")); err != nil {
		t.Fatal(err)
	}
	if s.Trusted(repo) {
		t.Error("a folder put at a trusted path is trusted")
	}
	if again, _ := Load(path); again.Trusted(repo) {
		t.Error("a folder put at a trusted path is trusted after loading again")
	}
	if err := s.Set(repo, true); err != nil || !s.Trusted(repo) {
		t.Errorf("trusting it anew: %v", err)
	}

	old := filepath.Join(dir, "old.json")
	os.WriteFile(old, []byte(`{"trusted": [`+strconv.Quote(repo)+`]}`), 0o600)
	if legacy, err := Load(old); err != nil || !legacy.Trusted(repo) {
		t.Errorf("a list from before identities: %v", err)
	}
}

// Answers given at once, from several tabs, are all saved: no save of an
// older list lands after a newer one.
func TestConcurrentSetsAllSaved(t *testing.T) {
	var repos []string
	for i := range 8 {
		d := filepath.Join(t.TempDir(), strconv.Itoa(i), ".git")
		os.MkdirAll(d, 0o755)
		repos = append(repos, d)
	}
	for round := range 50 {
		path := filepath.Join(t.TempDir(), "trust.json")
		s, _ := Load(path)
		var wg sync.WaitGroup
		for _, d := range repos {
			wg.Go(func() {
				if err := s.Set(d, true); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		back, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range repos {
			if !back.Trusted(d) {
				t.Fatalf("round %d: %s was not saved", round, d)
			}
		}
	}
}

// A store file that does not read leaves the app a usable, empty store,
// trusting nothing; the file is kept aside, never written over.
func TestUnreadableStore(t *testing.T) {
	repo := filepath.Join(t.TempDir(), ".git")
	os.MkdirAll(repo, 0o755)
	for _, content := range []string{"", "{broken", `{"trusted": 3}`} {
		dir := t.TempDir()
		path := filepath.Join(dir, "trust.json")
		os.WriteFile(path, []byte(content), 0o600)
		s, err := Load(path)
		if err == nil || s == nil {
			t.Fatalf("%q: %v %v", content, s, err)
		}
		if s.Trusted(repo) {
			t.Errorf("%q: trusts", content)
		}
		if kept, err := os.ReadFile(path + ".bad"); err != nil || string(kept) != content {
			t.Errorf("%q: not kept aside: %q %v", content, kept, err)
		}
		if err := s.Set(repo, true); err != nil {
			t.Fatal(err)
		}
		if again, err := Load(path); err != nil || !again.Trusted(repo) {
			t.Errorf("%q: the new answer is not saved: %v", content, err)
		}
	}
}

// What a later version of the app wrote in the file, a key of its own
// and a key in a folder's entry, is still there after this version saves
// an answer; a folder no longer trusted takes its entry with it.
func TestKeepsWhatALaterVersionAdded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trust.json")
	repo := filepath.Join(dir, "project", ".git")
	other := filepath.Join(dir, "other", ".git")
	os.MkdirAll(repo, 0o755)
	os.MkdirAll(other, 0o755)
	repo = clean(repo)
	id, err := identity(repo)
	if err != nil {
		t.Fatal(err)
	}
	later := `{
	"trusted": [` + strconv.Quote(repo) + `],
	"folders": {
		` + strconv.Quote(repo) + `: {"id": ` + strconv.Quote(id) + `, "colour": "teal"},
		"/elsewhere/.git": {"declined": true}
	},
	"version": 2,
	"policy": {"askEvery": "30d"}
}`
	os.WriteFile(path, []byte(later), 0o600)
	s, err := Load(path)
	if err != nil || !s.Trusted(repo) {
		t.Fatalf("not read: %v", err)
	}
	if err := s.Set(other, true); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Trusted []string
		Folders map[string]map[string]json.RawMessage
		Version json.RawMessage
		Policy  json.RawMessage
	}
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if string(got.Version) != "2" || !strings.Contains(string(got.Policy), `"askEvery": "30d"`) {
		t.Errorf("the file's own keys were dropped:\n%s", data)
	}
	if f := got.Folders[repo]; string(f["colour"]) != `"teal"` || string(f["id"]) != strconv.Quote(id) {
		t.Errorf("the folder's keys were dropped:\n%s", data)
	}
	if string(got.Folders["/elsewhere/.git"]["declined"]) != "true" {
		t.Errorf("an entry of a folder not trusted was dropped:\n%s", data)
	}
	if again, err := Load(path); err != nil || !again.Trusted(repo) || !again.Trusted(other) {
		t.Fatalf("not read back: %v\n%s", err, data)
	}

	if err := s.Set(repo, false); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), "teal") || !strings.Contains(string(data), "askEvery") {
		t.Errorf("after the folder is no longer trusted:\n%s", data)
	}
}

// A quit while an answer is being saved waits for the save, which the
// next launch then finds; a save that does not end does not hold the
// quit past its bound.
func TestWaitHasTheAnswerSaved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trust.json")
	repo := filepath.Join(t.TempDir(), ".git")
	os.MkdirAll(repo, 0o755)
	s, _ := Load(path)
	if !s.Wait(0) {
		t.Fatal("waited with nothing being saved")
	}
	held, release := make(chan struct{}), make(chan struct{})
	writeHook = func() {
		close(held)
		<-release
	}
	t.Cleanup(func() { writeHook = nil })
	saved := make(chan error, 1)
	go func() { saved <- s.Set(repo, true) }()
	<-held
	if s.Wait(10 * time.Millisecond) {
		t.Fatal("Wait returned as though saved while the save was held")
	}
	waited := make(chan bool, 1)
	go func() { waited <- s.Wait(time.Minute) }()
	close(release)
	if !<-waited {
		t.Fatal("Wait gave up on a save that ended")
	}
	if again, err := Load(path); err != nil || !again.Trusted(repo) {
		t.Fatalf("the answer is not in the file once Wait returned: %v", err)
	}
	if err := <-saved; err != nil {
		t.Fatal(err)
	}
}
