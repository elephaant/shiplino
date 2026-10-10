package sync

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/elephaant/shiplino/internal/config"
	"github.com/elephaant/shiplino/internal/store"
	"github.com/elephaant/shiplino/pkg/model"
)

var ctx = context.Background()

func TestMain(m *testing.M) {
	keyring.MockInit() // never touch the real keychain
	pollUnit = time.Millisecond
	os.Exit(m.Run())
}

// fakeServer implements the sync protocol in memory.
type fakeServer struct {
	t  *testing.T
	mu gosync.Mutex

	devicePolls []string // answers to /v1/device/token, in order ("" = success)
	pollTimes   []time.Time

	access      string // the currently valid access token
	refreshOK   bool
	refreshes   int
	maxPerReq   int  // > 0: 413 for bigger requests
	always413   bool // 413 for everything
	tooMany     int  // answer 429 this many times
	retryAfter  string
	requests    int
	gzipOK      bool
	events      map[string]map[string]any // by id
	order       []string
	dedupKeys   map[string]bool
	deviceIDs   map[string]bool
	unavailable bool
	forbidden   bool   // 403: the account's role can't sync
	malformed   bool   // 400: the body is malformed
	rejectKind  string // events of this kind fail the schema check
	deviceName  string // sent to /v1/device/code
}

func newFake(t *testing.T) (*fakeServer, *httptest.Server) {
	f := &fakeServer{t: t, access: "access-1", refreshOK: true, gzipOK: true, events: map[string]map[string]any{}, dedupKeys: map[string]bool{}, deviceIDs: map[string]bool{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprintf(w, `{"error":%q}`, msg)
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	token := func(access string) {
		json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": "refresh-" + access, "expires_in": 3600,
			"workspace_id": "ws_1", "workspace_name": "Acme"})
	}
	switch r.URL.Path {
	case "/v1/device/code":
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		f.deviceName = body["device_name"]
		json.NewEncoder(w).Encode(DeviceCode{DeviceCode: "dev-code", UserCode: "ABCD-EFGH", VerificationURI: "http://127.0.0.1/device",
			VerificationURIComplete: "http://127.0.0.1/device?code=ABCD-EFGH", Interval: 1, ExpiresIn: 2000})
	case "/v1/device/token":
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["device_code"] != "dev-code" {
			writeErr(w, 400, "invalid_grant")
			return
		}
		f.pollTimes = append(f.pollTimes, time.Now())
		next := ""
		if len(f.devicePolls) > 0 {
			next, f.devicePolls = f.devicePolls[0], f.devicePolls[1:]
		}
		switch next {
		case "":
			token(f.access)
		case "authorization_pending":
			writeErr(w, 428, next)
		case "slow_down":
			w.Header().Set("Retry-After", "20")
			writeErr(w, 429, next)
		default:
			writeErr(w, 400, next)
		}
	case "/v1/token/refresh":
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		f.refreshes++
		if !f.refreshOK || !strings.HasPrefix(body["refresh_token"], "refresh-") {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":"invalid_grant","message":"refresh token revoked"}`)
			return
		}
		f.access = fmt.Sprintf("access-%d", f.refreshes+1)
		token(f.access)
	case "/v1/me":
		if r.Header.Get("Authorization") != "Bearer "+f.access {
			writeErr(w, 401, "unauthorized")
			return
		}
		fmt.Fprint(w, `{"user":{"email":"dev@example.com"},"workspace":{"id":"ws_1","name":"Acme","role":"member"},"device":{"name":"dev-laptop"}}`)
	case "/v1/sync/events":
		f.requests++
		if f.unavailable {
			writeErr(w, 503, "unavailable")
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+f.access {
			writeErr(w, 401, "unauthorized")
			return
		}
		if f.forbidden {
			writeErr(w, 403, "forbidden")
			return
		}
		if f.malformed {
			writeErr(w, 400, "malformed")
			return
		}
		if f.tooMany > 0 {
			f.tooMany--
			w.Header().Set("Retry-After", f.retryAfter)
			writeErr(w, 429, "rate_limited")
			return
		}
		if r.Header.Get("Content-Encoding") != "gzip" {
			f.gzipOK = false
			writeErr(w, 400, "gzip required")
			return
		}
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			f.gzipOK = false
			writeErr(w, 400, "bad gzip")
			return
		}
		raw, _ := io.ReadAll(zr)
		if len(raw) > MaxBatchBytes {
			writeErr(w, 413, "too large")
			return
		}
		var b struct {
			DeviceID   string           `json:"device_id"`
			DeviceName string           `json:"device_name"`
			Events     []map[string]any `json:"events"`
		}
		if err := json.Unmarshal(raw, &b); err != nil || b.DeviceID == "" || b.DeviceName == "" {
			writeErr(w, 400, "bad body")
			return
		}
		if f.always413 || len(b.Events) > MaxBatchEvents || f.maxPerReq > 0 && len(b.Events) > f.maxPerReq {
			writeErr(w, 413, "too many events")
			return
		}
		f.deviceIDs[b.DeviceID] = true
		acc, dup, rej := 0, 0, 0
		for _, e := range b.Events {
			if e["kind"] == f.rejectKind {
				rej++
				continue
			}
			id, _ := e["id"].(string)
			key, _ := e["dedup_key"].(string)
			if _, ok := f.events[id]; ok || f.dedupKeys[key] {
				dup++
				continue
			}
			f.events[id], f.dedupKeys[key] = e, true
			f.order = append(f.order, id)
			acc++
		}
		json.NewEncoder(w).Encode(Result{Accepted: acc, Duplicates: dup, Rejected: rej})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeServer) received() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]map[string]any, len(f.order))
	for i, id := range f.order {
		out[i] = f.events[id]
	}
	return out
}

