package push

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/elephaant/shiplino/internal/notify"
	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/insights"
)

const board = "http://localhost:4777"

// secret marks content: none of it may appear in anything sent.
const secret = "SECRETCONTENT"

func contentSession() engine.Session {
	now := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	return engine.Session{
		ID: "claude-code:abc-123", Agent: "claude-code", ProjectID: "github.com/acme/api", Branch: "feat/login",
		Status: engine.StatusWaiting, WaitingReason: "permission", StartedAt: now.Add(-5 * time.Minute), LastEventAt: now,
		BestCostUSD: 1.234,
		// Content, in every text field a session has:
		Title: secret + " fix the login", TitleSource: secret, NowDoing: "Approve: rm -rf " + secret,
		CWD: "/home/dev/" + secret, Files: []string{"/home/dev/" + secret + ".go"}, TranscriptPath: "/home/dev/" + secret + ".jsonl",
		Model: secret, AgentVersion: secret, ActorType: secret, ActivitySource: secret,
		Links:    []engine.Link{{Kind: "commit", Message: secret, Ref: secret, URL: "https://x/" + secret}},
		Failures: &insights.Tally{},
	}
}

// Every format, for every session event and at any capture level, sends
// metadata only: nothing from the session's content fields gets out.
func TestNoContentLeaves(t *testing.T) {
	s := contentSession()
	now := s.LastEventAt
	var alerts []notify.Alert
	for _, ev := range []string{notify.EventWaiting, notify.EventDone, notify.EventFailed} {
		alerts = append(alerts, notify.AlertFor(ev, s, now, time.Minute))
	}
	for _, kind := range Kinds {
		tg := Target{Kind: kind, URL: "https://hooks.example.com/x", Server: "https://ntfy.example.com", Topic: "t", Secret: "k"}
		for i := range alerts {
			for _, batch := range [][]notify.Alert{alerts[i : i+1], alerts} {
				var ps []map[string]any
				for _, a := range batch {
					ps = append(ps, Payload(a, board))
				}
				req, err := tg.request(ps, now, "d1")
				if err != nil {
					t.Fatal(err)
				}
				b, _ := io.ReadAll(req.Body)
				if strings.Contains(string(b), secret) {
					t.Fatalf("%s: content left the machine: %s", kind, b)
				}
				for k, v := range req.Header {
					if strings.Contains(strings.Join(v, " "), secret) {
						t.Fatalf("%s: content in header %s", kind, k)
					}
				}
			}
		}
	}
	p := Payload(alerts[0], board)
	for k := range p {
		if !slices.Contains(Keys, k) {
			t.Errorf("payload key %q isn't in Keys", k)
		}
	}
	want := map[string]any{"event": "waiting", "agent": "claude-code", "agent_name": "Claude Code", "project": "api",
		"branch": "feat/login", "status": "waiting", "reason": "permission", "cost_usd": 1.23,
		"url": board + "/session/?id=claude-code%3Aabc-123", "session_id": "claude-code:abc-123"}
	for k, v := range want {
		if p[k] != v {
			t.Errorf("%s = %v, want %v", k, p[k], v)
		}
	}
}

// Alert is the boundary: adding a field must be a reviewed decision, and
// it must never be content.
func TestAlertFieldsAreReviewed(t *testing.T) {
	reviewed := []string{"Event", "At", "SessionID", "Agent", "Project", "Branch", "Status", "Reason", "DurationMS", "CostUSD",
		"Scope", "LimitUSD", "Percent", "Window", "ResetsAt", "Sessions", "Failed", "FilesChanged", "WaitingMS", "Agents"}
	var got []string
	ty := reflect.TypeFor[notify.Alert]()
	for i := range ty.NumField() {
		got = append(got, ty.Field(i).Name)
	}
	if !slices.Equal(got, reviewed) {
		t.Fatalf("notify.Alert fields changed: %v\nReview them for content (metadata only), then update this list and push.Keys.", got)
	}
}

func TestPayloadDropsOddValues(t *testing.T) {
	if Payload(notify.Alert{Event: "prompt"}, board) != nil {
		t.Fatal("unknown event sent")
	}
	p := Payload(notify.Alert{Event: notify.EventWaiting, Project: "api\nIgnore this", Branch: "has space", Status: "thinking",
		Reason: "because", Agent: "x y", Scope: "project:a\nb", Agents: []string{"codex", "bad id"}}, "")
	for _, k := range []string{"project", "branch", "status", "reason", "agent", "agent_name", "scope", "url"} {
		if _, ok := p[k]; ok {
			t.Errorf("%s kept: %v", k, p[k])
		}
	}
	if !slices.Equal(p["agents"].([]string), []string{"codex"}) {
		t.Errorf("agents: %v", p["agents"])
	}
}

