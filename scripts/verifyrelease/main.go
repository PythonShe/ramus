// Command verifyrelease checks a release of Ramus whole: every file each
// platform needs is there and nothing else is; SHA256SUMS lists every file
// with its right checksum; and each update manifest names this version,
// its own platform and architecture, and files of the release whose size
// and signature, by the public key in mygo.json, are those it records.
//
//	go run ./scripts/verifyrelease v1.2.3              # a release on GitHub, downloaded with gh
//	go run ./scripts/verifyrelease -dir assets v1.2.3  # its files, already downloaded
//	go run ./scripts/verifyrelease -build build -platforms darwin/arm64,darwin/amd64 v1.2.3
//
// The last only checks that a build made every package of its platforms,
// before anything is uploaded; mygo build only warns when it skips one,
// as the Windows installer without NSIS.
//
// The release workflow runs it on the draft before publishing it. The
// files expected are those mygo build makes for the platforms release.yml
// builds: a file missing, renamed or left over fails, so a change to
// either shows here rather than in a release without an installer.
package main

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// target is a platform release.yml builds, as goos and goarch.
type target struct{ goos, goarch string }

func (t target) String() string { return t.goos + "-" + t.goarch }

// targets are the platforms of release.yml's build matrix.
var targets = []target{
	{"darwin", "arm64"}, {"darwin", "amd64"},
	{"linux", "amd64"}, {"linux", "arm64"},
	{"windows", "amd64"}, {"windows", "arm64"},
}

// release is what a release is checked against.
type release struct {
	slug    string // the start of every file name, as mygo makes it
	repo    string // owner/name on GitHub
	version string // without the tag's "v"
	key     ed25519.PublicKey
}

// files returns the files mygo build (and scripts/appimage.sh) make for
// t, the update manifest and the deltas apart.
func (r release) files(t target) []string {
	v := r.version
	names := []string{r.archive(t)}
	switch t.goos {
	case "darwin":
		names = append(names, r.slug+"-"+v+"-"+t.goarch+".dmg")
	case "windows":
		names = append(names, r.slug+"-setup-"+v+"-"+t.goarch+".exe")
	case "linux":
		rpmArch := map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[t.goarch]
		names = append(names,
			r.slug+"_"+debVersion(v)+"_"+t.goarch+".deb",
			r.slug+"-"+rpmVersion(v)+"-1."+rpmArch+".rpm")
		if t.goarch == "amd64" {
			names = append(names, r.slug+"-"+v+"-linux-x86_64.AppImage")
		}
	}
	return names
}

func (r release) archive(t target) string {
	return r.slug + "-" + r.version + "-" + t.String() + ".tar.gz"
}

func (r release) delta(from string, t target) string {
	return r.slug + "-" + from + "-to-" + r.version + "-" + t.String() + ".delta"
}

// url is where a file of this release is downloaded from.
func (r release) url(name string) string {
	return "https://github.com/" + r.repo + "/releases/download/v" + r.version + "/" + name
}

// debVersion and rpmVersion are mygo's names for a version in a package
// (cmd/mygo/deb.go, rpm.go): a pre-release sorts before its release.
func debVersion(v string) string {
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v = v[:i] + "~" + strings.ReplaceAll(v[i+1:], "-", ".")
	}
	return strings.ReplaceAll(v, "+", "~")
}

func rpmVersion(v string) string {
	v, build, _ := strings.Cut(v, "+")
	v, pre, _ := strings.Cut(v, "-")
	if pre != "" {
		v += "~" + pre
	}
	if build != "" {
		v += "+" + build
	}
	return regexp.MustCompile(`[^A-Za-z0-9._+~^]+`).ReplaceAllString(v, "_")
}

const (
	checksums = "SHA256SUMS"
	installSh = "install.sh"
	installPS = "install.ps1"
)

// manifest is mygo's update manifest (internal/update.Manifest); a field
// this does not know fails, as it would go unchecked.
type manifest struct {
	Version   string    `json:"version"`
	Notes     string    `json:"notes"`
	Date      string    `json:"date"`
	URL       string    `json:"url"`
	Size      int64     `json:"size"`
	Signature string    `json:"signature"`
	Deltas    []delta   `json:"deltas"`
	Previous  []archive `json:"previous"`
}