func TestDeviceFlow(t *testing.T) {
	for _, tc := range []struct {
		name    string
		polls   []string
		wantErr error
	}{
		{"approved after waiting", []string{"authorization_pending", "slow_down", "authorization_pending", ""}, nil},
		{"expired", []string{"authorization_pending", "expired_token"}, ErrExpired},
		{"denied", []string{"access_denied"}, ErrDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, srv := newFake(t)
			f.devicePolls = tc.polls
			var shown DeviceCode
			tok, err := Login(ctx, NewClient(srv.URL, "test"), func(d DeviceCode) { shown = d })
			if shown.UserCode != "ABCD-EFGH" {
				t.Fatalf("code not shown: %+v", shown)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr != nil {
				return
			}
			if tok.AccessToken != "access-1" || tok.WorkspaceID != "ws_1" || tok.WorkspaceName != "Acme" {
				t.Fatalf("token: %+v", tok)
			}
			// slow_down adds 5 s to the interval, or waits for its
			// Retry-After (20 s, here 20 ms) when that's longer.
			p := f.pollTimes
			if gap := p[2].Sub(p[1]); gap < 20*time.Millisecond {
				t.Fatalf("interval after slow_down = %v, want >= 20ms", gap)
			}
			if f.deviceName == "" {
				t.Fatal("device_name not sent with the device code request")
			}
		})
	}
}

// fixture is a Shiplino home with a store, config and signed-in creds.
type fixture struct {
	home string
	st   *store.Store
	u    *Uploader
	f    *fakeServer
	srv  *httptest.Server
	now  time.Time
}

