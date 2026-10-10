// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/otlp"
)

// fakeIngester stores events by dedup key, like the daemon's store.
type fakeIngester struct {
	keys   map[string]bool
	events []model.Event
	paused bool
}

func (f *fakeIngester) Ingest(_ context.Context, evs []model.Event) (int, int, error) {
	if f.paused {
		return 0, 0, ErrPaused
	}
	acc := 0
	for _, e := range evs {
		if !f.keys[e.DedupKey] {
			f.keys[e.DedupKey] = true
			f.events = append(f.events, e)
			acc++
		}
	}
	return acc, len(evs) - acc, nil
}

func ingestSetup(t *testing.T) (*fixture, *fakeIngester) {
	f := setup(t)
	in := &fakeIngester{keys: map[string]bool{}}
	f.s.Ingest = in
	return f, in
}

func (f *fixture) send(t *testing.T, path, contentType string, body []byte, mod func(*http.Request)) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest("POST", f.url+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", contentType)
	bearer(req)
	if mod != nil {
		mod(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestIngestValidatesAndDedups(t *testing.T) {
	f, in := ingestSetup(t)
	body := `[
		{"kind":"turn.start","agent":{"name":"my-agent"},"session_id":"my-agent:run-1","id":"e1","data":{"prompt":"hello"}},
		{"kind":"usage","agent":{"name":"my-agent"},"session_id":"my-agent:run-1","ts":"2026-10-10T10:00:00Z","dedup_key":"u1","data":{"cost_usd":0.5}},
		{"kind":"tool.start","agent":{"name":"my-agent"},"session_id":"run-1"},
		{"kind":"teleport","agent":{"name":"my-agent"},"session_id":"my-agent:run-1"},
		{"kind":"note","agent":{"name":"a:b"},"session_id":"a:b:c"},
		{"kind":"note","agent":{"name":"my-agent"},"session_id":"my-agent:run-1","actor_id":"claude-code:other"},
		{"kind":"note","agent":{},"session_id":"x:1"},
		"not an event"
	]`
	resp, b := f.send(t, "/api/v1/ingest", "application/json", []byte(body), nil)
	var res IngestResult
	json.Unmarshal(b, &res)
	if resp.StatusCode != 200 || res.Accepted != 2 || res.Duplicates != 0 || len(res.Errors) != 6 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	for i, want := range []string{"namespaced", "unknown kind", "agent.name must", "actor_id", "missing agent.name", "invalid event JSON"} {
		if e := res.Errors[i]; e.Index != i+2 || !strings.Contains(e.Error, want) {
			t.Errorf("error %d = %+v, want index %d and %q", i, e, i+2, want)
		}
	}
	first := in.events[0]
	if first.Collector != model.CollectorHTTP || first.ID == "e1" || first.V != 1 || first.TS.IsZero() || first.ReceivedAt.IsZero() ||
		first.DedupKey != "http:my-agent:run-1:e1" {
		t.Errorf("filled event: %+v", first)
	}
	if in.events[1].DedupKey != "http:my-agent:run-1:u1" || in.events[1].TS.Year() != 2026 {
		t.Errorf("usage event: %+v", in.events[1])
	}

	// A retry is idempotent.
	_, b = f.send(t, "/api/v1/ingest", "application/json", []byte(body), nil)
	json.Unmarshal(b, &res)
	if res.Accepted != 0 || res.Duplicates != 2 {
		t.Fatalf("retry: %s", b)
	}
	if st := f.s.IngestStats(); st.Requests != 2 || st.Accepted != 2 || st.Duplicates != 2 || st.Rejected != 12 || st.LastError == "" {
		t.Errorf("stats: %+v", st)
	}

	// Nothing valid: 400, same shape.
	resp, b = f.send(t, "/api/v1/ingest", "application/json", []byte(`[{"kind":"note"}]`), nil)
	if resp.StatusCode != 400 || !strings.Contains(string(b), `"accepted":0`) {
		t.Errorf("all invalid: %d %s", resp.StatusCode, b)
	}
	resp, _ = f.send(t, "/api/v1/ingest", "application/json", []byte(`{"kind":"note"}`), nil)
	if resp.StatusCode != 400 {
		t.Errorf("object instead of array: %d", resp.StatusCode)
	}
}

func TestIngestNDJSONAndGzip(t *testing.T) {
	f, in := ingestSetup(t)
	nd := "{\"kind\":\"note\",\"agent\":{\"name\":\"bot\"},\"session_id\":\"bot:1\",\"id\":\"a\"}\n\n{\"kind\":\"note\",\"agent\":{\"name\":\"bot\"},\"session_id\":\"bot:1\",\"id\":\"b\"}\n"
	var zipped bytes.Buffer
	zw := gzip.NewWriter(&zipped)
	zw.Write([]byte(nd))
	zw.Close()
	resp, b := f.send(t, "/api/v1/ingest", "application/x-ndjson", zipped.Bytes(), func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") })
	if resp.StatusCode != 200 || len(in.events) != 2 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	resp, _ = f.send(t, "/api/v1/ingest", "application/x-ndjson", []byte(nd), func(r *http.Request) { r.Header.Set("Content-Encoding", "br") })
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("brotli: %d", resp.StatusCode)
	}
}

func TestIngestLimits(t *testing.T) {
	f, in := ingestSetup(t)
	var many bytes.Buffer
	for i := 0; i <= MaxIngestEvents; i++ {
		fmt.Fprintf(&many, "{\"kind\":\"note\",\"agent\":{\"name\":\"bot\"},\"session_id\":\"bot:1\",\"id\":\"%d\"}\n", i)
	}
	if resp, _ := f.send(t, "/api/v1/ingest", "application/x-ndjson", many.Bytes(), nil); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("%d events: status %d", MaxIngestEvents+1, resp.StatusCode)
	}
	big := `[{"kind":"note","agent":{"name":"bot"},"session_id":"bot:1","data":{"x":"` + strings.Repeat("a", MaxIngestBytes) + `"}}]`
	if resp, _ := f.send(t, "/api/v1/ingest", "application/json", []byte(big), nil); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("over 4 MB: status %d", resp.StatusCode)
	}
	// A small gzip body that inflates past the limit is refused too.
	var bomb bytes.Buffer
	zw := gzip.NewWriter(&bomb)
	zw.Write([]byte(big))
	zw.Close()
	if resp, _ := f.send(t, "/api/v1/ingest", "application/json", bomb.Bytes(), func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("gzip bomb: status %d", resp.StatusCode)
	}
	if len(in.events) != 0 {
		t.Errorf("stored %d events from refused requests", len(in.events))
	}
}

func TestIngestAuthAndAvailability(t *testing.T) {
	f, in := ingestSetup(t)
	body := []byte(`[{"kind":"note","agent":{"name":"bot"},"session_id":"bot:1"}]`)
	for _, path := range []string{"/api/v1/ingest", "/v1/logs", "/v1/metrics", "/v1/traces"} {
		for name, mod := range map[string]func(*http.Request){
			"none":  func(r *http.Request) { r.Header.Del("Authorization") },
			"wrong": func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") },
		} {
			if resp, _ := f.send(t, path, "application/json", body, mod); resp.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s %s: status %d", path, name, resp.StatusCode)
			}
		}
		if resp, _ := f.send(t, path, "application/json", body, func(r *http.Request) { r.Host = "evil.example.com" }); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s from a foreign Host: status %d", path, resp.StatusCode)
		}
	}
	if len(in.events) != 0 {
		t.Fatal("unauthenticated events stored")
	}

	// Paused: nothing is stored, and the caller is told.
	in.paused = true
	_, b := f.send(t, "/api/v1/ingest", "application/json", body, nil)
	if !strings.Contains(string(b), `"paused":true`) || !strings.Contains(string(b), `"accepted":0`) {
		t.Errorf("paused: %s", b)
	}

	f.s.Ingest = nil
	if resp, _ := f.send(t, "/api/v1/ingest", "application/json", body, nil); resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("without an ingester: %d", resp.StatusCode)
	}
}

