package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/elephaant/shiplino/internal/config"
	cloudsync "github.com/elephaant/shiplino/internal/sync"
)

// fakeSync answers the sign-in part of the sync protocol.
func fakeSync(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/device/code":
			fmt.Fprint(w, `{"device_code":"d","user_code":"ABCD-EFGH","verification_uri":"http://127.0.0.1/device","verification_uri_complete":"http://127.0.0.1/device?c=ABCD-EFGH","interval":1,"expires_in":60}`)
		case "/v1/device/token":
			fmt.Fprint(w, `{"access_token":"tok-a","refresh_token":"tok-r","expires_in":3600,"workspace_id":"ws_1","workspace_name":"Acme"}`)
		case "/v1/me":
			fmt.Fprint(w, `{"user":{"email":"dev@example.com"},"workspace":{"id":"ws_1","role":"member"},"device":{}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func syncEnv(t *testing.T) (*env, *bytes.Buffer, *[]string) {
	t.Helper()
	keyring.MockInit()
	user := t.TempDir()
	var out bytes.Buffer
	var opened []string
	e := &env{out: &out, errOut: &out, home: filepath.Join(user, ".shiplino"), userHome: user, version: "test",
		openURL: func(u string) error { opened = append(opened, u); return nil }}
	return e, &out, &opened
}

func run(t *testing.T, e *env, out *bytes.Buffer, want int, args ...string) string {
	t.Helper()
	out.Reset()
	if code := syncCmd(context.Background(), e, args); code != want {
		t.Fatalf("sync %v: exit %d, want %d\n%s", args, code, want, out)
	}
	return out.String()
}

func TestSyncCommands(t *testing.T) {
	e, out, opened := syncEnv(t)
	srv := fakeSync(t)

	got := run(t, e, out, 0, "login", "--endpoint", srv.URL)
	for _, want := range []string{"ABCD-EFGH", "Signed in as dev@example.com to workspace Acme (role: member)", "OS keychain", "Nothing is sent until you allow a project"} {
		if !strings.Contains(got, want) {
			t.Errorf("login output lacks %q:\n%s", want, got)
		}
	}
	if len(*opened) != 1 || !strings.HasSuffix((*opened)[0], "?c=ABCD-EFGH") {
		t.Errorf("browser: %v", *opened)
	}
	cfg, err := config.Load(e.home)
	if err != nil || !cfg.Sync.Enabled || cfg.SyncEndpoint() != srv.URL {
		t.Fatalf("config after login: %+v %v", cfg.Sync, err)
	}

	run(t, e, out, 0, "allow", "example.com/acme/api")
	run(t, e, out, 0, "allow", "example.com/acme/api") // idempotent
	cfg, _ = config.Load(e.home)
	if len(cfg.Sync.Projects) != 1 || cfg.Sync.Projects[0] != "example.com/acme/api" {
		t.Fatalf("projects: %v", cfg.Sync.Projects)
	}

	got = run(t, e, out, 0, "status")
	for _, want := range []string{"Sync is on", "dev@example.com", "workspace Acme", "example.com/acme/api", "metadata only; paths relative to the project", "daemon isn't running"} {
		if !strings.Contains(got, want) {
			t.Errorf("status lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "tok-a") || strings.Contains(got, "tok-r") {
		t.Fatalf("status prints a token:\n%s", got)
	}

	got = run(t, e, out, 0, "status", "--dry-run")
	var b cloudsync.Batch
	if err := json.Unmarshal([]byte(got[:strings.LastIndex(got, "}")+1]), &b); err != nil || b.DeviceID == "" || !strings.Contains(got, "Nothing was sent") {
		t.Fatalf("dry run: %v\n%s", err, got)
	}

	// deny removes an allowed id, and excludes what a glob would still match.
	run(t, e, out, 0, "deny", "example.com/acme/api")
	run(t, e, out, 0, "allow", "example.com/acme/*")
	run(t, e, out, 0, "deny", "example.com/acme/secret")
	cfg, _ = config.Load(e.home)
	if len(cfg.Sync.Projects) != 1 || cfg.Sync.Projects[0] != "example.com/acme/*" || len(cfg.Sync.Exclude) != 1 || cfg.Sync.Exclude[0] != "example.com/acme/secret" {
		t.Fatalf("after deny: %+v", cfg.Sync)
	}
	run(t, e, out, 0, "allow", "example.com/acme/secret")
	if cfg, _ = config.Load(e.home); len(cfg.Sync.Exclude) != 0 {
		t.Fatalf("allow didn't lift the exclusion: %+v", cfg.Sync)
	}

	run(t, e, out, 0, "logout")
	if c, _, _, _ := (cloudsync.Vault{Home: e.home}).Load(); c != nil {
		t.Fatal("still signed in")
	}
	if cfg, _ = config.Load(e.home); cfg.Sync.Enabled {
		t.Fatal("sync still on after logout")
	}
	if got := run(t, e, out, 0, "status"); !strings.Contains(got, "nothing leaves this machine") {
		t.Fatalf("status after logout:\n%s", got)
	}
	run(t, e, out, 2, "bogus")

	// The service signed the device out (refresh refused): status and
	// doctor say so.
	run(t, e, out, 0, "login", "--endpoint", srv.URL)
	(cloudsync.Vault{Home: e.home}).Delete()
	os.WriteFile(filepath.Join(e.home, "sync-signed-out"), []byte("the sync service signed this device out: run `shiplino sync login`"), 0o600)
	if got := run(t, e, out, 0, "status"); !strings.Contains(got, "Signed out by the sync service") {
		t.Fatalf("status after a server sign-out:\n%s", got)
	}
	if c := syncChecks(e); len(c) != 1 || c[0].ok || !strings.Contains(c[0].detail, "signed out") {
		t.Fatalf("doctor: %+v", c)
	}
}

func TestSyncLoginRejectsPlainHTTP(t *testing.T) {
	e, out, _ := syncEnv(t)
	if got := run(t, e, out, 2, "login", "--endpoint", "http://sync.example.com"); !strings.Contains(got, "https") {
		t.Fatalf("output:\n%s", got)
	}
}

func TestDoctorShowsSyncCredentialFallback(t *testing.T) {
	e, out, _ := syncEnv(t)
	srv := fakeSync(t)
	keyring.MockInitWithError(errors.New("no secret service"))
	defer keyring.MockInit()
	got := run(t, e, out, 0, "login", "--endpoint", srv.URL)
	if !strings.Contains(got, "sync-credentials.json") || !strings.Contains(got, "no secret service") {
		t.Fatalf("login didn't say where the credentials went:\n%s", got)
	}
	var names []string
	for _, c := range syncChecks(e) {
		names = append(names, c.name+": "+c.detail)
		if c.name == "Sync creds" && (!c.warn || !strings.Contains(c.detail, "no secret service")) {
			t.Fatalf("creds check: %+v", c)
		}
	}
	if !strings.Contains(strings.Join(names, "\n"), "no project is allowed") {
		t.Fatalf("checks: %v", names)
	}
}