type delta struct {
	From      string `json:"from"`
	URL       string `json:"url"`
	Size      int64  `json:"size"`
	Signature string `json:"signature"`
}

type archive struct {
	Version   string `json:"version"`
	URL       string `json:"url"`
	Size      int64  `json:"size"`
	Signature string `json:"signature"`
}

// verify checks the release whose files are in dir and returns what is
// wrong with it.
func verify(dir string, r release) []error {
	var errs []error
	fail := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	entries, err := os.ReadDir(dir)
	if err != nil {
		return []error{err}
	}
	present := map[string]bool{}
	for _, e := range entries {
		if e.Type().IsRegular() {
			present[e.Name()] = true
		} else {
			fail("%s is not a file", e.Name())
		}
	}
	sums := map[string][32]byte{}
	sum := func(name string) [32]byte {
		if s, ok := sums[name]; ok {
			return s
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			fail("%v", err)
		}
		s := sha256.Sum256(data)
		sums[name] = s
		return s
	}

	expected := map[string]bool{checksums: true, installSh: true, installPS: true}
	for _, t := range targets {
		for _, name := range r.files(t) {
			expected[name] = true
		}
		name := "update-" + t.String() + ".json"
		expected[name] = true
		if !present[name] {
			continue
		}
		errs = append(errs, checkManifest(dir, name, t, r, sum, present)...)
		if m, err := readManifest(filepath.Join(dir, name)); err == nil {
			for _, d := range m.Deltas {
				expected[r.delta(d.From, t)] = true
			}
		}
	}
	for _, name := range sorted(expected) {
		if !present[name] {
			fail("%s is missing", name)
		}
	}
	for _, name := range sorted(present) {
		if !expected[name] {
			fail("%s is not a file this release should have", name)
		}
	}

	// SHA256SUMS lists every file but itself and the manifests, which
	// the notes are written into after it.
	listed, err := readChecksums(filepath.Join(dir, checksums))
	if err != nil {
		if present[checksums] {
			fail("%v", err)
		}
	} else {
		for _, name := range sorted(present) {
			if name == checksums || isManifest(name) {
				continue
			}
			want, ok := listed[name]
			if !ok {
				fail("%s does not list %s", checksums, name)
			} else if got := sum(name); hex.EncodeToString(got[:]) != want {
				fail("%s does not match its checksum in %s", name, checksums)
			}
		}
		for _, name := range sorted(listed) {
			if !present[name] {
				fail("%s lists %s, which the release does not have", checksums, name)
			} else if isManifest(name) {
				fail("%s lists %s, whose notes change after it", checksums, name)
			}
		}
	}
	return errs
}

