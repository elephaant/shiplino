package cli

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/elephaant/shiplino/internal/config"
	"github.com/elephaant/shiplino/internal/update"
)

// releaseServer serves one release, v0.2.0, whose archive holds bin.
func releaseServer(t *testing.T, bin []byte) *httptest.Server {
	t.Helper()
	name := "shiplino"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	var buf bytes.Buffer
	if runtime.GOOS == "windows" {
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create(name)
		w.Write(bin)
		zw.Close()
	} else {
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(bin)), Typeflag: tar.TypeReg})
		tw.Write(bin)
		tw.Close()
		gz.Close()
	}
	archive := update.ArchiveName(update.Version{Minor: 2}, runtime.GOOS, runtime.GOARCH)
	h := sha256.Sum256(buf.Bytes())
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/x/releases":
			json.NewEncoder(w).Encode([]map[string]any{{"tag_name": "v0.2.0", "html_url": "https://example.test/v0.2.0", "assets": []map[string]string{
				{"name": archive, "browser_download_url": srv.URL + "/dl/" + archive},
				{"name": "checksums.txt", "browser_download_url": srv.URL + "/dl/checksums.txt"},
				{"name": "checksums.txt.sigstore.json", "browser_download_url": srv.URL + "/dl/bundle"},
			}}})
		case "/dl/" + archive:
			w.Write(buf.Bytes())
		case "/dl/checksums.txt":
			fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(h[:]), archive)
		case "/dl/bundle":
			fmt.Fprint(w, "{}")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func updateEnv(t *testing.T) (*env, *bytes.Buffer) {
	t.Helper()
	e, out := testEnv(t)
	bin, err := os.ReadFile(e.self)
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Dir(e.binPath()), 0o700)
	if err := os.WriteFile(e.binPath(), bin, 0o755); err != nil {
		t.Fatal(err)
	}
	srv := releaseServer(t, bin)
	e.version = "0.1.0"
	// No service manager: every command "fails", so nothing is installed.
	e.svcRun = func(context.Context, string, ...string) ([]byte, error) { return nil, errors.New("not here") }
	e.upd = &update.Updater{
		Source: update.Source{API: srv.URL + "/repos/x", HTTP: srv.Client()},
		Verify: func(context.Context, string, string, string) error { return nil },
		// Both binaries are the test build; the kept one plays 0.1.0.
		Smoke: func(_ context.Context, bin string) (string, error) {
			if strings.Contains(filepath.Base(bin), "previous") {
				return "0.1.0", nil
			}
			return "0.2.0", nil
		},
	}
	return e, out
}

func TestUpdateCommand(t *testing.T) {
	e, out := updateEnv(t)
	ctx := context.Background()

	if code := updateCmd(ctx, e, []string{"--check"}); code != 0 || !strings.Contains(out.String(), "0.2.0 is available (you have 0.1.0)") {
		t.Fatalf("--check exit %d:\n%s", code, out)
	}
	if _, err := os.Stat(update.PreviousPath(e.binPath())); err == nil {
		t.Fatal("--check installed something")
	}
	if h := updateHint(e); !strings.Contains(h, "0.2.0 is available") {
		t.Fatalf("status hint %q", h)
	}

	out.Reset()
	if code := updateCmd(ctx, e, nil); code != 0 {
		t.Fatalf("update exit %d:\n%s", code, out)
	}
	for _, want := range []string{"Updating Shiplino 0.1.0 → 0.2.0", "Checksum verified", "Signature: verified", "Hook test", "Updated to 0.2.0", "--rollback"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(update.PreviousPath(e.binPath())); err != nil {
		t.Fatal("previous binary not kept")
	}

	// Now on 0.2.0: nothing newer, and never a downgrade.
	e.version = "0.2.0"
	out.Reset()
	if code := updateCmd(ctx, e, nil); code != 0 || !strings.Contains(out.String(), "is up to date") {
		t.Fatalf("second update exit %d:\n%s", code, out)
	}

	out.Reset()
	if code := updateCmd(ctx, e, []string{"--rollback"}); code != 0 || !strings.Contains(out.String(), "Rolled back to 0.1.0") {
		t.Fatalf("rollback exit %d:\n%s", code, out)
	}
	if st := update.LoadState(e.home); st.SkipAuto != "0.2.0" {
		t.Fatalf("rollback didn't stop auto_install from reinstalling: %+v", st)
	}
}

func TestUpdateRefuses(t *testing.T) {
	for _, tc := range []struct {
		name, version, self string
		args                []string
		want                string
		code                int
	}{
		{name: "dev build", version: "0.0.0-dev", want: "development build", code: 1},
		{name: "homebrew", version: "0.1.0", self: "/opt/homebrew/bin/shiplino", want: "brew upgrade shiplino", code: 1},
		{name: "bad flag", version: "0.1.0", args: []string{"--yolo"}, want: "unknown argument", code: 2},
		{name: "bad channel", version: "0.1.0", args: []string{"--channel", "nightly"}, want: "unknown update channel", code: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, out := updateEnv(t)
			e.version = tc.version
			if tc.self != "" {
				e.self = tc.self
			}
			before, _ := os.ReadFile(e.binPath())
			if code := updateCmd(context.Background(), e, tc.args); code != tc.code || !strings.Contains(out.String(), tc.want) {
				t.Fatalf("exit %d, want %d with %q:\n%s", code, tc.code, tc.want, out)
			}
			if after, _ := os.ReadFile(e.binPath()); !bytes.Equal(before, after) {
				t.Fatal("binary changed")
			}
		})
	}
}

func TestUpdateDoctorCheck(t *testing.T) {
	e, _ := updateEnv(t)
	c := updateChecks(e, config.Config{})[0]
	if !c.ok || c.warn || !strings.Contains(c.detail, "automatic checks off") {
		t.Fatalf("off: %+v", c)
	}
	update.SaveState(e.home, update.State{Latest: "0.2.0"})
	c = updateChecks(e, config.Config{Update: config.Update{Check: true}})[0]
	if !c.warn || !strings.Contains(c.detail, "0.2.0 is available") || c.fixHint != "shiplino update" {
		t.Fatalf("available: %+v", c)
	}
	update.SaveState(e.home, update.State{Error: "no network"})
	c = updateChecks(e, config.Config{Update: config.Update{Check: true}})[0]
	if !c.warn || !strings.Contains(c.detail, "no network") {
		t.Fatalf("failed check: %+v", c)
	}
}
