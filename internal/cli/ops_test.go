// Copyright 2026 The Shiplino Authors
// SPDX-License-Identifier: FSL-1.1-Apache-2.0

package cli

import (
	"context"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/elephaant/shiplino/internal/api"
	"github.com/elephaant/shiplino/internal/notify"
	"github.com/elephaant/shiplino/internal/spool"
	"github.com/elephaant/shiplino/internal/store"
	"github.com/elephaant/shiplino/pkg/engine"
	"github.com/elephaant/shiplino/pkg/model"
	"github.com/elephaant/shiplino/pkg/otlp"
)

// liveDaemon runs a real API server with a few sessions, writing the port
// and token files the CLI reads.
func liveDaemon(t *testing.T, e *env) {
	t.Helper()
	os.MkdirAll(e.home, 0o700)
	st, err := store.Open(filepath.Join(e.home, "data", "shiplino.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now()
	tx, _ := st.Begin(context.Background())
	for _, s := range []*engine.Session{
		{ID: "claude-code:a", RootID: "claude-code:a", Agent: "claude-code", Status: engine.StatusRunning, Title: "Fix login", NowDoing: "Editing auth.ts", StartedAt: now, LastEventAt: now, BestCostUSD: 0.42},
		{ID: "claude-code:b", RootID: "claude-code:b", Agent: "claude-code", Status: engine.StatusWaiting, Title: "Migrate DB", NowDoing: "Approve: db:migrate", StartedAt: now, LastEventAt: now},
		{ID: "claude-code:c", RootID: "claude-code:c", Agent: "claude-code", Status: engine.StatusDone, Title: "Old task", StartedAt: now.Add(-72 * time.Hour), LastEventAt: now.Add(-72 * time.Hour)},
	} {
		tx.PutSession(context.Background(), s)
	}
	tx.InsertEvent(context.Background(), model.Event{ID: model.NewULID(now), V: 1, TS: now, Kind: model.KindShellExec, Agent: model.Agent{Name: "claude-code"},
		Collector: model.CollectorHook, SessionID: "claude-code:a", DedupKey: "k1", Data: map[string]any{"command": "npm run migrate:latest"}})
	tx.Commit()
	token, _ := api.LoadToken(e.home)
	srv := api.New(st, api.NewHub(), token, "test", log.New(io.Discard, "", 0))
	srv.Status = func() any {
		return map[string]any{"lines": 10, "events": 9, "unknown": 1, "bad": 0, "spool_backlog_bytes": 0}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(e.home, "port"), []byte(strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)), 0o600)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go srv.Serve(ctx, ln)
}

func TestStatusAndLs(t *testing.T) {
	e, out := testEnv(t)
	if code := status(context.Background(), e, nil); code != 1 || !strings.Contains(out.String(), "isn't running") {
		t.Fatalf("status without daemon: %d %s", code, out)
	}
	liveDaemon(t, e)
	out.Reset()
	if code := status(context.Background(), e, nil); code != 0 {
		t.Fatalf("status: %s", out)
	}
	s := out.String()
	if !strings.Contains(s, "1 running · 1 waiting on you") || !strings.Contains(s, "⚠ claude-code") || !strings.Contains(s, "Approve: db:migrate") {
		t.Fatalf("status output:\n%s", s)
	}
	if strings.Index(s, "Migrate DB") > strings.Index(s, "Fix login") {
		t.Fatalf("waiting sessions should be listed first:\n%s", s)
	}

	out.Reset()
	ls(context.Background(), e, nil)
	if !strings.Contains(out.String(), "Old task") || !strings.Contains(out.String(), "$0.42") {
		t.Fatalf("ls:\n%s", out)
	}
	out.Reset()
	ls(context.Background(), e, []string{"--running"})
	if strings.Contains(out.String(), "Old task") || !strings.Contains(out.String(), "Fix login") {
		t.Fatalf("ls --running:\n%s", out)
	}
}

func TestPauseResume(t *testing.T) {
	e, out := testEnv(t)
	pause(context.Background(), e, nil)
	if !spool.Paused(e.home, time.Now()) {
		t.Fatal("not paused")
	}
	resume(context.Background(), e, nil)
	if spool.Paused(e.home, time.Now()) {
		t.Fatal("still paused")
	}
	pause(context.Background(), e, []string{"--for", "1h"})
	if !spool.Paused(e.home, time.Now()) || spool.Paused(e.home, time.Now().Add(2*time.Hour)) {
		t.Fatal("timed pause wrong")
	}
	if code := pause(context.Background(), e, []string{"--for", "soon"}); code != 2 {
		t.Fatalf("bad duration accepted:\n%s", out)
	}
}

func TestDoctor(t *testing.T) {
	e, out := testEnv(t)
	os.MkdirAll(filepath.Join(e.userHome, ".claude"), 0o700)
	// Nothing installed yet: binary missing, hooks missing, daemon down.
	if code := doctor(context.Background(), e, nil); code != 1 {
		t.Fatalf("doctor on a fresh machine passed:\n%s", out)
	}
	for _, want := range []string{"❌ Binary", "❌ Claude Code", "hooks missing", "❌ Daemon", "→ shiplino doctor --fix"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	setup(context.Background(), e, []string{"--no-service"})
	liveDaemon(t, e)
	out.Reset()
	if code := doctor(context.Background(), e, nil); code != 0 {
		t.Fatalf("doctor after setup:\n%s", out)
	}
	for _, want := range []string{"✅ Binary", "✅ Claude Code", "✅ Daemon", "1 unknown lines", "Last event", "✅ Ingest", "/api/v1/ingest", "Authorization: Bearer"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}

	// An update wiped the hooks: --fix puts them back.
	os.WriteFile(filepath.Join(e.userHome, ".claude", "settings.json"), []byte("{}\n"), 0o600)
	out.Reset()
	if code := doctor(context.Background(), e, []string{"--fix"}); code != 0 || !strings.Contains(out.String(), "fixed") {
		t.Fatalf("doctor --fix:\n%s", out)
	}
}

func TestIngestChecks(t *testing.T) {
	for _, tc := range []struct {
		name  string
		in    *api.IngestStats
		warns int
		want  string
	}{
		{"older daemon", nil, 0, "localhost:4777/api/v1/ingest"},
		{"quiet", &api.IngestStats{}, 0, "~/.shiplino/token"},
		{"rejected", &api.IngestStats{Rejected: 2, LastError: "event 0: missing agent.name"}, 1, "missing agent.name"},
		{"logs", &api.IngestStats{OTLPLogs: otlp.Stats{Records: 9, Events: 5, Ignored: 2, Unknown: 2}}, 1, "9 log records"},
		{"metrics only", &api.IngestStats{OTLPMetrics: 4}, 1, "OTEL_LOGS_EXPORTER=otlp"},
	} {
		checks := ingestChecks("http://localhost:4777", "~/.shiplino/token", tc.in)
		warns, text := 0, ""
		for _, c := range checks {
			if c.warn {
				warns++
			}
			text += c.detail + " " + c.fixHint + "\n"
		}
		if warns != tc.warns || !strings.Contains(text, tc.want) {
			t.Errorf("%s: %d warnings, want %d; %q not in:\n%s", tc.name, warns, tc.warns, tc.want, text)
		}
	}
}

func TestSearchAndExport(t *testing.T) {
	e, out := testEnv(t)
	liveDaemon(t, e)
	if code := search(context.Background(), e, []string{"migrate"}); code != 0 || !strings.Contains(out.String(), "«migrate»") || !strings.Contains(out.String(), "Fix login") {
		t.Fatalf("search %d:\n%s", code, out)
	}
	out.Reset()
	search(context.Background(), e, []string{"zzz"})
	if !strings.Contains(out.String(), "No matches") {
		t.Fatalf("no match:\n%s", out)
	}
	if code := search(context.Background(), e, nil); code != 2 {
		t.Fatalf("empty query: %d", code)
	}

	out.Reset()
	if code := export(context.Background(), e, nil); code != 0 || !strings.HasPrefix(out.String(), "id,parent_id,agent") || strings.Count(out.String(), "\n") != 4 {
		t.Fatalf("export csv %d:\n%s", code, out)
	}
	file := filepath.Join(t.TempDir(), "s.json")
	if code := export(context.Background(), e, []string{"--out", file, "--since", "1d"}); code != 0 {
		t.Fatalf("export json: %s", out)
	}
	if b, _ := os.ReadFile(file); !strings.HasPrefix(string(b), "[") || strings.Contains(string(b), "Old task") {
		t.Fatalf("json file (since 1d): %s", b)
	}
}

func TestNotifyTest(t *testing.T) {
	e, out := testEnv(t)
	var got []notify.Note
	e.notifySend = func(_ context.Context, n notify.Note) error { got = append(got, n); return nil }
	if code := notifyCmd(context.Background(), e, []string{"test"}); code != 0 || len(got) != 1 || !strings.Contains(out.String(), "Sent a test notification") {
		t.Fatalf("notify test %d %v:\n%s", code, got, out)
	}
	if code := notifyCmd(context.Background(), e, nil); code != 2 {
		t.Fatalf("usage: %d", code)
	}
}