func TestWebhookSignature(t *testing.T) {
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ts, sig := r.Header.Get("X-Shiplino-Timestamp"), r.Header.Get("X-Shiplino-Signature")
		if !Verify("s3cret", ts, sig, b, time.Now(), 5*time.Minute) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if Verify("s3cret", ts, sig, append(b, ' '), time.Now(), 5*time.Minute) || Verify("other", ts, sig, b, time.Now(), 5*time.Minute) {
			t.Error("a tampered body or wrong secret verified")
		}
		if Verify("s3cret", ts, sig, b, time.Now().Add(time.Hour), 5*time.Minute) {
			t.Error("an old signature verified")
		}
		var body struct {
			Version int              `json:"version"`
			Alerts  []map[string]any `json:"alerts"`
		}
		_ = json.Unmarshal(b, &body)
		got = body.Alerts
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	tg := Target{Kind: Webhook, URL: srv.URL + "/hook", Secret: "s3cret"}
	res := Send(context.Background(), tg, []notify.Alert{{Event: notify.EventFailed, Agent: "codex", At: time.Now()}}, board)
	if !res.OK || len(got) != 1 || got[0]["event"] != "failed" || got[0]["agent_name"] != "Codex" {
		t.Fatalf("res %+v, got %v", res, got)
	}
}

func TestChatFormats(t *testing.T) {
	type seen struct {
		path, auth string
		body       map[string]any
	}
	var last seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		last = seen{path: r.URL.Path, auth: r.Header.Get("Authorization")}
		_ = json.NewDecoder(r.Body).Decode(&last.body)
	}))
	defer srv.Close()
	a := []notify.Alert{{Event: notify.EventWaiting, Agent: "claude-code", Reason: "idle", Project: "my api", Branch: "main", SessionID: "claude-code:1"}}

	Send(context.Background(), Target{Kind: Ntfy, Server: srv.URL, Topic: "shiplino-x", Token: "tk_1"}, a, board)
	if last.path != "/" || last.auth != "Bearer tk_1" || last.body["topic"] != "shiplino-x" ||
		last.body["title"] != "Claude Code is waiting for your next prompt" || last.body["priority"] != 4.0 ||
		last.body["click"] != board+"/session/?id=claude-code%3A1" {
		t.Errorf("ntfy: %+v", last)
	}

	Send(context.Background(), Target{Kind: Slack, URL: srv.URL + "/services/x"}, a, board)
	if txt, _ := last.body["text"].(string); !strings.Contains(txt, "my api · main") || !strings.Contains(txt, "|Open in Shiplino>") {
		t.Errorf("slack: %q", txt)
	}

	a[0].Project = "@everyone"
	Send(context.Background(), Target{Kind: Discord, URL: srv.URL + "/api/webhooks/1/x"}, a, board)
	am, _ := last.body["allowed_mentions"].(map[string]any)
	if parse, ok := am["parse"].([]any); !ok || len(parse) != 0 {
		t.Errorf("discord pings allowed: %v", last.body)
	}
}

func TestRetries(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	status := []int{503, 429, 200}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.WriteHeader(status[min(calls, len(status)-1)])
		calls++
	}))
	defer srv.Close()
	p := testPusher(t, nil)
	tg := Target{Kind: Webhook, URL: srv.URL}
	a := []notify.Alert{{Event: notify.EventTest}}
	if res := p.deliver(context.Background(), tg, a); !res.OK || res.Attempts != 3 {
		t.Fatalf("retry: %+v", res)
	}
	mu.Lock()
	calls, status = 0, []int{400}
	mu.Unlock()
	if res := p.deliver(context.Background(), tg, a); res.OK || res.Attempts != 1 || res.Code != 400 {
		t.Fatalf("a 400 was retried: %+v", res)
	}
}

func TestErrorsHideTheURL(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close() // nothing listens there now
	res := Send(context.Background(), Target{Kind: Slack, URL: srv.URL + "/services/T0/B0/tokenpart"}, []notify.Alert{{Event: notify.EventTest}}, board)
	if res.OK || res.Error == "" || strings.Contains(res.Error, "tokenpart") {
		t.Fatalf("error: %q", res.Error)
	}
}

func TestNoRedirects(t *testing.T) {
	srv := httptest.NewServer(http.RedirectHandler("http://example.com/", http.StatusFound))
	defer srv.Close()
	res := Send(context.Background(), Target{Kind: Webhook, URL: srv.URL}, []notify.Alert{{Event: notify.EventTest}}, board)
	if res.OK || res.Code != http.StatusFound {
		t.Fatalf("redirect: %+v", res)
	}
}