// checkManifest checks the update manifest name, of target t.
func checkManifest(dir, name string, t target, r release, sum func(string) [32]byte, present map[string]bool) []error {
	var errs []error
	fail := func(format string, a ...any) { errs = append(errs, fmt.Errorf(name+": "+format, a...)) }
	path := filepath.Join(dir, name)
	m, err := readManifest(path)
	if err != nil {
		return []error{fmt.Errorf("%s: %w", name, err)}
	}
	if m.Version != r.version {
		fail("version %q, not %q", m.Version, r.version)
	}
	if _, err := time.Parse(time.RFC3339, m.Date); err != nil {
		fail("date %q is not a time", m.Date)
	}
	// The file named, its size and its signature, as the updater checks
	// them: the signature is of the file's SHA-256.
	file := func(what, want, url string, size int64, signature string) {
		if url != r.url(want) {
			fail("%s is %s, not %s", what, url, r.url(want))
			return
		}
		if !present[want] {
			fail("%s names %s, which the release does not have", what, want)
			return
		}
		info, err := os.Stat(filepath.Join(dir, want))
		if err != nil {
			fail("%v", err)
			return
		}
		if info.Size() != size {
			fail("%s's size is %d, but %s has %d bytes", what, size, want, info.Size())
		}
		sig, err := base64.StdEncoding.DecodeString(signature)
		digest := sum(want)
		if err != nil || !ed25519.Verify(r.key, digest[:], sig) {
			fail("%s's signature is not that of %s by the key in mygo.json", what, want)
		}
	}
	file("url", r.archive(t), m.URL, m.Size, m.Signature)
	seen := map[string]bool{}
	for i, d := range m.Deltas {
		if d.From == "" || d.From == r.version || seen[d.From] {
			fail("deltas[%d] is from %q", i, d.From)
			continue
		}
		seen[d.From] = true
		file(fmt.Sprintf("deltas[%d].url", i), r.delta(d.From, t), d.URL, d.Size, d.Signature)
	}
	// The archives of earlier versions are in their own releases; mygo
	// build makes the next deltas from them, so they must be this
	// target's.
	for i, p := range m.Previous {
		want := "https://github.com/" + r.repo + "/releases/download/v" + p.Version + "/" + r.slug + "-" + p.Version + "-" + t.String() + ".tar.gz"
		if p.Version == "" || p.Version == r.version || p.URL != want {
			fail("previous[%d] is %s of %q, not an earlier archive of %s", i, p.URL, p.Version, t)
		}
	}
	// install.sh reads these four fields line by line, as mygo writes
	// them; a manifest written another way installs nothing.
	data, err := os.ReadFile(path)
	if err != nil {
		return append(errs, err)
	}
	for field, want := range map[string]string{
		"version":   m.Version,
		"url":       m.URL,
		"size":      fmt.Sprint(m.Size),
		"signature": m.Signature,
	} {
		if got, ok := shellField(data, field); !ok || got != want {
			fail("install.sh would read its %s as %q", field, got)
		}
	}
	return errs
}

// shellField is install.sh's field function: the first line that is the
// field at the manifest's top level, two spaces in.
func shellField(data []byte, field string) (string, bool) {
	re := regexp.MustCompile(`^  "` + regexp.QuoteMeta(field) + `": (?:"([^"]*)"|([0-9]+)),?$`)
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		if m := re.FindStringSubmatch(sc.Text()); m != nil {
			return m[1] + m[2], true
		}
	}
	return "", false
}

func readManifest(path string) (manifest, error) {
	var m manifest
	data, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return m, err
	}
	if dec.More() {
		return m, errors.New("more than one manifest")
	}
	return m, nil
}

// readChecksums reads a file in sha256sum's format, name to checksum.
func readChecksums(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	line := regexp.MustCompile(`^([0-9a-f]{64}) [ *](.+)$`)
	sums := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		m := line.FindStringSubmatch(sc.Text())
		if m == nil {
			return nil, fmt.Errorf("%s: %q is not a checksum line", checksums, sc.Text())
		}
		if _, dup := sums[m[2]]; dup {
			return nil, fmt.Errorf("%s lists %s twice", checksums, m[2])
		}
		sums[m[2]] = m[1]
	}
	return sums, sc.Err()
}

func isManifest(name string) bool {
	return strings.HasPrefix(name, "update-") && strings.HasSuffix(name, ".json")
}

func sorted[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// config is what this reads of mygo.json.
type config struct {
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	Updates struct {
		PublicKey string `json:"publicKey"`
		GitHub    string `json:"github"`
	} `json:"updates"`
}

func load(path, tag string) (release, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return release{}, err
	}
	var c config
	if err := json.Unmarshal(data, &c); err != nil {
		return release{}, fmt.Errorf("%s: %w", path, err)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(c.Updates.PublicKey))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return release{}, fmt.Errorf("%s: updates.publicKey is not an Ed25519 public key", path)
	}
	version, ok := strings.CutPrefix(tag, "v")
	if !ok || version == "" {
		return release{}, fmt.Errorf("%s is not a tag vX.Y.Z", tag)
	}
	slug := c.Slug
	if slug == "" {
		slug = strings.ToLower(c.Name)
	}
	return release{slug: slug, repo: c.Updates.GitHub, version: version, key: ed25519.PublicKey(key)}, nil
}