func newFixture(t *testing.T, syncToml string) *fixture {
	t.Helper()
	keyring.MockInit()
	fx := &fixture{home: t.TempDir(), now: time.Now()}
	fx.f, fx.srv = newFake(t)
	st, err := store.Open(filepath.Join(fx.home, "data", "shiplino.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fx.st = st
	fx.writeConfig(t, syncToml)
	if _, _, err := (Vault{Home: fx.home}).Save(&Creds{Endpoint: fx.srv.URL, AccessToken: "access-1", RefreshToken: "refresh-access-1",
		ExpiresAt: fx.now.Add(time.Hour), WorkspaceID: "ws_1", WorkspaceName: "Acme"}); err != nil {
		t.Fatal(err)
	}
	fx.u = fx.uploader()
	return fx
}

func (fx *fixture) uploader() *Uploader {
	u := NewUploader(fx.home, fx.st, nil, "test")
	u.now = func() time.Time { return fx.now }
	return u
}

func (fx *fixture) writeConfig(t *testing.T, syncToml string) {
	t.Helper()
	body := strings.ReplaceAll(syncToml, "ENDPOINT", fx.srv.URL)
	if err := os.WriteFile(config.Path(fx.home), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

var n int

// add stores events for a project, like the daemon would.
func (fx *fixture) add(t *testing.T, project string, kind model.Kind, data map[string]any) model.Event {
	t.Helper()
	n++
	ts := time.Now()
	e := model.Event{ID: model.NewULID(ts), V: 1, TS: ts, Kind: kind, Agent: model.Agent{Name: "claude-code"}, Collector: model.CollectorHook,
		SessionID: "claude-code:s-" + project, DedupKey: fmt.Sprintf("k%d", n), Data: data, Raw: &model.RawRef{Ref: "spool/claude-code/s.jsonl:1"}}
	if project != "" {
		e.Project = &model.Project{ID: project, CWD: "/home/dev/" + project}
	}
	tx, err := fx.st.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.InsertEvent(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return e
}

func (fx *fixture) state(t *testing.T) store.SyncState {
	t.Helper()
	s, err := fx.st.SyncState(ctx, "ws_1")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const syncOn = `
[sync]
enabled = true
endpoint = "ENDPOINT"
projects = ["github.com/acme/*"]
exclude = ["github.com/acme/secret"]
`

func TestUploadsOnlyAllowedProjectsStrippedAndRedacted(t *testing.T) {
	fx := newFixture(t, "capture_level = \"full\"\n[redaction]\nextra_patterns = [\"ACME-[0-9]+\"]\n"+syncOn)
	const gh = "ghp_0123456789abcdefghijklmnopqrstuvwxyzAB"
	fx.add(t, "github.com/acme/api", model.KindTurnStart, map[string]any{"prompt": "fix ACME-42 with " + gh, "prompt_chars": 30})
	fx.add(t, "github.com/acme/api", model.KindShellExec, map[string]any{"command": "make test", "exit_code": 0})
	fx.add(t, "github.com/acme/secret", model.KindTurnStart, map[string]any{"prompt": "secret plans"})
	fx.add(t, "github.com/other/x", model.KindTurnStart, map[string]any{"prompt": "other"})
	fx.add(t, "", model.KindNote, map[string]any{"message": "no project"})
	fx.add(t, "github.com/acme/web", model.KindFileEdit, map[string]any{"path": "/home/dev/github.com/acme/web/src/a.go", "lines_added": 3})

	if wait := fx.u.Step(ctx); wait != uploadEvery {
		t.Fatalf("wait = %v", wait)
	}
	got := fx.f.received()
	if len(got) != 3 {
		t.Fatalf("sent %d events, want 3 (acme/api ×2, acme/web): %v", len(got), got)
	}
	for _, e := range got {
		p := e["project"].(map[string]any)["id"].(string)
		if p != "github.com/acme/api" && p != "github.com/acme/web" {
			t.Errorf("sent project %s", p)
		}
		if _, ok := e["raw"]; ok {
			t.Errorf("raw ref sent: %v", e)
		}
	}
	// Default sync level is minimal: no prompt or command, metadata kept.
	d0, d1, d2 := got[0]["data"].(map[string]any), got[1]["data"].(map[string]any), got[2]["data"].(map[string]any)
	if _, ok := d0["prompt"]; ok || d0["prompt_chars"] != float64(30) {
		t.Errorf("turn.start at minimal: %v", d0)
	}
	if _, ok := d1["command"]; ok || d1["exit_code"] != float64(0) {
		t.Errorf("shell.exec at minimal: %v", d1)
	}
	if d2["path"] != "src/a.go" {
		t.Errorf("file path: %v", d2)
	}
	st := fx.state(t)
	if st.Uploaded != 3 || st.LastUpload.IsZero() || st.LastError != "" {
		t.Fatalf("state: %+v", st)
	}

	// An old capture_level = "standard" is ignored: content never goes.
	fx2 := newFixture(t, "capture_level = \"full\"\n"+strings.Replace(syncOn, "[sync]", "[sync]\ncapture_level = \"standard\"\nsend_user = true", 1))
	fx2.add(t, "github.com/acme/api", model.KindTurnStart, map[string]any{"prompt": "fix the login bug", "prompt_chars": 17})
	fx2.u.Step(ctx)
	got = fx2.f.received()
	if len(got) != 1 {
		t.Fatalf("legacy level: %d events", len(got))
	}
	if d := got[0]["data"].(map[string]any); d["prompt"] != nil || d["prompt_chars"] != float64(17) || got[0]["user"] != nil {
		t.Fatalf("legacy level sent content: %v", got[0])
	}
}

func TestSyncIgnoredSettings(t *testing.T) {
	c := config.Config{Sync: config.Sync{CaptureLevel: "full", SendUser: true}}
	if n := c.SyncIgnored(); len(n) != 2 {
		t.Fatalf("notes: %v", n)
	}
	if n := (config.Config{Sync: config.Sync{CaptureLevel: "minimal"}}).SyncIgnored(); len(n) != 0 {
		t.Fatalf("minimal noted: %v", n)
	}
}

func TestNothingAllowedSendsNothing(t *testing.T) {
	fx := newFixture(t, "[sync]\nenabled = true\nendpoint = \"ENDPOINT\"\n")
	fx.add(t, "github.com/acme/api", model.KindTurnStart, map[string]any{"prompt": "x"})
	fx.u.Step(ctx)
	if fx.f.requests != 0 || fx.state(t).Cursor != 0 {
		t.Fatalf("requests=%d cursor=%d", fx.f.requests, fx.state(t).Cursor)
	}
	// Allowing a project later sends its history.
	fx.writeConfig(t, "[sync]\nenabled = true\nendpoint = \"ENDPOINT\"\nprojects = [\"github.com/acme/api\"]\n")
	fx.u.Step(ctx)
	if len(fx.f.received()) != 1 {
		t.Fatalf("after allow: %d events", len(fx.f.received()))
	}
}

func TestDisabledSendsNothing(t *testing.T) {
	fx := newFixture(t, strings.Replace(syncOn, "enabled = true", "enabled = false", 1))
	fx.add(t, "github.com/acme/api", model.KindTurnStart, map[string]any{"prompt": "x"})
	fx.u.Step(ctx)
	if fx.f.requests != 0 {
		t.Fatal("sent while disabled")
	}
	if v := fx.u.View(ctx); v.SignedIn || v.Enabled {
		t.Fatalf("view: %+v", v)
	}
}

func TestWideningAllowListRescansHistory(t *testing.T) {
	fx := newFixture(t, "[sync]\nenabled = true\nendpoint = \"ENDPOINT\"\nprojects = [\"github.com/acme/api\"]\n")
	fx.add(t, "github.com/acme/api", model.KindTurnStart, map[string]any{})
	fx.add(t, "github.com/acme/web", model.KindTurnStart, map[string]any{})
	fx.u.Step(ctx)
	if len(fx.f.received()) != 1 || fx.state(t).Cursor != 2 {
		t.Fatalf("first: %d events, cursor %d", len(fx.f.received()), fx.state(t).Cursor)
	}
	fx.writeConfig(t, "[sync]\nenabled = true\nendpoint = \"ENDPOINT\"\nprojects = [\"github.com/acme/api\", \"github.com/acme/web\"]\n")
	fx.u.Step(ctx)
	if got := fx.f.received(); len(got) != 2 || got[1]["project"].(map[string]any)["id"] != "github.com/acme/web" {
		t.Fatalf("after widening: %v", got)
	}
	if st := fx.state(t); st.Uploaded != 2 { // the resent duplicate is not counted again
		t.Fatalf("uploaded = %d", st.Uploaded)
	}
}

func TestRefreshOn401(t *testing.T) {
	fx := newFixture(t, syncOn)
	fx.add(t, "github.com/acme/api", model.KindTurnStart, map[string]any{})
	fx.f.access = "access-rotated" // the server no longer accepts access-1
	fx.u.Step(ctx)
	if fx.f.refreshes != 1 || len(fx.f.received()) != 1 {
		t.Fatalf("refreshes=%d events=%d", fx.f.refreshes, len(fx.f.received()))
	}
	c, _, _, _ := Vault{Home: fx.home}.Load()
	if c.AccessToken != fx.f.access || c.RefreshToken != "refresh-"+fx.f.access {
		t.Fatalf("refreshed creds not saved: %+v", c)
	}
}

func TestRefreshWhenExpired(t *testing.T) {
	fx := newFixture(t, syncOn)
	fx.add(t, "github.com/acme/api", model.KindTurnStart, map[string]any{})
	fx.now = fx.now.Add(2 * time.Hour) // past expires_at
	fx.f.access = "access-2"
	fx.u.Step(ctx)
	if fx.f.refreshes != 1 || len(fx.f.received()) != 1 || fx.f.requests != 1 {
		t.Fatalf("refreshes=%d events=%d requests=%d", fx.f.refreshes, len(fx.f.received()), fx.f.requests)
	}
}

func TestRefreshRejectedSignsOut(t *testing.T) {
	fx := newFixture(t, syncOn)
	fx.add(t, "github.com/acme/api", model.KindTurnStart, map[string]any{})
	fx.f.access, fx.f.refreshOK = "access-other", false // 400 invalid_grant
	fx.u.Step(ctx)
	vault := Vault{Home: fx.home}
	if c, _, _, _ := vault.Load(); c != nil {
		t.Fatal("tokens kept after the service signed the device out")
	}
	v := fx.u.View(ctx)
	if !v.NeedsLogin || v.SignedIn || !strings.Contains(v.LastError, "sync login") || fx.state(t).Cursor != 0 {
		t.Fatalf("view: %+v", v)
	}
	reqs := fx.f.requests
	fx.now = fx.now.Add(credsEvery)
	fx.u.Step(ctx)
	if fx.f.requests != reqs {
		t.Fatal("kept uploading after being signed out")
	}
	// Signing in again resumes, and clears the notice.
	fx.f.access = "access-new"
	vault.Save(&Creds{Endpoint: fx.srv.URL, AccessToken: "access-new", RefreshToken: "refresh-access-new", WorkspaceID: "ws_1"})
	fx.now = fx.now.Add(credsEvery)
	fx.u.Step(ctx)
	if len(fx.f.received()) != 1 || fx.u.View(ctx).NeedsLogin || vault.SignedOut() != "" {
		t.Fatalf("after login: %d events, %+v", len(fx.f.received()), fx.u.View(ctx))
	}
}

func TestForbiddenPausesUploads(t *testing.T) {
	fx := newFixture(t, syncOn)
	fx.add(t, "github.com/acme/api", model.KindTurnStart, map[string]any{})
	fx.f.forbidden = true
	if wait := fx.u.Step(ctx); wait != forbiddenWait {
		t.Fatalf("wait = %v", wait)
	}
	for range 3 {
		fx.now = fx.now.Add(time.Minute)
		fx.u.Step(ctx)
	}
	if fx.f.requests != 1 {
		t.Fatalf("%d requests while forbidden", fx.f.requests)
	}
	if v := fx.u.View(ctx); !v.Forbidden || !v.SignedIn || fx.state(t).Cursor != 0 {
		t.Fatalf("view: %+v", v)
	}
	// The role changed: the hourly check succeeds.
	fx.f.forbidden = false
	fx.now = fx.now.Add(forbiddenWait)
	fx.u.Step(ctx)
	if len(fx.f.received()) != 1 || fx.u.View(ctx).Forbidden {
		t.Fatalf("after role change: %d events", len(fx.f.received()))
	}
}

func TestMalformedBacksOff(t *testing.T) {
	fx := newFixture(t, syncOn)
	fx.add(t, "github.com/acme/api", model.KindTurnStart, map[string]any{})
	fx.f.malformed = true
	wait := fx.u.Step(ctx)
	if wait < uploadEvery/2 || !strings.Contains(fx.state(t).LastError, "malformed") {
		t.Fatalf("wait %v, state %+v", wait, fx.state(t))
	}
	fx.u.Step(ctx) // still backing off
	if fx.f.requests != 1 {
		t.Fatalf("%d requests: retried in a tight loop", fx.f.requests)
	}
}

func TestRejectedEventsArePassed(t *testing.T) {
	fx := newFixture(t, syncOn)
	fx.add(t, "github.com/acme/api", model.KindTurnStart, map[string]any{})
	fx.add(t, "github.com/acme/api", model.KindNote, map[string]any{})
	fx.add(t, "github.com/acme/api", model.KindTurnEnd, map[string]any{})
	fx.f.rejectKind = string(model.KindNote)
	fx.u.Step(ctx)
	st := fx.state(t)
	if st.Cursor != 3 || st.Uploaded != 2 || st.Rejected != 1 {
		t.Fatalf("state: %+v", st)
	}
	reqs := fx.f.requests
	fx.u.Step(ctx)
	if fx.f.requests != reqs || fx.u.View(ctx).Rejected != 1 {
		t.Fatal("rejected event retried")
	}
}

func TestSplitsOn413(t *testing.T) {
	fx := newFixture(t, syncOn)
	for range 7 {
		fx.add(t, "github.com/acme/api", model.KindToolStart, map[string]any{"tool": "edit"})
	}
	fx.f.maxPerReq = 2
	fx.u.Step(ctx)
	got := fx.f.received()
	if len(got) != 7 || fx.state(t).Cursor != 7 {
		t.Fatalf("sent %d, cursor %d", len(got), fx.state(t).Cursor)
	}
	for i := 1; i < len(got); i++ {
		if got[i]["id"].(string) < got[i-1]["id"].(string) {
			t.Fatal("events out of order")
		}
	}
	// A single event the service still refuses is skipped, not retried forever.
	fx.add(t, "github.com/acme/api", model.KindToolStart, map[string]any{"tool": "edit"})
	fx.f.always413 = true
	fx.u.Step(ctx)
	if len(fx.f.received()) != 7 || fx.state(t).Cursor != 8 {
		t.Fatalf("oversized event: sent %d, cursor %d", len(fx.f.received()), fx.state(t).Cursor)
	}
}

func TestHonorsRetryAfterThenBacksOff(t *testing.T) {
	fx := newFixture(t, syncOn)
	fx.add(t, "github.com/acme/api", model.KindTurnStart, map[string]any{})
	fx.f.tooMany, fx.f.retryAfter = 1, "7"
	if wait := fx.u.Step(ctx); wait != 7*time.Second {
		t.Fatalf("wait = %v, want Retry-After 7s", wait)
	}
	if fx.state(t).Cursor != 0 || !strings.Contains(fx.state(t).LastError, "429") {
		t.Fatalf("state: %+v", fx.state(t))
	}
	if wait := fx.u.Step(ctx); wait <= 0 || wait > 7*time.Second || fx.f.requests != 1 {
		t.Fatalf("retried early: wait %v, %d requests", wait, fx.f.requests)
	}
	fx.now = fx.now.Add(8 * time.Second)
	fx.u.Step(ctx)
	if len(fx.f.received()) != 1 || fx.state(t).LastError != "" {
		t.Fatalf("after retry: %d events, %+v", len(fx.f.received()), fx.state(t))
	}

	// Without Retry-After: exponential backoff with jitter, capped.
	fx.add(t, "github.com/acme/api", model.KindTurnStart, map[string]any{})
	fx.f.unavailable = true
	var waits []time.Duration
	for range 3 {
		fx.now = fx.now.Add(time.Hour)
		waits = append(waits, fx.u.Step(ctx))
	}
	if waits[0] < uploadEvery/2 || waits[0] > uploadEvery || waits[2] < 2*uploadEvery || waits[2] > 4*uploadEvery {
		t.Fatalf("backoff: %v", waits)
	}
	for n := 1; n < 20; n++ {
		if d := backoff(n, 0); d > maxBackoff || d < uploadEvery/2 {
			t.Fatalf("backoff(%d) = %v", n, d)
		}
	}
}

func TestOfflineKeepsCursor(t *testing.T) {
	fx := newFixture(t, syncOn)
	fx.add(t, "github.com/acme/api", model.KindTurnStart, map[string]any{})
	fx.srv.Close() // offline
	if wait := fx.u.Step(ctx); wait < uploadEvery/2 {
		t.Fatalf("wait = %v", wait)
	}
	if st := fx.state(t); st.Cursor != 0 || st.LastError == "" {
		t.Fatalf("state: %+v", st)
	}
}

func TestResumesAfterCrashWithoutDuplicates(t *testing.T) {
	fx := newFixture(t, syncOn)
	for range 3 {
		fx.add(t, "github.com/acme/api", model.KindTurnStart, map[string]any{})
	}
	fx.u.Step(ctx)
	// The daemon restarts: a new uploader continues from the stored cursor.
	for range 2 {
		fx.add(t, "github.com/acme/api", model.KindTurnStart, map[string]any{})
	}
	fx.uploader().Step(ctx)
	if got := fx.f.received(); len(got) != 5 || fx.state(t).Uploaded != 5 {
		t.Fatalf("after restart: %d events, uploaded %d", len(got), fx.state(t).Uploaded)
	}
	// A crash after the service accepted a batch but before the cursor was
	// saved: the batch is sent again and the service drops the duplicates.
	st := fx.state(t)
	st.Cursor = 3
	fx.st.PutSyncState(ctx, st)
	fx.uploader().Step(ctx)
	if got := fx.f.received(); len(got) != 5 || fx.state(t).Cursor != 5 {
		t.Fatalf("after replay: %d events, cursor %d", len(got), fx.state(t).Cursor)
	}
	if len(fx.f.deviceIDs) != 1 {
		t.Fatalf("device id changed: %v", fx.f.deviceIDs)
	}
}

func TestPrepareStopsAtBatchLimits(t *testing.T) {
	scope := ScopeFor(config.Config{CaptureLevel: "full", Sync: config.Sync{Projects: []string{"*"}}})
	defer func(n int) { batchBytes = n }(batchBytes)
	batchBytes = 64 << 10
	var big []any // ~8 KiB of file paths per event
	for range 16 {
		big = append(big, strings.Repeat("y", 500))
	}
	var rows []store.SyncRow
	for i := range 12 {
		e := model.Event{ID: fmt.Sprint(i), V: 1, Kind: model.KindFileEdit, Data: map[string]any{"files": big}}
		b, _ := json.Marshal(e)
		rows = append(rows, store.SyncRow{RowID: int64(i + 1), Body: b, ProjectID: "p"})
	}
	items, last := scope.Prepare(rows)
	if len(items) != 7 || last != 7 {
		t.Fatalf("items=%d last=%d, want 7 (size limit)", len(items), last)
	}
	size := 0
	for _, it := range items {
		size += len(it.body)
	}
	if size > batchBytes {
		t.Fatalf("batch is %d bytes", size)
	}
}

func TestGlob(t *testing.T) {
	s := ScopeFor(config.Config{Sync: config.Sync{Projects: []string{"github.com/acme/*", "local:/home/dev/notes"}, Exclude: []string{"*/acme/secret*"}}})
	for id, want := range map[string]bool{
		"github.com/acme/api": true, "github.com/acme/team/svc": true, "github.com/acme/secret-x": false,
		"github.com/other/api": false, "local:/home/dev/notes": true, "local:/home/dev/notes2": false, "": false,
	} {
		if got := s.Allowed(id); got != want {
			t.Errorf("Allowed(%q) = %v", id, got)
		}
	}
	if !ScopeFor(config.Config{Sync: config.Sync{Projects: []string{"*"}}}).Allowed("") {
		t.Error(`"*" should allow events without a project`)
	}
}

func TestKeychainFallbackToFile(t *testing.T) {
	home := t.TempDir()
	v := Vault{Home: home}
	c := &Creds{Endpoint: "https://sync.example.com", AccessToken: "a", RefreshToken: "r", WorkspaceID: "ws_1"}

	keyring.MockInitWithError(errors.New("no secret service"))
	where, why, err := v.Save(c)
	if err != nil || where != InFile || !strings.Contains(why, "no secret service") {
		t.Fatalf("save: %s %q %v", where, why, err)
	}
	fi, err := os.Stat(v.CredsPath())
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", fi.Mode().Perm())
	}
	got, where, why, err := v.Load()
	if err != nil || where != InFile || why == "" || got.AccessToken != "a" {
		t.Fatalf("load: %+v %s %q %v", got, where, why, err)
	}

	// With a keychain, credentials move there and the file is removed.
	keyring.MockInit()
	if where, _, err := v.Save(c); err != nil || where != InKeychain {
		t.Fatalf("save to keychain: %s %v", where, err)
	}
	if _, err := os.Stat(v.CredsPath()); !os.IsNotExist(err) {
		t.Fatal("file left behind")
	}
	if got, where, _, _ := v.Load(); got == nil || where != InKeychain {
		t.Fatalf("load from keychain: %v %s", got, where)
	}
	if err := v.Delete(); err != nil {
		t.Fatal(err)
	}
	if got, _, _, _ := v.Load(); got != nil {
		t.Fatal("still signed in after Delete")
	}
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Duration{
		"":   0,
		"30": 30 * time.Second,
		"-1": 0,
		now.Add(time.Minute).Format(http.TimeFormat): time.Minute,
		"soon": 0,
	} {
		if got := retryAfter(in, now); got != want {
			t.Errorf("retryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestDescribeAndDryRun(t *testing.T) {
	fx := newFixture(t, syncOn)
	fx.add(t, "github.com/acme/api", model.KindTurnStart, map[string]any{"prompt": "hello"})
	cfg, _ := config.Load(fx.home)
	c, where, _, _ := Vault{Home: fx.home}.Load()
	b, err := NextBatch(ctx, fx.home, cfg, c, fx.st)
	if err != nil || len(b.Events) != 1 || strings.Contains(string(b.Events[0]), "hello") || b.DeviceID == "" {
		t.Fatalf("dry run: %v %s", err, b.Events)
	}
	if fx.f.requests != 0 || fx.state(t).Cursor != 0 {
		t.Fatal("dry run sent or saved something")
	}
	v := Describe(ctx, fx.home, cfg, c, where, "", fx.st)
	if !v.SignedIn || v.WorkspaceName != "Acme" || v.Backlog != 1 || v.CredentialStore != InKeychain {
		t.Fatalf("view: %+v", v)
	}
	out, _ := json.Marshal(v)
	if strings.Contains(string(out), "access-1") || strings.Contains(string(out), "refresh-") {
		t.Fatalf("view leaks a token: %s", out)
	}
}

func TestSendsProjectRelativePaths(t *testing.T) {
	ev := func(p *model.Project, data map[string]any) model.Event {
		return model.Event{ID: "e1", V: 1, Kind: model.KindToolStart, User: "dev", Project: p, Data: data}
	}
	scope := func(_ string, sendUser bool) Scope {
		return ScopeFor(config.Config{CaptureLevel: "full", Sync: config.Sync{SendUser: sendUser, Projects: []string{"*"}}})
	}
	repo := &model.Project{ID: "example.com/acme/api", CWD: "/home/dev/api/sub", RepoRoot: "/home/dev/api", Remote: "example.com/acme/api", Branch: "main", Head: "abc123"}
	data := func() map[string]any {
		return map[string]any{
			"tool": "edit", "input_summary": "/home/dev/api/src/a.go",
			"path": "/home/dev/api/src/a.go", "file_path": "/etc/hosts", "cwd": "/home/dev/api",
			"transcript_path": "/home/dev/.agent/sessions/s1.jsonl",
			"files":           []any{"README.md", "/home/dev/api/b/c.go", "/tmp/x.txt"}, "lines_added": 3,
		}
	}

	got := scope("minimal", true).Outgoing(ev(repo, data()))
	if got.User != "" {
		t.Errorf("user sent: %q", got.User)
	}
	if p := got.Project; p.CWD != "" || p.RepoRoot != "" || p.ID != repo.ID || p.Branch != "main" || p.Remote != repo.Remote || p.Head != "abc123" {
		t.Errorf("project: %+v", p)
	}
	if repo.CWD == "" {
		t.Fatal("the stored event was changed")
	}
	want := map[string]any{
		"input_summary": "src/a.go", "path": "src/a.go", "file_path": "…/hosts", "lines_added": 3,
	}
	if got.Data["cwd"] != nil || got.Data["transcript_path"] != nil {
		t.Errorf("local-only fields sent: %v", got.Data)
	}
	for k, v := range want {
		if got.Data[k] != v {
			t.Errorf("%s = %v, want %v", k, got.Data[k], v)
		}
	}
	if f := got.Data["files"].([]any); f[0] != "README.md" || f[1] != "b/c.go" || f[2] != "…/x.txt" {
		t.Errorf("files = %v", f)
	}
	out, _ := json.Marshal(got)
	if strings.Contains(string(out), "/home/dev") {
		t.Errorf("an absolute path was sent: %s", out)
	}

	// A folder project without a repo root uses the folder from its id.
	dir := &model.Project{ID: "dir:/home/dev/notes", CWD: "/home/dev/notes"}
	got = scope("minimal", false).Outgoing(ev(dir, map[string]any{"tool": "read", "path": "/home/dev/notes/todo.md"}))
	if got.Data["path"] != "todo.md" || got.Project.CWD != "" {
		t.Errorf("dir project: %v %+v", got.Data, got.Project)
	}

	// Titles go only with send_titles, redacted and capped.
	start := func() model.Event {
		return model.Event{ID: "e2", V: 1, Kind: model.KindSessionStart, Project: repo,
			Data: map[string]any{"title": "Fix login " + strings.Repeat("x", 200), "title_source": "prompt", "prompt": "fix login"}}
	}
	if got = scope("", false).Outgoing(start()); got.Data["title"] != nil || got.Data["prompt"] != nil {
		t.Errorf("title sent without send_titles: %v", got.Data)
	}
	s := ScopeFor(config.Config{CaptureLevel: "full", Sync: config.Sync{SendTitles: true, Projects: []string{"*"}}})
	got = s.Outgoing(start())
	if title, _ := got.Data["title"].(string); !strings.HasPrefix(title, "Fix login") || len([]rune(title)) > 121 || got.Data["title_source"] != "prompt" || got.Data["prompt"] != nil {
		t.Errorf("send_titles: %v", got.Data)
	}
}
