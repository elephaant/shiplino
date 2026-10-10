// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package daemon

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/internal/api"
	"github.com/elephaant/shiplino/internal/spool"
)

const ingestToken = "test-token-0123456789abcdef0123456789abcdef"

// apiServer serves the real API on top of the env's daemon.
func (e *env) apiServer() *httptest.Server {
	e.t.Helper()
	srv := api.New(e.st, api.NewHub(), ingestToken, "test", log.New(io.Discard, "", 0))
	srv.Ingest = e.d
	ts := httptest.NewServer(srv.Handler())
	e.t.Cleanup(ts.Close)
	return ts
}

func post(t *testing.T, url, contentType string, body []byte) string {
	t.Helper()
	req, _ := http.NewRequest("POST", url, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+ingestToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("%s: %d %s", url, resp.StatusCode, b)
	}
	return string(b)
}

func TestIngestTakesTheSpoolPath(t *testing.T) {
	e := newEnv(t)
	ts := e.apiServer()
	secret := "gh" + "p_" + strings.Repeat("A1b2C3d4E5", 4) // fake, assembled so scanners don't flag it
	body := `[
		{"kind":"turn.start","agent":{"name":"my-agent"},"session_id":"my-agent:run-1","id":"t1","project":{"cwd":"/home/dev/app"},"data":{"prompt":"deploy with ` + secret + `","args":["--token",  "` + secret + `"]}},
		{"kind":"usage","agent":{"name":"my-agent"},"session_id":"my-agent:run-1","id":"u1","data":{"input_tokens":10,"cost_usd":0.25}},
		{"kind":"turn.end","agent":{"name":"my-agent"},"session_id":"my-agent:run-1","id":"t2","data":{"status":"ok"}}
	]`
	if got := post(t, ts.URL+"/api/v1/ingest", "application/json", []byte(body)); !strings.Contains(got, `"accepted":3,"duplicates":0`) {
		t.Fatalf("ingest: %s", got)
	}
	if got := post(t, ts.URL+"/api/v1/ingest", "application/json", []byte(body)); !strings.Contains(got, `"accepted":0,"duplicates":3`) {
		t.Fatalf("retry: %s", got)
	}
	s := e.session("my-agent:run-1")
	if s.Turns != 1 || s.Status != "done" || s.CostUSD != 0.25 || s.InputTokens != 10 || s.Title == "" {
		t.Fatalf("session: %+v", s)
	}
	if strings.Contains(s.Title, secret) {
		t.Fatal("secret in the title")
	}
	evs, err := e.st.Events(ctx, "my-agent:run-1", "", 10)
	if err != nil || len(evs) != 3 {
		t.Fatalf("events: %v %v", evs, err)
	}
	if raw := evs[0].Data; strings.Contains(raw["prompt"].(string), secret) || strings.Contains(raw["args"].([]any)[1].(string), secret) {
		t.Fatalf("secret stored: %v", raw)
	}
	if e.d.Stats().Events != 3 {
		t.Errorf("stats: %+v", e.d.Stats())
	}

	// Paused: nothing is stored.
	if err := spool.Pause(e.home, time.Time{}); err != nil {
		t.Fatal(err)
	}
	more := `[{"kind":"note","agent":{"name":"my-agent"},"session_id":"my-agent:run-2"}]`
	if got := post(t, ts.URL+"/api/v1/ingest", "application/json", []byte(more)); !strings.Contains(got, `"paused":true`) {
		t.Fatalf("paused: %s", got)
	}
	if n := e.eventCount(); n != 3 {
		t.Fatalf("%d events stored while paused", n)
	}
}

// A Claude Code OTLP export (protobuf, then the same records as JSON)
// becomes a session with the agent's own per-request cost.
func TestOTLPExportBecomesSessionCost(t *testing.T) {
	e := newEnv(t)
	ts := e.apiServer()
	js, err := os.ReadFile(filepath.Join("..", "..", "pkg", "otlp", "testdata", "claude-code-logs.json"))
	if err != nil {
		t.Fatal(err)
	}
	pb, err := os.ReadFile(filepath.Join("..", "..", "pkg", "otlp", "testdata", "claude-code-logs.pb"))
	if err != nil {
		t.Fatal(err)
	}
	post(t, ts.URL+"/v1/logs", "application/x-protobuf", pb)
	post(t, ts.URL+"/v1/logs", "application/json", js)

	s := e.session("claude-code:0f0e0d0c-1111-4222-8333-444455556666")
	if s.Telemetry == nil || s.Telemetry.Requests != 2 || s.BestCostUSD != 0.1275 || s.CostSource != "reported" {
		t.Fatalf("cost: best %v (%s), telemetry %+v", s.BestCostUSD, s.CostSource, s.Telemetry)
	}
	if s.InputTokens != 2100 || s.TokensSource != "telemetry" || s.Turns != 1 || s.ToolCalls != 1 || s.Model != "claude-opus-5" || s.AgentVersion != "2.1.300" {
		t.Fatalf("session: %+v", s)
	}
	if n := e.eventCount(); n != 5 {
		t.Fatalf("%d events stored, want 5 (the JSON copy is all duplicates)", n)
	}
}