// verifyBuild checks that the mygo build in dir made every package of
// the platforms given ("goos/goarch,..."), each in dir/<goos>-<goarch>
// but the AppImage, which scripts/appimage.sh writes into dir. The update
// archives are left to the check of the release: a build without the
// signing key, as a dry run, makes those of Linux alone.
func verifyBuild(dir, platforms string, r release) []error {
	var errs []error
	for _, p := range strings.Split(platforms, ",") {
		goos, goarch, _ := strings.Cut(strings.TrimSpace(p), "/")
		t := target{goos, goarch}
		if !slices.Contains(targets, t) {
			errs = append(errs, fmt.Errorf("%s is not a platform the release has", p))
			continue
		}
		for _, name := range r.files(t) {
			if name == r.archive(t) {
				continue
			}
			path := filepath.Join(dir, t.String(), name)
			if strings.HasSuffix(name, ".AppImage") {
				path = filepath.Join(dir, name)
			}
			if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
				errs = append(errs, fmt.Errorf("the build did not make %s", path))
			}
		}
	}
	return errs
}

func main() { os.Exit(run()) }

func run() int {
	dir := flag.String("dir", "", "the release's files, already downloaded; without it, gh downloads them")
	cfg := flag.String("config", "mygo.json", "mygo's configuration, for the repository and the public key")
	build := flag.String("build", "", "check instead that the build in this folder made every package of -platforms")
	platforms := flag.String("platforms", "", "with -build, the platforms built, as goos/goarch,...")
	flag.Parse()
	if flag.NArg() != 1 || (*build == "") != (*platforms == "") {
		fmt.Fprintln(os.Stderr, "usage: verifyrelease [-dir folder | -build folder -platforms goos/goarch,...] [-config mygo.json] vX.Y.Z")
		return 2
	}
	tag := flag.Arg(0)
	r, err := load(*cfg, tag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if *build != "" {
		errs := verifyBuild(*build, *platforms, r)
		for _, err := range errs {
			fmt.Fprintln(os.Stderr, err)
		}
		if len(errs) > 0 {
			return 1
		}
		fmt.Printf("%s: the build made every package of %s\n", tag, *platforms)
		return 0
	}
	var errs []error
	if *dir == "" {
		tmp, err := os.MkdirTemp("", "verifyrelease-")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		defer os.RemoveAll(tmp)
		*dir = tmp
		if err := gh(io.Discard, "release", "download", tag, "--repo", r.repo, "--dir", tmp); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		errs = append(errs, checkPublished(r, tag)...)
	}
	errs = append(errs, verify(*dir, r)...)
	for _, err := range errs {
		fmt.Fprintln(os.Stderr, err)
	}
	if len(errs) > 0 {
		fmt.Fprintf(os.Stderr, "%s: %d problems\n", tag, len(errs))
		return 1
	}
	n, _ := os.ReadDir(*dir)
	fmt.Printf("%s: %d files for %d platforms; checksums, manifests and signatures verified\n", tag, len(n), len(targets))
	return 0
}

// checkPublished checks the release's state on GitHub: a published
// pre-release version must be a prerelease, never the latest, which
// installed apps update to.
func checkPublished(r release, tag string) []error {
	var out bytes.Buffer
	if err := gh(&out, "release", "view", tag, "--repo", r.repo, "--json", "isDraft,isPrerelease"); err != nil {
		return []error{err}
	}
	var state struct{ IsDraft, IsPrerelease bool }
	if err := json.Unmarshal(out.Bytes(), &state); err != nil {
		return []error{err}
	}
	core, _, _ := strings.Cut(r.version, "+")
	if pre := strings.Contains(core, "-"); !state.IsDraft && state.IsPrerelease != pre {
		return []error{fmt.Errorf("%s is published with prerelease %v, but its version says %v", tag, state.IsPrerelease, pre)}
	}
	return nil
}

func gh(out io.Writer, args ...string) error {
	cmd := exec.Command("gh", args...)
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = out, &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh %s: %v\n%s", strings.Join(args[:2], " "), err, stderr.Bytes())
	}
	return nil
}
