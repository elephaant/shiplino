package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// entry is one file in a fake release archive.
type entry struct {
	name string
	body string
	typ  byte // tar type; 0 = regular
}

func tarGz(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.name, Mode: 0o755, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if e.typ != 0 {
			h.Typeflag, h.Size, h.Linkname = e.typ, 0, "/etc/passwd"
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if e.typ == 0 {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func zipped(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(e.body))
	}
	zw.Close()
	return buf.Bytes()
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// fakeRelease is one release on the fake server.
type fakeRelease struct {
	tag        string
	prerelease bool
	draft      bool
	files      map[string][]byte // asset name → content
	sums       string            // checksums.txt; "" = computed from files
}

// server is a fake GitHub API plus download host, over TLS.
type server struct {
	*httptest.Server
	releases []fakeRelease
}

func newServer(t *testing.T, releases ...fakeRelease) *server {
	t.Helper()
	s := &server{releases: releases}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/x/releases" {
			var list []map[string]any
			for _, rel := range s.releases {
				var assets []map[string]string
				for name := range rel.files {
					assets = append(assets, map[string]string{"name": name, "browser_download_url": s.URL + "/dl/" + rel.tag + "/" + name})
				}
				assets = append(assets, map[string]string{"name": "checksums.txt", "browser_download_url": s.URL + "/dl/" + rel.tag + "/checksums.txt"})
				list = append(list, map[string]any{"tag_name": rel.tag, "draft": rel.draft, "prerelease": rel.prerelease, "html_url": "https://example.test/" + rel.tag, "assets": assets})
			}
			json.NewEncoder(w).Encode(list)
			return
		}
		for _, rel := range s.releases {
			name, ok := strings.CutPrefix(r.URL.Path, "/dl/"+rel.tag+"/")
			if !ok {
				continue
			}
			if name == "checksums.txt" {
				if rel.sums != "" {
					fmt.Fprint(w, rel.sums)
					return
				}
				for n, b := range rel.files {
					fmt.Fprintf(w, "%s  %s\n", sum(b), n)
				}
				return
			}
			if b, ok := rel.files[name]; ok {
				w.Write(b)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) source() Source {
	return Source{API: s.URL + "/repos/x", HTTP: s.Client()}
}

// release builds a normal release whose archive holds a binary with the
// given content, for linux/amd64 and windows/amd64.
func release(t *testing.T, tag, binary string) fakeRelease {
	t.Helper()
	v, _ := ParseVersion(tag)
	return fakeRelease{tag: tag, prerelease: v.Prerelease(), files: map[string][]byte{
		ArchiveName(v, "linux", "amd64"):   tarGz(t, []entry{{name: "LICENSE", body: "x"}, {name: "shiplino", body: binary}}),
		ArchiveName(v, "windows", "amd64"): zipped(t, []entry{{name: "LICENSE", body: "x"}, {name: "shiplino.exe", body: binary}}),
		"checksums.txt.sigstore.json":      []byte(`{"fake":"bundle"}`),
	}}
}

// installed makes a fake installed binary and returns an updater for it.
func installed(t *testing.T, s *server, goos, current, content string) *Updater {
	t.Helper()
	name := "shiplino"
	if goos == "windows" {
		name += ".exe"
	}
	target := filepath.Join(t.TempDir(), "bin", name)
	os.MkdirAll(filepath.Dir(target), 0o700)
	if err := os.WriteFile(target, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Updater{
		Source: s.source(), Target: target, Current: current, GOOS: goos, GOARCH: "amd64",
		Verify: func(ctx context.Context, sums, bundle, identity string) error {
			if _, err := os.Stat(bundle); err != nil {
				return err
			}
			if !strings.HasPrefix(identity, identityPrefix+"v") {
				return fmt.Errorf("identity %q", identity)
			}
			return nil
		},
		// The fake binaries are text: "version" prints the file's own
		// version line.
		Smoke: func(ctx context.Context, bin string) (string, error) {
			b, err := os.ReadFile(bin)
			if err != nil {
				return "", err
			}
			v, ok := strings.CutPrefix(string(b), "fake shiplino ")
			if !ok {
				return "", fmt.Errorf("exec format error")
			}
			return v, nil
		},
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