func TestOTLPReceiver(t *testing.T) {
	f, in := ingestSetup(t)
	js, err := os.ReadFile("../../pkg/otlp/testdata/claude-code-logs.json")
	if err != nil {
		t.Fatal(err)
	}
	pb, err := os.ReadFile("../../pkg/otlp/testdata/claude-code-logs.pb")
	if err != nil {
		t.Fatal(err)
	}

	resp, b := f.send(t, "/v1/logs", "application/x-protobuf", pb, nil)
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/x-protobuf" || len(b) != 0 || len(in.events) != 5 {
		t.Fatalf("protobuf: %d %q %q, %d events", resp.StatusCode, resp.Header.Get("Content-Type"), b, len(in.events))
	}
	// The same records again as JSON: all duplicates.
	resp, b = f.send(t, "/v1/logs", "application/json", js, nil)
	if resp.StatusCode != 200 || string(b) != "{}" || len(in.events) != 5 {
		t.Fatalf("json: %d %s, %d events", resp.StatusCode, b, len(in.events))
	}
	if resp, _ := f.send(t, "/v1/logs", "text/plain", js, nil); resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain: %d", resp.StatusCode)
	}
	if resp, _ := f.send(t, "/v1/logs", "application/json", []byte("nope"), nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad body: %d", resp.StatusCode)
	}
	if resp, _ := f.send(t, "/v1/logs", "application/x-protobuf", []byte{0x0a, 0xff, 0xff, 0xff, 0xff, 0x0f}, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("corrupt protobuf: %d", resp.StatusCode)
	}
	if resp, _ := f.send(t, "/v1/traces", "application/x-protobuf", []byte{1, 2, 3}, nil); resp.StatusCode != 200 {
		t.Errorf("traces: %d", resp.StatusCode)
	}
	if resp, b := f.send(t, "/v1/metrics", "application/json", []byte(`{"resourceMetrics":[]}`), nil); resp.StatusCode != 200 || string(b) != "{}" {
		t.Errorf("metrics: %d %s", resp.StatusCode, b)
	}
	if resp, _ := f.send(t, "/v1/metrics", "text/plain", nil, nil); resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("metrics as text/plain: %d", resp.StatusCode)
	}
	st := f.s.IngestStats()
	if st.OTLPLogs != (otlp.Stats{Records: 16, Events: 10, Ignored: 4, Unknown: 4}) || st.OTLPTraces != 1 || st.OTLPMetrics != 1 {
		t.Errorf("stats: %+v", st)
	}

	// The status endpoint exposes the counters (doctor reads them).
	_, b = f.get(t, "/api/v1/status", bearer)
	if !strings.Contains(string(b), `"otlp_logs":{"records":16`) {
		t.Errorf("status: %s", b)
	}
}
