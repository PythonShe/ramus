package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeRelease writes a whole release of version 1.0.0, with a delta from
// 0.9.0 for each platform, signed with a key of its own, as mygo build and
// the release workflow make one.
func fakeRelease(t *testing.T) (string, release, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	r := release{slug: "ramus", repo: "owner/ramus", version: "1.0.0", key: pub}
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{installSh, installPS} {
		write(name, "script "+name)
	}
	for _, tg := range targets {
		for _, name := range r.files(tg) {
			write(name, "package "+name)
		}
		write(r.delta("0.9.0", tg), "delta "+tg.String())
		writeManifest(t, dir, tg, r, priv)
	}
	writeSums(t, dir)
	return dir, r, priv
}

func writeManifest(t *testing.T, dir string, tg target, r release, priv ed25519.PrivateKey) {
	t.Helper()
	signed := func(name string) (int64, string) {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		return int64(len(data)), base64.StdEncoding.EncodeToString(ed25519.Sign(priv, sum[:]))
	}
	m := manifest{Version: r.version, Date: "2026-11-19T10:00:00Z", Notes: "Notes."}
	m.URL = r.url(r.archive(tg))
	m.Size, m.Signature = signed(r.archive(tg))
	d := delta{From: "0.9.0", URL: r.url(r.delta("0.9.0", tg))}
	d.Size, d.Signature = signed(r.delta("0.9.0", tg))
	m.Deltas = []delta{d}
	m.Previous = []archive{{Version: "0.9.0", URL: "https://github.com/owner/ramus/releases/download/v0.9.0/ramus-0.9.0-" + tg.String() + ".tar.gz", Size: 1, Signature: "x"}}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "update-"+tg.String()+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeSums(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, e := range entries {
		if e.Name() == checksums || isManifest(e.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		lines = append(lines, hex.EncodeToString(sum[:])+"  "+e.Name())
	}
	slices.Sort(lines)
	if err := os.WriteFile(filepath.Join(dir, checksums), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestAWholeReleaseVerifies(t *testing.T) {
	dir, r, _ := fakeRelease(t)
	if errs := verify(dir, r); len(errs) > 0 {
		t.Fatalf("a whole release fails: %v", errs)
	}
}

func TestVerifyFindsWhatIsWrongWithARelease(t *testing.T) {
	edit := func(t *testing.T, dir, name string, f func(*manifest)) {
		path := filepath.Join(dir, name)
		m, err := readManifest(path)
		if err != nil {
			t.Fatal(err)
		}
		f(&m)
		data, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name  string
		spoil func(t *testing.T, dir string, r release, priv ed25519.PrivateKey)
		want  string
	}{
		{"a Windows installer missing, in SHA256SUMS or not", func(t *testing.T, dir string, r release, _ ed25519.PrivateKey) {
			os.Remove(filepath.Join(dir, "ramus-setup-1.0.0-arm64.exe"))
			writeSums(t, dir)
		}, "ramus-setup-1.0.0-arm64.exe is missing"},
		{"an installer renamed", func(t *testing.T, dir string, r release, _ ed25519.PrivateKey) {
			os.Rename(filepath.Join(dir, "ramus-setup-1.0.0-amd64.exe"), filepath.Join(dir, "ramus-1.0.0-windows-x86_64-setup.exe"))
			writeSums(t, dir)
		}, "ramus-1.0.0-windows-x86_64-setup.exe is not a file this release should have"},
		{"a manifest missing", func(t *testing.T, dir string, r release, _ ed25519.PrivateKey) {
			os.Remove(filepath.Join(dir, "update-linux-arm64.json"))
		}, "update-linux-arm64.json is missing"},
		{"a manifest of another version", func(t *testing.T, dir string, r release, _ ed25519.PrivateKey) {
			edit(t, dir, "update-darwin-arm64.json", func(m *manifest) { m.Version = "9.0.0" })
		}, `update-darwin-arm64.json: version "9.0.0", not "1.0.0"`},
		{"a manifest naming another architecture's archive, signed", func(t *testing.T, dir string, r release, _ ed25519.PrivateKey) {
			amd, err := readManifest(filepath.Join(dir, "update-linux-amd64.json"))
			if err != nil {
				t.Fatal(err)
			}
			edit(t, dir, "update-linux-arm64.json", func(m *manifest) { m.URL, m.Size, m.Signature = amd.URL, amd.Size, amd.Signature })
		}, "update-linux-arm64.json: url is https://github.com/owner/ramus/releases/download/v1.0.0/ramus-1.0.0-linux-amd64.tar.gz, not"},
		{"a manifest naming a file the release lacks", func(t *testing.T, dir string, r release, _ ed25519.PrivateKey) {
			os.Remove(filepath.Join(dir, r.delta("0.9.0", target{"windows", "amd64"})))
			writeSums(t, dir)
		}, "update-windows-amd64.json: deltas[0].url names ramus-0.9.0-to-1.0.0-windows-amd64.delta, which the release does not have"},
		{"a size that is not the file's", func(t *testing.T, dir string, r release, _ ed25519.PrivateKey) {
			edit(t, dir, "update-windows-arm64.json", func(m *manifest) { m.Size++ })
		}, "update-windows-arm64.json: url's size is"},
		{"a signature by another key", func(t *testing.T, dir string, r release, _ ed25519.PrivateKey) {
			_, other, _ := ed25519.GenerateKey(nil)
			writeManifest(t, dir, target{"darwin", "amd64"}, r, other)
		}, "update-darwin-amd64.json: url's signature is not that of ramus-1.0.0-darwin-amd64.tar.gz"},
		{"an archive changed after it was signed", func(t *testing.T, dir string, r release, _ ed25519.PrivateKey) {
			os.WriteFile(filepath.Join(dir, "ramus-1.0.0-linux-amd64.tar.gz"), []byte("other"), 0o644)
			writeSums(t, dir)
		}, "update-linux-amd64.json: url's size is"},
		{"a file whose checksum is not SHA256SUMS's", func(t *testing.T, dir string, r release, _ ed25519.PrivateKey) {
			os.WriteFile(filepath.Join(dir, "ramus-1.0.0-arm64.dmg"), []byte("other"), 0o644)
		}, "ramus-1.0.0-arm64.dmg does not match its checksum in SHA256SUMS"},
		{"a file SHA256SUMS does not list, as when two builds raced on it", func(t *testing.T, dir string, r release, _ ed25519.PrivateKey) {
			data, _ := os.ReadFile(filepath.Join(dir, checksums))
			var kept []string
			for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				if !strings.Contains(l, "darwin") && !strings.HasSuffix(l, ".dmg") {
					kept = append(kept, l)
				}
			}
			os.WriteFile(filepath.Join(dir, checksums), []byte(strings.Join(kept, "\n")+"\n"), 0o644)
		}, "SHA256SUMS does not list ramus-1.0.0-amd64.dmg"},
		{"SHA256SUMS listing a file the release lacks", func(t *testing.T, dir string, r release, _ ed25519.PrivateKey) {
			f, _ := os.OpenFile(filepath.Join(dir, checksums), os.O_APPEND|os.O_WRONLY, 0)
			fmt.Fprintf(f, "%064x  ramus-0.9.0-arm64.dmg\n", 0)
			f.Close()
		}, "SHA256SUMS lists ramus-0.9.0-arm64.dmg, which the release does not have"},
		{"a manifest install.sh cannot read", func(t *testing.T, dir string, r release, _ ed25519.PrivateKey) {
			m, _ := readManifest(filepath.Join(dir, "update-darwin-arm64.json"))
			data, _ := json.Marshal(m)
			os.WriteFile(filepath.Join(dir, "update-darwin-arm64.json"), data, 0o644)
		}, "update-darwin-arm64.json: install.sh would read its"},
		{"a manifest with a field this does not check", func(t *testing.T, dir string, r release, _ ed25519.PrivateKey) {
			path := filepath.Join(dir, "update-darwin-arm64.json")
			data, _ := os.ReadFile(path)
			os.WriteFile(path, []byte(strings.Replace(string(data), "{", "{\n  \"extra\": 1,", 1)), 0o644)
		}, `update-darwin-arm64.json: json: unknown field "extra"`},
		{"a delta no manifest names", func(t *testing.T, dir string, r release, _ ed25519.PrivateKey) {
			os.WriteFile(filepath.Join(dir, r.delta("0.8.0", target{"linux", "amd64"})), []byte("d"), 0o644)
			writeSums(t, dir)
		}, "ramus-0.8.0-to-1.0.0-linux-amd64.delta is not a file this release should have"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir, r, priv := fakeRelease(t)
			c.spoil(t, dir, r, priv)
			errs := verify(dir, r)
			var msgs []string
			for _, err := range errs {
				msgs = append(msgs, err.Error())
			}
			if !slices.ContainsFunc(msgs, func(m string) bool { return strings.Contains(m, c.want) }) {
				t.Fatalf("want an error with %q, got:\n%s", c.want, strings.Join(msgs, "\n"))
			}
		})
	}
}

func TestPackageNamesOfAPreRelease(t *testing.T) {
	r := release{slug: "ramus", version: "1.0.0-rc.1"}
	got := r.files(target{"linux", "amd64"})
	for _, want := range []string{"ramus_1.0.0~rc.1_amd64.deb", "ramus-1.0.0~rc.1-1.x86_64.rpm", "ramus-1.0.0-rc.1-linux-x86_64.AppImage"} {
		if !slices.Contains(got, want) {
			t.Errorf("%s missing from %v", want, got)
		}
	}
}

func TestABuildWithoutItsInstallerFails(t *testing.T) {
	r := release{slug: "ramus", version: "1.0.0"}
	dir := t.TempDir()
	platforms := "linux/amd64,linux/arm64,windows/amd64,windows/arm64"
	for _, p := range strings.Split(platforms, ",") {
		goos, goarch, _ := strings.Cut(p, "/")
		tg := target{goos, goarch}
		if err := os.MkdirAll(filepath.Join(dir, tg.String()), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, name := range r.files(tg) {
			path := filepath.Join(dir, tg.String(), name)
			if strings.HasSuffix(name, ".AppImage") {
				path = filepath.Join(dir, name)
			}
			if err := os.WriteFile(path, []byte(name), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	if errs := verifyBuild(dir, platforms, r); len(errs) > 0 {
		t.Fatalf("a whole build fails: %v", errs)
	}
	os.Remove(filepath.Join(dir, "windows-arm64", "ramus-setup-1.0.0-arm64.exe"))
	errs := verifyBuild(dir, platforms, r)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "ramus-setup-1.0.0-arm64.exe") {
		t.Fatalf("a build without its installer: %v", errs)
	}
}