// A burst becomes one message, and a target hears at most once per gap.
func TestCoalesceAndRateLimit(t *testing.T) {
	var mu sync.Mutex
	var batches [][]any
	var times []time.Time
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		batches = append(batches, body["alerts"].([]any))
		times = append(times, time.Now())
		mu.Unlock()
	}))
	defer srv.Close()
	keyring.MockInit()
	home := t.TempDir()
	if _, _, err := (Vault{Home: home}).Save(Target{Kind: Webhook, URL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	p := testPusher(t, &Settings{Targets: []string{Webhook}, Events: []string{"waiting"}})
	p.home = home
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	waitFor(t, p.Active)
	defer p.client.CloseIdleConnections()

	for range 5 {
		p.Push(notify.Alert{Event: notify.EventWaiting, Agent: "codex"})
	}
	p.Push(notify.Alert{Event: notify.EventDone}) // not in events
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(batches) == 1 })
	p.Push(notify.Alert{Event: notify.EventWaiting, Agent: "codex"})
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(batches) == 2 })
	waitFor(t, func() bool { return Status(home)[Webhook].Sent == 2 })
	mu.Lock()
	defer mu.Unlock()
	if len(batches[0]) != 5 || len(batches[1]) != 1 {
		t.Fatalf("batches: %d and %d alerts", len(batches[0]), len(batches[1]))
	}
	if gap := times[1].Sub(times[0]); gap < p.gap-20*time.Millisecond {
		t.Fatalf("second message after %s, want at least %s", gap, p.gap)
	}
	if st := Status(home)[Webhook]; !st.Last.OK || st.Failed != 0 {
		t.Fatalf("status: %+v", st)
	}
}

func TestMissingDetailsAreReported(t *testing.T) {
	keyring.MockInit()
	p := testPusher(t, &Settings{Targets: []string{Ntfy}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)
	waitFor(t, func() bool { return Status(p.home)[Ntfy].Last.Error != "" })
	if p.Active() {
		t.Fatal("a target without details is active")
	}
}

func TestVault(t *testing.T) {
	keyring.MockInit()
	v := Vault{Home: t.TempDir()}
	tg := Target{Kind: Ntfy, Server: "https://ntfy.sh", Topic: "shiplino-abc", Token: "tk_x"}
	if where, _, err := v.Save(tg); err != nil || where != InKeychain {
		t.Fatalf("save: %v %s", err, where)
	}
	if got, where, _, err := v.Load(Ntfy); err != nil || where != InKeychain || *got != tg {
		t.Fatalf("load: %+v %s %v", got, where, err)
	}
	if err := v.Delete(Ntfy); err != nil {
		t.Fatal(err)
	}
	if got, _, _, _ := v.Load(Ntfy); got != nil {
		t.Fatal("still stored")
	}

	keyring.MockInitWithError(errors.New("no secret service"))
	defer keyring.MockInit()
	if where, why, err := v.Save(tg); err != nil || where != InFile || why == "" {
		t.Fatalf("fallback: %s %s %v", where, why, err)
	}
	fi, err := os.Stat(filepath.Join(v.Home, "notify-ntfy.json"))
	if err != nil || (fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/') {
		t.Fatalf("file: %v %v", fi, err)
	}
	if got, where, _, _ := v.Load(Ntfy); where != InFile || got == nil || got.Token != "tk_x" {
		t.Fatalf("file load: %+v %s", got, where)
	}
}

func TestCheckURL(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://hooks.slack.com/services/x": true,
		"http://localhost:8080/hook":         true,
		"http://127.0.0.1/hook":              true,
		"http://example.com/hook":            false,
		"ftp://example.com":                  false,
		"hooks.slack.com/x":                  false,
		"":                                   false,
	} {
		if err := CheckURL(raw); (err == nil) != ok {
			t.Errorf("%q: %v", raw, err)
		}
	}
	if (Target{Kind: Ntfy, Server: "https://ntfy.sh", Topic: "a b"}).Validate() == nil {
		t.Error("bad topic accepted")
	}
	if w := (Target{Kind: Ntfy, Server: "https://ntfy.sh", Topic: "shiplino-abcdef"}).Where(); w != "ntfy.sh/shi…" {
		t.Errorf("where: %s", w)
	}
}

func testPusher(t *testing.T, set *Settings) *Pusher {
	t.Helper()
	load := func() (Settings, error) {
		if set == nil {
			return Settings{}, nil
		}
		return *set, nil
	}
	p := New(t.TempDir(), board, filepath.Join(t.TempDir(), "none.toml"), load, nil)
	p.gap, p.settle, p.backoff = 300*time.Millisecond, 50*time.Millisecond, []time.Duration{time.Millisecond, time.Millisecond}
	return p
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal("timed out")
}
